package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// launcher is a harness with a second agent to hand work to. The two answer on
// different models, so the replay server can script each of them, and the
// first agent's list names the second, the way the dialog would.
func launcher(t *testing.T) (*harness, config.Agent) {
	t.Helper()
	h := newHarness(t)
	reviewer := h.addAgent(t, "Reviewer", h.url, "Beta-30B")
	h.setAgent(t, func(a *config.Agent) { a.Calls = []string{reviewer.ID} })
	return h, reviewer
}

func childOf(t *testing.T, h *harness, parent *Session) *Session {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range h.engine.liveSessions() {
			if s.Parent == parent.ID {
				return s
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no subagent was launched")
	return nil
}

// toolOutput is what one tool call answered with, for a test asserting on the
// sentence the model reads rather than on the transcript. It waits, since a
// tool that delivers a message is seen at the other end before the entry that
// records it is appended here.
func toolOutput(t *testing.T, s *Session, name string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for i := len(s.Entries) - 1; i >= 0; i-- {
			if s.Entries[i].Kind == "tool" && s.Entries[i].ToolName == name {
				out := s.Entries[i].Content
				s.mu.Unlock()
				return out
			}
		}
		s.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no %s call in the transcript", name)
	return ""
}

func waitForEntryFrom(t *testing.T, s *Session, from string) store.Entry {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, e := range s.Entries {
			if e.From == from {
				s.mu.Unlock()
				return e
			}
		}
		s.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("no message from %s arrived", from)
	return store.Entry{}
}

// The launcher's turn carries on the moment the subagent starts, and what the
// subagent found arrives later as a message.
func TestRunAgentDoesNotWaitAndReportsBack(t *testing.T) {
	h, _ := launcher(t)
	release := h.replay.hold("Beta-30B")
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "have the reviewer look at internal/llm", nil)

	// The subagent is still working and the launcher has already finished.
	child := childOf(t, h, s)
	waitIdle(t, s)
	if child.State() == StateIdle {
		t.Fatal("the subagent finished before the launcher, so nothing was proved")
	}
	out := toolOutput(t, s, "subagent")
	if !strings.Contains(out, "started Reviewer") || !strings.Contains(out, child.ID) {
		t.Errorf("subagent answered %q, want the handle", out)
	}

	release()
	got := waitForEntryFrom(t, s, "Reviewer")
	if got.Content != "The repository has cmd, internal and web." {
		t.Errorf("the entry = %q, want the answer as it was written", got.Content)
	}
	waitIdle(t, s)

	// The sender is named where the prompt is built, since that is the only
	// place the model can read it.
	sent := ""
	for i := 0; i < h.replay.Count(); i++ {
		body := h.replay.Body(i)
		if body["model"] != "Alpha-12B" {
			continue
		}
		msgs, _ := body["messages"].([]any)
		for _, raw := range msgs {
			m, _ := raw.(map[string]any)
			if text, _ := m["content"].(string); strings.Contains(text, "Message from Reviewer") {
				sent = text
			}
		}
	}
	if !strings.Contains(sent, "cmd, internal and web") {
		t.Errorf("the launcher's prompt never named the sender: %q", sent)
	}
}

// A subagent stays on the agent it was named with, and is filed under the
// title its call gave rather than costing a request for one.
func TestChildRunsOnItsNamedAgent(t *testing.T) {
	h, reviewer := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)
	waitForEntryFrom(t, s, "Reviewer")

	child.mu.Lock()
	agentID, model, title, named := child.AgentID, child.Model, child.Title, child.Named
	child.mu.Unlock()
	if agentID != reviewer.ID || model != "Beta-30B" {
		t.Errorf("the subagent ran on %s/%s, want %s/Beta-30B", agentID, model, reviewer.ID)
	}
	if !named || title != "Review internal/llm" {
		t.Errorf("title = %q named = %v, want the title the call gave", title, named)
	}

	// Moving the chip must not move work already under way.
	h.engine.SetAgent()
	child.mu.Lock()
	after := child.AgentID
	child.mu.Unlock()
	if after != reviewer.ID {
		t.Errorf("the subagent moved to %s when the chip did", after)
	}
}

// A subagent hands work to nobody and asks nobody, since no person is reading
// its transcript.
func TestChildIsOfferedNeitherRunAgentNorAskQuestion(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)
	waitForEntryFrom(t, s, "Reviewer")

	var sent []any
	for i := 0; i < h.replay.Count(); i++ {
		body := h.replay.Body(i)
		if body["model"] != "Beta-30B" {
			continue
		}
		if list, ok := body["tools"].([]any); ok {
			sent = list
		}
	}
	if len(sent) == 0 {
		t.Fatal("the subagent was sent no tools at all")
	}
	names := map[string]bool{}
	for _, raw := range sent {
		spec, _ := raw.(map[string]any)
		fn, _ := spec["function"].(map[string]any)
		names[fn["name"].(string)] = true
	}
	for _, banned := range []string{"subagent", "ask_user_question"} {
		if names[banned] {
			t.Errorf("the subagent was offered %s", banned)
		}
	}
	for _, want := range []string{"send_message", "read", "bash"} {
		if !names[want] {
			t.Errorf("the subagent was not offered %s", want)
		}
	}
	if child.Parent != s.ID {
		t.Errorf("parent = %q, want %q", child.Parent, s.ID)
	}
}

