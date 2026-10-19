// Package llm talks to one model server. Three protocols are spoken: a
// llama.cpp server, in router or single-model mode, an OpenAI-compatible chat
// endpoint, and the OpenAI responses endpoint. Which one an address speaks is
// told to the client rather than discovered, so nothing here probes to find
// out and nothing has to be re-checked when an address changes what it is.
// The package converts HTTP and SSE into Go values and knows nothing about
// sessions, tools or permissions.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"syscall"
	"time"
)

// maxErrorBody is how much of a failing response is kept for the message.
const maxErrorBody = 8 << 10

// Every protocol serves the model list. The other two paths are the two
// shapes of generating: the chat completion every OpenAI-compatible server
// has, and the response OpenAI moved to.
const (
	modelsPath    = "/v1/models"
	chatPath      = "/v1/chat/completions"
	responsesPath = "/v1/responses"
)

// Client is the model server at one address: the HTTP half every backend
// shares, and the backend that address turned out to speak. It has no global
// timeout because a chat stream can legitimately run for minutes; deadlines
// come from ctx. A server may hold its headers until the prompt is read, so
// the wait for them is as long as the wait for a reply's first event.
type Client struct {
	BaseURL string
	Token   string
	// Protocol is the one this address speaks. It is set once, at
	// construction, because the pool keys a client on it.
	Protocol Protocol
	HTTP     *http.Client
}

func New(baseURL, token string, protocol Protocol) *Client {
	return &Client{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Token:    token,
		Protocol: protocol,
		HTTP: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: streamFirstEvent,
				ExpectContinueTimeout: time.Second,
				DisableCompression:    true,
			},
		},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// send performs a request, and repeats it once on a fresh connection when a
// kept-alive connection turns out to have been closed by the server. Some
// servers close an idle connection without saying so, and the request sent
// on it fails before the server has read it.
func (c *Client) send(req *http.Request) (*http.Response, error) {
	reused := false
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}
	resp, err := c.httpClient().Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err == nil || !reused || !closedUnderneath(err) || req.GetBody == nil || req.Context().Err() != nil {
		return resp, err
	}
	body, berr := req.GetBody()
	if berr != nil {
		return resp, err
	}
	again := req.Clone(req.Context())
	again.Body = body
	c.httpClient().CloseIdleConnections()
	return c.httpClient().Do(again)
}

// closedUnderneath reports whether a request failed because the connection it
// was sent on had already been closed.
func closedUnderneath(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

// authorize adds the bearer token, if there is one, to any request.
func (c *Client) authorize(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// getJSON performs a GET and decodes the body into v. A non-2xx status or an
// undecodable body becomes an *Error carrying op.
func (c *Client) getJSON(ctx context.Context, op, model, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return classify(op, c.BaseURL, model, 0, "", err)
	}
	req.Header.Set("Accept", "application/json")
	c.authorize(req)
	resp, err := c.send(req)
	if err != nil {
		return classify(op, c.BaseURL, model, 0, "", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return classify(op, c.BaseURL, model, resp.StatusCode, string(body), nil)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return classify(op, c.BaseURL, model, 0, "", err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &Error{Kind: KindMalformed, Op: op, err: err,
			Message: "The model server sent a reply that could not be read: check that " + c.BaseURL + " is a model server"}
	}
	return nil
}

// CloseIdle releases the connections a client is no longer going to use. A
// request in flight keeps its own connection.
func (c *Client) CloseIdle() {
	c.httpClient().CloseIdleConnections()
}
