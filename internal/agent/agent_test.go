package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// recorder collects published events so a test can assert what a browser
// would have seen.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) Publish(_ string, e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) ofType(kind string) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.Type == kind {
			out = append(out, e)
		}
	}
	return out
}

func (r *recorder) states() []string {
	var out []string
	for _, e := range r.ofType("status") {
		out = append(out, e.Fields["state"].(string))
	}
	return out
}

type harness struct {
	engine  *Engine
	cfg     *config.Store
	broker  *perms.Broker
	rec     *recorder
	replay  *replay
	store   *store.Sessions
	project string
	cancel  context.CancelFunc
}

func newHarness(t *testing.T, fixtures ...string) *harness {
	t.Helper()
	home := t.TempDir()
	paths := config.DiscoverPathsIn(home)
	if err := paths.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Open(paths)
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "file.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.AddProject(project); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(project)

	st, err := store.Open(paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := store.OpenFiles(paths.Files)
	if err != nil {
		t.Fatal(err)
	}
	rp := newReplay(t, fixtures...)
	srv := rp.Server()
	t.Cleanup(srv.Close)

	rec := &recorder{}
	broker := perms.New(cfg, func(sid string, r perms.Request) { rec.Publish(sid, PermissionEvent(r)) })
	ctx, cancel := context.WithCancel(context.Background())
	engine := NewEngine(ctx, cfg, st, blobs, llm.New(srv.URL, ""), broker, rec)
	// The temporary home goes away when the test ends, so nothing the engine
	// started may still be writing sessions into it.
	t.Cleanup(func() {
		cancel()
		engine.Wait(5 * time.Second)
	})

	return &harness{engine: engine, cfg: cfg, broker: broker, rec: rec,
		replay: rp, store: st, project: real, cancel: cancel}
}

func (h *harness) session(t *testing.T) *Session {
	return h.sessionWith(t, "Alpha-12B")
}

func (h *harness) sessionWith(t *testing.T, model string) *Session {
	t.Helper()
	if _, err := h.cfg.Update(func(c *config.Config) { c.Model = model }); err != nil {
		t.Fatal(err)
	}
	s, err := h.engine.Create(h.project)
	if err != nil {
		t.Fatal(err)
	}
	// Choosing a model reads it, and a session refuses a message until that
	// has finished, which is what the composer does.
	waitIdle(t, s)
	return s
}

// waitForRequests blocks until the replay server has been asked n times, which
// is how a test waits for the title request that follows a first turn.
func waitForRequests(t *testing.T, h *harness, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if h.replay.Count() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the replay server was asked %d times, want %d", h.replay.Count(), n)
}

func waitIdle(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == StateIdle {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("session stayed in %s", s.State())
}

func TestPlainReply(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	if _, err := h.engine.Send(s.ID, "what is here", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, fill, _ := s.Snapshot()
	if len(doc.Entries) != 2 {
		t.Fatalf("entries = %d, want user then assistant", len(doc.Entries))
	}
	if doc.Entries[1].Kind != "assistant" || !strings.Contains(doc.Entries[1].Content, "cmd, internal and web") {
		t.Errorf("assistant entry = %+v", doc.Entries[1])
	}
	if doc.Title != "Read the project layout" {
		t.Errorf("title = %q", doc.Title)
	}
	if fill == 0 {
		t.Error("fill was not computed")
	}
	states := h.rec.states()
	// Choosing the model reads it, which is the loading pair in front.
	want := []string{"loading", "idle", "preflight", "streaming", "final", "idle"}
	if strings.Join(states, ",") != strings.Join(want, ",") {
		t.Errorf("states = %v, want %v", states, want)
	}
	if len(h.rec.ofType("delta")) != 2 {
		t.Errorf("delta events = %d", len(h.rec.ofType("delta")))
	}
	if len(h.rec.ofType("usage")) != 1 {
		t.Errorf("usage events = %d", len(h.rec.ofType("usage")))
	}
	if len(h.rec.ofType("done")) != 1 {
		t.Errorf("done events = %d", len(h.rec.ofType("done")))
	}
}

func TestTurnCostIsRecordedOnTheEntryThatEndedIt(t *testing.T) {
	// Two requests in one turn, so the tokens are a sum rather than the last
	// response's own count: 22 for the tool call and 48 for the answer.
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list the files here", nil)

	id := waitForCard(t, h, s.ID)
	if err := h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "assistant" {
		t.Fatalf("last entry = %s, want assistant", last.Kind)
	}
	if last.Meta["tokens"] != 70 {
		t.Errorf("tokens = %v, want 70", last.Meta["tokens"])
	}
	if last.Meta["requests"] != 2 {
		t.Errorf("requests = %v, want 2", last.Meta["requests"])
	}
	if ms, ok := last.Meta["ms"].(int64); !ok || ms < 0 {
		t.Errorf("ms = %v", last.Meta["ms"])
	}
	// The rate is measured from the first token of each response, so a replay
	// served from memory reports a large one rather than none at all.
	if rate, ok := last.Meta["tok_per_s"].(float64); !ok || rate <= 0 {
		t.Errorf("tok_per_s = %v", last.Meta["tok_per_s"])
	}

	done := h.rec.ofType("done")
	if len(done) != 1 {
		t.Fatalf("done events = %d", len(done))
	}
	if done[0].Fields["tokens"] != 70 {
		t.Errorf("done tokens = %v, want 70", done[0].Fields["tokens"])
	}
}

// A turn the user stopped has no assistant entry to carry the cost, and the
// done event that ends it reports none.
func TestStoppedTurnReportsNoCost(t *testing.T) {
	h := newHarness(t, "stream_sleep.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "sleep", nil)
	waitForCard(t, h, s.ID)
	if _, err := h.engine.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	done := h.rec.ofType("done")
	if len(done) != 1 {
		t.Fatalf("done events = %d", len(done))
	}
	if _, ok := done[0].Fields["tokens"]; ok {
		t.Errorf("a stopped turn reported a cost: %+v", done[0].Fields)
	}
}

func TestEffortReachesTheRequestBody(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	if _, err := h.cfg.Update(func(c *config.Config) { c.Effort = config.EffortHigh }); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "hello", nil)
	waitIdle(t, s)

	body := h.replay.Body(0)
	// The captured template reports supports_reasoning_effort false, so high
	// takes the budget column.
	if body["reasoning_budget"] != -1.0 {
		t.Errorf("reasoning_budget = %v, want -1", body["reasoning_budget"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Error("reasoning_effort was sent to a template that cannot take it")
	}
	if body["reasoning_format"] != "deepseek" {
		t.Errorf("reasoning_format = %v", body["reasoning_format"])
	}
	sent, _ := body["tools"].([]any)
	if want := len(tools.Schemas()); len(sent) != want {
		t.Errorf("tools = %d, want %d", len(sent), want)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want system and user", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || !strings.Contains(first["content"].(string), h.project) {
		t.Errorf("system message = %v", first)
	}
}

// The model is read when it is chosen, so the first request of a session is
// already shaped by what its template accepts.
func TestFirstRequestIsShapedByTheTemplate(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	if _, err := h.cfg.Update(func(c *config.Config) { c.PreserveReasoning = false }); err != nil {
		t.Fatal(err)
	}
	s := h.sessionWith(t, "Delta3.8-27B Q5")
	h.engine.Send(s.ID, "first", nil)
	waitIdle(t, s)

	first := h.replay.Body(0)
	kw, _ := first["chat_template_kwargs"].(map[string]any)
	if kw == nil || kw["preserve_reasoning"] != false {
		t.Errorf("chat_template_kwargs = %v, want preserve_reasoning false", first["chat_template_kwargs"])
	}
	if first["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", first["reasoning_effort"])
	}
	if _, ok := first["reasoning_budget"]; ok {
		t.Error("reasoning_budget was sent to a template that takes named levels")
	}
}

// A props read that lands between two steps of one turn does not change the
// shape of the requests. The flags reach the template, so a turn that changes
// them renders a different prompt and the model server reprocesses everything
// before the change.
func TestCapsDoNotChangeMidTurn(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.sessionWith(t, "Delta3.8-27B Q5")
	h.engine.Send(s.ID, "list the files here", nil)

	id := waitForCard(t, h, s.ID)
	if err := h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	first, second := h.replay.Body(0), h.replay.Body(1)
	for _, field := range []string{"reasoning_effort", "reasoning_budget", "chat_template_kwargs"} {
		a, _ := json.Marshal(first[field])
		b, _ := json.Marshal(second[field])
		if string(a) != string(b) {
			t.Errorf("%s = %s, was %s", field, b, a)
		}
	}
}

func TestSingleToolCallWithApproval(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list the files here", nil)

	id := waitForCard(t, h, s.ID)
	if err := h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if len(doc.Entries) != 4 {
		t.Fatalf("entries = %d: %+v", len(doc.Entries), doc.Entries)
	}
	if doc.Entries[1].Reasoning == "" {
		t.Error("reasoning was not stored")
	}
	tool := doc.Entries[2]
	if tool.Kind != "tool" || tool.ToolCallID != "call_0" || tool.ExitCode == nil || *tool.ExitCode != 0 {
		t.Errorf("tool entry = %+v", tool)
	}
	if tool.Permission == nil || tool.Permission.Decision != "once" {
		t.Errorf("permission record = %+v", tool.Permission)
	}
	if !strings.Contains(tool.Content, "file.txt") {
		t.Errorf("tool output = %q", tool.Content)
	}
	if len(h.rec.ofType("tool_output")) != 1 {
		t.Error("no tool_output event")
	}
	// Reasoning goes back with the assistant message it came from, so the
	// prompt for the next step matches what the model generated.
	second := h.replay.Body(1)
	if !strings.Contains(mustJSON(second), "The user wants a directory listing") {
		t.Error("reasoning was not replayed in the history")
	}
}

func TestToolCallEventsCarryTheIndex(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	id := waitForCard(t, h, s.ID)
	h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce})
	waitIdle(t, s)

	for _, e := range h.rec.ofType("tool_call") {
		if _, ok := e.Fields["index"]; !ok {
			t.Error("tool_call event has no index")
		}
	}
	for _, e := range h.rec.ofType("tool_output") {
		if _, ok := e.Fields["index"]; !ok {
			t.Error("tool_output event has no index")
		}
	}
}

func TestTwoToolCallsAreSequential(t *testing.T) {
	h := newHarness(t, "stream_two_tools.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist })
	h.cfg.AddAllow(h.project, "echo")
	s := h.session(t)
	h.engine.Send(s.ID, "echo twice", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	var outputs []string
	for _, e := range doc.Entries {
		if e.Kind == "tool" {
			outputs = append(outputs, e.Content)
		}
	}
	if len(outputs) != 2 {
		t.Fatalf("tool entries = %d", len(outputs))
	}
	if !strings.Contains(outputs[0], "one") || !strings.Contains(outputs[1], "two") {
		t.Errorf("outputs out of order: %v", outputs)
	}
}

func TestDenialReachesTheModel(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	id := waitForCard(t, h, s.ID)
	h.broker.Resolve(id, perms.Decision{Kind: perms.DecideDeny, Reason: "wrong directory"})
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	tool := doc.Entries[2]
	if tool.Content != "denied by user: wrong directory" {
		t.Errorf("tool content = %q", tool.Content)
	}
	if tool.Permission.Decision != "deny" {
		t.Errorf("permission record = %+v", tool.Permission)
	}
	if !strings.Contains(mustJSON(h.replay.Body(1)), "denied by user: wrong directory") {
		t.Error("the denial did not reach the model")
	}
}

func TestAlwaysWritesTheAllowlist(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	id := waitForCard(t, h, s.ID)
	h.broker.Resolve(id, perms.Decision{Kind: perms.DecideAlways, Prefix: "ls"})
	waitIdle(t, s)

	if _, ok := h.cfg.AllowMatch(h.project, "ls -la"); !ok {
		t.Error("the rule was not stored")
	}
	raw, err := os.ReadFile(h.cfg.Paths().Allowlist)
	if err != nil || !strings.Contains(string(raw), `"ls"`) {
		t.Errorf("allowlist.json = %q %v", raw, err)
	}
}

func TestFullModeRunsEverythingWithoutPrompting(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	waitIdle(t, s)

	if len(h.broker.Pending(s.ID)) != 0 {
		t.Error("full mode still asked for a decision")
	}
	doc, _, _, _ := s.Snapshot()
	tool := doc.Entries[2]
	if tool.Kind != "tool" || tool.ExitCode == nil || *tool.ExitCode != 0 {
		t.Fatalf("tool entry = %+v", tool)
	}
	if tool.Permission == nil || tool.Permission.Decision != "full" {
		t.Errorf("permission record = %+v, want a decision of full, not one that reads as an allowlist hit", tool.Permission)
	}
}

func TestStopDuringACommand(t *testing.T) {
	h := newHarness(t, "stream_sleep.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist })
	h.cfg.AddAllow(h.project, "sleep")
	s := h.session(t)
	h.engine.Send(s.ID, "sleep", nil)

	waitForState(t, s, StateExecuting)
	if _, err := h.engine.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "status" || last.Content != "Stopped" {
		t.Errorf("last entry = %+v, want the stop marker", last)
	}
	if last.InContext {
		t.Error("the stop marker must stay out of context")
	}
}

func TestStopWhileWaitingForADecision(t *testing.T) {
	h := newHarness(t, "stream_tool.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	card := waitForCard(t, h, s.ID)
	h.engine.Stop(s.ID)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	found := false
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.Content == "stopped by user" {
			found = true
			if e.Permission == nil || e.Permission.ID != card {
				t.Errorf("the stopped record lost the id the card was asked with: %+v", e.Permission)
			}
		}
	}
	if !found {
		t.Errorf("no stopped tool result, entries = %+v", doc.Entries)
	}

	// Without this the card stays open in the browser and the composer waits
	// on a decision nothing will ever answer.
	closed := h.rec.ofType("permission_done")
	if len(closed) != 1 {
		t.Fatalf("got %d permission_done events, want 1", len(closed))
	}
	if closed[0].Fields["id"] != card || closed[0].Fields["decision"] != "stopped" {
		t.Errorf("permission_done = %+v, want id %q stopped", closed[0].Fields, card)
	}
}

func TestStopOnIdleIsANoOp(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	state, err := h.engine.Stop(s.ID)
	if err != nil || state != StateIdle {
		t.Errorf("Stop on idle = %v %v", state, err)
	}
}

// A message sent while a card is open waits for the turn rather than being
// refused, and the stop that answers the card delivers it.
func TestSendWhileBusyIsQueued(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	waitForCard(t, h, s.ID)

	sent, err := h.engine.Send(s.ID, "again", nil)
	if err != nil || !sent.Queued {
		t.Fatalf("Send while busy = %+v %v, want it queued", sent, err)
	}
	h.engine.Stop(s.ID)
	waitForEntry(t, s, "again")
	waitIdle(t, s)
}

func TestUpstreamErrorEndsTheTurnInFailed(t *testing.T) {
	h := newHarness(t, "stream_error.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "go", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "error" || !strings.Contains(last.Content, "context shift is disabled") {
		t.Errorf("last entry = %+v", last)
	}
	if len(h.rec.ofType("error")) == 0 {
		t.Error("no error event was published")
	}
	states := h.rec.states()
	if states[len(states)-2] != "failed" {
		t.Errorf("states = %v, want failed before idle", states)
	}
}

func TestMalformedFramesReportWithoutFailing(t *testing.T) {
	h := newHarness(t, "stream_malformed.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "go", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if !strings.Contains(doc.Entries[1].Content, "first and last") {
		t.Errorf("assistant content = %q", doc.Entries[1].Content)
	}
	errs := h.rec.ofType("error")
	if len(errs) != 1 || errs[0].Fields["kind"] != "malformed" {
		t.Errorf("error events = %+v", errs)
	}
}

func TestGetResolvesLiveThenDiskThenNotFound(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	again, err := h.engine.Get(s.ID)
	if err != nil || again != s {
		t.Error("Get did not return the live session")
	}

	h.engine.mu.Lock()
	delete(h.engine.live, s.ID)
	h.engine.mu.Unlock()
	loaded, err := h.engine.Get(s.ID)
	if err != nil {
		t.Fatalf("Get from disk: %v", err)
	}
	if loaded.ID != s.ID || loaded == s {
		t.Error("Get did not rebuild the session from the file")
	}
	if _, err := h.engine.Get("sess_aaaaaaaaaaaaaaaa"); err != ErrNotFound {
		t.Errorf("Get unknown = %v", err)
	}
}

func TestCreateRejectsUnknownProject(t *testing.T) {
	h := newHarness(t)
	if _, err := h.engine.Create("/not/a/project"); err != ErrNoProject {
		t.Errorf("Create = %v, want ErrNoProject", err)
	}
	if _, err := h.cfg.Update(func(c *config.Config) { c.Model = "" }); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Create(h.project); err != ErrNoModel {
		t.Errorf("Create with no model chosen = %v", err)
	}
}

func TestComputeFillBeforeAndAfterUsage(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: strings.Repeat("x", 400), InContext: true, Visible: true})
	estimated := s.computeFill()
	s.mu.Unlock()
	if estimated != 100 {
		t.Errorf("estimate = %d, want 400 characters over 4", estimated)
	}

	s.mu.Lock()
	s.LastUsage = &llm.Usage{PromptTokens: 1000, CompletionTokens: 24}
	s.lastUsageEntry = len(s.Entries) - 1
	s.appendEntry(store.Entry{Kind: "tool", Role: "tool", Content: strings.Repeat("y", 800), InContext: true, Visible: true})
	withUsage := s.computeFill()
	s.mu.Unlock()
	if withUsage != 1024+200 {
		t.Errorf("fill = %d, want the report plus the entries added since", withUsage)
	}
}

func TestAssumedContextArmsCompaction(t *testing.T) {
	h := newHarness(t, "stream_summary.txt", "stream_plain.txt")
	s := h.session(t)
	s.mu.Lock()
	s.NCtx, s.NCtxSource = 0, ""
	nctx, source := s.effectiveNCtx()
	s.mu.Unlock()
	if nctx != assumedNCtx || source != "assumed" {
		t.Fatalf("effectiveNCtx = %d/%s", nctx, source)
	}

	h.cfg.Update(func(c *config.Config) { c.CompactAt = 0.05 })
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: strings.Repeat("x", 40000), InContext: true, Visible: true})
	s.mu.Unlock()
	h.engine.Send(s.ID, "carry on", nil)
	waitIdle(t, s)

	if len(h.rec.ofType("compacted")) != 1 {
		t.Errorf("compaction did not fire against the assumed size")
	}
}

func TestCompactionRewritesInContextOnly(t *testing.T) {
	h := newHarness(t, "stream_summary.txt")
	s := h.session(t)
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "earlier work", InContext: true, Visible: true})
	s.mu.Unlock()

	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	for i, e := range doc.Entries[:len(doc.Entries)-1] {
		if e.InContext {
			t.Errorf("entry %d stayed in context", i)
		}
		if !e.Visible {
			t.Errorf("entry %d lost its visibility", i)
		}
	}
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "compaction" || !last.InContext || last.Content != "Read file.txt, was denied a command, wrote out.txt. Next: report." {
		t.Errorf("compaction entry = %+v", last)
	}
	// The entry keeps the summary alone; the framing is added to the message
	// built from it, so the transcript can show the summary as it was written.
	s.mu.Lock()
	msgs := s.contextMessages("darwin", nil)
	s.mu.Unlock()
	sent := msgs[len(msgs)-1]
	if sent.Role != "user" || !strings.HasPrefix(sent.Content, "Summary of earlier work:") ||
		!strings.Contains(sent.Content, "wrote out.txt") {
		t.Errorf("compaction message = %+v", sent)
	}
	if last.Meta["before"] == nil || last.Meta["after"] == nil || last.Meta["n"] != 1 {
		t.Errorf("meta = %+v", last.Meta)
	}
	if doc.Compactions != 1 {
		t.Errorf("compactions = %d", doc.Compactions)
	}

	body := h.replay.Body(0)
	if _, ok := body["tools"]; ok {
		t.Error("the compaction request carried tools")
	}
	if body["reasoning_budget"] != 0.0 {
		t.Errorf("the compaction request did not turn thinking off: %v", body["reasoning_budget"])
	}
	if len(h.rec.ofType("compacted")) != 1 {
		t.Error("no compacted event")
	}
}