// A subagent answers its launcher rather than a person, and the message joins
// the turn the launcher is in the middle of.
func TestChildMessageReachesItsLauncher(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_send_parent.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)

	got := waitForEntryFrom(t, s, "Reviewer")
	if !strings.Contains(got.Content, "which package did you mean") {
		t.Errorf("the launcher did not get the question: %q", got.Content)
	}
	if out := toolOutput(t, child, "send_message"); !strings.HasPrefix(out, "sent") {
		t.Errorf("send_message answered %q", out)
	}
	waitIdle(t, s)
}

// send_message is for a question or for progress, not for the result, so a
// turn that used it still reports what it finished with.
func TestASubagentThatSpokeStillReportsItsAnswer(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_send_parent.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)

	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, child)
	waitIdle(t, s)

	var said []string
	s.mu.Lock()
	for _, e := range s.Entries {
		if e.From == "Reviewer" {
			said = append(said, e.Content)
		}
	}
	s.mu.Unlock()
	if len(said) != 2 {
		t.Fatalf("the launcher was told %d times, want the message and the answer: %q", len(said), said)
	}
	if !strings.Contains(said[0], "which package did you mean") {
		t.Errorf("the first = %q, want what the subagent chose to send", said[0])
	}
	if !strings.Contains(said[1], "has cmd, internal and web") {
		t.Errorf("the second = %q, want the answer the turn ended on", said[1])
	}
}

// The launcher is told its change panel is stale when a subagent writes, since
// nothing else on its stream mentions a file it did not write itself.
func TestARollUpTellsTheLauncherItsFilesMoved(t *testing.T) {
	h, _ := launcher(t)
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_read.txt", "stream_write_once.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)
	waitIdle(t, child)
	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, s)

	if len(h.rec.ofType("changed")) == 0 {
		t.Error("the launcher was never told its files moved")
	}
	s.mu.Lock()
	held := len(s.Baselines)
	s.mu.Unlock()
	if held == 0 {
		t.Error("nothing rolled up, so the event proved nothing")
	}
}

// The launcher follows a subagent all the way to idle. A turn ends outside
// setState, so the state that says the work is over is the one most easily
// left unpublished.
func TestTheLauncherIsToldWhenASubagentGoesIdle(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)
	waitIdle(t, child)
	waitIdle(t, s)

	// The state is set before it is published, so the event is what to wait
	// for rather than the state that produced it.
	deadline := time.Now().Add(10 * time.Second)
	var states []string
	for time.Now().Before(deadline) {
		states = states[:0]
		fill := 0
		for _, e := range h.rec.ofType("child") {
			if e.Fields["session"] == child.ID {
				states = append(states, e.Fields["state"].(string))
				fill, _ = e.Fields["fill"].(int)
			}
		}
		if len(states) > 0 && states[len(states)-1] == string(StateIdle) {
			// The launcher's panel reads the run's context from this event.
			if fill <= 0 {
				t.Errorf("fill = %d, want the context the run holds", fill)
			}
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Errorf("the subagent was never reported idle to its launcher: %v", states)
}

// A message from another agent reaches an idle launcher as an event, or the
// turn it starts answers a message no browser ever drew.
func TestAMessageToAnIdleLauncherIsPublished(t *testing.T) {
	h, _ := launcher(t)
	// The launcher's turn ends before the subagent reports, so the report
	// starts a turn rather than joining one.
	release := h.replay.hold("Beta-30B")
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	childOf(t, h, s)
	waitIdle(t, s)

	release()
	got := waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, s)

	drawn := false
	for _, e := range h.rec.ofType("message") {
		if e.Fields["from"] == "Reviewer" && e.Fields["seq"] == got.Seq {
			drawn = true
		}
	}
	if !drawn {
		t.Errorf("no message event named the sender: %+v", h.rec.ofType("message"))
	}

	// A typed message is on the page that sent it, so drawing it again would
	// be a second bubble for one message.
	for _, e := range h.rec.ofType("message") {
		if e.Fields["from"] == "" {
			t.Errorf("a typed message was drawn by the harness: %+v", e.Fields)
		}
	}
}

// A subagent that never reached for send_message still answers, or the work
// its launcher asked for lands nowhere.
func TestASilentSubagentStillReports(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)

	got := waitForEntryFrom(t, s, "Reviewer")
	if got.Content != "The repository has cmd, internal and web." {
		t.Errorf("the entry = %q, want the answer as it was written", got.Content)
	}
	waitIdle(t, s)
}

