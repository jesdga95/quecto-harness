package agent

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// queueDuringTurn sends a message while the model is answering, which is where
// a message typed mid-turn lands. The replay server is slowed so the stream is
// still open when it arrives.
func queueDuringTurn(t *testing.T, h *harness, s *Session, text string, files ...string) Sent {
	t.Helper()
	waitForState(t, s, StateStreaming)
	sent, err := h.engine.Send(s.ID, text, files)
	if err != nil {
		t.Fatal(err)
	}
	if !sent.Queued {
		t.Fatalf("%q started a turn of its own", text)
	}
	return sent
}

// waitForEntry blocks until the transcript holds an entry with the text, which
// is how a test waits for a queued message to be picked up.
func waitForEntry(t *testing.T, s *Session, text string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		doc, _, _, _ := s.Snapshot()
		for _, e := range doc.Entries {
			if e.Content == text {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no entry reading %q", text)
}

// The reply the model was writing finishes, and the message it was typed
// against becomes the next thing the model reads.
func TestSteerBecomesTheNextTurn(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	h.replay.Delay = 8 * time.Millisecond
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	queueDuringTurn(t, h, s, "and the tests")
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	kinds := []string{}
	for _, e := range doc.Entries {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "user,assistant,user,assistant" {
		t.Fatalf("entries = %v, want the message picked up as a second turn", kinds)
	}
	if doc.Entries[2].Content != "and the tests" || !doc.Entries[2].InContext {
		t.Errorf("third entry = %+v, want the queued message in context", doc.Entries[2])
	}
	if h.replay.Count() != 2 {
		t.Errorf("requests = %d, want the turn to have carried on", h.replay.Count())
	}
	if s.Steer() != nil {
		t.Error("the queue still holds the message it delivered")
	}
}

// Work already under way is not thrown away: the batch the model asked for
// runs, and the message lands after it.
func TestSteerWaitsForTheToolBatch(t *testing.T) {
	h := newHarness(t, "stream_two_tools.txt", "stream_plain.txt")
	h.replay.Delay = 8 * time.Millisecond
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	queueDuringTurn(t, h, s, "read the readme instead")
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	kinds := []string{}
	for _, e := range doc.Entries {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "user,assistant,tool,tool,user,assistant" {
		t.Fatalf("entries = %v, want both calls run before the message", kinds)
	}
	for _, e := range doc.Entries {
		if e.Kind == "tool" && strings.HasPrefix(e.Content, "not run") {
			t.Errorf("a call was skipped: %+v", e)
		}
	}

	// The request that follows answers every call and carries the message.
	body := h.replay.Body(1)
	msgs, _ := body["messages"].([]any)
	tools, users := 0, 0
	for _, m := range msgs {
		row, _ := m.(map[string]any)
		switch row["role"] {
		case "tool":
			tools++
		case "user":
			users++
		}
	}
	if tools != 2 || users != 2 {
		t.Errorf("tool messages = %d, user messages = %d, want two of each", tools, users)
	}
}

// Two messages typed against one turn arrive as one, a line each.
func TestSteersJoin(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	h.replay.Delay = 8 * time.Millisecond
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	queueDuringTurn(t, h, s, "one")
	sent := queueDuringTurn(t, h, s, "two")
	if sent.Steer == nil || sent.Steer.Text != "one\ntwo" {
		t.Fatalf("queue = %+v, want the two joined", sent.Steer)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if doc.Entries[2].Content != "one\ntwo" {
		t.Errorf("third entry = %q, want one message", doc.Entries[2].Content)
	}
}

// A stop ends the turn without picking the message up, so it is sent as an
// ordinary one.
func TestStopDeliversTheQueue(t *testing.T) {
	h := newHarness(t, "stream_sleep.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist })
	h.cfg.AddAllow(h.project, "sleep")
	s := h.session(t)
	h.engine.Send(s.ID, "sleep", nil)

	waitForState(t, s, StateExecuting)
	sent, err := h.engine.Send(s.ID, "never mind, what is here", nil)
	if err != nil || !sent.Queued {
		t.Fatalf("send during a command = %+v %v, want it queued", sent, err)
	}
	h.engine.Stop(s.ID)

	waitForEntry(t, s, "never mind, what is here")
	waitIdle(t, s)
	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "assistant" {
		t.Errorf("last entry = %+v, want the reply to the delivered message", last)
	}
	if s.Steer() != nil {
		t.Error("the queue still holds the message the stop delivered")
	}
	// No browser drew this one for itself, so the harness has to.
	if len(h.rec.ofType("message")) != 1 {
		t.Errorf("message events = %d, want the delivered message on the stream",
			len(h.rec.ofType("message")))
	}
}

// Files queued with a message reach the prompt with it.
func TestSteerCarriesItsAttachment(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	h.replay.Delay = 8 * time.Millisecond
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	queueDuringTurn(t, h, s, "read this", filepath.Join(h.project, "file.txt"))
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if len(doc.Entries[2].Attachments) != 1 {
		t.Fatalf("third entry = %+v, want the file queued with it", doc.Entries[2])
	}
	body := h.replay.Body(1)
	msgs, _ := body["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := last["content"].(string)
	if !strings.Contains(content, "one\ntwo") {
		t.Errorf("the last message = %q, want the file inlined into it", content)
	}
}

// A set of files that would take the joined message over the cap is refused
// whole, so the queue keeps what was already typed.
func TestSteerOverTheAttachmentCapIsRefused(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	h.replay.Delay = 8 * time.Millisecond
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)

	path := filepath.Join(h.project, "file.txt")
	queueDuringTurn(t, h, s, "these", path, path, path)
	if _, err := h.engine.Send(s.ID, "and these", []string{path, path, path}); err == nil {
		t.Fatal("six files were accepted into one message")
	}
	held := s.Steer()
	if held == nil || held.Text != "these" || len(held.Attachments) != 3 {
		t.Errorf("queue = %+v, want what was typed before the refusal", held)
	}
	waitIdle(t, s)
}

// The queue is not part of the document, so a message waiting on a turn that
// the process lost is not left in the transcript as one that was sent.
func TestSteerIsNotStored(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	h.replay.Delay = 8 * time.Millisecond
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	queueDuringTurn(t, h, s, "and the tests")

	doc, err := h.store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range doc.Entries {
		if e.Content == "and the tests" {
			t.Fatal("a queued message was written to the session document")
		}
	}
	waitIdle(t, s)
}