func TestCompactRefusedWhileBusy(t *testing.T) {
	h := newHarness(t, "stream_tool.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	waitForCard(t, h, s.ID)
	if err := h.engine.Compact(s.ID); err != ErrBusy {
		t.Errorf("Compact while busy = %v", err)
	}
	h.engine.Stop(s.ID)
	waitIdle(t, s)
}

// Choosing a model points every session at it and forgets what the previous
// model's tokenizer implied.
func TestSetModelRepointsAndResets(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	s.mu.Lock()
	s.NCtx, s.NCtxSource = 131072, "props"
	s.LastUsage = &llm.Usage{PromptTokens: 100}
	s.mu.Unlock()

	if _, err := h.cfg.Update(func(c *config.Config) { c.Model = "Delta3.8-27B Q5" }); err != nil {
		t.Fatal(err)
	}
	h.engine.SetModel("Delta3.8-27B Q5")
	waitIdle(t, s)

	doc, _, _, caps := s.Snapshot()
	if doc.Model != "Delta3.8-27B Q5" {
		t.Errorf("session model = %q", doc.Model)
	}
	if doc.LastUsage != nil {
		t.Error("last usage was carried across models")
	}
	// The new model was read, so its window and its template stand.
	if doc.NCtxSource != "props" || !caps.Known {
		t.Errorf("the new model was not read: n_ctx from %q caps %+v", doc.NCtxSource, caps)
	}
	for _, e := range doc.Entries {
		if e.Kind == "status" && strings.Contains(e.Content, "odel") {
			t.Errorf("the transcript reported the change: %+v", e)
		}
	}
}

// A session restored from a file has never had its model read, whatever the
// file says, so opening it reads it the way creating one does. It takes no
// message until that has finished.
func TestOpeningARestoredSessionReadsTheModel(t *testing.T) {
	h := newHarness(t)
	s := h.sessionWith(t, "Delta3.8-27B Q5")
	id := s.ID

	// Forgotten the way a restart forgets it, with the file left behind.
	h.engine.mu.Lock()
	delete(h.engine.live, id)
	h.engine.mu.Unlock()
	h.replay.mu.Lock()
	h.replay.Loads = nil
	h.replay.mu.Unlock()

	opened, err := h.engine.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Send(id, "hello", nil); err != ErrLoading {
		t.Errorf("send while the model is read = %v, want ErrLoading", err)
	}
	waitIdle(t, opened)

	h.replay.mu.Lock()
	loads := append([]string(nil), h.replay.Loads...)
	h.replay.mu.Unlock()
	if len(loads) != 1 || loads[0] != "Delta3.8-27B Q5" {
		t.Errorf("loads = %v, want the model read once on open", loads)
	}
	// And again on the next open, since a model server restarted between two
	// of them holds nothing.
	if _, err := h.engine.Open(id); err != nil {
		t.Fatal(err)
	}
	if got := opened.State(); got != StateLoading {
		t.Errorf("state = %s, want the model read again on a second open", got)
	}
	waitIdle(t, opened)
}

// Picking a model is what asks a router to hold it, so the read happens even
// when no session is open to wait on it.
func TestPickingAModelLoadsItWithNothingOpen(t *testing.T) {
	h := newHarness(t)
	if _, err := h.cfg.Update(func(c *config.Config) { c.Model = "Delta3.8-27B Q5" }); err != nil {
		t.Fatal(err)
	}
	h.engine.SetModel("Delta3.8-27B Q5")
	h.engine.Wait(5 * time.Second)

	h.replay.mu.Lock()
	loads := append([]string(nil), h.replay.Loads...)
	h.replay.mu.Unlock()
	if len(loads) != 1 || loads[0] != "Delta3.8-27B Q5" {
		t.Errorf("loads = %v, want the model that was picked", loads)
	}
}

// A session that was not open when the model changed takes it at its next
// message rather than answering with the model it last ran against.
func TestSessionTakesTheModelAtItsNextTurn(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	// Changed behind the session's back, as a session on disk would see it.
	if _, err := h.cfg.Update(func(c *config.Config) { c.Model = "Delta3.8-27B Q5" }); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "hello", nil)
	waitIdle(t, s)

	if got := h.replay.Body(0)["model"]; got != "Delta3.8-27B Q5" {
		t.Errorf("the turn asked %v, want the harness's model", got)
	}
	doc, _, _, _ := s.Snapshot()
	if doc.Model != "Delta3.8-27B Q5" {
		t.Errorf("session model = %q", doc.Model)
	}
}