// The limit is the launcher's own, and it counts the subagents still working.
func TestConcurrencyBoundsLiveChildren(t *testing.T) {
	h, reviewer := launcher(t)
	if _, _, err := h.cfg.UpdateAgent(reviewer.ID, func(a *config.Agent) { a.Concurrency = 1 }); err != nil {
		t.Fatal(err)
	}
	release := h.replay.hold("Beta-30B")
	h.replay.forModel("Alpha-12B", "stream_subagent_two.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "launch two", nil)
	childOf(t, h, s)
	waitIdle(t, s)

	s.mu.Lock()
	var outs []string
	for _, e := range s.Entries {
		if e.Kind == "tool" && e.ToolName == "subagent" {
			outs = append(outs, e.Content)
		}
	}
	s.mu.Unlock()
	if len(outs) != 2 {
		t.Fatalf("subagent ran %d times, want 2", len(outs))
	}
	if !strings.Contains(outs[0], "started Reviewer") {
		t.Errorf("the first launch = %q", outs[0])
	}
	if !strings.Contains(outs[1], "runs up to 1 at a time") {
		t.Errorf("the second launch = %q, want it refused", outs[1])
	}
	release()
}

// A model pinned to one slot answers one request at a time, so an agent that
// runs one at a time cannot take work from itself: its own turn is the slot.
func TestAnAgentThatRunsOneAtATimeWillNotTakeItsOwnWork(t *testing.T) {
	h := newHarness(t)
	// The only agent names itself, which is how one hands work to itself. Its
	// own turn is under way when the call is made.
	mine := h.setAgent(t, func(a *config.Agent) {
		a.Name = "Alpha"
		a.Concurrency = 1
		a.Calls = []string{a.ID}
	})
	h.replay.forModel("Alpha-12B", "stream_subagent_self.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "hand it to yourself", nil)
	waitIdle(t, s)

	out := toolOutput(t, s, "subagent")
	if !strings.Contains(out, "runs up to 1 at a time") {
		t.Errorf("subagent answered %q, want it refused against the agent's own slot", out)
	}
	if h.engine.busy(mine.ID) != 0 {
		t.Errorf("the launcher is still counted busy after its turn ended")
	}
}

// With a spare slot, an agent that names itself takes the work on top of its
// own turn, and the launch works the way any other one does.
func TestAnAgentCanTakeItsOwnWork(t *testing.T) {
	h := newHarness(t)
	mine := h.setAgent(t, func(a *config.Agent) {
		a.Name = "Alpha"
		a.Concurrency = 2
		a.Calls = []string{a.ID}
	})
	// The child runs on the same model as the parent, so the order of the
	// child's request against the parent's second is not the test's to
	// control: the two plain replies stand in for either.
	h.replay.forModel("Alpha-12B", "stream_subagent_self.txt", "stream_plain.txt", "stream_plain.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "hand it to yourself", nil)
	child := childOf(t, h, s)
	waitIdle(t, s)
	waitIdle(t, child)

	out := toolOutput(t, s, "subagent")
	if !strings.Contains(out, "started Alpha") || !strings.Contains(out, child.ID) {
		t.Errorf("subagent answered %q, want the handle", out)
	}
	child.mu.Lock()
	agentID, model := child.AgentID, child.Model
	child.mu.Unlock()
	if agentID != mine.ID || model != mine.Model {
		t.Errorf("the child runs on agent %s / model %s, want the launcher's own", agentID, model)
	}
	got := waitForEntryFrom(t, s, "Alpha")
	if got.Content != "The repository has cmd, internal and web." {
		t.Errorf("the report = %q, want the child's answer", got.Content)
	}
	// The report starts a turn of its own in the launcher.
	waitIdle(t, s)
	if h.engine.busy(mine.ID) != 0 {
		t.Errorf("the agent is still counted busy after both turns ended")
	}
}

// The prompt says how many runs each listed agent takes. "Start them together"
// with no number attached is how a model comes to write four calls at an agent
// that answers one at a time.
func TestThePromptNamesTheRunsAnAgentTakes(t *testing.T) {
	h, reviewer := launcher(t)
	if _, _, err := h.cfg.UpdateAgent(reviewer.ID, func(a *config.Agent) {
		a.Description = "Reviews Go and does not write it."
		a.Concurrency = 3
	}); err != nil {
		t.Fatal(err)
	}
	h.replay.forModel("Alpha-12B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	waitIdle(t, s)

	system, _ := textsFor(h, "Alpha-12B")
	if !strings.Contains(system, "- Reviewer (up to 3 at a time): Reviews Go and does not write it.") {
		t.Errorf("the launcher's prompt = %q, want the agent listed with the runs it takes", system)
	}
}

// What has filled up is read where the model can act on it, at the tail of the
// conversation, rather than in the prompt that opens every request.
func TestStartedRunCountsTheSlotsInUse(t *testing.T) {
	h, reviewer := launcher(t)
	if _, _, err := h.cfg.UpdateAgent(reviewer.ID, func(a *config.Agent) { a.Concurrency = 3 }); err != nil {
		t.Fatal(err)
	}
	release := h.replay.hold("Beta-30B")
	h.replay.forModel("Alpha-12B", "stream_subagent_two.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "launch two", nil)
	childOf(t, h, s)
	waitIdle(t, s)

	s.mu.Lock()
	var outs []string
	for _, e := range s.Entries {
		if e.Kind == "tool" && e.ToolName == "subagent" {
			outs = append(outs, e.Content)
		}
	}
	s.mu.Unlock()
	if len(outs) != 2 {
		t.Fatalf("subagent ran %d times, want 2", len(outs))
	}
	if !strings.Contains(outs[0], "1 of its 3 slots in use") {
		t.Errorf("the first launch = %q, want the slot it filled", outs[0])
	}
	if !strings.Contains(outs[1], "2 of its 3 slots in use") {
		t.Errorf("the second launch = %q, want both slots counted", outs[1])
	}
	release()
}

// A card a subagent raises is drawn in the conversation the person has open,
// labelled with the agent that asked, and answering it there releases the
// subagent.
func TestChildCardIsDrawnInTheLauncher(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_tool.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)

	id := waitForCard(t, h, s.ID)
	req, ok := h.broker.Lookup(id)
	if !ok {
		t.Fatal("the card went missing")
	}
	if req.SessionID != child.ID || req.Route != s.ID {
		t.Errorf("card blocks %s and is drawn in %s, want %s and %s", req.SessionID, req.Route, child.ID, s.ID)
	}
	if req.Agent != "Reviewer" {
		t.Errorf("card agent = %q, want the one that asked", req.Agent)
	}
	if got := h.broker.Pending(child.ID); len(got) != 0 {
		t.Errorf("the subagent's own transcript drew the card: %+v", got)
	}
	if err := h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce}); err != nil {
		t.Fatal(err)
	}
	waitForEntryFrom(t, s, "Reviewer")
}

// Launching an agent and messaging one touch nothing on the machine, so
// neither is carded.
func TestLaunchingAnAgentIsNotCarded(t *testing.T) {
	h, _ := launcher(t)
	for _, tool := range []string{"subagent", "send_message"} {
		if got := h.broker.Evaluate(h.project, tool, "", "", false, config.ModeAsk); got != perms.ActionAllow {
			t.Errorf("%s = %v in ask mode, want allow", tool, got)
		}
	}
}

// Stopping a conversation stops the agents working for it.
func TestStopCascadesToChildren(t *testing.T) {
	h, _ := launcher(t)
	h.replay.Delay = 30 * time.Millisecond
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_sleep.txt")
	h.replay.forModel("Beta-30B", "stream_sleep.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)
	waitState(t, child, StateStreaming)

	if _, err := h.engine.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, child)
	child.mu.Lock()
	stopped := false
	for _, e := range child.Entries {
		if e.Kind == "status" && strings.Contains(e.Content, "Stopped") {
			stopped = true
		}
	}
	child.mu.Unlock()
	if !stopped {
		t.Error("the subagent was not stopped with its launcher")
	}
}

// A subagent run is reachable only through its launcher, so deleting the
// launcher takes it too.
func TestDeleteRemovesChildren(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)
	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, s)
	waitIdle(t, child)

	if kids := h.store.Children(s.ID); len(kids) != 1 || kids[0].ID != child.ID {
		t.Fatalf("children = %+v", kids)
	}
	if list := h.store.List(h.project); len(list) != 1 || list[0].ID != s.ID {
		t.Errorf("the sidebar listed a subagent run: %+v", list)
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Load(child.ID); err == nil {
		t.Error("the subagent's file outlived its launcher")
	}
}

// The files a subagent wrote belong in the launcher's change panel, and a path
// the launcher wrote first keeps the launcher's older copy.
func TestChildBaselinesRollUp(t *testing.T) {
	h, _ := launcher(t)
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })
	h.replay.forModel("Alpha-12B", "stream_read.txt", "stream_write_once.txt", "stream_plain.txt",
		"stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_read.txt", "stream_write_once.txt", "stream_write.txt", "stream_plain.txt")

	// The launcher writes file.txt first, so the copy kept is the one from
	// before either of them touched it.
	s := h.session(t)
	h.engine.Send(s.ID, "write the file", nil)
	waitIdle(t, s)
	mine := baselineOf(t, s, "file.txt")
	before, err := h.store.Blob(s.ID, mine.Blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != "one\ntwo\n" {
		t.Fatalf("the launcher's copy = %q, want the file as it started", before)
	}

	h.engine.Send(s.ID, "now have the reviewer work on it", nil)
	child := childOf(t, h, s)
	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, child)

	// out.txt is the subagent's alone and comes across.
	rolled := baselineOf(t, s, "out.txt")
	if rolled.State != store.BaselineNew {
		t.Errorf("out.txt = %+v, want it recorded as new", rolled)
	}
	// file.txt is a path both wrote, and the launcher's older copy stands.
	after := baselineOf(t, s, "file.txt")
	got, err := h.store.Blob(s.ID, after.Blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(before) {
		t.Errorf("the launcher's baseline was overwritten with %q, want %q", got, before)
	}
	changes, err := h.engine.Changes(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Errorf("the change panel lists %d files, want both", len(changes))
	}
}

