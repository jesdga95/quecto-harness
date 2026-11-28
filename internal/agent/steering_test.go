package agent

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/store"
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
	msgs := h.replay.Body(1)["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" || last["content"] != prompts.Steer("and the tests") {
		t.Fatalf("follow-up after a plain reply = %v", last)
	}
	if s.Steer() != nil {
		t.Error("the queue still holds the message it delivered")
	}
}

// Work already under way is not thrown away: the batch the model asked for
// runs, and the message lands after it.
func TestSteerWaitsForTheToolBatch(t *testing.T) {
	h := newHarness(t, "stream_two_tools.txt", "stream_plain.txt", "stream_summary.txt")
	h.replay.Delay = 8 * time.Millisecond
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })
	s := h.session(t)
	const follow = "Also read the readme."
	first := earlierWork + "List the files."
	h.engine.Send(s.ID, first, nil)
	queueDuringTurn(t, h, s, follow)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	kinds := []string{}
	for _, e := range doc.Entries {
		if e.Kind != "status" {
			kinds = append(kinds, e.Kind)
		}
	}
	if strings.Join(kinds, ",") != "user,assistant,tool,tool,user,assistant" {
		t.Fatalf("entries = %v, want both calls run before the message", kinds)
	}
	for _, e := range doc.Entries {
		if e.Kind == "tool" && strings.HasPrefix(e.Content, "not run") {
			t.Errorf("a call was skipped: %+v", e)
		}
	}

	framed := prompts.Steer(follow)
	rows := h.replay.Body(1)["messages"].([]any)
	if len(rows) != 5 || rows[1].(map[string]any)["role"] != "user" || rows[1].(map[string]any)["content"] != first {
		t.Fatal("the request must contain the original user message and end with the two results")
	}
	for i, id := range []string{"call_0", "call_1"} {
		row := rows[3+i].(map[string]any)
		want := doc.Entries[2+i].Content
		if i == 1 {
			want += "\n\n" + framed
		}
		if row["role"] != "tool" || row["tool_call_id"] != id || row["name"] != "bash" || row["content"] != want {
			t.Fatalf("result %s = %v", id, row)
		}
	}

	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	saved, err := h.store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry := saved.Entries[4]
	if saved.Compactions != 1 || !entry.InContext || entry.Content != follow || entry.Meta["steer"] != true {
		t.Fatal("compaction did not preserve the raw follow-up in the retained tail")
	}
	reopened := fromDoc(saved)
	messages := reopened.contextMessages(config.Agent{}, "", "", brief{}, nil)
	if messages[len(messages)-3].Content != doc.Entries[3].Content ||
		!messages[len(messages)-2].Update || messages[len(messages)-2].Content != framed ||
		strings.Count(messages[1].Content, first) != 1 || strings.Contains(messages[1].Content, follow) {
		t.Fatal("the compacted session lost the request or repeated the retained update")
	}
}

func TestToolUpdateKeepsMediaInOrder(t *testing.T) {
	image := llm.ImagePart("image/png", []byte("output"))
	attachment := llm.ImagePart("image/png", []byte("input"))
	video := llm.VideoPart("video/mp4", []byte("video"))
	images := map[string]llm.Part{"out": image, "in": attachment, "clip": video}
	s := &Session{Entries: []store.Entry{
		{Kind: "assistant", InContext: true, ToolCalls: []llm.ToolCall{{ID: "last"}}},
		{Kind: "tool", ToolCallID: "last", Content: "result", InContext: true,
			Attachments: []store.Attachment{{Kind: "image", File: "out"}}},
		{Kind: "user", Content: "update", InContext: true, Meta: map[string]any{"steer": true},
			Attachments: []store.Attachment{{Kind: "image", File: "in"}}},
	}}
	messages := func() []llm.Message { return s.contextMessages(config.Agent{}, "", "", brief{}, images)[2:] }
	msgs := messages()
	want := []llm.Part{llm.TextPart("result"), image}
	if len(msgs) != 3 || msgs[0].ToolCallID != "last" || !slices.Equal(msgs[0].Parts, want) ||
		msgs[1].Role != "user" || !msgs[1].Update || msgs[1].Content != prompts.Steer("update") ||
		msgs[2].Role != "user" || !slices.Equal(msgs[2].Parts, []llm.Part{attachment}) {
		t.Fatalf("image placement = %v", msgs)
	}
	s.Entries[2].Attachments = append(s.Entries[2].Attachments, store.Attachment{Kind: "video", File: "clip"})
	s.Entries[2].Meta["mode"] = string(config.WorkPlan)
	msgs = messages()
	if len(msgs) != 3 || !slices.Equal(msgs[0].Parts, want) || msgs[1].Role != "user" || msgs[1].Update ||
		msgs[1].Content != modeRules(config.WorkPlan, brief{})+"\n\n"+prompts.Steer("update") ||
		msgs[2].Role != "user" || !slices.Equal(msgs[2].Parts, []llm.Part{attachment, video}) {
		t.Fatalf("mode change with attachments = %v", msgs)
	}
}

