package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeServer(name string) Server {
	return Server{
		Name: name, Enabled: true, Transport: TransportStdio,
		Command: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$"},
		Env: map[string]string{"MCP_FAKE": "ok"},
	}
}

// httpServer is a server record named remote that reaches the HTTP fake.
func httpServer(srv *httptest.Server) Server {
	s := httpSpec(srv)
	return Server{Name: "remote", Enabled: true, Transport: TransportHTTP, URL: s.URL, Headers: s.Headers}
}

func newTestManager(t *testing.T, servers ...Server) *Manager {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(filepath.Join(dir, "mcp"), dir, "test")
	m.Configure(servers)
	t.Cleanup(m.Close)
	return m
}

func wires(offered []Offered) string {
	var out []string
	for _, o := range offered {
		out = append(out, o.Wire)
	}
	return strings.Join(out, ",")
}

func stateOf(m *Manager, name string) ServerStatus {
	for _, s := range m.Status() {
		if s.Name == name {
			return s
		}
	}
	return ServerStatus{}
}

func TestManagerLazyStartAndCache(t *testing.T) {
	m := newTestManager(t, fakeServer("fake"))
	if st := stateOf(m, "fake"); st.State != StateIdle || len(st.Tools) != 0 {
		t.Fatalf("before a turn: %+v", st)
	}
	ctx := context.Background()
	offered, notes := m.Tools(ctx, "/p")
	if got := wires(offered); got != "mcp__fake__echo,mcp__fake__image,mcp__fake__noschema,mcp__fake__slow" || len(notes) != 0 {
		t.Fatalf("tools = %s, notes %q", got, notes)
	}
	if st := stateOf(m, "fake"); st.State != StateRunning || st.Running != 1 {
		t.Fatalf("after a turn: %+v", st)
	}
	if _, err := os.Stat(m.cachePath("fake")); err != nil {
		t.Fatalf("no cache: %v", err)
	}
	r, err := m.Call(ctx, "/p", "fake", "echo", json.RawMessage(`{"text":"x"}`), time.Minute)
	if err != nil || r.Text != `{"text":"x"}` {
		t.Fatalf("call = %+v, %v", r, err)
	}
	if _, err := m.Call(ctx, "/p", "nope", "echo", nil, time.Minute); err == nil {
		t.Fatal("called a server that is not configured")
	}
}

func TestManagerCrashKeepsSchemas(t *testing.T) {
	old := backoff
	backoff = []time.Duration{300 * time.Millisecond}
	t.Cleanup(func() { backoff = old })

	m := newTestManager(t, fakeServer("fake"))
	ctx := context.Background()
	m.Tools(ctx, "")
	if _, err := m.Call(ctx, "", "fake", "die", nil, time.Minute); err == nil || !strings.Contains(err.Error(), "stopped during the call") {
		t.Fatalf("die err = %v", err)
	}
	waitFor(t, func() bool { return stateOf(m, "fake").State == StateFailed })
	if offered, _ := m.Tools(ctx, ""); len(offered) != 4 {
		t.Fatalf("a crashed server dropped its tools: %s", wires(offered))
	}
	if _, err := m.Call(ctx, "", "fake", "echo", nil, time.Minute); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("call inside the backoff = %v", err)
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := m.Call(ctx, "", "fake", "echo", nil, time.Minute); err != nil {
		t.Fatalf("call after the backoff = %v", err)
	}
}

func TestManagerDiskCache(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "mcp")
	m := NewManager(cache, dir, "test")
	_, srv := newHTTPFake(t)
	broken := httpServer(srv)
	broken.Name = "fake"
	m.Configure([]Server{broken})
	m.Tools(context.Background(), "")
	m.Close()
	srv.Close()
	other := broken
	other.Name = "other"
	m2 := NewManager(cache, dir, "test")
	m2.Configure([]Server{broken, other})
	defer m2.Close()
	offered, notes := m2.Tools(context.Background(), "")
	if len(offered) != 4 || len(notes) != 1 || !strings.Contains(notes[0], "other") {
		t.Fatalf("tools = %s, notes %q", wires(offered), notes)
	}
	if st := stateOf(m2, "fake"); st.State != StateFailed || len(st.Tools) != 4 {
		t.Fatalf("status = %+v", st)
	}
}

func TestManagerRefreshesTheDiskListAfterAnOutage(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"unused", "tested", "running"}
	h.initStatus = http.StatusServiceUnavailable
	s := httpServer(srv)
	m := newTestManager(t, s)
	seedCache(t, m, s, "saved")
	ctx := context.Background()
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__saved" {
		t.Fatalf("offer during the outage = %s", wires(offered))
	}
	h.mu.Lock()
	h.initStatus = 0
	h.mu.Unlock()
	if _, _, _, err := m.Probe(ctx, s, ""); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(m, s.Name).Tools; len(got) != 1 || got[0] != "tested" {
		t.Errorf("settings after Test = %v, want [tested]", got)
	}
	waitFor(t, func() bool {
		offered, _ := m.Tools(ctx, "")
		return wires(offered) == "mcp__remote__running"
	})
}