func TestOnlyALoadCarriesAutoloadTrue(t *testing.T) {
	h := newHarness(t)
	// Alpha-12B is loaded in the model list, so reading it must not offer to
	// start it. Delta is not, so preparing it is a load.
	h.sessionWith(t, "Alpha-12B")

	h.replay.mu.Lock()
	queries := append([]string(nil), h.replay.Queries...)
	loads := append([]string(nil), h.replay.Loads...)
	h.replay.mu.Unlock()
	if len(queries) == 0 {
		t.Fatal("the model was never read")
	}
	for _, q := range queries {
		if !strings.Contains(q, "autoload=false") {
			t.Errorf("a loaded model was read with %q", q)
		}
	}
	if len(loads) != 0 {
		t.Errorf("loads = %v, want none for a model already loaded", loads)
	}

	h.sessionWith(t, "Delta3.8-27B Q5")
	h.replay.mu.Lock()
	loads = append([]string(nil), h.replay.Loads...)
	h.replay.mu.Unlock()
	if len(loads) != 1 || loads[0] != "Delta3.8-27B Q5" {
		t.Errorf("loads = %v, want the unloaded model loaded once", loads)
	}
}

// A message typed while the model loads has no turn to join, so it is refused
// rather than queued against nothing.
func TestMessageIsRefusedWhileTheModelLoads(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	h.replay.Props = 300 * time.Millisecond
	if _, err := h.cfg.Update(func(c *config.Config) { c.Model = "Delta3.8-27B Q5" }); err != nil {
		t.Fatal(err)
	}
	s, err := h.engine.Create(h.project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Send(s.ID, "too early", nil); err != ErrLoading {
		t.Errorf("Send while loading = %v, want ErrLoading", err)
	}
	h.replay.mu.Lock()
	h.replay.Props = 0
	h.replay.mu.Unlock()
	waitIdle(t, s)
	if _, err := h.engine.Send(s.ID, "now", nil); err != nil {
		t.Fatalf("Send once ready = %v", err)
	}
	waitIdle(t, s)
}

func TestUnansweredToolCallIsRepaired(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "go", InContext: true, Visible: true})
	s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant", InContext: true, Visible: true,
		ToolCalls: []llm.ToolCall{{ID: "call_x", Type: "function",
			Function: llm.FunctionCall{Name: "run_shell", Arguments: `{"command":"ls"}`}}}})
	msgs := s.contextMessages("darwin", nil)
	s.mu.Unlock()

	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.ToolCallID != "call_x" {
		t.Fatalf("last message = %+v, an unanswered call must be paired", last)
	}
	if last.Content != "not run: the session was interrupted" {
		t.Errorf("content = %q", last.Content)
	}
}

