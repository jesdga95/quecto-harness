package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/store"
)

// lastUserSeq is what the browser names a message by when it asks for it to
// be sent again.
func lastUserSeq(t *testing.T, s *Session) int {
	t.Helper()
	doc, _, _, _ := s.Snapshot()
	for i := len(doc.Entries) - 1; i >= 0; i-- {
		if doc.Entries[i].Kind == "user" {
			return doc.Entries[i].Seq
		}
	}
	t.Fatal("the session has no user entry")
	return 0
}

// waitForTitle blocks until the session carries the name, which is how a test
// waits for the request that follows a first turn to have landed.
func waitForTitle(t *testing.T, s *Session, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if doc, _, _, _ := s.Snapshot(); doc.Title == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	doc, _, _, _ := s.Snapshot()
	t.Fatalf("title = %q, want %q", doc.Title, want)
}

func TestResendReplacesTheLastTurn(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	waitIdle(t, s)

	h.engine.Send(s.ID, "and the tests", nil)
	waitIdle(t, s)

	before, _, _, _ := s.Snapshot()
	dropped := before.Entries[len(before.Entries)-1]
	text := "and the tests, in one line"
	seq, err := h.engine.Resend(s.ID, lastUserSeq(t, s), &text)
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if len(doc.Entries) != 4 {
		t.Fatalf("entries = %d, want two turns", len(doc.Entries))
	}
	last := doc.Entries[2]
	if last.Kind != "user" || last.Content != text {
		t.Fatalf("third entry = %+v, want the rewritten message", last)
	}
	if !last.Edited {
		t.Error("the rewritten message is not marked as edited")
	}
	if last.Seq != seq || last.Seq <= dropped.Seq {
		t.Errorf("seq = %d, want a number above the discarded %d", last.Seq, dropped.Seq)
	}
	if doc.Entries[3].Kind != "assistant" {
		t.Errorf("fourth entry = %+v, want the reply to the rewritten message", doc.Entries[3])
	}

	// The model is asked the rewritten question and never sees the old one.
	body := h.replay.Body(h.replay.Count() - 1)
	msgs, _ := body["messages"].([]any)
	joined := ""
	for _, m := range msgs {
		if row, ok := m.(map[string]any); ok {
			if c, ok := row["content"].(string); ok {
				joined += c + "\n"
			}
		}
	}
	if !strings.Contains(joined, text) {
		t.Errorf("the request does not carry the rewritten message: %q", joined)
	}
	if strings.Contains(joined, "and the tests\n") {
		t.Errorf("the request still carries the message as it was: %q", joined)
	}
	if len(h.rec.ofType("resync")) != 1 {
		t.Errorf("resync events = %d, want one so every tab refetches", len(h.rec.ofType("resync")))
	}
}

