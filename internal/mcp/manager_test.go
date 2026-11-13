package mcp

import (
	"context"
	"encoding/json"
	"errors"
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
	m.Configure([]Server{fakeServer("fake")})
	m.Tools(context.Background(), "")
	m.Close()

	broken := fakeServer("fake")
	broken.Command = "/no/such/server"
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
		return m.dirty["fake"]
	})
	m.Tools(ctx, "")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirty["fake"] {
		t.Fatal("the list was not read again")
	}
}

func TestManagerNamesAndOrder(t *testing.T) {
	missing := func(n string) Server {
		return Server{Name: n, Enabled: true, Transport: TransportStdio, Command: "/no/such"}
	}
	m := newTestManager(t, missing("b"), missing("a"))
	long := strings.Repeat("d", maxDescription+10)
	m.tools["a"] = []Tool{{Name: "z"}, {Name: "x.y", Description: long}, {Name: "x_y"}, {Name: strings.Repeat("n", 70)}}
	m.tools["b"] = []Tool{{Name: "t", Title: "Titled"}}
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
