package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quecto-harness/internal/config"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// launcher is a harness with a second agent to hand work to. The two answer on
// different models, so the replay server can script each of them.
func launcher(t *testing.T) (*harness, config.Agent) {
	t.Helper()
	h := newHarness(t)
	reviewer := h.addAgent(t, "Reviewer", h.url, "Beta-30B")
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
	h.replay.forModel("Alpha-12B", "stream_run_agent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "have the reviewer look at internal/llm", nil)

	// The subagent is still working and the launcher has already finished.
	child := childOf(t, h, s)
	waitIdle(t, s)
	if child.State() == StateIdle {
		t.Fatal("the subagent finished before the launcher, so nothing was proved")
	}
	out := toolOutput(t, s, "run_agent")
	if !strings.Contains(out, "started Reviewer") || !strings.Contains(out, child.ID) {
		t.Errorf("run_agent answered %q, want the handle", out)
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

// A subagent stays on the agent it was named with, and is filed under its task
// rather than costing a request for a title.
func TestChildRunsOnItsNamedAgent(t *testing.T) {
	h, reviewer := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_run_agent.txt", "stream_plain.txt", "stream_plain.txt")
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
	if !named || !strings.Contains(title, "review internal/llm") {
		t.Errorf("title = %q named = %v, want it cut from the task", title, named)
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
	h.replay.forModel("Alpha-12B", "stream_run_agent.txt", "stream_plain.txt", "stream_plain.txt")
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
	for _, banned := range []string{"run_agent", "ask_question"} {
		if names[banned] {
			t.Errorf("the subagent was offered %s", banned)
		}
	}
	for _, want := range []string{"send_message", "read_file", "run_shell"} {
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
	h.replay.forModel("Alpha-12B", "stream_run_agent.txt", "stream_plain.txt", "stream_plain.txt")
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

// The limit is the launcher's own, and it counts the subagents still working.
func TestConcurrencyBoundsLiveChildren(t *testing.T) {
	h, _ := launcher(t)
	h.setAgent(t, func(a *config.Agent) { a.Concurrency = 1 })
	release := h.replay.hold("Beta-30B")
	h.replay.forModel("Alpha-12B", "stream_run_agent_two.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "launch two", nil)
	childOf(t, h, s)
	waitIdle(t, s)

	s.mu.Lock()
	var outs []string
	for _, e := range s.Entries {
		if e.Kind == "tool" && e.ToolName == "run_agent" {
			outs = append(outs, e.Content)
		}
	}
	s.mu.Unlock()
	if len(outs) != 2 {
		t.Fatalf("run_agent ran %d times, want 2", len(outs))
	}
	if !strings.Contains(outs[0], "started Reviewer") {
		t.Errorf("the first launch = %q", outs[0])
	}
	if !strings.Contains(outs[1], "which is the limit") {
		t.Errorf("the second launch = %q, want it refused", outs[1])
	}
	release()
}

// A card a subagent raises is drawn in the conversation the person has open,
// labelled with the agent that asked, and answering it there releases the
// subagent.
func TestChildCardIsDrawnInTheLauncher(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_run_agent.txt", "stream_plain.txt", "stream_plain.txt")
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
	for _, tool := range []string{"run_agent", "send_message"} {
		if got := h.broker.Evaluate(h.project, tool, "", config.ModeAsk); got != perms.ActionAllow {
			t.Errorf("%s = %v in ask mode, want allow", tool, got)
		}
	}
}

// Stopping a conversation stops the agents working for it.
func TestStopCascadesToChildren(t *testing.T) {
	h, _ := launcher(t)
	h.replay.Delay = 30 * time.Millisecond
	h.replay.forModel("Alpha-12B", "stream_run_agent.txt", "stream_sleep.txt")
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
	h.replay.forModel("Alpha-12B", "stream_run_agent.txt", "stream_plain.txt", "stream_plain.txt")
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
	h.replay.forModel("Alpha-12B", "stream_write_once.txt", "stream_plain.txt",
		"stream_run_agent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_write_once.txt", "stream_write.txt", "stream_plain.txt")

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
	h.replay.forModel("Alpha-12B", "stream_run_agent_bad.txt", "stream_plain.txt", "stream_plain.txt")

	s := h.session(t)
	h.engine.Send(s.ID, "delegate it", nil)
	waitIdle(t, s)

	out := toolOutput(t, s, "run_agent")
	if !strings.Contains(out, "no agent named") || !strings.Contains(out, "Reviewer") {
		t.Errorf("run_agent answered %q, want the names that do exist", out)
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
	top := newSession("sess_top", h.project, h.agent.ID, "Alpha-12B")
	if b := h.engine.briefFor(top, cfg); !b.offer.Agents || !b.offer.Ask || len(b.agents) != 2 || b.launcher != "" {
		t.Errorf("a conversation's brief = %+v", b)
	}
	child := newSession("sess_kid", h.project, h.agent.ID, "Alpha-12B")
	child.Parent = "sess_top"
	child.Entries = []store.Entry{{Kind: "user", From: "Local", Content: "do it"}}
	b := h.engine.briefFor(child, cfg)
	if b.offer.Agents || b.offer.Ask || !b.offer.Message || b.launcher != "Local" {
		t.Errorf("a subagent's brief = %+v", b)
	}
	if len(tools.Schemas(b.offer)) >= len(tools.Schemas(topBrief().offer)) {
		t.Error("a subagent was sent as many tools as its launcher")
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

func TestCutStopsOnAWord(t *testing.T) {
	if got := cut("review internal/llm and say what is wrong here", 20); got != "review internal/llm" {
		t.Errorf("cut = %q", got)
	}
	if got := cut("short", 20); got != "short" {
		t.Errorf("cut = %q", got)
	}
}

// A file the harness never wrote is not in the change list, so a project with
// nothing done to it stays empty.
func TestRollUpSkipsAMissingBlob(t *testing.T) {
	h, _ := launcher(t)
	parent := newSession(store.NewID(), h.project, h.agent.ID, "Alpha-12B")
	child := newSession(store.NewID(), h.project, h.agent.ID, "Beta-30B")
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
