package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// HTTPSpec is a server reached over Streamable HTTP at one address.
type HTTPSpec struct {
	URL string
	// Headers go on every request, which is where a static credential lives.
	Headers map[string]string
	// Client is the HTTP client; nil uses one with no overall timeout, since
	// a tool call is bounded by its context instead.
	Client *http.Client
}

// errSessionExpired is a 404 on a request that carried a session id: the
// server has forgotten the session, and only a new initialize recovers.
var errSessionExpired = errors.New("the server ended the session")

type httpTransport struct {
	spec   HTTPSpec
	client *http.Client

	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	in     chan message

	mu      sync.Mutex
	session string
	version string
	closed  bool
}

func newHTTP(spec HTTPSpec) (*httpTransport, error) {
	if spec.URL == "" {
		return nil, errors.New("no address is set")
	}
	if !strings.HasPrefix(spec.URL, "http://") && !strings.HasPrefix(spec.URL, "https://") {
		return nil, fmt.Errorf("%s is not an http or https address", spec.URL)
	}
	client := spec.Client
	if client == nil {
		client = &http.Client{}
	}
	base, cancel := context.WithCancel(context.Background())
	return &httpTransport{spec: spec, client: client, base: base, cancel: cancel, in: make(chan message, 64)}, nil
}

// setVersion records the negotiated protocol version, which every request
// after initialize names in a header.
func (h *httpTransport) setVersion(v string) {
	h.mu.Lock()
	h.version = v
	h.mu.Unlock()
}

func (h *httpTransport) Send(ctx context.Context, frame []byte) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return ErrClosed
	}
	h.wg.Add(1)
	session, version := h.session, h.version
	h.mu.Unlock()

	rctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(h.base, cancel)
	release := func() { stop(); cancel(); h.wg.Done() }

	req, err := http.NewRequestWithContext(rctx, http.MethodPost, h.spec.URL, bytes.NewReader(frame))
	if err != nil {
		release()
		return fmt.Errorf("build a request for %s: %w", h.spec.URL, err)
	}
	h.headers(req, session, version)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := h.client.Do(req)
	if err != nil {
		release()
		return fmt.Errorf("reach %s: %w", h.spec.URL, err)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		h.mu.Lock()
		if h.session == "" {
			h.session = sid
		}
		h.mu.Unlock()
	}
	if err := statusError(resp, session != ""); err != nil {
		resp.Body.Close()
		release()
		return err
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		release()
		return nil
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	go func() {
		defer release()
		defer resp.Body.Close()
		if ct == "text/event-stream" {
			_ = readSSE(resp.Body, func(data []byte) { h.push(decodeFrames(data)) })
			return
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame+1))
		if err != nil || len(raw) > maxFrame {
			return
		}
		h.push(decodeFrames(raw))
	}()
	return nil
}

func (h *httpTransport) headers(req *http.Request, session, version string) {
	for k, v := range h.spec.Headers {
		req.Header.Set(k, v)
	}
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
}

// statusError turns a refusal into an error the reader can act on. A 2xx is
// no error.
func statusError(resp *http.Response, hadSession bool) error {
	code := resp.StatusCode
	if code >= 200 && code < 300 {
		return nil
	}
	if code == http.StatusNotFound && hadSession {
		return errSessionExpired
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return fmt.Errorf("the server refused the request (HTTP %d): check the headers that carry the credential", code)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("the server answered HTTP %d", code)
	}
	return fmt.Errorf("the server answered HTTP %d: %s", code, msg)
}

func (h *httpTransport) push(ms []message) {
	for _, m := range ms {
		select {
		case h.in <- m:
		case <-h.base.Done():
			return
		}
	}
}

func (h *httpTransport) Recv() <-chan message { return h.in }

func (h *httpTransport) Err() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	return nil
}

// Close ends the session on the server, as a courtesy that may fail, and
// stops every reply still being read.
func (h *httpTransport) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	session, version := h.session, h.version
	h.mu.Unlock()
	if session != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if req, err := http.NewRequestWithContext(ctx, http.MethodDelete, h.spec.URL, nil); err == nil {
			h.headers(req, session, version)
			if resp, err := h.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
		cancel()
	}
	h.cancel()
	h.wg.Wait()
	close(h.in)
	return nil
}

// readSSE hands the data of each message event to fn. Events of another type
// are skipped, and an event past maxFrame ends the stream.
func readSSE(r io.Reader, fn func([]byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxFrame)
	var data []byte
	event := ""
	has := false
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			if has && (event == "" || event == "message") {
				fn(data)
			}
			data, event, has = data[:0], "", false
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "data":
			if has {
				data = append(data, '\n')
			}
			data = append(data, value...)
			has = true
		case "event":
			event = string(value)
		}
	}
	if has && (event == "" || event == "message") {
		fn(data)
	}
	return sc.Err()
}
