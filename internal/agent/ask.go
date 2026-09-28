package agent

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"quecto-harness/internal/llm"
	"quecto-harness/internal/tools"
)

// maxOptions bounds a question. Past four answers the card stops being a
// choice and the model should be reading the project instead.
const maxOptions = 4

var (
	ErrNoSuchQuestion = errors.New("That question is no longer open: the turn moved on")
	ErrNotMultiSelect = errors.New("That question takes one answer")
	ErrNoAnswer       = errors.New("The answer is empty: choose an option or type one")
	ErrNoSuchOption   = errors.New("There is no option with that number")
)

// Option is one answer offered to the user.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is one open question. CallID names the tool call it belongs to, so
// the browser can attach it to the row that call already drew.
type Question struct {
	ID        string   `json:"id"`
	SessionID string   `json:"session"`
	CallID    string   `json:"call_id"`
	Text      string   `json:"question"`
	Options   []Option `json:"options"`
	Multi     bool     `json:"multi_select,omitempty"`
}

// Answer is what came back. Options are indexes into the question's own list;
// Text is an answer typed instead of chosen.
type Answer struct {
	Options []int  `json:"options"`
	Text    string `json:"text"`
}

// asker blocks a turn on a question and lets an HTTP handler answer it from
// another goroutine. It is the perms.Broker shape with no policy: a question
// is always put to the user, so there is nothing to evaluate first.
type asker struct {
	mu   sync.Mutex
	wait map[string]chan Answer
	open map[string]Question
	byS  map[string][]string
}

func newAsker() *asker {
	return &asker{
		wait: map[string]chan Answer{},
		open: map[string]Question{},
		byS:  map[string][]string{},
	}
}

// ask registers a question, draws the card and blocks. The card is drawn from
// in here so it cannot be answered before the question is registered, and the
// channel is buffered so answer never waits for the turn goroutine to be
// scheduled.
func (a *asker) ask(ctx context.Context, q Question, draw func()) (Answer, error) {
	ch := make(chan Answer, 1)
	a.mu.Lock()
	a.wait[q.ID] = ch
	a.open[q.ID] = q
	a.byS[q.SessionID] = append(a.byS[q.SessionID], q.ID)
	a.mu.Unlock()

	draw()

	select {
	case <-ctx.Done():
		a.forget(q.ID, q.SessionID)
		return Answer{}, ctx.Err()
	case ans, ok := <-ch:
		if !ok {
			return Answer{}, context.Canceled
		}
		return ans, nil
	}
}

// answer settles an open question. An unknown id is what a second click on the
// card produces.
func (a *asker) answer(id string, ans Answer) error {
	a.mu.Lock()
	ch, ok := a.wait[id]
	q := a.open[id]
	if ok {
		delete(a.wait, id)
		delete(a.open, id)
		a.dropIDLocked(q.SessionID, id)
	}
	a.mu.Unlock()
	if !ok {
		return ErrNoSuchQuestion
	}
	ch <- ans
	return nil
}

// lookup returns an open question, which is how a handler checks a submitted
// answer against the options it was offered.
func (a *asker) lookup(id string) (Question, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	q, ok := a.open[id]
	return q, ok
}

// pending is the question a session is waiting on, for a browser that loaded
// the page while the turn was blocked. A turn runs its tool calls one at a
// time, so there is at most one.
func (a *asker) pending(sessionID string) *Question {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range a.byS[sessionID] {
		if q, ok := a.open[id]; ok {
			return &q
		}
	}
	return nil
}

func (a *asker) cancelSession(sessionID string) {
	a.mu.Lock()
	ids := a.byS[sessionID]
	delete(a.byS, sessionID)
	var chans []chan Answer
	for _, id := range ids {
		if ch, ok := a.wait[id]; ok {
			chans = append(chans, ch)
			delete(a.wait, id)
			delete(a.open, id)
		}
	}
	a.mu.Unlock()
	for _, ch := range chans {
		close(ch)
	}
}

func (a *asker) forget(id, sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.wait, id)
	delete(a.open, id)
	a.dropIDLocked(sessionID, id)
}

func (a *asker) dropIDLocked(sessionID, id string) {
	ids := a.byS[sessionID]
	for i, v := range ids {
		if v == id {
			a.byS[sessionID] = append(ids[:i:i], ids[i+1:]...)
			return
		}
	}
}

// Answer settles an open question from outside the turn. Validation of the
// answer against the options belongs to the caller, which has the question.
func (e *Engine) Answer(id string, ans Answer) error {
	return e.asks.answer(id, ans)
}

// OpenQuestion returns a question by id, so a handler can check an answer
// against the options that were offered.
func (e *Engine) OpenQuestion(id string) (Question, bool) {
	return e.asks.lookup(id)
}

// PendingQuestion is the question a session is blocked on, or nil.
func (e *Engine) PendingQuestion(sessionID string) *Question {
	return e.asks.pending(sessionID)
}

// askQuestion puts a choice to the user and blocks the turn until it is
// answered. The answer is the tool result, so the turn carries on with it
// rather than ending and waiting for a new message.
func (e *Engine) askQuestion(ctx context.Context, s *Session, call llm.ToolCall) (tools.Result, error) {
	q, err := parseQuestion(json.RawMessage(call.Function.Arguments))
	if err != nil {
		return tools.Result{}, err
	}
	q.ID = newQuestionID()
	q.SessionID = s.ID
	q.CallID = call.ID

	// The row the call already drew is what becomes the card, so the event
	// carries the id to answer rather than the question a second time.
	draw := func() {
		e.publish(s.ID, ev("question", map[string]any{"id": q.CallID, "ask": q.ID}))
	}
	ans, err := e.asks.ask(ctx, q, draw)
	if err != nil {
		return tools.Result{Output: "no answer: the session was stopped"}, nil
	}
	return tools.Result{Output: renderAnswer(q, ans)}, nil
}

func parseQuestion(raw json.RawMessage) (Question, error) {
	var args struct {
		Question string `json:"question"`
		Options  []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
		Multi bool `json:"multi_select"`
	}
	if len(raw) == 0 {
		return Question{}, errors.New("ask_user_question: the arguments were empty")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Question{}, fmt.Errorf("ask_user_question: the arguments are not valid JSON: %s", err)
	}
	text := strings.TrimSpace(args.Question)
	if text == "" {
		return Question{}, errors.New("ask_user_question: question is empty, say what is being asked")
	}
	var opts []Option
	for _, o := range args.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" {
			continue
		}
		opts = append(opts, Option{Label: label, Description: strings.TrimSpace(o.Description)})
	}
	if len(opts) < 2 {
		return Question{}, errors.New("ask_user_question: give two to four options, each with a label")
	}
	if len(opts) > maxOptions {
		return Question{}, fmt.Errorf("ask_user_question: %d options, give at most %d", len(opts), maxOptions)
	}
	return Question{Text: text, Options: opts, Multi: args.Multi}, nil
}

// renderAnswer is the answer as the model is handed it. A typed answer is the
// user's own words, so it is passed through as it was written.
func renderAnswer(q Question, ans Answer) string {
	var labels []string
	for _, i := range ans.Options {
		if i >= 0 && i < len(q.Options) {
			labels = append(labels, q.Options[i].Label)
		}
	}
	text := strings.TrimSpace(ans.Text)
	switch {
	case len(labels) > 0 && text != "":
		return strings.Join(labels, ", ") + ": " + text
	case len(labels) > 0:
		return strings.Join(labels, ", ")
	case text != "":
		return text
	}
	return "no answer"
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func newQuestionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "ask_" + idEncoding.EncodeToString(b)[:12]
}