func TestDeleteRequiresIdle(t *testing.T) {
	h := newHarness(t, "stream_tool.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	waitForCard(t, h, s.ID)
	if err := h.engine.Delete(s.ID); err != ErrBusy {
		t.Errorf("Delete while busy = %v", err)
	}
	h.engine.Stop(s.ID)
	waitIdle(t, s)
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Get(s.ID); err != ErrNotFound {
		t.Errorf("the session survived the delete: %v", err)
	}
}

// TestToolCallLimit spends the budget over two rounds of two calls and asserts
// that the turn ends in an answer rather than in a refusal.
func TestToolCallLimit(t *testing.T) {
	h := newHarness(t, "stream_two_tools.txt", "stream_two_tools.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) {
		c.Mode = config.ModeAllowlist
		c.MaxToolCalls = 4
	})
	h.cfg.AddAllow(h.project, "echo")
	s := h.session(t)
	h.engine.Send(s.ID, "loop forever", nil)
	waitIdle(t, s)

	if h.replay.Count() != 3 {
		t.Errorf("model calls = %d, want 3 turns", h.replay.Count())
	}
	doc, _, _, _ := s.Snapshot()
	noted := false
	for _, e := range doc.Entries {
		if e.Kind == "status" && strings.Contains(e.Content, "Tool call limit reached after 4 calls") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("no entry says the limit was reached: %+v", doc.Entries)
	}
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "assistant" || len(last.ToolCalls) != 0 {
		t.Errorf("last entry = %+v, want an assistant answer with no calls", last)
	}
}

