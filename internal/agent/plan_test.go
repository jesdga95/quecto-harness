package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// sentTools names the tools a request carried.
func sentTools(body map[string]any) []string {
	var out []string
	list, _ := body["tools"].([]any)
	for _, t := range list {
		fn, _ := t.(map[string]any)["function"].(map[string]any)
		name, _ := fn["name"].(string)
		out = append(out, name)
	}
	return out
}

// systemOf is the system message a request carried.
func systemOf(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	first, _ := msgs[0].(map[string]any)
	text, _ := first["content"].(string)
	return text
}

// usersOf is the text of every user message a request carried, in order.
func usersOf(body map[string]any) []string {
	msgs, _ := body["messages"].([]any)
	var out []string
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		if msg["role"] != "user" {
			continue
		}
		text, _ := msg["content"].(string)
		out = append(out, text)
	}
	return out
}

// waitForNewQuestion blocks until the turn waits on a question other than the
// one already answered.
func waitForNewQuestion(t *testing.T, h *harness, sessionID, answered string) Question {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if q := h.engine.PendingQuestion(sessionID); q != nil && q.ID != answered {
			return *q
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no new question was asked")
	return Question{}
}

// A plan turn is sent plan's prompt and tools, writes the draft, submits it,
// is told what to change, rewrites and submits again, and is approved. The
// same turn then carries on as execute, with the plan in its prompt and its
// steps as the todo list.
func TestAPlanIsRevisedThenApprovedAndCarriedOut(t *testing.T) {
	h := newHarness(t, "stream_write_plan.txt", "stream_exit_plan_mode.txt",
		"stream_write_plan.txt", "stream_exit_plan_mode.txt", "stream_plain.txt",
		"stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	if err := h.engine.SetMode(s.ID, config.WorkPlan); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "cache the list", nil)

	first := waitForQuestion(t, h, s.ID)
	if first.Text != "Cache the list" || len(first.Options) != 2 {
		t.Fatalf("plan card = %+v", first)
	}
	body := h.replay.Body(0)
	got := sentTools(body)
	if !slices.Contains(got, "exit_plan_mode") || !slices.Contains(got, "write_plan") {
		t.Errorf("plan turn sent %v", got)
	}
	// Plan mode's rules ride on the message sent in it, not on the system
	// message, which stays the one execute sends.
	if strings.Contains(systemOf(body), "plan mode") {
		t.Errorf("plan turn's system message = %q", systemOf(body))
	}
	if users := usersOf(body); len(users) == 0 || !strings.Contains(users[0], "now in plan mode") ||
		!strings.HasSuffix(users[0], "cache the list") {
		t.Errorf("plan turn's message = %q", users)
	}

	if err := h.engine.Answer(first.ID, Answer{Options: []int{1}, Text: "add a test"}); err != nil {
		t.Fatal(err)
	}
	second := waitForNewQuestion(t, h, s.ID, first.ID)
	if err := h.engine.Answer(second.ID, Answer{Options: []int{0}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	var results []string
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.ToolName == "exit_plan_mode" {
			results = append(results, e.Content)
		}
	}
	if !slices.Equal(results, []string{"not approved: add a test", "approved"}) {
		t.Errorf("exit_plan_mode results = %q", results)
	}
	if doc.Mode != config.WorkExecute {
		t.Errorf("mode after approval = %q, want execute", doc.Mode)
	}
	if doc.Plan == nil || doc.Plan.Title != "Cache the list" || len(doc.Plan.Steps) != 2 {
		t.Fatalf("stored plan = %+v", doc.Plan)
	}
	if len(doc.Todos) != 2 || doc.Todos[0].Status != todoInProgress || doc.Todos[1].Content != "Test it" {
		t.Errorf("todos = %+v, want the steps with the first in hand", doc.Todos)
	}

	var kickoff bool
	for _, e := range doc.Entries {
		if e.Kind == "user" && e.Meta["kickoff"] == true {
			kickoff = true
			if e.Content != prompts.PlanApproved("Cache the list", renderPlan(*doc.Plan)) || e.Meta["mode"] != "execute" {
				t.Errorf("kickoff entry = %+v", e)
			}
		}
	}
	if !kickoff {
		t.Error("no entry started the execution")
	}
	kickoffs := 0
	for _, ev := range h.rec.ofType("mode") {
		if ev.Fields["kickoff"] == true {
			kickoffs++
		}
	}
	if kickoffs != 1 {
		t.Errorf("kickoff mode events = %d, want the one the approval published", kickoffs)
	}

	if doc.Draft == nil || doc.Draft.Migration != "None" {
		t.Errorf("the draft did not stay after approval: %+v", doc.Draft)
	}
	if n := len(h.rec.ofType("plan_draft")); n != 2 {
		t.Errorf("plan_draft events = %d, want one per write_plan", n)
	}
	// The execute reply leaves the plan unfinished, so the turn is sent back
	// to it twice before it ends.
	if h.replay.Count() != 7 {
		t.Fatalf("requests = %d, want four plan requests, one execute and two rounds sent back", h.replay.Count())
	}
	// The approval changes nothing at the head of the prompt: the same tools
	// and system message, with the plan in the message that approved it.
	exec := h.replay.Body(4)
	if !slices.Equal(sentTools(exec), got) || systemOf(exec) != systemOf(body) {
		t.Errorf("execute request sent %v and a different system message", sentTools(exec))
	}
	users := usersOf(exec)
	if kickoff := users[len(users)-1]; !strings.Contains(kickoff, "plan mode is over") ||
		!strings.Contains(kickoff, "Plan: Cache the list") || !strings.Contains(kickoff, "Migration: None") {
		t.Errorf("execute kickoff message = %q", kickoff)
	}
}

// A plan turn has no write tools, and a call to one it copied from the
// history is answered and not run.
func TestPlanModeAnswersAWriteItDidNotSend(t *testing.T) {
	h := newHarness(t, "stream_write.txt", "stream_plain.txt")
	s := h.session(t)
	if err := h.engine.SetMode(s.ID, config.WorkPlan); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "write it", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	result := toolResult(t, doc, "write")
	if result.Content != "write is not available in plan mode" {
		t.Errorf("tool result = %q", result.Content)
	}
	if result.Permission != nil {
		t.Errorf("a call outside the mode reached the permission policy: %+v", result.Permission)
	}
	if _, err := os.Stat(filepath.Join(h.project, "out.txt")); !os.IsNotExist(err) {
		t.Errorf("the file was written: %v", err)
	}
}

// A mode the person picks starts new work, so the approved plan goes.
func TestSetModeDropsThePlanAndRefusesAnUnknownMode(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	if err := h.engine.SetMode(s.ID, config.WorkMode("design")); err != ErrBadMode {
		t.Errorf("an unknown mode = %v, want ErrBadMode", err)
	}
	if s.WorkMode() != config.WorkExecute {
		t.Errorf("a new session is in %q, want execute", s.WorkMode())
	}

	plan := store.Plan{Title: "T", Steps: []string{"one"}}
	s.mu.Lock()
	s.Plan, s.Draft = &plan, &plan
	s.mu.Unlock()

	if err := h.engine.SetMode(s.ID, config.WorkPlan); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Plan != nil || s.Draft != nil {
		t.Errorf("after a switch plan = %+v, draft = %+v", s.Plan, s.Draft)
	}
}

// Plan mode lives on the plan tools, which a toolset may withhold, so a
// switch to plan is refused before it changes the session.
func TestSetModeRefusesPlanWithoutThePlanTools(t *testing.T) {
	h := newHarness(t)
	if _, err := h.cfg.Update(func(c *config.Config) { c.Toolset = config.ToolsetMinimal }); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	err := h.engine.SetMode(s.ID, config.WorkPlan)
	if err == nil {
		t.Fatal("plan was set under the minimal toolset")
	}
	if !strings.Contains(err.Error(), "minimal") {
		t.Errorf("the refusal does not name the toolset: %v", err)
	}
	if s.WorkMode() != config.WorkExecute {
		t.Errorf("the refusal moved the session to %q", s.WorkMode())
	}
}

// The toolset is read when a session is created, so a setting changed later
// reaches new sessions and leaves this one as it was.
func TestASessionKeepsTheToolsetItWasCreatedWith(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	if _, err := h.cfg.Update(func(c *config.Config) { c.Toolset = config.ToolsetMinimal }); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "go", nil)
	waitIdle(t, s)

	var sent []string
	for _, tool := range h.replay.LastBody()["tools"].([]any) {
		sent = append(sent, tool.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	if !slices.Contains(sent, "grep") {
		t.Errorf("tools sent = %v, want the full set the session started with", sent)
	}
	if err := h.engine.SetMode(s.ID, config.WorkPlan); err != nil {
		t.Errorf("plan refused to a session created under full: %v", err)
	}
	if later := h.session(t); later.Toolset != config.ToolsetMinimal {
		t.Errorf("a new session has %q, want minimal", later.Toolset)
	}
}

// write_plan replaces the sections it sends and keeps the rest, and names
// what is still missing.
func TestWritePlanMergesSections(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	if _, err := h.engine.writePlan(s, []byte(`{}`)); err == nil {
		t.Error("a call with no section was taken")
	}
	res, err := h.engine.writePlan(s, []byte(`{"title":" T ","steps":[" one ",""],"extra":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "wrote title, steps") || !strings.Contains(res.Output, "still empty: goal, code, tests, validation, migration, risks") {
		t.Errorf("result = %q", res.Output)
	}
	res, err = h.engine.writePlan(s, []byte(`{"goal":"g","code":["c"],"tests":["t"],"validation":["v"],"migration":"None","risks":["r"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "call exit_plan_mode") {
		t.Errorf("a complete draft said %q", res.Output)
	}
	s.mu.Lock()
	d := *s.Draft
	s.mu.Unlock()
	if d.Title != "T" || !slices.Equal(d.Steps, []string{"one"}) || d.Goal != "g" {
		t.Errorf("draft = %+v", d)
	}
	if _, err := parseDraft([]byte(`{`)); err == nil {
		t.Error("broken JSON was taken")
	}
}

// exit_plan_mode puts nothing on a card until every required section is written.
func TestSubmitPlanRefusesAnIncompleteDraft(t *testing.T) {
	h := newHarness(t, "stream_exit_plan_mode.txt", "stream_plain.txt")
	s := h.session(t)
	if err := h.engine.SetMode(s.ID, config.WorkPlan); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "plan it", nil)
	waitIdle(t, s)
	doc, _, _, _ := s.Snapshot()
	if got := toolResult(t, doc, "exit_plan_mode").Content; !strings.Contains(got, "there is no draft") {
		t.Errorf("submit with no draft = %q", got)
	}
	if missing(store.Plan{Title: "T", Goal: "g", Code: []string{"c"}, Tests: []string{"t"},
		Validation: []string{"v"}, Migration: "None", Risks: []string{"r"}, Steps: []string{"s"}}) != nil {
		t.Error("a plan with every required section is missing one")
	}
	if got := missing(store.Plan{Title: "T"}); !slices.Equal(got, []string{"goal", "code", "tests", "validation", "migration", "risks", "steps"}) {
		t.Errorf("missing = %v", got)
	}
}

// A subagent works in the mode of the session that launched it, and is never
// sent exit_plan_mode: its result goes to the launcher.
func TestASubagentKeepsItsLaunchersMode(t *testing.T) {
	h, _ := launcher(t)
	child := newSession("sess_kid", h.project, h.agent.ID, "Alpha-12B", config.ToolsetFull)
	child.Parent = "sess_top"
	child.Mode = config.WorkPlan
	b := h.engine.briefFor(child, h.cfg.Config())
	names := tools.Names(b.offer)
	if b.offer.Mode != config.WorkPlan || slices.Contains(names, "exit_plan_mode") ||
		slices.Contains(names, "write_plan") || b.offer.InMode("write") {
		t.Errorf("a planning subagent was sent %v in %q", names, b.offer.Mode)
	}
}

// A compaction carries what the messages it summarised held: plan mode's rules
// while the session is in it, and the approved plan once the message that
// approved it is summarised away.
func TestCompactionCarriesTheModeAndThePlan(t *testing.T) {
	s := newSession("sess_carry", "/p", "a", "m", config.ToolsetFull)
	b := brief{offer: tools.Offer{Mode: config.WorkPlan, Ask: true}}
	s.appendEntry(store.Entry{Kind: "user", Content: "plan it", Meta: map[string]any{"mode": "plan"}})
	if mode := s.modeAt(0, false); mode != config.WorkPlan {
		t.Fatalf("mode = %s, want plan", mode)
	}
	if carry := s.carryFor(config.WorkPlan, b, 0, false); !strings.Contains(carry, "now in plan mode") {
		t.Errorf("carry in plan mode = %q", carry)
	}

	plan := store.Plan{Title: "Cache the list", Steps: []string{"Add it"}}
	s.Plan = &plan
	kickoff := s.appendEntry(store.Entry{Kind: "user", Content: "approved",
		Meta: map[string]any{"mode": "execute", "kickoff": true}})
	if mode := s.modeAt(0, false); mode != config.WorkExecute {
		t.Fatalf("mode after the approval = %s, want execute", mode)
	}
	if carry := s.carryFor(config.WorkExecute, b, 0, false); !strings.Contains(carry, "Plan: Cache the list") {
		t.Errorf("carry once the approval is summarised = %q", carry)
	}
	if carry := s.carryFor(config.WorkExecute, b, kickoff.Seq, true); carry != "" {
		t.Errorf("carry with the approval kept = %q, want nothing", carry)
	}
}
