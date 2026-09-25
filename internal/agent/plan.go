package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/prompts"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// ErrBadMode is a work mode outside the three the harness knows.
var ErrBadMode = errors.New("The mode is not plan or execute")

// The two answers a plan card offers. The first approves; anything else,
// including words typed instead, sends the plan back.
const (
	planApprove = "Approve and execute"
	planRevise  = "Keep planning"
)

// SetMode sets the mode the session's next turn runs in. A turn already
// running keeps the mode it started with. A mode the person picks starts new
// work, so the plan approved for the last piece of it is dropped.
func (e *Engine) SetMode(id string, mode config.WorkMode) error {
	if !config.ValidWorkMode(string(mode)) {
		return ErrBadMode
	}
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	changed := s.Mode != mode
	if changed {
		s.Mode = mode
		s.Plan = nil
		// The system prompt and the tools are the mode's own, so what was
		// measured for the last one says nothing about this one.
		s.baseProbed = false
	}
	s.mu.Unlock()
	if !changed {
		return nil
	}
	e.save(s)
	e.publish(s.ID, ev("mode", map[string]any{"mode": string(mode)}))
	return nil
}

// WorkMode is the mode a session's next turn runs in.
func (s *Session) WorkMode() config.WorkMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Mode
}

func parsePlan(raw json.RawMessage) (store.Plan, error) {
	var args struct {
		Title string   `json:"title"`
		Goal  string   `json:"goal"`
		Files []string `json:"files"`
		Steps []string `json:"steps"`
		Check string   `json:"check"`
		Open  []string `json:"open"`
	}
	if len(raw) == 0 {
		return store.Plan{}, errors.New("submit_plan: the arguments were empty")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return store.Plan{}, fmt.Errorf("submit_plan: the arguments are not valid JSON: %s", err)
	}
	p := store.Plan{
		Title: strings.TrimSpace(args.Title),
		Goal:  strings.TrimSpace(args.Goal),
		Files: trimAll(args.Files),
		Steps: trimAll(args.Steps),
		Check: strings.TrimSpace(args.Check),
		Open:  trimAll(args.Open),
	}
	if p.Title == "" {
		return store.Plan{}, errors.New("submit_plan: title is empty, name the change")
	}
	if len(p.Steps) == 0 {
		return store.Plan{}, errors.New("submit_plan: steps is empty")
	}
	if len(p.Steps) > maxTodos {
		return store.Plan{}, fmt.Errorf("submit_plan: %d steps, keep the plan under %d", len(p.Steps), maxTodos)
	}
	return p, nil
}

func trimAll(in []string) []string {
	var out []string
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// seedTodos is the todo list an approved plan starts, with its first step in
// hand.
func seedTodos(p store.Plan) []store.Todo {
	out := make([]store.Todo, 0, len(p.Steps))
	for i, step := range p.Steps {
		status := "todo"
		if i == 0 {
			status = "doing"
		}
		out = append(out, store.Todo{Text: step, Status: status})
	}
	return out
}

func renderPlan(p store.Plan) string {
	return prompts.Plan(prompts.PlanVars{Title: p.Title, Goal: p.Goal, Files: p.Files,
		Steps: p.Steps, Check: p.Check, Open: p.Open})
}

// submitPlan puts the plan to the user on a card and blocks the turn until it
// is answered. An approval is held on the session, and the turn switches to
// execute once the rest of this reply's calls are answered.
func (e *Engine) submitPlan(ctx context.Context, s *Session, call llm.ToolCall) (tools.Result, error) {
	plan, err := parsePlan(json.RawMessage(call.Function.Arguments))
	if err != nil {
		return tools.Result{}, err
	}
	q := Question{
		ID: newQuestionID(), SessionID: s.ID, CallID: call.ID, Text: plan.Title,
		Options: []Option{{Label: planApprove}, {Label: planRevise}},
	}
	draw := func() {
		e.publish(s.ID, ev("question", map[string]any{"id": q.CallID, "ask": q.ID}))
	}
	ans, err := e.asks.ask(ctx, q, draw)
	if err != nil {
		return tools.Result{Output: "no answer: the session was stopped"}, nil
	}
	if len(ans.Options) > 0 && ans.Options[0] == 0 {
		s.mu.Lock()
		s.approved = &plan
		s.mu.Unlock()
		return tools.Result{Output: "approved", Meta: map[string]any{"approved": true}}, nil
	}
	text := strings.TrimSpace(ans.Text)
	if text == "" {
		text = "the user wants changes and did not say which: ask with ask_question"
	}
	return tools.Result{Output: "not approved: " + text, Meta: map[string]any{"approved": false}}, nil
}

// takeApproval moves the session into execute mode on the plan approved this
// turn, if there is one. The plan goes onto the session and its steps become
// the todo list, and the message that starts the work is appended, so the next
// request of the same turn is an execute request. It reports whether it did.
func (e *Engine) takeApproval(s *Session) bool {
	s.mu.Lock()
	plan := s.approved
	s.approved = nil
	if plan == nil {
		s.mu.Unlock()
		return false
	}
	s.Mode = config.WorkExecute
	s.Plan = plan
	s.Todos = seedTodos(*plan)
	s.baseProbed = false
	entry := s.appendEntry(store.Entry{Kind: "user", Role: "user",
		Content: prompts.PlanApproved(plan.Title), InContext: true, Visible: true,
		Meta: map[string]any{"mode": string(config.WorkExecute), "kickoff": true}})
	todos := append([]store.Todo(nil), s.Todos...)
	s.mu.Unlock()

	e.save(s)
	e.publish(s.ID, ev("todos", map[string]any{"todos": todos}))
	e.publish(s.ID, ev("mode", map[string]any{"mode": string(config.WorkExecute),
		"kickoff": true, "seq": entry.Seq, "title": plan.Title}))
	return true
}

// unavailable answers a call to a tool this turn's mode did not send. The
// history can hold calls from an earlier mode, and a model copies what it sees,
// so the call is answered rather than failed.
func (e *Engine) unavailable(s *Session, call llm.ToolCall, mode config.WorkMode) store.Entry {
	content := fmt.Sprintf("%s is not available in %s mode", call.Function.Name, mode)
	s.mu.Lock()
	entry := s.appendEntry(store.Entry{Kind: "tool", Role: "tool",
		ToolCallID: call.ID, ToolName: call.Function.Name, Content: content,
		InContext: true, Visible: true})
	s.mu.Unlock()
	return entry
}