// TestWriteTodosKeepsThePlan asserts that the fourth tool asks nothing, keeps
// the plan on the session, and hands it back to the model.
func TestWriteTodosKeepsThePlan(t *testing.T) {
	h := newHarness(t, "stream_todos.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "plan the work", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if len(doc.Todos) != 3 {
		t.Fatalf("todos = %+v", doc.Todos)
	}
	if doc.Todos[0].Status != "done" || doc.Todos[1].Status != "doing" || doc.Todos[2].Status != "todo" {
		t.Errorf("statuses = %+v", doc.Todos)
	}
	if doc.Todos[1].Text != "Add the handler" {
		t.Errorf("text = %q", doc.Todos[1].Text)
	}

	var result store.Entry
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.ToolName == "write_todos" {
			result = e
		}
	}
	if !strings.Contains(result.Content, "1 of 3 done") || !strings.Contains(result.Content, "[>] Add the handler") {
		t.Errorf("tool result = %q", result.Content)
	}
	if result.Permission == nil || result.Permission.Decision != "auto" {
		t.Errorf("the plan was put to the user: %+v", result.Permission)
	}
	if events := h.rec.ofType("todos"); len(events) != 1 {
		t.Errorf("todos events = %d, want 1", len(events))
	}
}

// TestAttachmentReachesThePrompt asserts that an attached file is read once,
// kept with the message, and inlined under it when the prompt is built.
func TestAttachmentReachesThePrompt(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("the plan is to ship"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := h.engine.Send(s.ID, "read this", []string{path}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	first := doc.Entries[0]
	if len(first.Attachments) != 1 || first.Attachments[0].Path != path {
		t.Fatalf("entry attachments = %+v", first.Attachments)
	}
	if first.Content != "read this" {
		t.Errorf("the file was folded into the message text: %q", first.Content)
	}

	msgs, _ := h.replay.Body(0)["messages"].([]any)
	user, _ := msgs[len(msgs)-1].(map[string]any)
	content, _ := user["content"].(string)
	if !strings.Contains(content, "the plan is to ship") || !strings.Contains(content, path) {
		t.Errorf("the prompt did not carry the file: %q", content)
	}

	// A file that cannot be read refuses the message rather than sending half.
	if _, err := h.engine.Send(s.ID, "and this", []string{filepath.Join(t.TempDir(), "gone.md")}); err == nil {
		t.Error("a missing attachment was accepted")
	}
	doc, _, _, _ = s.Snapshot()
	for _, e := range doc.Entries {
		if e.Kind == "user" && e.Content == "and this" {
			t.Error("the refused message was written to the session")
		}
	}
}

// TestImageReachesThePromptAsParts asserts that an image goes to the file
// store rather than into the session document, and travels in the message as
// the array of parts the endpoint takes.
func TestImageReachesThePromptAsParts(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, onePixelPNG(), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := h.engine.Send(s.ID, "what is this", []string{png}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	att := doc.Entries[0].Attachments
	if len(att) != 1 || att[0].Kind != "image" || att[0].File == "" {
		t.Fatalf("attachments = %+v", att)
	}
	if att[0].Content != "" {
		t.Error("the image bytes were written into the session document")
	}
	if raw, err := h.engine.files.Read(att[0].File); err != nil || len(raw) != att[0].Bytes {
		t.Errorf("the file store holds %d bytes, err %v", len(raw), err)
	}

	msgs, _ := h.replay.Body(0)["messages"].([]any)
	user, _ := msgs[len(msgs)-1].(map[string]any)
	parts, ok := user["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("user content = %v", user["content"])
	}
	text, _ := parts[0].(map[string]any)
	if text["type"] != "text" || !strings.Contains(text["text"].(string), png) {
		t.Errorf("first part = %v", text)
	}
	image, _ := parts[1].(map[string]any)
	url, _ := image["image_url"].(map[string]any)
	if image["type"] != "image_url" || !strings.HasPrefix(url["url"].(string), "data:image/png;base64,") {
		t.Errorf("second part = %v", image)
	}

	// Deleting the session takes the file with it.
	id := att[0].File
	h.engine.Stop(s.ID)
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.files.Read(id); err == nil {
		t.Error("the file outlived the session that held it")
	}
}

// TestImageNeedsAModelThatTakesOne asserts the message is refused before it
// is written, rather than failing inside the turn.
func TestImageNeedsAModelThatTakesOne(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.sessionWith(t, "No-Such-Model")
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, onePixelPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Send(s.ID, "look", []string{png}); err == nil {
		t.Fatal("an image went to a model the server does not have")
	}
	doc, _, _, _ := s.Snapshot()
	if len(doc.Entries) != 0 {
		t.Errorf("the refused message was written: %+v", doc.Entries)
	}
}

// onePixelPNG is the smallest header that sniffs as a png.
func onePixelPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89,
	}
}

// The model names the session from its opening message, before the turn that
// answers it, and a title the user typed is left alone.
func TestTitleBeforeFirstReply(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is in this project", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if doc.Title != "Read the project layout" {
		t.Errorf("title = %q, want the one the model wrote", doc.Title)
	}
	if doc.Named {
		t.Error("a title the harness asked for was recorded as one the user typed")
	}

	h.replay.mu.Lock()
	titles := append([]map[string]any(nil), h.replay.Titles...)
	h.replay.mu.Unlock()
	if len(titles) != 1 {
		t.Fatalf("naming requests = %d, want one", len(titles))
	}
	msgs, _ := titles[0]["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("the naming request carried %d messages, want the opening one alone", len(msgs))
	}
	only, _ := msgs[0].(map[string]any)
	content, _ := only["content"].(string)
	if only["role"] != "user" || !strings.Contains(content, "Six words at most") {
		t.Errorf("message = %v", only)
	}
	if !strings.Contains(content, "what is in this project") {
		t.Error("the naming request did not carry the message it is naming")
	}
	if titles[0]["reasoning_budget"] != 0.0 {
		t.Errorf("the naming request did not turn thinking off: %v", titles[0]["reasoning_budget"])
	}

	// A typed title outlives a second turn, which asks for no title at all.
	if err := h.engine.Rename(s.ID, "Mine"); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "and now", nil)
	waitIdle(t, s)
	doc, _, _, _ = s.Snapshot()
	if doc.Title != "Mine" || !doc.Named {
		t.Errorf("title = %q named = %v", doc.Title, doc.Named)
	}
	h.replay.mu.Lock()
	again := len(h.replay.Titles)
	h.replay.mu.Unlock()
	if again != 1 {
		t.Errorf("naming requests = %d, want the session named once", again)
	}

	if h.replay.Count() != 2 {
		t.Errorf("model calls = %d, want the two turns", h.replay.Count())
	}
}

