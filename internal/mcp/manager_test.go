package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestCatalogKeyIsReusedAcrossStatusReads(t *testing.T) {
	s := fakeServer("local")
	m := newTestManager(t, s)
	seedCache(t, m, s, "saved")
	path := filepath.Join(m.cacheDir, ".catalog-key")
	if err := os.Rename(path, path+".aside"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := stateOf(m, s.Name).Tools; len(got) != 1 || got[0] != "saved" {
			t.Fatalf("Cached catalog = %v", got)
		}
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("Status recreated the key: %v", err)
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
