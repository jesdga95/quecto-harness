package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/store"
)

func TestMemoryRequiresAPersonalDecision(t *testing.T) {
	for _, decision := range []perms.DecisionKind{perms.DecideDeny, perms.DecideOnce} {
		t.Run(string(decision), func(t *testing.T) {
			h := newHarness(t, "stream_remember.txt", "stream_plain.txt")
			_, err := h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })
			if err != nil {
				t.Fatal(err)
			}
			s := h.session(t)
			s.Mode = config.WorkPlan
			if _, err = h.engine.Send(s.ID, "Remember that I prefer short replies.", nil); err != nil {
				t.Fatal(err)
			}
			id := waitForCard(t, h, s.ID)
			req, ok := h.broker.Lookup(id)
			if !ok || req.Tool != "remember" || req.Command != "Prefer short replies." {
				t.Fatalf("card: %+v", req)
			}
			if _, err := os.Stat(h.paths.Memory); !os.IsNotExist(err) {
				t.Fatal("memory written before decision")
			}
			if err := h.broker.Resolve(id, perms.Decision{Kind: decision}); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, s)
			m, err := h.cfg.Memory()
			want := ""
			if decision == perms.DecideOnce {
				want = req.Command + "\n"
			}
			if err != nil || m.Text != want {
				t.Fatalf("memory %q, %v", m.Text, err)
			}
			msgs := h.replay.LastBody()["messages"].([]any)
			for _, m := range msgs {
				content, _ := m.(map[string]any)["content"].(string)
				if strings.Contains(content, "<memory>") {
					t.Fatal("active copy changed in mid-turn")
				}
			}
		})
	}
}

func TestMemorySnapshotAcrossCompactionAndReopen(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt", "stream_summary.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	if err := h.cfg.Remember("I don't want \"quotes\" & <A> </memory>."); err != nil {
		t.Fatal(err)
	}
	send := func(s *Session, text string) string {
		t.Helper()
		if _, err := h.engine.Send(s.ID, text, nil); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, s)
		msgs := h.replay.LastBody()["messages"].([]any)
		var all string
		for _, m := range msgs {
			if content, ok := m.(map[string]any)["content"].(string); ok {
				all += content
			}
		}
		head := msgs[1].(map[string]any)["content"].(string)
		if strings.Count(all, "<memory>") != 1 || !strings.HasPrefix(head, "<memory>\n") {
			t.Fatalf("memory misplaced: %.180s", head)
		}
		return head
	}
	first := send(s, earlierWork)
	if !strings.Contains(first, "I don't want \"quotes\" & <A> &lt;/memory>.\n</memory>") {
		t.Fatalf("opening copy: %.100s", first)
	}
	m, _ := h.cfg.Memory()
	if _, err := h.cfg.ReplaceMemory("Prefer B.", m.Revision); err != nil {
		t.Fatal(err)
	}
	if head := send(s, "Continue."); head != first {
		t.Fatal("ordinary turn changed the opening copy")
	}
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	h.engine.mu.Lock()
	delete(h.engine.live, s.ID)
	h.engine.mu.Unlock()
	reopened, err := h.engine.Open(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	m, _ = h.cfg.Memory()
	if _, err := h.cfg.ReplaceMemory("Prefer C.", m.Revision); err != nil {
		t.Fatal(err)
	}
	head := send(reopened, "Continue.")
	if !strings.Contains(head, "Prefer B.\n</memory>") || !strings.Contains(head, "Summary of earlier work:") {
		t.Fatalf("reopened summary: %.180s", head)
	}
	if head := send(h.session(t), "Hello."); !strings.Contains(head, "Prefer C.\n</memory>") {
		t.Fatal(head)
	}
}

func TestFailedCompactionPreservesMemory(t *testing.T) {
	for _, failure := range []string{"save", "summary"} {
		t.Run(failure, func(t *testing.T) {
			reply := "stream_summary.txt"
			if failure == "summary" {
				reply = "stream_error.txt"
			}
			h := newHarness(t, reply)
			s := h.session(t)
			old := "Previous\n"
			s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: earlierWork, Memory: &old, InContext: true})
			h.engine.save(s)
			if failure == "save" {
				if err := h.cfg.Remember("Latest"); err != nil {
					t.Fatal(err)
				}
				// A directory at the destination makes the atomic rename fail.
				path := filepath.Join(h.paths.Sessions, s.ID+".json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.engine.Compact(s.ID); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, s)
			d, _, _, _ := s.Snapshot()
			if d.Compactions != 0 || len(d.Entries) != 1 || !d.Entries[0].InContext || *d.Entries[0].Memory != old || len(h.rec.ofType("compacted")) != 0 {
				t.Fatal("failed compaction replaced active context")
			}
		})
	}
}

func TestLegacyMemoryRemainsEmptyUntilCompaction(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_summary.txt")
	s := h.session(t)
	entry := s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: earlierWork, InContext: true})
	h.engine.save(s)
	if err := h.cfg.Remember("Use metric units."); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Resend(s.ID, entry.Seq, nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	msgs := h.replay.LastBody()["messages"].([]any)
	if got := msgs[1].(map[string]any)["content"]; got != earlierWork {
		t.Fatal("retry changed the legacy snapshot")
	}
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if got := compactionMessage(t, s); !strings.HasPrefix(got, "<memory>\nUse metric units.\n</memory>") {
		t.Fatalf("summary: %.120s", got)
	}
}