func TestManagerPerProject(t *testing.T) {
	s := fakeServer("fake")
	s.Cwd = "{project}"
	m := newTestManager(t, s)
	ctx := context.Background()
	a, b := t.TempDir(), t.TempDir()
	ra, err := m.Call(ctx, a, "fake", "whoami", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := m.Call(ctx, b, "fake", "whoami", nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ea, _ := filepath.EvalSymlinks(a)
	eb, _ := filepath.EvalSymlinks(b)
	if !strings.HasSuffix(ra.Text, ea) || !strings.HasSuffix(rb.Text, eb) {
		t.Fatalf("a = %s, b = %s", ra.Text, rb.Text)
	}
	if st := stateOf(m, "fake"); st.Running != 2 {
		t.Fatalf("status = %+v", st)
	}
}

func TestManagerConfigureRestartsOnChange(t *testing.T) {
	s := fakeServer("fake")
	m := newTestManager(t, s)
	ctx := context.Background()
	m.Tools(ctx, "")

	s.Allow = []string{"echo"}
	m.Configure([]Server{s})
	if st := stateOf(m, "fake"); st.State != StateRunning {
		t.Fatalf("an allow change stopped the server: %+v", st)
	}
	s.Args = append(s.Args, "-test.v=false")
	m.Configure([]Server{s})
	if st := stateOf(m, "fake"); st.State != StateIdle {
		t.Fatalf("a changed command kept the server: %+v", st)
	}
	s.Enabled = false
	m.Configure([]Server{s})
	if m.Enabled() {
		t.Fatal("a disabled server counts as enabled")
	}
	if offered, _ := m.Tools(ctx, ""); offered != nil {
		t.Fatalf("a disabled server offered %s", wires(offered))
	}
}

func TestManagerListChanged(t *testing.T) {
	m := newTestManager(t, fakeServer("fake"))
	ctx := context.Background()
	m.Tools(ctx, "")
	if _, err := m.Call(ctx, "", "fake", "notify", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.dirty[instKey{server: "fake"}]
	})
	m.Tools(ctx, "")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirty[instKey{server: "fake"}] {
		t.Fatal("the list was not read again")
	}
}

func TestManagerNamesAndOrder(t *testing.T) {
	missing := func(n string) Server {
		return Server{Name: n, Enabled: true, Transport: TransportStdio, Command: "/no/such"}
	}
	m := newTestManager(t, missing("b"), missing("a"))
	long := strings.Repeat("d", maxDescription+10)
	m.tools[instKey{server: "a"}] = []Tool{{Name: "z"}, {Name: "x.y", Description: long}, {Name: "x_y"}, {Name: strings.Repeat("n", 70)}}
	m.tools[instKey{server: "b"}] = []Tool{{Name: "t", Title: "Titled"}}
	offered, notes := m.Tools(context.Background(), "")
	if got := wires(offered); got != "mcp__a__x_y,mcp__a__z,mcp__b__t" {
		t.Fatalf("order = %s", got)
	}
	if offered[0].Tool != "x.y" || len([]rune(offered[0].Description)) != maxDescription+1 {
		t.Fatalf("first = %+v", offered[0])
	}
	if offered[2].Description != "Titled" {
		t.Fatalf("title fallback = %q", offered[2].Description)
	}
	if len(notes) != 2 {
		t.Fatalf("notes = %q", notes)
	}
}

func TestManagerClose(t *testing.T) {
	m := newTestManager(t, fakeServer("fake"))
	m.Tools(context.Background(), "")
	m.Close()
	if _, err := m.Call(context.Background(), "", "fake", "echo", nil, time.Minute); err == nil {
		t.Fatal("a call ran after close")
	}
}

func TestManagerProbe(t *testing.T) {
	m := newTestManager(t, fakeServer("fake"))
	info, tools, notes, err := m.Probe(context.Background(), fakeServer("fake"), "")
	if err != nil || info.Name != "fake" || len(tools) != 4 || len(notes) != 3 {
		t.Fatalf("probe = %+v %d %q %v", info, len(tools), notes, err)
	}
	if st := stateOf(m, "fake"); st.State != StateIdle || len(st.Tools) != 4 {
		t.Fatalf("probe should fill the list and leave nothing running: %+v", st)
	}
}

// A test result belongs to the launch settings that produced it.
func TestManagerDropsProbeWhenTheLaunchChanges(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"first"}
	s := httpServer(srv)
	m := newTestManager(t, s)
	ctx := context.Background()
	if _, _, _, err := m.Probe(ctx, s, ""); err != nil {
		t.Fatal(err)
	}
	s.Allow = []string{"first"}
	m.Configure([]Server{s})
	if got := stateOf(m, s.Name).Tools; len(got) != 1 || got[0] != "first" {
		t.Fatalf("tools after an allow change = %v", got)
	}
	original := s
	s.URL += "/changed"
	m.Configure([]Server{s})
	if got := stateOf(m, s.Name).Tools; len(got) != 0 {
		t.Errorf("tools after a URL change = %v", got)
	}
	m.Configure([]Server{original})
	if _, _, _, err := m.Probe(ctx, original, ""); err != nil {
		t.Fatal(err)
	}
	m.Configure(nil)
	m.Configure([]Server{original})
	if got := stateOf(m, original.Name).Tools; len(got) != 0 {
		t.Errorf("tools after removal and re-addition = %v", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A server that forgot the session, as after its own restart, gets a new one
// and the call still runs, without the server being counted as failed.
func TestManagerRenewsAnEndedSession(t *testing.T) {
	h, srv := newHTTPFake(t)
	m := newTestManager(t, httpServer(srv))
	ctx := context.Background()
	if _, err := m.Call(ctx, "", "remote", "echo", json.RawMessage(`{"text":"a"}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	forgetSession(h)
	r, err := m.Call(ctx, "", "remote", "echo", json.RawMessage(`{"text":"b"}`), time.Minute)
	if err != nil || r.Text != `{"text":"b"}` {
		t.Fatalf("after the server forgot the session: %+v, %v", r, err)
	}
	if st := stateOf(m, "remote"); st.State != StateRunning || st.Error != "" {
		t.Errorf("state = %+v, want running with no error", st)
	}
}

// A session that ended under a tools/list is renewed there too, and leaves
// the server ready for the next call rather than failed.
func TestManagerRenewsASessionEndedUnderAList(t *testing.T) {
	h, srv := newHTTPFake(t)
	m := newTestManager(t, httpServer(srv))
	ctx := context.Background()
	if offered, _ := m.Tools(ctx, ""); len(offered) != 4 {
		t.Fatalf("first list = %d tools", len(offered))
	}
	forgetSession(h)
	m.mu.Lock()
	m.dirty[instKey{server: "remote"}] = true
	m.mu.Unlock()
	if offered, _ := m.Tools(ctx, ""); len(offered) != 4 {
		t.Fatalf("list after the session ended = %d tools", len(offered))
	}
	if _, err := m.Call(ctx, "", "remote", "echo", json.RawMessage(`{"text":"c"}`), time.Minute); err != nil {
		t.Fatalf("the call after the list = %v", err)
	}
	if st := stateOf(m, "remote"); st.State != StateRunning || st.Error != "" {
		t.Errorf("state = %+v", st)
	}
}

// A renewed session may list other tools. The turn under way keeps its
// offer and the next one lists again from the new session.
func TestManagerListsAgainAfterARenewedSession(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"a", "b"}
	m := newTestManager(t, httpServer(srv))
	ctx := context.Background()
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__a" {
		t.Fatalf("first offer = %s", wires(offered))
	}
	forgetSession(h)
	if _, err := m.Call(ctx, "", "remote", "echo", nil, time.Minute); err != nil {
		t.Fatalf("call across the renewal = %v", err)
	}
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__b" {
		t.Fatalf("offer after the renewal = %s", wires(offered))
	}
}

// A 404 on notifications/initialized ends a session that was never used, so
// the start runs initialize once more rather than counting a failure.
func TestManagerStartsAgainWhenInitializedIsRefused(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.lost = 1
	m := newTestManager(t, httpServer(srv))
	ctx := context.Background()
	if _, err := m.Call(ctx, "", "remote", "echo", json.RawMessage(`{"text":"a"}`), time.Minute); err != nil {
		t.Fatalf("call = %v", err)
	}
	if st := stateOf(m, "remote"); st.State != StateRunning || st.Error != "" {
		t.Errorf("state = %+v, want running", st)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inits != 2 {
		t.Errorf("initializes = %d, want 2", h.inits)
	}
}

// Probe gets the same one more initialize, and no more than one.
func TestManagerProbeStartsAgainWhenInitializedIsRefused(t *testing.T) {
	h, srv := newHTTPFake(t)
	s := httpServer(srv)
	m := newTestManager(t, s)
	ctx := context.Background()
	h.lost = 1
	if _, tools, _, err := m.Probe(ctx, s, ""); err != nil || len(tools) != 4 {
		t.Fatalf("probe = %d tools, %v", len(tools), err)
	}
	h.mu.Lock()
	h.lost, h.inits = 3, 0
	h.mu.Unlock()
	if _, _, _, err := m.Probe(ctx, s, ""); !errors.Is(err, errSessionExpired) {
		t.Fatalf("probe with every session lost = %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inits != 2 {
		t.Errorf("initializes = %d, want 2", h.inits)
	}
}

// A late 404 from a client already replaced leaves the instance as the new
// client's start left it: a refused initialize keeps its retry wait.
func TestManagerKeepsTheWaitAfterALateEndedSession(t *testing.T) {
	h, srv := newHTTPFake(t)
	s := httpServer(srv)
	m := newTestManager(t, s)
	ctx := context.Background()
	if _, err := m.Call(ctx, "", "remote", "echo", json.RawMessage(`{"text":"a"}`), time.Minute); err != nil {
		t.Fatal(err)
	}
	in := m.instanceFor(s, "")
	in.mu.Lock()
	old := in.client
	in.mu.Unlock()
	forgetSession(h)
	h.mu.Lock()
	h.initStatus = http.StatusServiceUnavailable
	h.mu.Unlock()
	if _, err := m.Call(ctx, "", "remote", "echo", json.RawMessage(`{"text":"b"}`), time.Minute); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("call with initialize refused = %v", err)
	}
	if _, err := old.CallTool(ctx, "echo", nil); !errors.Is(err, errSessionExpired) {
		t.Fatalf("second call on the old client = %v", err)
	}
	in.forget(old)
	if st := stateOf(m, "remote"); st.State != StateFailed {
		t.Fatalf("state after the late 404 = %+v, want failed", st)
	}
	h.mu.Lock()
	before := h.inits
	h.mu.Unlock()
	if _, err := m.Call(ctx, "", "remote", "echo", nil, time.Minute); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("call inside the wait = %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inits != before {
		t.Errorf("an initialize went out inside the retry wait")
	}
}

// A session that ends on a notification, with no call to renew it, still has
// the next offer list the tools again.
func TestManagerListsAgainAfterASessionEndsOnItsOwn(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"a", "b"}
	m := newTestManager(t, httpServer(srv))
	ctx := context.Background()
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__a" {
		t.Fatalf("first offer = %s", wires(offered))
	}
	m.mu.Lock()
	var c *Client
	for k, in := range m.inst {
		if k.server == "remote" {
			in.mu.Lock()
			c = in.client
			in.mu.Unlock()
		}
	}
	m.mu.Unlock()
	if c == nil {
		t.Fatal("no running client")
	}
	forgetSession(h)
	_ = c.send(ctx, message{Method: "notifications/cancelled", Params: json.RawMessage(`{}`)})
	waitFor(t, func() bool { return stateOf(m, "remote").State == StateIdle })
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__b" {
		t.Fatalf("offer after the session ended = %s", wires(offered))
	}
}

// Test opens a session of its own, so the list it gets does not stand in for
// the one the running session owes after a renewal.
func TestManagerTestDoesNotReplaceTheListARenewalOwes(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"a", "b", "c"}
	s := httpServer(srv)
	m := newTestManager(t, s)
	ctx := context.Background()
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__a" {
		t.Fatalf("first offer = %s", wires(offered))
	}
	forgetSession(h)
	if _, err := m.Call(ctx, "", "remote", "echo", nil, time.Minute); err != nil {
		t.Fatalf("call across the renewal = %v", err)
	}
	if _, _, _, err := m.Probe(ctx, s, ""); err != nil {
		t.Fatal(err)
	}
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__b" {
		t.Fatalf("offer after the renewal and a test = %s, want the running session's", wires(offered))
	}
}

// The same with a turn between the renewal and Test: the turn takes the
// renewed list, and Test still does not replace it while that session runs.
func TestManagerTestAfterATurnKeepsTheRunningList(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"a", "b", "c"}
	s := httpServer(srv)
	m := newTestManager(t, s)
	ctx := context.Background()
	m.Tools(ctx, "")
	forgetSession(h)
	if _, err := m.Call(ctx, "", "remote", "echo", nil, time.Minute); err != nil {
		t.Fatalf("call across the renewal = %v", err)
	}
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__b" {
		t.Fatalf("offer after the renewal = %s", wires(offered))
	}
	if _, _, _, err := m.Probe(ctx, s, ""); err != nil {
		t.Fatal(err)
	}
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__b" {
		t.Fatalf("offer after a test = %s, want the running session's", wires(offered))
	}
}

// Test before anything ran: its list shows in the settings, and the first
// offer comes from the session turns run on.
func TestManagerOffersTheRunningListAfterAFirstTest(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"a", "b"}
	s := httpServer(srv)
	m := newTestManager(t, s)
	ctx := context.Background()
	if _, _, _, err := m.Probe(ctx, s, ""); err != nil {
		t.Fatal(err)
	}
	if st := stateOf(m, "remote"); len(st.Tools) != 1 || st.Tools[0] != "a" {
		t.Fatalf("settings after Test = %v, want Test's list", st.Tools)
	}
	if _, err := m.Call(ctx, "", "remote", "echo", nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	if offered, _ := m.Tools(ctx, ""); wires(offered) != "mcp__remote__b" {
		t.Fatalf("offer = %s, want the running session's", wires(offered))
	}
}

func TestToolsBelongToTheConfiguredLaunch(t *testing.T) {
	a, first := newHTTPFake(t)
	b, second := newHTTPFake(t)
	a.catalog, b.catalog = []string{"alpha"}, []string{"beta"}
	m := newTestManager(t, httpServer(first))
	ctx := context.Background()
	if got, _ := m.Tools(ctx, ""); wires(got) != "mcp__remote__alpha" {
		t.Fatal(wires(got))
	}
	s := httpServer(second)
	offline := NewManager(m.cacheDir, m.logDir, "test")
	offline.Configure([]Server{s})
	defer offline.Close()
	if got := stateOf(offline, s.Name).Tools; len(got) != 0 {
		t.Errorf("Catalog after restart with a different launch: %v", got)
	}
	offline.Close()
	m.Configure([]Server{s})
	if _, err := os.Stat(m.cachePath(s.Name)); !os.IsNotExist(err) {
		t.Fatalf("Previous cache remains: %v", err)
	}
	if _, _, _, err := m.Probe(ctx, s, ""); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(m, s.Name).Tools; len(got) != 1 || got[0] != "beta" {
		t.Errorf("Settings after replacement: %v", got)
	}
	if got, _ := m.Tools(ctx, ""); wires(got) != "mcp__remote__beta" {
		t.Errorf("Offer after replacement: %s", wires(got))
	}
}

func TestReplacementSessionExpiryWaits(t *testing.T) {
	for _, method := range []string{"tools/list", "tools/call"} {
		t.Run(method, func(t *testing.T) {
			h, srv := newHTTPFake(t)
			h.expireMethod = method
			m := newTestManager(t, httpServer(srv))
			request := func() {
				if method == "tools/list" {
					m.Tools(context.Background(), "")
				} else {
					m.Call(context.Background(), "", "remote", "echo", nil, time.Second)
				}
			}
			request()
			request()
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.inits != 2 || stateOf(m, "remote").State != StateFailed {
				t.Errorf("Initializations = %d, state = %v", h.inits, stateOf(m, "remote").State)
			}
		})
	}
}

func TestListChangedDuringListIsFetchedNextTurn(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"old"}
	h.notifyDuringList = true
	m := newTestManager(t, httpServer(srv))
	m.Tools(context.Background(), "")
	if got, _ := m.Tools(context.Background(), ""); wires(got) != "mcp__remote__updated" {
		t.Errorf("Second offer = %s", wires(got))
	}
}

func TestClosedManagerExplainsWhyToolsAreUnavailable(t *testing.T) {
	s := fakeServer("fake")
	m := newTestManager(t, s)
	m.Tools(context.Background(), "")
	m.Close()
	tools, notes := m.Tools(context.Background(), "")
	if len(tools) != 0 || !strings.Contains(strings.Join(notes, " "), "stopping") {
		t.Fatalf("Closed manager: %d tools, %v", len(tools), notes)
	}
}

func TestProjectCatalogsSurviveSeparateLaunches(t *testing.T) {
	s := fakeServer("local")
	s.Env = map[string]string{"MCP_FAKE": "project", "MCP_TOOL": "{project}"}
	m := newTestManager(t, s)
	for _, project := range []string{"alpha", "beta", "alpha"} {
		if offered, _ := m.Tools(context.Background(), project); wires(offered) != "mcp__local__"+project {
			t.Fatalf("Project %s offered %s", project, wires(offered))
		}
	}
	other := NewManager(m.cacheDir, m.logDir, "test")
	defer other.Close()
	for _, project := range []string{"alpha", "beta"} {
		k := keyFor(s, project)
		tools, ok := other.readCache(s, k)
		if !ok || len(tools) != 1 || tools[0].Name != project {
			t.Fatalf("Disk catalog for %s = %v", project, tools)
		}
	}
	m.invalidate(m.instanceFor(s, "beta"))
	m.mu.Lock()
	dirty := m.dirty[keyFor(s, "alpha")]
	m.mu.Unlock()
	if dirty {
		t.Fatal("Changed another project's catalog")
	}
	m.Configure(nil)
	for _, project := range []string{"alpha", "beta"} {
		if _, err := os.Stat(m.catalogPath(keyFor(s, project))); !os.IsNotExist(err) {
			t.Fatalf("Evicted project cache = %v", err)
		}
	}

}

func TestRPCErrorBreaksConsecutiveExpirations(t *testing.T) {
	h, srv := newHTTPFake(t)
	m := newTestManager(t, httpServer(srv))
	ctx := context.Background()
	if _, err := m.Call(ctx, "", "remote", "echo", nil, time.Second); err != nil {
		t.Fatal(err)
	}
	forgetSession(h)
	if _, err := m.Call(ctx, "", "remote", "unknown", nil, time.Second); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("Remote error = %v", err)
	}
	forgetSession(h)
	if _, err := m.Call(ctx, "", "remote", "echo", nil, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyLaunchFieldsPreserveCache(t *testing.T) {
	s := fakeServer("local")
	s.Env = map[string]string{}
	m := newTestManager(t, s)
	seedCache(t, m, s, "kept")
	s.Env = nil
	m.Configure([]Server{s})
	if _, err := os.Stat(m.cachePath(s.Name)); err != nil {
		t.Fatal(err)
	}
}

func seedCache(t *testing.T, m *Manager, s Server, name string) {
	t.Helper()
	k := keyFor(s, "")
	tmp := m.prepareCache(s, k, []Tool{{Name: name}})
	if err := os.Rename(tmp, m.catalogPath(k)); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchFingerprintIsPrivateToTheCache(t *testing.T) {
	s := Server{Name: "test", Transport: TransportHTTP, URL: "https://localhost", Headers: map[string]string{"Authorization": "test-credential"}}
	a, b := newTestManager(t, s), newTestManager(t, s)
	k := keyFor(s, "")
	digest := a.launchDigest(s, k)
	if digest == "" || digest == b.launchDigest(s, k) {
		t.Fatal("Independent cache keys produced the same fingerprint")
	}
	info, err := os.Stat(filepath.Join(a.cacheDir, ".catalog-key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("Missing private key", err)
	}
	s.Headers["Authorization"] = "rotated"
	if digest == a.launchDigest(s, k) {
		t.Fatal("Rotating the credential retained the fingerprint")
	}
}

func TestStatusWaitsForThePrivateKeyToReturn(t *testing.T) {
	s := fakeServer("local")
	m := newTestManager(t, s)
	seedCache(t, m, s, "saved")
	path := filepath.Join(m.cacheDir, ".catalog-key")
	if err := os.Rename(path, path+".aside"); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(m, s.Name); got.Listed {
		t.Fatalf("Listed with missing key: %+v", got)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("Status recreated the key: %v", err)
	}
	if err := os.Rename(path+".aside", path); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(m, s.Name).Tools; len(got) != 1 || got[0] != "saved" {
		t.Fatal(got)
	}
}

func TestCatalogKeyFailureReportsTheRepairOnceAndRetries(t *testing.T) {
	s := fakeServer("local")
	m := newTestManager(t, s)
	if err := os.MkdirAll(m.cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.cacheDir, ".catalog-key")
	if err := os.WriteFile(path, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(previous)
	for range 2 {
		if tmp := m.prepareCache(s, keyFor(s, ""), []Tool{{Name: "saved"}}); tmp != "" {
			t.Fatal("Published with a damaged key")
		}
	}
	if !strings.Contains(out.String(), path) || strings.Count(out.String(), "MCP disk cache") != 1 {
		t.Fatalf("Diagnostic = %q", out.String())
	}
	if err := os.Rename(path, path+".aside"); err != nil {
		t.Fatal(err)
	}
	seedCache(t, m, s, "recovered")
	if got := stateOf(m, s.Name).Tools; len(got) != 1 || got[0] != "recovered" {
		t.Fatalf("Recovered catalog = %v", got)
	}
}

func TestCatalogEvictionUsesLiteralNames(t *testing.T) {
	base := t.TempDir()
	s := fakeServer("local")
	dir := filepath.Join(base, "state[copy]")
	other := filepath.Join(base, "statec")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir, "", "test")
	defer m.Close()
	m.Configure([]Server{s})
	k := instKey{s.Name, "work"}
	tmp := m.prepareCache(s, k, []Tool{{Name: "saved"}})
	if err := os.Rename(tmp, m.catalogPath(k)); err != nil {
		t.Fatal(err)
	}
	keep := []string{filepath.Join(dir, "local.notes.json"), filepath.Join(dir, "local."+strings.Repeat("z", 64)+".json"), filepath.Join(other, filepath.Base(m.catalogPath(k)))}
	for _, p := range keep {
		if err := os.WriteFile(p, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m.Configure(nil)
	if _, err := os.Stat(m.catalogPath(k)); !os.IsNotExist(err) {
		t.Errorf("Catalog was retained: %v", err)
	}
	for _, p := range keep {
		if b, err := os.ReadFile(p); err != nil || string(b) != "keep" {
			t.Errorf("Missing %s", p)
		}
	}
}

func TestProjectStatusSurvivesRestartAndProbe(t *testing.T) {
	s := fakeServer("local")
	s.Env = map[string]string{"MCP_FAKE": "project", "MCP_TOOL": "{project}"}
	m := newTestManager(t, s)
	if got, _ := m.Tools(context.Background(), "alpha"); wires(got) != "mcp__local__alpha" {
		t.Fatal(got)
	}
	if _, _, _, err := m.Probe(context.Background(), s, "beta"); err != nil {
		t.Fatal(err)
	}
	other := NewManager(m.cacheDir, m.logDir, "test")
	defer other.Close()
	other.Configure([]Server{s})
	st := stateOf(other, s.Name)
	if !st.Listed || st.Project != "beta" || len(st.Tools) != 1 || st.Tools[0] != "beta" {
		t.Fatalf("Latest project listing = %+v", st)
	}
	empty := other.instanceFor(s, "empty")
	if err := other.publishList(empty, []Tool{}, 0); err != nil {
		t.Fatal(err)
	}
	last := NewManager(m.cacheDir, m.logDir, "test")
	defer last.Close()
	last.Configure([]Server{s, fakeServer("unknown")})
	if known := stateOf(last, s.Name); !known.Listed || known.Project != "empty" || len(known.Tools) != 0 {
		t.Fatalf("Empty catalog = %+v", known)
	}
	if stateOf(last, "unknown").Listed {
		t.Fatal("Unknown catalog was listed")
	}
	if _, ok := other.readCache(s, keyFor(s, "beta")); ok {
		t.Fatal("Probe populated the turn catalog")
	}
}

func TestCatalogMaintenancePreservesUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s := fakeServer("local")
	data, err := json.Marshal(launchFields(s))
	if err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf(`{"launch":"%x","tools":[]}`, sha256.Sum256(data))
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("local.json", legacy)
	write(".catalog-123456", "incomplete")
	write(".catalog-notes", "keep")
	write("local.notes.json", "keep")
	m := NewManager(dir, "", "test")
	defer m.Close()
	if m.cacheKey() == nil {
		t.Fatal("Could not create a private catalog key")
	}
	m.Configure([]Server{s})
	for _, name := range []string{"local.json", ".catalog-123456"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("Stale file %s: %v", name, err)
		}
	}
	for _, name := range []string{".catalog-notes", "local.notes.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	s.Args = append(s.Args, "{project}")
	m.Configure([]Server{s})
	for i := range 40 {
		if i == maxProjectCatalogs {
			if _, ok := m.readCache(s, keyFor(s, "0")); !ok {
				t.Fatal("Missing catalog before eviction")
			}
		}
		in := m.instanceFor(s, fmt.Sprint(i))
		if err := m.publishList(in, []Tool{}, 0); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(1600000000+int64(i), 0)
		if err := os.Chtimes(m.catalogPath(in.key), at, at); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for i := range 40 {
		_, err := os.Lstat(m.catalogPath(keyFor(s, fmt.Sprint(i))))
		want := i == 0 || i >= 9
		if (err == nil) != want || err != nil && !os.IsNotExist(err) {
			t.Errorf("Project %d: wanted retained=%v, stat=%v", i, want, err)
		}
		if err == nil {
			count++
		}
	}
	if count != 32 {
		t.Fatalf("Retained %d catalogs", count)
	}
}

func TestConcurrentCatalogCreatorsUseTheSameKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	keys := make(chan []byte, 12)
	for range cap(keys) {
		go func() {
			m := NewManager(dir, "", "test")
			defer m.Close()
			<-start
			keys <- m.cacheKey()
		}()
	}
	close(start)
	first := <-keys
	if len(first) != 32 {
		t.Fatalf("Key length = %d", len(first))
	}
	for i := 1; i < cap(keys); i++ {
		if !bytes.Equal(first, <-keys) {
			t.Fatal("Concurrent creators used different keys")
		}
	}
}

func TestStatusFindsOlderProjectCatalogs(t *testing.T) {
	s := fakeServer("local")
	s.Cwd = "{project}"
	m := newTestManager(t, s)
	now := time.Now()
	for i, project := range []string{"alpha", "beta", "obsolete"} {
		k := keyFor(s, project)
		digest := m.launchDigest(s, k)
		if project == "obsolete" {
			digest = strings.Repeat("0", 64)
		}
		raw, err := json.Marshal(map[string]any{"launch": digest, "tools": []Tool{{Name: project}}})
		if err != nil {
			t.Fatal(err)
		}
		path := m.catalogPath(k)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		at := now.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	other := NewManager(m.cacheDir, m.logDir, "test")
	defer other.Close()
	other.Configure([]Server{s})
	st := other.Status("obsolete", "alpha", "beta")[0]
	if !st.Listed || st.Project != "beta" || len(st.Tools) != 1 || st.Tools[0] != "beta" {
		t.Fatalf("Older project catalog = %+v", st)
	}
}

func TestStartupDropsCatalogsOfRemovedServers(t *testing.T) {
	shared, project, kept := fakeServer("shared"), fakeServer("local"), fakeServer("kept")
	project.Cwd = "{project}"
	m := newTestManager(t, shared, project, kept)
	for _, srv := range []Server{shared, project, kept} {
		if err := m.publishList(m.instanceFor(srv, "alpha"), []Tool{{Name: "saved"}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	m.Close()
	other := NewManager(m.cacheDir, m.logDir, "test")
	defer other.Close()
	kept.Enabled = false
	doc := filepath.Join(m.cacheDir, "notes.json")
	if err := os.WriteFile(doc, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	other.Configure([]Server{kept})
	for _, p := range []string{m.cachePath("shared"), m.catalogPath(keyFor(project, "alpha")), m.statusPath("local")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("Obsolete catalog %s: %v", p, err)
		}
	}
	for _, p := range []string{doc, m.cachePath("kept"), filepath.Join(m.cacheDir, ".catalog-key")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSharedStatusUsesTheLastSuccessfulListing(t *testing.T) {
	h, srv := newHTTPFake(t)
	h.catalog = []string{"first", "second"}
	s := httpServer(srv)
	m := newTestManager(t, s)
	if got, _ := m.Tools(context.Background(), ""); wires(got) != "mcp__remote__first" {
		t.Fatal(got)
	}
	if _, _, _, err := m.Probe(context.Background(), s, ""); err != nil {
		t.Fatal(err)
	}
	fresh := NewManager(m.cacheDir, m.logDir, "test")
	defer fresh.Close()
	fresh.Configure([]Server{s})
	for _, current := range []*Manager{m, fresh} {
		if got := stateOf(current, s.Name).Tools; len(got) != 1 || got[0] != "second" {
			t.Fatalf("Latest listing = %v", got)
		}
		if got, ok := current.readCache(s, keyFor(s, "")); !ok || got[0].Name != "first" {
			t.Fatalf("Turn catalog = %v", got)
		}
	}
}

func TestCatalogNamesAndLaunchesSurviveRestart(t *testing.T) {
	upper, lower := fakeServer("Local"), fakeServer("local")
	m := newTestManager(t, upper, lower)
	for _, s := range []Server{upper, lower} {
		seedCache(t, m, s, s.Name)
	}
	if strings.EqualFold(m.cachePath(upper.Name), m.cachePath(lower.Name)) {
		t.Fatal("Catalog paths collide on a case-insensitive volume")
	}
	fresh := func(servers ...Server) *Manager {
		n := NewManager(m.cacheDir, m.logDir, "test")
		t.Cleanup(n.Close)
		n.Configure(servers)
		return n
	}
	m.Close()
	n := fresh(upper, lower)
	for _, s := range []Server{upper, lower} {
		if got := stateOf(n, s.Name).Tools; len(got) != 1 || got[0] != s.Name {
			t.Fatal(got)
		}
	}
	alternate := upper.Clone()
	alternate.Args = append(alternate.Args, "other")
	n.Close()
	fresh(alternate, lower).Close()
	n = fresh(upper, lower)
	if stateOf(n, upper.Name).Listed || !stateOf(n, lower.Name).Listed {
		t.Fatal("Old launch survived or unrelated catalog was lost")
	}
}

func TestPublicCatalogKeyDisablesDiskCache(t *testing.T) {
	s := fakeServer("local")
	m := newTestManager(t, s)
	if err := os.MkdirAll(m.cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.cacheDir, ".catalog-key")
	if err := os.WriteFile(path, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if tmp := m.prepareCache(s, keyFor(s, ""), []Tool{}); tmp != "" {
		t.Fatalf("Published with a readable key: %s", tmp)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if m.cacheKey() == nil {
		t.Fatal("Private key was not usable after repair")
	}
}

func TestUnreadableRetiredCatalogNamesItsRecoveryPath(t *testing.T) {
	m := newTestManager(t)
	if err := os.MkdirAll(m.cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := m.cachePath("removed")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	var messages bytes.Buffer
	old := log.Writer()
	log.SetOutput(&messages)
	t.Cleanup(func() { log.SetOutput(old) })
	other := NewManager(m.cacheDir, m.logDir, "test")
	defer other.Close()
	other.Configure(nil)
	if !strings.Contains(messages.String(), path) {
		t.Fatalf("Missing record path in %s", messages.String())
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "{" {
		t.Fatalf("Recovery record = %q, %v", data, err)
	}
}

func TestSharedCatalogMigrationUsesTheLatestListing(t *testing.T) {
	s := fakeServer("Local")
	m := newTestManager(t, s)
	early := time.Unix(1500000000, 0)
	for i, item := range []struct{ name, tool string }{{".last-Local.json", "probe"}, {"Local.json", "turn"}} {
		tmp := m.prepareCache(s, keyFor(s, ""), []Tool{{Name: item.tool}})
		path := filepath.Join(m.cacheDir, item.name)
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
		at := early.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	other := NewManager(m.cacheDir, m.logDir, "test")
	defer other.Close()
	other.Configure([]Server{s, fakeServer("local")})
	if got := stateOf(other, s.Name).Tools; len(got) != 1 || got[0] != "turn" {
		t.Fatalf("Migrated listing = %v", got)
	}
	if stateOf(other, "local").Listed {
		t.Fatal("Legacy catalog assigned to both names")
	}
}