// A retry carries no text and leaves the message, and its edited mark, alone.
func TestResendWithoutTextIsARetry(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	waitIdle(t, s)

	if _, err := h.engine.Resend(s.ID, lastUserSeq(t, s), nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if len(doc.Entries) != 2 {
		t.Fatalf("entries = %d, want one turn", len(doc.Entries))
	}
	if doc.Entries[0].Content != "what is here" || doc.Entries[0].Edited {
		t.Errorf("first entry = %+v, want the message as it was sent", doc.Entries[0])
	}
}

func TestResendKeepsTheAttachment(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	path := filepath.Join(h.project, "file.txt")
	if _, err := h.engine.Send(s.ID, "read this", []string{path}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	text := "read this properly"
	if _, err := h.engine.Resend(s.ID, lastUserSeq(t, s), &text); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	files := doc.Entries[0].Attachments
	if len(files) != 1 || files[0].Path != path || !strings.Contains(files[0].Content, "one") {
		t.Fatalf("attachments = %+v, want the file the message was sent with", files)
	}

	body := h.replay.Body(h.replay.Count() - 1)
	msgs, _ := body["messages"].([]any)
	first, _ := msgs[1].(map[string]any)
	content, _ := first["content"].(string)
	if !strings.Contains(content, text) || !strings.Contains(content, "one\ntwo") {
		t.Errorf("the message sent again = %q, want the new words and the same file", content)
	}
}

// The opening message names the session, so rewriting it renames.
func TestResendRenamesFromTheOpeningMessage(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	waitIdle(t, s)
	// The name the model wrote lands first, so the rewrite is what replaces it
	// rather than the other way round.
	waitForTitle(t, s, "Read the project layout")

	text := "what is in the internal folder"
	if _, err := h.engine.Resend(s.ID, lastUserSeq(t, s), &text); err != nil {
		t.Fatal(err)
	}
	// Read before the turn ends, since the model names the session again after
	// it and this is about what the rewrite itself did.
	doc, _, _, _ := s.Snapshot()
	if doc.Title != text {
		t.Errorf("title = %q, want the rewritten message", doc.Title)
	}
	waitIdle(t, s)
}

func TestResendRefusesAnEarlierMessage(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	waitIdle(t, s)
	first := lastUserSeq(t, s)

	h.engine.Send(s.ID, "and the tests", nil)
	waitIdle(t, s)

	text := "changed my mind"
	if _, err := h.engine.Resend(s.ID, first, &text); err != ErrNotLast {
		t.Fatalf("error = %v, want ErrNotLast", err)
	}
	doc, _, _, _ := s.Snapshot()
	if len(doc.Entries) != 4 {
		t.Errorf("entries = %d, want the history left alone", len(doc.Entries))
	}
}

func TestResendRefusesWhileTheTurnRuns(t *testing.T) {
	h := newHarness(t, "stream_sleep.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist })
	h.cfg.AddAllow(h.project, "sleep")
	s := h.session(t)
	h.engine.Send(s.ID, "sleep", nil)
	waitForState(t, s, StateExecuting)

	text := "never mind"
	if _, err := h.engine.Resend(s.ID, lastUserSeq(t, s), &text); err != ErrBusy {
		t.Fatalf("error = %v, want ErrBusy", err)
	}
	h.engine.Stop(s.ID)
	waitIdle(t, s)
}

// A model is read inside a turn, so a session reading one is a turn to stop.
// Sending a message again while it loads is refused, and the transcript is
// left alone.
func TestResendRefusesWhileTheModelLoads(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_title.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "hello", nil)
	waitIdle(t, s)
	before, _, _, _ := s.Snapshot()

	// Where a turn sits while it reads the model it is about to talk to.
	s.mu.Lock()
	s.state = StateLoading
	s.mu.Unlock()

	if _, err := h.engine.Resend(s.ID, lastUserSeq(t, s), nil); err != ErrBusy {
		t.Fatalf("error = %v, want ErrBusy", err)
	}
	after, _, _, _ := s.Snapshot()
	if len(after.Entries) != len(before.Entries) {
		t.Errorf("entries = %d, want the %d that were there", len(after.Entries), len(before.Entries))
	}
}

// A message that is only attachments stays sendable; one edited down to
// nothing with no files is refused.
func TestResendRefusesAnEmptyMessage(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_title.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	waitIdle(t, s)

	empty := "   "
	if _, err := h.engine.Resend(s.ID, lastUserSeq(t, s), &empty); err != ErrEmptyText {
		t.Fatalf("error = %v, want ErrEmptyText", err)
	}
}

// A rewind drops a compaction with the turn it happened in, which puts the
// entries it summarised back in context, and takes the plan back to the one
// the surviving entries left.
func TestRewindRestoresContextAndPlan(t *testing.T) {
	s := newSession(store.NewID(), "/p", "Alpha-12B")
	todos := func(id, plan string) store.Entry {
		return store.Entry{Kind: "assistant", Role: "assistant", InContext: true, Visible: true,
			ToolCalls: []llm.ToolCall{{ID: id, Type: "function",
				Function: llm.FunctionCall{Name: "write_todos", Arguments: plan}}}}
	}

	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "one", InContext: true, Visible: true})
	s.appendEntry(todos("call_1", `{"todos":[{"text":"Read the routes","status":"doing"}]}`))
	s.appendEntry(store.Entry{Kind: "tool", Role: "tool", ToolCallID: "call_1",
		ToolName: "write_todos", Content: "0 of 1 done", InContext: true, Visible: true})
	second := s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "two", InContext: true, Visible: true})

	// The turn compacted, which takes everything above out of context.
	for i := range s.Entries {
		s.Entries[i].InContext = false
	}
	s.Compactions = 1
	s.appendEntry(store.Entry{Kind: "compaction", Role: "user", Content: "the story so far",
		InContext: true, Visible: true})
	s.appendEntry(todos("call_2", `{"todos":[{"text":"Add the handler","status":"todo"}]}`))
	s.appendEntry(store.Entry{Kind: "tool", Role: "tool", ToolCallID: "call_2",
		ToolName: "write_todos", Content: "0 of 1 done", InContext: true, Visible: true})
	s.Todos = []store.Todo{{Text: "Add the handler", Status: "todo"}}
	s.LastUsage = &llm.Usage{PromptTokens: 900, CompletionTokens: 100}
	s.LastToolTokens = 15
	s.lastUsageEntry = len(s.Entries) - 1

	s.rewindTo(3)

	if len(s.Entries) != 3 {
		t.Fatalf("entries = %d, want the three before the message", len(s.Entries))
	}
	for i, e := range s.Entries {
		if !e.InContext {
			t.Errorf("entry %d (%s) stayed out of context after the compaction was dropped", i, e.Kind)
		}
	}
	if s.Compactions != 0 {
		t.Errorf("compactions = %d, want none left", s.Compactions)
	}
	if len(s.Todos) != 1 || s.Todos[0].Text != "Read the routes" {
		t.Errorf("todos = %+v, want the plan the surviving entries left", s.Todos)
	}
	if s.LastUsage != nil || s.LastToolTokens != 0 || s.lastUsageEntry != -1 {
		t.Error("the usage anchor still points at a turn the history no longer holds")
	}
	if second.Seq != 4 {
		t.Fatalf("the message under test had seq %d", second.Seq)
	}
	next := s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "two again", InContext: true, Visible: true})
	if next.Seq <= 7 {
		t.Errorf("seq = %d, want a number no discarded entry used", next.Seq)
	}
}

// A rewind past the only compaction leaves no plan when nothing surviving set
// one.
func TestRewindClearsAPlanNothingSurvivingSet(t *testing.T) {
	s := newSession(store.NewID(), "/p", "Alpha-12B")
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "one", InContext: true, Visible: true})
	s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant", InContext: true, Visible: true,
		ToolCalls: []llm.ToolCall{{ID: "call_1", Type: "function",
			Function: llm.FunctionCall{Name: "write_todos", Arguments: `{"todos":[{"text":"Read","status":"todo"}]}`}}}})
	s.appendEntry(store.Entry{Kind: "tool", Role: "tool", ToolCallID: "call_1",
		ToolName: "write_todos", Content: "0 of 1 done", InContext: true, Visible: true})
	s.Todos = []store.Todo{{Text: "Read", Status: "todo"}}

	s.rewindTo(0)

	if len(s.Entries) != 0 || len(s.Todos) != 0 {
		t.Errorf("entries = %d, todos = %+v", len(s.Entries), s.Todos)
	}
}
