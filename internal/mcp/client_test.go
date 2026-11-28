package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHelperProcess is the fake server run as a child process. It does
// nothing unless the test binary was started as one.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("MCP_FAKE")
	if mode == "" {
		return
	}
	defer os.Exit(0)
	var wmu sync.Mutex
	emit := func(m message) {
		raw, _ := json.Marshal(m)
		wmu.Lock()
		os.Stdout.Write(append(raw, '\n'))
		wmu.Unlock()
	}
	version := ""
	if mode == "badversion" {
		version = "1999-01-01"
	}
	f := newFake(version, emit)
	f.extra = map[string]func() string{
		"die": func() string { os.Exit(2); return "" },
		"whoami": func() string {
			wd, _ := os.Getwd()
			return strings.Join(os.Args[1:], " ") + " in " + wd
		},
	}
	_ = readLines(os.Stdin, func(line []byte) {
		var m message
		if json.Unmarshal(line, &m) != nil {
			return
		}
		switch mode {
		case "silent":
			return
		case "crash":
			fmt.Fprintln(os.Stderr, "starting")
			fmt.Fprintln(os.Stderr, "boom: no token")
			os.Exit(3)
		case "noise":
			os.Stdout.WriteString("not json\n" + strings.Repeat("x", maxFrame+10) + "\n")
		}
		f.handle(m)
	})
}

func stdioSpec(mode string) Spec {
	return Spec{Stdio: &StdioSpec{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestHelperProcess$"},
		Env:     []string{"MCP_FAKE=" + mode},
	}}
}