func baselineOf(t *testing.T, s *Session, path string) store.Baseline {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.Baselines {
		if b.Path == path {
			return b
		}
	}
	t.Fatalf("%s is not in the change list: %+v", path, s.Baselines)
	return store.Baseline{}
}

// A name nothing matches is a tool error the turn carries on from.
func TestUnknownAgentIsAToolError(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent_bad.txt", "stream_plain.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	waitIdle(t, s)

	out := toolOutput(t, s, "subagent")
	if !strings.Contains(out, "no agent named") || !strings.Contains(out, "Reviewer") {
		t.Errorf("subagent answered %q, want the names that do exist", out)
	}
	if len(h.engine.liveChildren(s.ID)) != 0 {
		t.Error("a bad name started an agent anyway")
	}
	s.mu.Lock()
	last := s.Entries[len(s.Entries)-1]
	s.mu.Unlock()
	if last.Kind != "assistant" {
		t.Errorf("the turn ended on a %s entry, want it to have carried on", last.Kind)
	}
}

// A conversation somebody started is offered the agents; a subagent is offered
// the launcher instead.
func TestBriefFollowsTheParent(t *testing.T) {
	h, _ := launcher(t)
	cfg := h.cfg.Config()
	top := newSession("sess_top", h.project, h.agent.ID, "Alpha-12B", config.ToolsetFull)
	// Two agents are configured, and the one answering is spending its only
	// slot on this turn, so the one it can hand work to is the other.
	b := h.engine.briefFor(top, cfg)
	if !b.offer.Agents || !b.offer.Ask || b.launcher != "" {
		t.Errorf("a conversation's brief = %+v", b)
	}
	if len(b.agents) != 1 || b.agents[0].Name != "Reviewer" {
		t.Errorf("the agents it may hand work to = %+v", b.agents)
	}
	if len(b.agents) == 1 && b.agents[0].Slots != 1 {
		t.Errorf("the other agent was listed with %d slots, want its own 1", b.agents[0].Slots)
	}
	child := newSession("sess_kid", h.project, h.agent.ID, "Alpha-12B", config.ToolsetFull)
	child.Parent = "sess_top"
	child.Entries = []store.Entry{{Kind: "user", From: "Local", Content: "do it"}}
	b = h.engine.briefFor(child, cfg)
	if b.offer.Agents || b.offer.Ask || !b.offer.Message || b.launcher != "sess_top" {
		t.Errorf("a subagent's brief = %+v", b)
	}
	if len(tools.Schemas(b.offer)) >= len(tools.Schemas(topBrief().offer)) {
		t.Error("a subagent was sent as many tools as its launcher")
	}
}