// TestEndToEndTurn drives a read, a denial, a write and a forced compaction
// through one engine and asserts what landed on disk.
func TestEndToEndTurn(t *testing.T) {
	h := newHarness(t, "stream_read.txt", "stream_tool.txt", "stream_write.txt", "stream_summary.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "read, run, write", nil)

	// read_file is allowed by policy, run_shell is denied, write_file is
	// approved once, then the threshold forces a compaction.
	id := waitForCard(t, h, s.ID)
	h.broker.Resolve(id, perms.Decision{Kind: perms.DecideDeny, Reason: "not that one"})
	id = waitForCard(t, h, s.ID)
	h.cfg.Update(func(c *config.Config) { c.CompactAt = 0.05 })
	s.mu.Lock()
	s.NCtx = 200
	s.mu.Unlock()
	h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce})
	waitIdle(t, s)

	written, err := os.ReadFile(filepath.Join(h.project, "out.txt"))
	if err != nil || string(written) != "written by the model\n" {
		t.Errorf("out.txt = %q %v", written, err)
	}

	doc, err := h.store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Compactions != 1 {
		t.Fatalf("compactions = %d", doc.Compactions)
	}
	sawDivider := false
	for _, e := range doc.Entries {
		if e.Kind == "compaction" {
			sawDivider = true
			continue
		}
		if !sawDivider {
			if e.InContext {
				t.Errorf("entry %d before the divider is still in context", e.Seq)
			}
			if !e.Visible {
				t.Errorf("entry %d before the divider lost its visibility", e.Seq)
			}
		}
	}
	if !sawDivider {
		t.Fatal("no compaction divider")
	}
	if !strings.Contains(mustJSON(h.replay.Body(2)), "denied by user: not that one") {
		t.Error("the denial never reached the model")
	}
	before := mustJSON(h.replay.Body(2)["messages"])
	after := mustJSON(h.replay.LastBody()["messages"])
	if len(after) >= len(before) {
		t.Errorf("the history after the compaction is %d bytes, before it was %d", len(after), len(before))
	}
}

