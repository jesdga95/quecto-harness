package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/search"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

func scratchOf(t *testing.T, h *harness, s *Session) string {
	t.Helper()
	return h.engine.briefFor(context.Background(), s, h.cfg.Config()).scratch
}

// A conversation has its own scratch folder, and a subagent run shares its
// launcher's.
func TestASubagentSharesItsLaunchersScratch(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	child := subagentOf(t, h, parent)
	want, err := h.store.ScratchDir(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := scratchOf(t, h, parent); got != want {
		t.Errorf("launcher's scratch = %q, want %q", got, want)
	}
	if got := scratchOf(t, h, child); got != want {
		t.Errorf("subagent's scratch = %q, want its launcher's %q", got, want)
	}
	if h.store.SpillDir(parent.ID) == h.store.SpillDir(child.ID) {
		t.Error("the two share their saved output folder")
	}
}

// An owner whose id the harness did not make gets no scratch folder, so a
// session file cannot point the folder anywhere else.
func TestAnOwnerTheHarnessDidNotMakeHasNoScratch(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	s.mu.Lock()
	s.Parent = "../../escape"
	s.mu.Unlock()
	if got := scratchOf(t, h, s); got != "" {
		t.Errorf("scratch = %q, want none", got)
	}
}

// The system prompt is the same bytes from one turn to the next and in either
// mode, ends with the scratch line, and making it creates no folder.
func TestTheScratchLineIsStableAndPassive(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	render := func() string {
		b := h.engine.briefFor(context.Background(), s, h.cfg.Config())
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.systemMessage(h.agent, h.engine.os, h.engine.today(), b).Content
	}
	first := render()
	if again := render(); again != first {
		t.Error("the system prompt changed between two turns")
	}
	if err := h.engine.SetMode(s.ID, config.WorkPlan); err != nil {
		t.Fatal(err)
	}
	if planned := render(); planned != first {
		t.Error("the system prompt changed with the mode")
	}
	dir, _ := h.store.ScratchDir(s.ID)
	if !strings.HasSuffix(first, `use "$QUECTO_SCRATCH" in bash.`) || !strings.Contains(first, dir) {
		t.Errorf("the prompt does not end with the scratch line: %q", first[max(0, len(first)-300):])
	}
	if _, err := os.Stat(filepath.Dir(dir)); err == nil {
		t.Error("rendering the prompt made the session folder")
	}
}

// The policy reads a path under the scratch folder as scratch, by its words.
func TestPlaceOfAScratchPath(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	runner, err := tools.NewRunner(h.project, h.cfg.Config())
	if err != nil {
		t.Fatal(err)
	}
	runner.Hide(h.cfg.Paths().Home)
	runner.Scratch = scratchOf(t, h, s)
	cases := map[string]perms.Place{
		filepath.Join(runner.Scratch, "a.txt"): perms.InScratch,
		"file.txt":                             perms.InProject,
		"/etc/hosts":                           perms.Outside,
	}
	for path, want := range cases {
		if got := placeOf(runner, path); got != want {
			t.Errorf("placeOf(%s) = %d, want %d", path, got, want)
		}
	}
}

// Deleting a conversation removes its scratch folder; deleting one of its
// subagent runs leaves the folder it shares.
func TestTheScratchFolderGoesWithItsConversation(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	dir := scratchOf(t, h, parent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := h.engine.Delete(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("deleting the subagent run took the shared folder: %v", err)
	}
	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the scratch folder outlived its conversation")
	}
}

// A run of reads takes only the calls the policy lets run without a card, in
// order, and leaves the rest to run one at a time: it never waits on a person.
func TestAReadRunTakesOnlyWhatRunsWithoutACard(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("outside text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := h.cfg.Config()
	runner, err := tools.NewRunner(h.project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	runner.Hide(h.cfg.Paths().Home)
	runner.Seen = s.seen
	read := func(id, path string) llm.ToolCall {
		return llm.ToolCall{ID: id, Type: "function", Function: llm.FunctionCall{Name: "read", Arguments: `{"path":` + strconv.Quote(path) + `}`}}
	}
	calls := []llm.ToolCall{read("call_a", "file.txt"), read("call_out", secret), read("call_b", "file.txt")}
	offer := h.engine.briefFor(context.Background(), s, cfg).offer

	ran := h.engine.runReads(context.Background(), s, cfg, runner, calls, 0, &repeats{}, offer, &turnClock{})
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	s.mu.Unlock()
	if ran != 1 {
		t.Fatalf("ran %d calls, want the one before the call that needs a card", ran)
	}
	if p := h.broker.Pending(s.ID); len(p) != 0 {
		t.Errorf("a run of reads drew %d cards", len(p))
	}
	doc, _, _, _ := s.Snapshot()
	var answered []string
	for _, e := range doc.Entries {
		if e.Kind == "tool" {
			answered = append(answered, e.ToolCallID)
			if strings.Contains(e.Content, "outside text") {
				t.Error("the outside file was read")
			}
		}
	}
	if len(answered) != 1 || answered[0] != "call_a" {
		t.Errorf("answered = %v, want call_a alone", answered)
	}
}

// A conversation opened again by a new engine finds its scratch files where
// they were, and has to read one before replacing it.
func TestAReopenedConversationKeepsItsScratch(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	dir := scratchOf(t, h, s)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(h.paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	other := NewEngine(ctx, h.cfg, st, h.engine.files, Clients{Pool: llm.NewPool()}, search.NewProviders(), h.broker, h.rec)
	again, err := other.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := other.briefFor(context.Background(), again, h.cfg.Config())
	if b.scratch != dir {
		t.Fatalf("scratch after reopening = %q, want %q", b.scratch, dir)
	}
	runner, err := tools.NewRunner(h.project, h.cfg.Config())
	if err != nil {
		t.Fatal(err)
	}
	runner.Hide(h.cfg.Paths().Home)
	runner.Seen = again.seen
	runner.Scratch = b.scratch
	raw, _ := json.Marshal(map[string]string{"path": notes, "content": "replaced\n"})
	if _, err := runner.WriteFile(context.Background(), raw); err == nil {
		t.Error("a scratch file was replaced without being read after reopening")
	}
	if got, _ := os.ReadFile(notes); string(got) != "kept\n" {
		t.Errorf("notes = %q, want it kept", got)
	}
}
