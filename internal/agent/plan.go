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
		s.Draft = nil
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

// draftPatch is a write_plan call: the sections it sends, each nil when it
// was left out, so a section left out keeps what the draft holds.
type draftPatch struct {
	Title      *string   `json:"title"`
	Goal       *string   `json:"goal"`
	Code       *[]string `json:"code"`
	Tests      *[]string `json:"tests"`
	Validation *[]string `json:"validation"`
	Migration  *string   `json:"migration"`
	Risks      *[]string `json:"risks"`
	Steps      *[]string `json:"steps"`
	Open       *[]string `json:"open"`
}

func parseDraft(raw json.RawMessage) (draftPatch, error) {
	var p draftPatch
	if len(raw) == 0 {
		return p, errors.New("write_plan: the arguments were empty")
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("write_plan: the arguments are not valid JSON: %s", err)
	}
	if p.Steps != nil && len(trimAll(*p.Steps)) > maxTodos {
		return p, fmt.Errorf("write_plan: %d steps, keep the plan under %d", len(trimAll(*p.Steps)), maxTodos)
	}
	return p, nil
}

// merge writes the sections a patch sent over the draft and reports which
// they were.
func (p draftPatch) merge(d *store.Plan) []string {
	var wrote []string
	text := func(name string, from *string, to *string) {
		if from != nil {
			*to = strings.TrimSpace(*from)
			wrote = append(wrote, name)
		}
	}
	list := func(name string, from *[]string, to *[]string) {
		if from != nil {
			*to = trimAll(*from)
			wrote = append(wrote, name)
		}
	}
	text("title", p.Title, &d.Title)
	text("goal", p.Goal, &d.Goal)
	list("code", p.Code, &d.Code)
	list("tests", p.Tests, &d.Tests)
	list("validation", p.Validation, &d.Validation)
	text("migration", p.Migration, &d.Migration)
	list("risks", p.Risks, &d.Risks)
	list("steps", p.Steps, &d.Steps)
	list("open", p.Open, &d.Open)
	return wrote
}

// missing names the required sections a draft has left empty. Open is the one
// section a finished plan may leave empty.
func missing(d store.Plan) []string {
	var out []string
	if d.Title == "" {
		out = append(out, "title")
	}
	if d.Goal == "" {
		out = append(out, "goal")
	}
	for _, sec := range []struct {
		name string
		list []string
	}{{"code", d.Code}, {"tests", d.Tests}, {"validation", d.Validation}} {
		if len(sec.list) == 0 {
			out = append(out, sec.name)
		}
	}
	if d.Migration == "" {
		out = append(out, "migration")
	}
	if len(d.Risks) == 0 {
		out = append(out, "risks")
	}
	if len(d.Steps) == 0 {
		out = append(out, "steps")
	}
	return out
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

// writePlan merges sections into the session's draft and publishes it, so
// the panel follows the plan as it is written.
func (e *Engine) writePlan(s *Session, raw json.RawMessage) (tools.Result, error) {
	patch, err := parseDraft(raw)
	if err != nil {
		return tools.Result{}, err
	}
	s.mu.Lock()
	if s.Draft == nil {
		s.Draft = &store.Plan{}
	}
	wrote := patch.merge(s.Draft)
	draft := *s.Draft
	s.mu.Unlock()

	e.publish(s.ID, ev("plan_draft", map[string]any{"plan": draft}))
	if len(wrote) == 0 {
		return tools.Result{}, errors.New("write_plan: no section was sent")
	}
	out := "wrote " + strings.Join(wrote, ", ")
	if left := missing(draft); len(left) > 0 {
		out += ". still empty: " + strings.Join(left, ", ")
	} else {
		out += ". every required section is written: call exit_plan_mode"
	}
	return tools.Result{Output: out}, nil
}

// seedTodos is the todo list an approved plan starts, with its first step in
// hand.
func seedTodos(p store.Plan) []store.Todo {
	out := make([]store.Todo, 0, len(p.Steps))
	for i, step := range p.Steps {
		status := todoPending
		if i == 0 {
			status = todoInProgress
		}
		out = append(out, store.Todo{Content: step, Status: status})
	}
	return out
}

func renderPlan(p store.Plan) string {
	return prompts.Plan(prompts.PlanVars{Title: p.Title, Goal: p.Goal, Code: p.Code,
		Tests: p.Tests, Validation: p.Validation, Migration: p.Migration, Risks: p.Risks,
		Steps: p.Steps, Open: p.Open})
}

// submitPlan puts the draft to the user on a card and blocks the turn until it
// is answered. An approval is held on the session, and the turn switches to
// execute once the rest of this reply's calls are answered.
func (e *Engine) submitPlan(ctx context.Context, s *Session, call llm.ToolCall) (tools.Result, error) {
	s.mu.Lock()
	var plan store.Plan
	drafted := s.Draft != nil
	if drafted {
		plan = *s.Draft
	}
	s.mu.Unlock()
	if !drafted {
		return tools.Result{}, errors.New("exit_plan_mode: there is no draft: write the plan with write_plan first")
	}
	if left := missing(plan); len(left) > 0 {
		return tools.Result{}, fmt.Errorf("exit_plan_mode: the plan has no %s: write them with write_plan", strings.Join(left, ", "))
	}
	q := Question{
		ID: newQuestionID(), SessionID: s.ID, CallID: call.ID, Text: plan.Title,
		Options: []Option{{Label: planApprove}, {Label: planRevise}},
	}
	draw := func() {
		e.publish(s.ID, ev("question", map[string]any{"id": q.CallID, "ask": q.ID}))
	}
	ans, err := e.waitOn(ctx, s, q, draw)
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
		text = "the user wants changes and did not say which: ask with ask_user_question"
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
	entry := s.appendEntry(store.Entry{Kind: "user", Role: "user",
		Content: prompts.PlanApproved(plan.Title, renderPlan(*plan)), InContext: true, Visible: true,
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