// An agent may name itself in its own calls list, and the turn's own slot
// decides whether the name is worth offering.
func TestBriefListsTheCallerWhenASlotIsFree(t *testing.T) {
	h := newHarness(t)
	mine := h.setAgent(t, func(a *config.Agent) { a.Calls = []string{a.ID} })
	cfg := h.cfg.Config()
	top := newSession("sess_top", h.project, h.agent.ID, "Alpha-12B", config.ToolsetFull)
	b := h.engine.briefFor(top, cfg)
	if len(b.agents) != 0 || b.offer.Agents {
		t.Errorf("a single-slot agent is offered to itself: %+v", b.agents)
	}
	h.setAgent(t, func(a *config.Agent) { a.Concurrency = 2 })
	b = h.engine.briefFor(top, h.cfg.Config())
	if len(b.agents) != 1 || b.agents[0].Name != mine.Name {
		t.Errorf("a two-slot agent is not offered to itself: %+v", b.agents)
	}
	// The number the prompt will show is what is left of its limit once this
	// turn's own slot is spent, so a model launching onto its own agent is
	// told the truth it will meet at launch.
	if len(b.agents) == 1 && b.agents[0].Slots != 1 {
		t.Errorf("it was listed with %d slots, want 1 of its 2", b.agents[0].Slots)
	}
	if !b.offer.Agents {
		t.Errorf("the brief offers no agents: %+v", b)
	}
}