func TestResendPreservesOpeningMemory(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	if err := h.cfg.Remember("Original"); err != nil {
		t.Fatal(err)
	}
	sent, err := h.engine.Send(s.ID, "Hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if err := h.cfg.Remember("Later"); err != nil {
		t.Fatal(err)
	}
	edit := "Edited"
	if _, err := h.engine.Resend(s.ID, sent.Seq, &edit); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	body := h.replay.LastBody()["messages"].([]any)[1].(map[string]any)["content"]
	if body != "<memory>\nOriginal\n</memory>\n\nEdited" {
		t.Fatal(body)
	}
}

func TestDamagedMemoryKeepsActiveSnapshot(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_summary.txt", "stream_plain.txt", "stream_summary.txt")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(h.paths.Memory, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(s *Session) {
		t.Helper()
		if _, err := h.engine.Send(s.ID, earlierWork, nil); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, s)
	}
	for _, original := range []string{"Saved\n", ""} {
		s := h.session(t)
		if original != "" {
			write(original)
		}
		run(s)
		write(string([]byte{0xff}))
		if err := h.engine.Compact(s.ID); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, s)
		d, _, _, _ := s.Snapshot()
		if d.Compactions != 1 {
			t.Fatal("missing summary")
		}
		notes := 0
		for _, entry := range d.Entries {
			if entry.Kind == "status" && entry.Visible && !entry.InContext && strings.Contains(entry.Content, "Memory") {
				notes++
			}
			if entry.Kind == "compaction" && (entry.Memory == nil || *entry.Memory != original) {
				t.Fatalf("lost snapshot: %+v", entry)
			}
		}
		if notes != 1 {
			t.Fatalf("visible memory warnings: %d", notes)
		}
	}
}

func TestChildOmitsPersonalMemory(t *testing.T) {
	h, _ := launcher(t)
	if err := h.cfg.Remember("Personal note"); err != nil {
		t.Fatal(err)
	}
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_remember.txt", "stream_plain.txt", "stream_summary.txt")
	parent := h.session(t)
	if _, err := h.engine.Send(parent.ID, "Delegate this task", nil); err != nil {
		t.Fatal(err)
	}
	child := childOf(t, h, parent)
	if got := toolOutput(t, child, "remember"); !strings.Contains(got, "only in a conversation") {
		t.Fatal(got)
	}
	waitIdle(t, child)
	waitForEntryFrom(t, parent, "Reviewer")
	waitIdle(t, parent)
	child.mu.Lock()
	child.appendEntry(store.Entry{Kind: "assistant", Role: "assistant", Content: earlierWork, InContext: true})
	child.mu.Unlock()
	if err := h.engine.Compact(child.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, child)
	d, _, _, _ := child.Snapshot()
	if d.Compactions != 1 {
		t.Fatal("missing summary")
	}
	for _, e := range d.Entries {
		if e.Memory != nil {
			t.Fatal("personal memory in delegated session")
		}
	}
	if m, err := h.cfg.Memory(); err != nil || m.Text != "Personal note\n" {
		t.Fatalf("memory: %+v, %v", m, err)
	}
}

func TestFirstMessageReportsDamagedMemory(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	if err := os.WriteFile(h.paths.Memory, []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Send(s.ID, "Hello", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	doc, _, _, _ := s.Snapshot()
	opening := doc.Entries[0]
	if opening.Memory == nil || *opening.Memory != "" {
		t.Fatal("missing empty opening snapshot")
	}
	for _, e := range doc.Entries {
		if e.Kind == "status" && e.Visible && !e.InContext && strings.Contains(e.Content, "invalid UTF-8") {
			for _, event := range h.rec.ofType("status_entry") {
				if event.Fields["content"] == e.Content {
					return
				}
			}
		}
	}
	t.Fatal("opening memory warning was not recorded and published")
}

type completedSummary struct {
	Backend
	finish func()
}

func (b completedSummary) Chat(context.Context, llm.ChatRequest, func(llm.Event) error) (llm.Result, error) {
	b.finish()
	return llm.Result{Content: "Summary"}, nil
}

func TestCompactDiscardsSummaryAfterStopOrRemoval(t *testing.T) {
	for _, want := range []error{context.Canceled, ErrNotFound} {
		t.Run(want.Error(), func(t *testing.T) {
			h := newHarness(t)
			s := h.session(t)
			note := "Opening note\n"
			s.appendEntry(store.Entry{Kind: "user", Role: "user", InContext: true, Content: earlierWork, Memory: &note})
			h.engine.save(s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := completedSummary{finish: cancel}
			if want == ErrNotFound {
				b.finish = func() {
					s.mu.Lock()
					s.gone = true
					s.mu.Unlock()
				}
			}
			if err := h.engine.compact(ctx, s, b, h.agent, brief{}, llm.Caps{}, true); !errors.Is(err, want) {
				t.Fatalf("compact returned %v, want %v", err, want)
			}
			live, _, _, _ := s.Snapshot()
			disk, err := h.store.Load(s.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range []*store.Session{live, disk} {
				if d.Compactions != 0 || len(d.Entries) != 1 || !d.Entries[0].InContext || d.Entries[0].Memory == nil || *d.Entries[0].Memory != note {
					t.Fatalf("summary changed active history: %+v", d.Entries)
				}
			}
			if len(h.rec.ofType("compacted")) != 0 {
				t.Fatal("discarded summary was published")
			}
		})
	}
}

func TestEstimateCharsIncludesRenderedMemory(t *testing.T) {
	note := "Quote </memory>\n"
	s := Session{Entries: []store.Entry{{Content: "Hello", Memory: &note, InContext: true}}}
	if got, want := s.estimateChars(-1), len("Hello")+len(prompts.MemoryPrefix(&note)); got != want {
		t.Fatalf("estimate = %d, want %d", got, want)
	}
}