func start(t *testing.T, spec Spec, o Options) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Start(ctx, spec, o)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// exercise runs the same checks against any transport.
func exercise(t *testing.T, spec Spec) {
	notes := make(chan string, 4)
	c := start(t, spec, Options{Version: "test", OnNotify: func(m string, _ json.RawMessage) { notes <- m }})
	ctx := context.Background()

	if c.Info.Name != "fake" || c.Info.Protocol != latestVersion || c.Info.Instructions != "Use the fake." {
		t.Fatalf("info = %+v", c.Info)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	tools, dropped, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	if got := strings.Join(names, ","); got != "echo,noschema,image,slow" {
		t.Fatalf("tools = %s", got)
	}
	if string(tools[1].InputSchema) != string(emptySchema) {
		t.Fatalf("noschema schema = %s", tools[1].InputSchema)
	}
	if tools[0].Description != "Echoes." {
		t.Fatalf("first echo should win, got %+v", tools[0])
	}
	if len(dropped) != 3 {
		t.Fatalf("notes = %q", dropped)
	}

	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || len(res.Content) != 1 || res.Content[0].Text != `{"text":"hi"}` {
		t.Fatalf("echo = %+v, %v", res, err)
	}
	if res, err = c.CallTool(ctx, "echo", nil); err != nil || res.Content[0].Text != `{}` {
		t.Fatalf("echo with no args = %+v, %v", res, err)
	}
	if _, err := c.CallTool(ctx, "echo", json.RawMessage(`[1]`)); err == nil {
		t.Fatal("an array of arguments was sent")
	}

	var rpc *RPCError
	if _, err := c.CallTool(ctx, "nope", nil); !errors.As(err, &rpc) || rpc.Code != codeInvalidParams {
		t.Fatalf("unknown tool err = %v", err)
	}

	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err = c.CallTool(short, "slow", nil)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow err = %v", err)
	}

	if _, err := c.CallTool(ctx, "notify", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-notes:
		if m != "notifications/tools/list_changed" {
			t.Fatalf("notification = %s", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification")
	}

	// The cancel notification is sent in the background; give it a moment.
	var report struct {
		Cancelled []string `json:"cancelled"`
		Roots     string   `json:"roots"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		res, err := c.CallTool(ctx, "report", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(res.Content[0].Text), &report)
		if len(report.Cancelled) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(report.Cancelled) != 1 || !strings.HasSuffix(report.Cancelled[0], " timed out") {
		t.Fatalf("cancelled = %q", report.Cancelled)
	}
	if report.Roots != fmt.Sprint(codeMethodNotFound) {
		t.Fatalf("roots/list answered %q", report.Roots)
	}
}

func TestStdio(t *testing.T) { exercise(t, stdioSpec("ok")) }

func TestStdioCrashReportsStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Start(ctx, stdioSpec("crash"), Options{})
	if err == nil || !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), "boom: no token") {
		t.Fatalf("err = %v", err)
	}
}

func TestStdioSilentTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := Start(ctx, stdioSpec("silent"), Options{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(began) > 5*time.Second {
		t.Fatal("close took too long")
	}
}

func TestStdioRefusesVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Start(ctx, stdioSpec("badversion"), Options{})
	if err == nil || !strings.Contains(err.Error(), "1999-01-01") {
		t.Fatalf("err = %v", err)
	}
}

func TestStdioSkipsNoise(t *testing.T) {
	c := start(t, stdioSpec("noise"), Options{})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStdioMissingCommand(t *testing.T) {
	_, err := Start(context.Background(), Spec{Stdio: &StdioSpec{Command: "/no/such/server"}}, Options{})
	if err == nil {
		t.Fatal("started a missing command")
	}
}

func TestStdioCloseEndsCalls(t *testing.T) {
	c := start(t, stdioSpec("ok"), Options{})
	done := make(chan error, 1)
	go func() {
		_, err := c.CallTool(context.Background(), "slow", nil)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call outlived close")
	}
}

// httpFake serves the fake over Streamable HTTP: JSON for most answers, an
// event stream for tool calls, one session, and 202 for what has no answer.
type httpFake struct {
	t       *testing.T
	fake    *fake
	mu      sync.Mutex
	session string
	deleted bool
	missing []string
	waiting map[string]chan message
	stale   bool
	// initStatus, when set, is the status every initialize is refused with.
	initStatus int
	inits      int
	// lost is how many notifications/initialized are answered 404, as by a
	// server that forgets a session as soon as it hands it out.
	lost int
	// catalog, when set, is the one tool each session lists, by the
	// initialize that opened it.
	catalog []string
	// live maps each session handed out and not forgotten to the number of
	// the initialize that opened it.
	live map[string]int
}

func newHTTPFake(t *testing.T) (*httpFake, *httptest.Server) {
	h := &httpFake{t: t, waiting: map[string]chan message{}, live: map[string]int{}}
	h.fake = newFake("", h.route)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return h, srv
}

// httpSpec reaches the fake with the credential it asks for.
func httpSpec(srv *httptest.Server) *HTTPSpec {
	return &HTTPSpec{URL: srv.URL, Headers: map[string]string{"X-Key": "secret"}}
}

// forgetSession makes the fake answer 404 to the session it handed out.
func forgetSession(h *httpFake) {
	h.mu.Lock()
	h.session = "forgotten"
	clear(h.live)
	h.mu.Unlock()
}

// route sends a frame to the POST waiting on its id, or drops a server
// request and notification, which this fake has no stream for.
func (h *httpFake) route(m message) {
	h.mu.Lock()
	ch := h.waiting[string(m.ID)]
	h.mu.Unlock()
	if ch != nil && m.isResponse() {
		ch <- m
	}
}

func (h *httpFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Key") != "secret" {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	h.mu.Lock()
	session, stale := h.session, h.stale
	opened, live := h.live[r.Header.Get("Mcp-Session-Id")]
	h.mu.Unlock()
	if r.Method == http.MethodDelete {
		h.mu.Lock()
		h.deleted = r.Header.Get("Mcp-Session-Id") == session
		h.mu.Unlock()
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var m message
	_ = json.Unmarshal(raw, &m)
	if m.Method == "initialize" {
		h.mu.Lock()
		h.inits++
		refuse := h.initStatus
		h.mu.Unlock()
		if refuse != 0 {
			http.Error(w, "busy", refuse)
			return
		}
	}
	if m.Method != "initialize" {
		h.mu.Lock()
		lose := m.Method == "notifications/initialized" && h.lost > 0
		if lose {
			h.lost--
		}
		h.mu.Unlock()
		if lose || stale || !live {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		if r.Header.Get("MCP-Protocol-Version") != latestVersion {
			h.mu.Lock()
			h.missing = append(h.missing, m.Method)
			h.mu.Unlock()
		}
	}
	h.mu.Lock()
	catalog := h.catalog
	h.mu.Unlock()
	if m.Method == "tools/list" && catalog != nil {
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{
			"tools": []any{map[string]any{"name": catalog[min(opened, len(catalog))-1], "inputSchema": map[string]any{"type": "object"}}},
		}})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
		return
	}
	if len(m.ID) == 0 || m.Method == "" {
		h.fake.handle(m)
		w.WriteHeader(http.StatusAccepted)
		return
	}
	ch := make(chan message, 1)
	h.mu.Lock()
	h.waiting[string(m.ID)] = ch
	h.mu.Unlock()
	if m.Method == "initialize" {
		h.mu.Lock()
		id := fmt.Sprintf("sess-%d", h.inits)
		h.session, h.live[id] = id, h.inits
		h.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", id)
	}
	go h.fake.handle(m)
	var reply message
	select {
	case reply = <-ch:
	case <-r.Context().Done():
		return
	}
	out, _ := json.Marshal(reply)
	if m.Method == "tools/call" {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, ": comment\n\nevent: other\ndata: {}\n\nid: 1\ndata: %s\n\n", out)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(out)
}

func TestHTTP(t *testing.T) {
	h, srv := newHTTPFake(t)
	spec := Spec{HTTP: httpSpec(srv)}
	c := start(t, spec, Options{})
	ctx := context.Background()
	tools, _, err := c.ListTools(ctx)
	if err != nil || len(tools) != 4 {
		t.Fatalf("list = %v, %v", tools, err)
	}
	res, err := c.CallTool(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || res.Content[0].Text != `{"text":"hi"}` {
		t.Fatalf("echo = %+v, %v", res, err)
	}
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	if _, err := c.CallTool(short, "slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow err = %v", err)
	}
	cancel()
	h.mu.Lock()
	missing := h.missing
	h.mu.Unlock()
	if len(missing) > 0 {
		t.Fatalf("requests without a protocol header: %q", missing)
	}
	c.Close()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.deleted {
		t.Fatal("the session was not deleted on close")
	}
}

func TestHTTPStaleSession(t *testing.T) {
	h, srv := newHTTPFake(t)
	c := start(t, Spec{HTTP: httpSpec(srv)}, Options{})
	h.mu.Lock()
	h.stale = true
	h.mu.Unlock()
	if err := c.Ping(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPRefusedCredential(t *testing.T) {
	_, srv := newHTTPFake(t)
	_, err := Start(context.Background(), Spec{HTTP: &HTTPSpec{URL: srv.URL}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPRefusesAddress(t *testing.T) {
	if _, err := Start(context.Background(), Spec{HTTP: &HTTPSpec{URL: "ftp://x"}}, Options{}); err == nil {
		t.Fatal("accepted an ftp address")
	}
}

// A session found ended on a notification ends the client as well, so no
// later request goes out without a session.
func TestHTTPEndedSessionOnANotification(t *testing.T) {
	h, srv := newHTTPFake(t)
	c := start(t, Spec{HTTP: httpSpec(srv)}, Options{})
	forgetSession(h)
	_ = c.send(context.Background(), message{Method: "notifications/cancelled", Params: json.RawMessage(`{}`)})
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not end")
	}
	if !c.Expired() {
		t.Error("the client did not mark the session ended")
	}
}

// Once the session is known to be ended, nothing more goes out on it: a
// request would otherwise reach the server with no session id.
func TestHTTPSendsNothingOnAnEndedSession(t *testing.T) {
	h, srv := newHTTPFake(t)
	tr, err := newHTTP(*httpSpec(srv))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	tr.mu.Lock()
	tr.session = "sess-1"
	tr.mu.Unlock()
	h.mu.Lock()
	h.session = "other"
	h.mu.Unlock()
	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/x"}`)); !errors.Is(err, errSessionExpired) {
		t.Fatalf("first send = %v", err)
	}
	if err := tr.Send(context.Background(), []byte(`{"jsonrpc":"2.0","id":9,"method":"ping"}`)); !errors.Is(err, errSessionExpired) {
		t.Errorf("a send after the session ended = %v, want it refused before going out", err)
	}
}

// A call already sent when another request finds the session ended fails,
// but never as an ended session, which would have it sent again on a new one.
func TestHTTPCallInFlightWhenTheSessionEnds(t *testing.T) {
	h, srv := newHTTPFake(t)
	c := start(t, Spec{HTTP: httpSpec(srv)}, Options{})
	done := make(chan error, 1)
	go func() {
		_, err := c.CallTool(context.Background(), "slow", nil)
		done <- err
	}()
	waitFor(t, func() bool {
		h.fake.mu.Lock()
		defer h.fake.mu.Unlock()
		return len(h.fake.slow) == 1
	})
	forgetSession(h)
	if err := c.Ping(context.Background()); !errors.Is(err, errSessionExpired) {
		t.Fatalf("ping = %v", err)
	}
	select {
	case err := <-done:
		if err == nil || errors.Is(err, errSessionExpired) {
			t.Fatalf("call in flight = %v, want a failure other than an ended session", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call outlived the session")
	}
}