// role is the one line of an agent's instructions the prompt shows.
func TestRoleIsTheFirstLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""},
		{"  Reviews Go.  ", "Reviews Go."},
		{"Reviews Go.\nDoes not write it.", "Reviews Go."},
		{"\n\nReviews Go.", "Reviews Go."},
	} {
		if got := role(config.Agent{Instructions: tc.in}); got != tc.want {
			t.Errorf("role(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The description says what an agent is for. Instructions answer for an agent
// written before the field existed, and are not read once one is set.
func TestDescriptionOutranksTheInstructions(t *testing.T) {
	a := config.Agent{Description: "Used for design work.", Instructions: "Answer in British English."}
	if got := role(a); got != "Used for design work." {
		t.Errorf("role = %q, want the description", got)
	}
	if got := role(config.Agent{Instructions: "Reviews Go."}); got != "Reviews Go." {
		t.Errorf("role = %q, want the instructions where there is no description", got)
	}
}

// A wait that is not a JSON boolean is refused, and no agent starts.
func TestSubagentWaitMustBeABoolean(t *testing.T) {
	h, _ := launcher(t)
	s := h.session(t)
	args := `{"agent":"Reviewer","title":"Review the package","task":"look","wait":"False"}`
	_, _, err := h.engine.startAgent(s, llm.ToolCall{ID: "call_w", Type: "function",
		Function: llm.FunctionCall{Name: "subagent", Arguments: args}}, nil)
	if err == nil || !strings.Contains(err.Error(), `wait got string "False", expected JSON boolean true or false`) {
		t.Errorf("%s answered %v", args, err)
	}
	for _, live := range h.engine.liveSessions() {
		if live.Parent == s.ID {
			t.Fatal("a refused wait started an agent")
		}
	}
}

// A title that is missing, blank or too long is refused before anything
// starts, and one that passes is filed with its whitespace collapsed.
func TestTitleIsRequiredAndBounded(t *testing.T) {
	h, _ := launcher(t)
	s := h.session(t)
	call := func(args string) llm.ToolCall {
		return llm.ToolCall{ID: "call_t", Type: "function", Function: llm.FunctionCall{Name: "subagent", Arguments: args}}
	}
	for _, tc := range []struct{ args, want string }{
		{`{"agent":"Reviewer","task":"look"}`, "title is empty"},
		{`{"agent":"Reviewer","title":"  \n ","task":"look"}`, "title is empty"},
		{`{"agent":"Reviewer","title":"` + strings.Repeat("é", maxTitle+1) + `","task":"look"}`, "the most is 60"},
	} {
		if _, _, err := h.engine.startAgent(s, call(tc.args), nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s answered %v, want %q", tc.args, err, tc.want)
		}
	}
	for _, live := range h.engine.liveSessions() {
		if live.Parent == s.ID {
			t.Fatal("a refused title started an agent")
		}
	}

	h.replay.forModel("Alpha-12B", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")
	child, _, err := h.engine.startAgent(s, call(`{"agent":"Reviewer","title":"  Review\n  the   llm package ","task":"look"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	child.mu.Lock()
	title := child.Title
	child.mu.Unlock()
	if title != "Review the llm package" {
		t.Errorf("title = %q, want it trimmed and collapsed", title)
	}
	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, s)
}

// A call with no title is a tool error the turn carries on from, and the task
// is not taken as a name in its place.
func TestAnUntitledCallIsAToolError(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent_untitled.txt", "stream_plain.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	waitIdle(t, s)

	if out := toolOutput(t, s, "subagent"); !strings.Contains(out, "title is empty") {
		t.Errorf("subagent answered %q, want the missing title named", out)
	}
	for _, live := range h.engine.liveSessions() {
		if live.Parent == s.ID {
			t.Error("an untitled call started an agent anyway")
		}
	}
	s.mu.Lock()
	last := s.Entries[len(s.Entries)-1]
	s.mu.Unlock()
	if last.Kind != "assistant" {
		t.Errorf("the turn ended on a %s entry, want it to have carried on", last.Kind)
	}
}

// A file the harness never wrote is not in the change list, so a project with
// nothing done to it stays empty.
func TestRollUpSkipsAMissingBlob(t *testing.T) {
	h, _ := launcher(t)
	parent := newSession(store.NewID(), h.project, h.agent.ID, "Alpha-12B", config.ToolsetFull)
	child := newSession(store.NewID(), h.project, h.agent.ID, "Beta-30B", config.ToolsetFull)
	child.Parent = parent.ID
	child.Baselines = []store.Baseline{{Path: "gone.txt", State: store.BaselineText, Blob: store.BlobName("gone.txt")}}
	h.engine.rollUp(child, parent)
	if len(parent.Baselines) != 0 {
		t.Errorf("a baseline with no copy was listed: %+v", parent.Baselines)
	}

	// One with a copy comes across.
	if err := h.store.WriteBlob(child.ID, store.BlobName("here.txt"), []byte("old\n")); err != nil {
		t.Fatal(err)
	}
	child.Baselines = append(child.Baselines, store.Baseline{
		Path: "here.txt", State: store.BaselineText, Blob: store.BlobName("here.txt")})
	h.engine.rollUp(child, parent)
	if len(parent.Baselines) != 1 || parent.Baselines[0].Path != "here.txt" {
		t.Fatalf("baselines = %+v", parent.Baselines)
	}
	got, err := h.store.Blob(parent.ID, parent.Baselines[0].Blob)
	if err != nil || string(got) != "old\n" {
		t.Errorf("copy = %q %v", got, err)
	}
	_ = os.WriteFile(filepath.Join(h.project, "here.txt"), []byte("new\n"), 0o644)
}

// A disabled agent is not in the list the prompt names, and a name the model
// remembers from before it was disabled no longer matches.
func TestADisabledAgentTakesNoWork(t *testing.T) {
	h, reviewer := launcher(t)
	if _, _, err := h.cfg.UpdateAgent(reviewer.ID, func(a *config.Agent) { a.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	cfg := h.cfg.Config()

	top := newSession("sess_top", h.project, h.agent.ID, "Alpha-12B", config.ToolsetFull)
	b := h.engine.briefFor(top, cfg)
	if len(b.agents) != 0 || b.offer.Agents {
		t.Errorf("the agents it may hand work to = %+v, want none", b.agents)
	}
	if _, err := findAgent(cfg, h.agent, "Reviewer"); err == nil {
		t.Error("a disabled agent was found by name")
	}

	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "have the reviewer look at internal/llm", nil)
	waitIdle(t, s)
	if out := toolOutput(t, s, "subagent"); !strings.Contains(out, "no agent") {
		t.Errorf("subagent answered %q, want a refusal", out)
	}
	if len(h.engine.liveChildren(s.ID)) != 0 {
		t.Error("a disabled agent was started anyway")
	}
}

// An agent the caller's list does not name is not in the brief, and a call by
// name is refused by the tool rather than started.
func TestAnAgentOffTheListTakesNoWork(t *testing.T) {
	h, _ := launcher(t)
	h.setAgent(t, func(a *config.Agent) { a.Calls = nil })
	cfg := h.cfg.Config()

	// The list names nothing, so the brief offers no subagent at all.
	top := newSession("sess_top", h.project, h.agent.ID, "Alpha-12B", config.ToolsetFull)
	b := h.engine.briefFor(top, cfg)
	if len(b.agents) != 0 || b.offer.Agents {
		t.Errorf("the agents it may hand work to = %+v, want none", b.agents)
	}
	if _, err := findAgent(cfg, h.agent, "Reviewer"); err == nil {
		t.Error("an agent off the list was found by name")
	}
	if _, err := findAgent(cfg, h.agent, "Nobody"); err == nil || !strings.Contains(err.Error(), "no agents to run this") {
		t.Errorf("a misspelled name was told %v, want the empty-list sentence", err)
	}

	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "have the reviewer look at internal/llm", nil)
	waitIdle(t, s)
	if out := toolOutput(t, s, "subagent"); !strings.Contains(out, "no agent") {
		t.Errorf("subagent answered %q, want a refusal", out)
	}
	if len(h.engine.liveChildren(s.ID)) != 0 {
		t.Error("an agent off the list was started anyway")
	}
}

// A misspelled call is told what the caller was told, not the whole roster:
// an enabled agent off the list stays out of the names.
func TestAMisspelledCallIsToldTheListNotTheRoster(t *testing.T) {
	h, _ := launcher(t)
	h.addAgent(t, "Scout", h.url, "Gamma-7B")
	cfg := h.cfg.Config()
	if _, err := findAgent(cfg, h.agent, "Nobody"); err == nil || !strings.Contains(err.Error(), "Reviewer") || strings.Contains(err.Error(), "Scout") {
		t.Errorf("a misspelled name was told %v, want the list without the off-list agent", err)
	}
}

// The gate is the caller's list, not anything on the target: an agent nobody
// else names still takes work from one that names it.
func TestAnUnnamedTargetStillLaunches(t *testing.T) {
	h, _ := launcher(t)
	release := h.replay.hold("Beta-30B")
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "have the reviewer look at internal/llm", nil)
	child := childOf(t, h, s)
	waitIdle(t, s)
	release()
	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, s)
	if !strings.Contains(toolOutput(t, s, "subagent"), child.ID) {
		t.Error("the launch was not answered with a handle")
	}
}

// usable does not gate on the list: an agent that names nobody answers as the
// selected agent, which is the default for one the dialog just made.
func TestAnAgentThatNamesNobodyAnswersAsTheSelectedAgent(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	a, _ := h.cfg.Config().Agent()
	if a.Calls != nil {
		t.Fatalf("a fresh agent came up with a call list: %+v", a.Calls)
	}
	if err := usable(a); err != nil {
		t.Fatalf("an agent that names nobody was reported not ready: %v", err)
	}
	s := h.session(t)
	if _, err := h.engine.Send(s.ID, "what is here", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "assistant" || !strings.Contains(last.Content, "cmd, internal and web") {
		t.Errorf("assistant entry = %+v, want the answer", last)
	}
}

// An agent is addressed by session handle. A subagent's prompt gives it its
// launcher's, and every message carries the handle of whoever sent it.
func TestAgentsAreAddressedByHandle(t *testing.T) {
	h, _ := launcher(t)
	release := h.replay.hold("Beta-30B")
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "have the reviewer look at internal/llm", nil)
	child := childOf(t, h, s)
	waitIdle(t, s)
	release()
	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, s)

	// The subagent was told where to send, and the task it arrived on says who
	// it came from and where that is.
	system, task := textsFor(h, "Beta-30B")
	if !strings.Contains(system, s.ID) {
		t.Errorf("the subagent's prompt never gave its launcher's handle: %q", system)
	}
	if !strings.Contains(task, "Message from Local ("+s.ID+")") {
		t.Errorf("the task did not carry the sender's handle: %q", task)
	}

	// The launcher reads the same thing about the agent it started.
	_, report := textsFor(h, "Alpha-12B")
	if !strings.Contains(report, "Message from Reviewer ("+child.ID+")") {
		t.Errorf("the report did not carry the sender's handle: %q", report)
	}
}

// A name is not a handle, and the refusal says which handles are.
func TestSendMessageToANameNamesTheHandles(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "have the reviewer look at internal/llm", nil)
	child := childOf(t, h, s)
	waitIdle(t, s)

	_, err := h.engine.sendMessage(s, llm.ToolCall{
		Function: llm.FunctionCall{Arguments: `{"to":"Reviewer","text":"one more thing"}`},
	})
	if err == nil {
		t.Fatal("a name was accepted as a handle")
	}
	if !strings.Contains(err.Error(), child.ID) {
		t.Errorf("the refusal = %q, want the handle that would have worked", err)
	}
}

// textsFor is the last system prompt and the last message text one model was
// sent, for a test reading what the model was told.
func textsFor(h *harness, model string) (system, message string) {
	for i := 0; i < h.replay.Count(); i++ {
		body := h.replay.Body(i)
		if body["model"] != model {
			continue
		}
		msgs, _ := body["messages"].([]any)
		for _, raw := range msgs {
			m, _ := raw.(map[string]any)
			text, _ := m["content"].(string)
			switch {
			case m["role"] == "system":
				system = text
			case strings.HasPrefix(text, "Message from "):
				message = text
			}
		}
	}
	return system, message
}

// A child whose last reply hit the token limit reports that it ran out of room,
// rather than passing the cut-off text on as a finished answer.
func TestAnswerOfNamesACutOffReply(t *testing.T) {
	e := &Engine{}
	child := newSession("sess_child", "/p", "a", "m", config.ToolsetFull)
	child.Entries = []store.Entry{{Kind: "assistant", Content: "half an answer",
		Meta: map[string]any{"finish_reason": "length"}}}
	got := e.answerOf(child)
	if !strings.HasPrefix(got, "half an answer\n") || !strings.Contains(got, "ran out of room") {
		t.Errorf("answer = %q", got)
	}
	child.Entries[0].Meta["finish_reason"] = "stop"
	if got := e.answerOf(child); got != "half an answer" {
		t.Errorf("a finished reply = %q, want it as it stands", got)
	}
}

// A subagent call waits by default: the result is the call's own result, and
// nothing arrives as a message on top of it.
func TestSubagentWaitsForItsResult(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent_wait.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	child := childOf(t, h, s)
	waitIdle(t, s)
	waitIdle(t, child)

	out := toolOutput(t, s, "subagent")
	if !strings.HasPrefix(out, "Reviewer finished and will do no further work") ||
		!strings.Contains(out, "The repository has cmd, internal and web.") {
		t.Errorf("result = %q, want the subagent's answer", out)
	}
	doc, _, _, _ := s.Snapshot()
	for _, e := range doc.Entries {
		if e.Kind == "user" && e.From == "Reviewer" {
			t.Errorf("the result also arrived as a message: %q", e.Content)
		}
	}
}

// Waiting subagent calls in one reply run at the same time: both are working
// before either has answered.
func TestSiblingSubagentsRunTogether(t *testing.T) {
	h, reviewer := launcher(t)
	if _, _, err := h.cfg.UpdateAgent(reviewer.ID, func(a *config.Agent) { a.Concurrency = 2 }); err != nil {
		t.Fatal(err)
	}
	release := h.replay.hold("Beta-30B")
	h.replay.forModel("Alpha-12B", "stream_subagent_two_wait.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "launch two", nil)
	deadline := time.Now().Add(10 * time.Second)
	for len(h.engine.liveChildren(s.ID)) < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if n := len(h.engine.liveChildren(s.ID)); n != 2 {
		t.Fatalf("working subagents = %d, want both before either answered", n)
	}
	release()
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	results := 0
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.ToolName == "subagent" && strings.HasPrefix(e.Content, "Reviewer finished") {
			results++
		}
	}
	if results != 2 {
		t.Errorf("results = %d, want one per call", results)
	}
}
