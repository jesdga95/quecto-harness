package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/store"
)

// waitForQuestion blocks until the turn is waiting on an answer, which is what
// a test does instead of racing the tool call.
func waitForQuestion(t *testing.T, h *harness, sessionID string) Question {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if q := h.engine.PendingQuestion(sessionID); q != nil {
			return *q
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no question was asked")
	return Question{}
}

func toolResult(t *testing.T, doc *store.Session, name string) store.Entry {
	t.Helper()
	var out store.Entry
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.ToolName == name {
			out = e
		}
	}
	if out.ToolName == "" {
		t.Fatalf("no %s result, entries = %+v", name, doc.Entries)
	}
	return out
}

// TestAskQuestionBlocksUntilAnswered asserts that the turn holds on the
// question, that the answer is the tool result, and that it reaches the model.
func TestAskQuestionBlocksUntilAnswered(t *testing.T) {
	h := newHarness(t, "stream_ask.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "add a cache", nil)

	q := waitForQuestion(t, h, s.ID)
	if q.Text != "Where should the cache live?" || len(q.Options) != 2 {
		t.Fatalf("question = %+v", q)
	}
	if q.Options[1].Label != "On disk" || q.Options[1].Description != "Kept between runs" {
		t.Errorf("options = %+v", q.Options)
	}
	if q.CallID != "call_0" {
		t.Errorf("call id = %q, want the id of the call that asked", q.CallID)
	}
	if s.State() != StateDeciding {
		t.Errorf("state while waiting = %s, want %s", s.State(), StateDeciding)
	}

	// The card carries the id to answer, since the row the call drew is what
	// becomes the card.
	asked := h.rec.ofType("question")
	if len(asked) != 1 {
		t.Fatalf("got %d question events, want 1", len(asked))
	}
	if asked[0].Fields["ask"] != q.ID || asked[0].Fields["id"] != "call_0" {
		t.Errorf("question event = %+v", asked[0].Fields)
	}

	if err := h.engine.Answer(q.ID, Answer{Options: []int{1}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	result := toolResult(t, doc, "ask_user_question")
	if result.Content != "On disk" {
		t.Errorf("tool result = %q, want the label that was chosen", result.Content)
	}
	if result.Permission == nil || result.Permission.Decision != "auto" {
		t.Errorf("the question was put to the user twice: %+v", result.Permission)
	}
	if !strings.Contains(mustJSON(h.replay.Body(1)), "On disk") {
		t.Error("the answer did not reach the model")
	}
	if h.engine.PendingQuestion(s.ID) != nil {
		t.Error("the answered question is still open")
	}
}

// TestAskQuestionTakesTypedWords asserts that an answer nobody offered is
// passed to the model as it was written.
func TestAskQuestionTakesTypedWords(t *testing.T) {
	h := newHarness(t, "stream_ask.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "add a cache", nil)

	q := waitForQuestion(t, h, s.ID)
	if err := h.engine.Answer(q.ID, Answer{Text: "neither, drop the cache"}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if got := toolResult(t, doc, "ask_user_question").Content; got != "neither, drop the cache" {
		t.Errorf("tool result = %q", got)
	}
}

// TestAskQuestionJoinsSeveralAnswers asserts that a multi-select question
// hands back every label that was chosen.
func TestAskQuestionJoinsSeveralAnswers(t *testing.T) {
	h := newHarness(t, "stream_ask_multi.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "commit it", nil)

	q := waitForQuestion(t, h, s.ID)
	if !q.Multi {
		t.Fatalf("question = %+v, want multi-select", q)
	}
	if err := h.engine.Answer(q.ID, Answer{Options: []int{0, 2}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if got := toolResult(t, doc, "ask_user_question").Content; got != "gofmt, go test -race" {
		t.Errorf("tool result = %q", got)
	}
}

// TestAskQuestionRefusesOneOption asserts that a question with nothing to
// choose between is refused with a message the model can act on, and that the
// turn carries on rather than blocking on a card that cannot be answered.
func TestAskQuestionRefusesOneOption(t *testing.T) {
	h := newHarness(t, "stream_ask_bad.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "ship it", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if got := toolResult(t, doc, "ask_user_question").Content; !strings.Contains(got, "two to four options") {
		t.Errorf("tool result = %q", got)
	}
	if len(h.rec.ofType("question")) != 0 {
		t.Error("a card was drawn for a question that cannot be answered")
	}
}

// TestStopUnderAQuestion asserts that a stop ends a turn waiting on an answer,
// and that the tool result says no answer was given rather than leaving the
// assistant message unpaired.
func TestStopUnderAQuestion(t *testing.T) {
	h := newHarness(t, "stream_ask.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "add a cache", nil)

	q := waitForQuestion(t, h, s.ID)
	if _, err := h.engine.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if got := toolResult(t, doc, "ask_user_question").Content; got != "no answer: the session was stopped" {
		t.Errorf("tool result = %q", got)
	}
	if h.engine.PendingQuestion(s.ID) != nil {
		t.Error("the question is still open after the stop")
	}
	// The card closes on the result the browser is sent for every exit, so a
	// stop does not leave the composer waiting on a card nothing will answer.
	closed := h.rec.ofType("tool_output")
	if len(closed) != 1 || closed[0].Fields["id"] != q.CallID {
		t.Errorf("tool_output events = %+v", closed)
	}
}

// TestAnswerTwiceIsRefused asserts that the second click on a card is refused,
// which is what the browser reports as a card that has moved on.
func TestAnswerTwiceIsRefused(t *testing.T) {
	h := newHarness(t, "stream_ask.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "add a cache", nil)

	q := waitForQuestion(t, h, s.ID)
	if err := h.engine.Answer(q.ID, Answer{Options: []int{0}}); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Answer(q.ID, Answer{Options: []int{1}}); err != ErrNoSuchQuestion {
		t.Errorf("second answer = %v, want %v", err, ErrNoSuchQuestion)
	}
	waitIdle(t, s)
}

// TestAskQuestionMultiRunsInOrder asserts that a call with several questions
// reveals them one at a time, that each answer advances to the next, and that
// the turn ends with one result carrying every answer in the order asked.
func TestAskQuestionMultiRunsInOrder(t *testing.T) {
	h := newHarness(t, "stream_ask_many.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "add a cache", nil)

	q0 := waitForQuestion(t, h, s.ID)
	if q0.Index != 0 || q0.Text != "Where should the cache live?" {
		t.Fatalf("first question = %+v, want the call's first", q0)
	}
	if err := h.engine.Answer(q0.ID, Answer{Options: []int{1}}); err != nil {
		t.Fatal(err)
	}

	q1 := waitForQuestion(t, h, s.ID)
	if q1.Index != 1 || q1.Text != "Which checks before the commit?" {
		t.Fatalf("second question = %+v, want the call's second", q1)
	}
	if err := h.engine.Answer(q1.ID, Answer{Options: []int{0, 2}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	result := toolResult(t, doc, "ask_user_question")
	want := "1. Where should the cache live? On disk\n2. Which checks before the commit? gofmt, go test -race"
	if result.Content != want {
		t.Errorf("tool result = %q, want %q", result.Content, want)
	}

	asked := h.rec.ofType("question")
	if len(asked) != 2 {
		t.Fatalf("got %d question events, want one per question", len(asked))
	}
	if asked[0].Fields["index"] != 0 || asked[1].Fields["index"] != 1 {
		t.Errorf("question event indexes = %v then %v, want 0 then 1", asked[0].Fields["index"], asked[1].Fields["index"])
	}
	if h.engine.PendingQuestion(s.ID) != nil {
		t.Error("a question is still open after the call answered")
	}
}

// TestAskQuestionRefusesOutOfOrder asserts that a sub-question is one-shot:
// once answered it is closed, so a second answer to it is refused rather than
// recorded twice, and the call carries on to the next question.
func TestAskQuestionRefusesOutOfOrder(t *testing.T) {
	h := newHarness(t, "stream_ask_many.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "add a cache", nil)

	q0 := waitForQuestion(t, h, s.ID)
	if err := h.engine.Answer(q0.ID, Answer{Options: []int{1}}); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Answer(q0.ID, Answer{Options: []int{0}}); err != ErrNoSuchQuestion {
		t.Errorf("second answer to a sub-question = %v, want %v", err, ErrNoSuchQuestion)
	}

	q1 := waitForQuestion(t, h, s.ID)
	if err := h.engine.Answer(q1.ID, Answer{Options: []int{0}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
}

// TestAskQuestionRefusesTooMany asserts that a call with more questions than
// the limit is refused with a message the model can act on, and that no card
// is drawn for a set that cannot be answered one at a time.
func TestAskQuestionRefusesTooMany(t *testing.T) {
	h := newHarness(t, "stream_ask_toomany.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "ship it", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if got := toolResult(t, doc, "ask_user_question").Content; !strings.Contains(got, "at most 5") {
		t.Errorf("tool result = %q, want a refusal at the limit", got)
	}
	if len(h.rec.ofType("question")) != 0 {
		t.Error("a card was drawn for a set that cannot be answered")
	}
}