func waitForCard(t *testing.T, h *harness, sessionID string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p := h.broker.Pending(sessionID); len(p) > 0 {
			return p[0].ID
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no permission card appeared")
	return ""
}

func waitForState(t *testing.T, s *Session, want State) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("session is %s, want %s", s.State(), want)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func waitForProbes(t *testing.T, h *harness, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		h.replay.mu.Lock()
		got := len(h.replay.Probes)
		h.replay.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the context measurement did not run")
}

func TestContextBreakdownFromMeasuredParts(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_title.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is in this project", nil)
	waitIdle(t, s)
	waitForProbes(t, h, 2)

	// The fixtures report 20 tokens for the system message alone and 35 with
	// the schemas attached, so the schemas cost 15.
	var parts *Breakdown
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if parts = s.Breakdown(); parts != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if parts == nil {
		t.Fatal("no breakdown after the parts were measured")
	}
	if parts.System != 20 {
		t.Errorf("system = %d, want 20", parts.System)
	}
	if parts.Tools != 15 {
		t.Errorf("tools = %d, want 15", parts.Tools)
	}
	// The turn reported 44 prompt and 48 completion tokens.
	if want := 44 + 48 - 20 - 15; parts.Messages != want {
		t.Errorf("messages = %d, want %d", parts.Messages, want)
	}

	h.replay.mu.Lock()
	first, second := h.replay.Probes[0], h.replay.Probes[1]
	h.replay.mu.Unlock()
	if _, ok := first["tools"]; ok {
		t.Error("the first measurement carried tools, so it cannot isolate the system message")
	}
	if _, ok := second["tools"]; !ok {
		t.Error("the second measurement carried no tools, so the difference is not the schema cost")
	}
	msgs, _ := first["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("the measurement carried %d messages, want the system message alone", len(msgs))
	}
	if m, _ := msgs[0].(map[string]any); m["role"] != "system" {
		t.Errorf("measured message role = %v, want system", m["role"])
	}
}

func TestContextMeasurementRunsOncePerSession(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "first", nil)
	waitIdle(t, s)
	waitForProbes(t, h, 2)

	h.engine.Send(s.ID, "second", nil)
	waitIdle(t, s)

	h.replay.mu.Lock()
	got := len(h.replay.Probes)
	h.replay.mu.Unlock()
	if got != 2 {
		t.Errorf("the parts were measured %d times, want twice for one model", got)
	}
}