func TestQueuedSendersKeepTheirAttribution(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	s.appendEntry(store.Entry{Kind: "assistant", InContext: true, ToolCalls: []llm.ToolCall{{ID: "call"}}})
	s.appendEntry(store.Entry{Kind: "tool", ToolCallID: "call", Content: "result", InContext: true})
	from := sender{Name: "Reviewer", Session: "child"}
	note := store.Attachment{Path: "note.txt", Content: "Text from the file"}
	batch := []Steer{
		{Text: "First", From: from},
		{Text: "Second"},
		{Text: "Third", From: from, Attachments: []store.Attachment{note}},
	}
	for _, m := range batch {
		if err := s.queue(m); err != nil {
			t.Fatal(err)
		}
	}
	h.engine.absorbSteer(s)
	doc, err := h.store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Entries) != 2+len(batch) {
		t.Fatalf("entries = %d", len(doc.Entries))
	}
	for i, m := range batch {
		entry := doc.Entries[i+2]
		if entry.Content != m.Text || entry.From != m.From.Name || entry.FromSession != m.From.Session ||
			entry.Meta["steer"] != true || !slices.Equal(entry.Attachments, m.Attachments) {
			t.Fatalf("stored entry %d = %+v", i, entry)
		}
	}
	msgs := fromDoc(doc).contextMessages(config.Agent{}, "", "", brief{}, nil)
	want := []string{prompts.Message(from.Name, from.Session, "First"), prompts.Steer("Second"),
		prompts.Message(from.Name, from.Session, "Third")}
	if len(msgs) != 7 || msgs[2].Content != "result" || msgs[6].Role != "user" ||
		msgs[6].Content != prompts.Attachment(note.Path, note.Content) {
		t.Fatalf("mixed batch and attachment = %v", msgs)
	}
	for i, text := range want {
		if m := msgs[3+i]; m.Role != "user" || !m.Update || m.Content != text {
			t.Fatalf("update %d = %+v", i, m)
		}
	}
}

func TestCompactionCarriesOpenRequestAndUpdates(t *testing.T) {
	entries := []store.Entry{
		{Kind: "user", Seq: 1, Content: "Older request"},
		{Kind: "user", Seq: 2, Content: "Open request"},
		{Kind: "user", Seq: 3, Content: "Extra detail", Meta: map[string]any{"steer": true}},
		{Kind: "user", Seq: 4, From: "Worker", Content: "Done", Meta: map[string]any{"steer": true}},
		{Kind: "user", Seq: 5, Content: "Continue the plan", Meta: map[string]any{"continue": true}},
	}
	human := "Open request\n\n" + prompts.Steer("Extra detail")
	if got := recentRequest(entries, 0, false, false); got != human+"\n\n"+prompts.Message("Worker", "", "Done") {
		t.Fatalf("whole turn = %q", got)
	}
	if got := recentRequest(entries, 4, true, false); got != human {
		t.Fatalf("before the retained tail = %q", got)
	}
	entries[1].Meta = map[string]any{"kickoff": true}
	if got := recentRequest(entries, 4, true, true); got != prompts.Steer("Extra detail") {
		t.Fatalf("updates beside the carried plan = %q", got)
	}
	if got := recentRequest(entries, 4, true, false); got != human {
		t.Fatalf("request without a carried plan = %q", got)
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
	from := sender{Name: "Worker", Session: "child"}
	if sent, err := h.engine.deliverFrom(s, from, "Work finished", nil); err != nil || !sent.Queued {
		t.Fatalf("second sender = %+v, %v", sent, err)
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
	if len(h.rec.ofType("message")) != 2 {
		t.Errorf("message events = %d, want one per sender", len(h.rec.ofType("message")))
	}
	entries := doc.Entries[len(doc.Entries)-3:]
	if entries[0].Content != "never mind, what is here" || entries[1].FromSession != from.Session ||
		entries[1].Content != "Work finished" || entries[0].Meta["steer"] == true || entries[1].Meta["steer"] == true {
		t.Fatalf("messages after stop = %+v", entries)
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
