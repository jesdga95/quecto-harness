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
	"quecto-harness/internal/llama"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/store"
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
	rp := newReplay(t, fixtures...)
	srv := rp.Server()
	t.Cleanup(srv.Close)

	rec := &recorder{}
	broker := perms.New(cfg, func(sid string, r perms.Request) { rec.Publish(sid, PermissionEvent(r)) })
	ctx, cancel := context.WithCancel(context.Background())
	engine := NewEngine(ctx, cfg, st, llama.New(srv.URL, ""), broker, rec)
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
	s, err := h.engine.Create(h.project, model)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// waitForCaps blocks until the props read that follows a turn has landed.
func waitForCaps(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, _, caps := s.Snapshot(); caps.Known {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the capabilities never arrived")
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
	if err := h.engine.Send(s.ID, "what is here"); err != nil {
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
	if doc.Title != "what is here" {
		t.Errorf("title = %q", doc.Title)
	}
	if fill == 0 {
		t.Error("fill was not computed")
	}
	states := h.rec.states()
	want := []string{"preflight", "preflight", "streaming", "final", "idle"}
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

func TestEffortReachesTheRequestBody(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	if _, err := h.cfg.Update(func(c *config.Config) { c.Effort = config.EffortHigh }); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "hello")
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
	tools, _ := body["tools"].([]any)
	if len(tools) != 3 {
		t.Errorf("tools = %d, want 3", len(tools))
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

// The kwarg is not known to apply until the model is loaded and read, so the
// first request to a cold model carries none.
func TestPreserveReasoningFollowsTheCapability(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	if _, err := h.cfg.Update(func(c *config.Config) { c.PreserveReasoning = false }); err != nil {
		t.Fatal(err)
	}
	s := h.sessionWith(t, "Delta3.8-27B Q5")
	h.engine.Send(s.ID, "first")
	waitIdle(t, s)

	if _, ok := h.replay.Body(0)["chat_template_kwargs"]; ok {
		t.Error("a kwarg was sent before the template had been read")
	}

	waitForCaps(t, s)
	if err := h.engine.Send(s.ID, "second"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	second := h.replay.Body(1)
	kw, _ := second["chat_template_kwargs"].(map[string]any)
	if kw == nil || kw["preserve_reasoning"] != false {
		t.Errorf("chat_template_kwargs = %v, want preserve_reasoning false", second["chat_template_kwargs"])
	}
	if second["reasoning_effort"] != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", second["reasoning_effort"])
	}
}

func TestSingleToolCallWithApproval(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list the files here")

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
	h.engine.Send(s.ID, "list")
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
	h.engine.Send(s.ID, "echo twice")
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
	h.engine.Send(s.ID, "list")
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
	h.engine.Send(s.ID, "list")
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
	h.engine.Send(s.ID, "list")
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
	h.engine.Send(s.ID, "sleep")

	waitForState(t, s, StateExecuting)
	if _, err := h.engine.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "status" || last.Content != "stopped" {
		t.Errorf("last entry = %+v, want the stop marker", last)
	}
	if last.InContext {
		t.Error("the stop marker must stay out of context")
	}
}

func TestStopWhileWaitingForADecision(t *testing.T) {
	h := newHarness(t, "stream_tool.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list")
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

func TestSendWhileBusyIsRefused(t *testing.T) {
	h := newHarness(t, "stream_tool.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list")
	waitForCard(t, h, s.ID)
	if err := h.engine.Send(s.ID, "again"); err != ErrBusy {
		t.Errorf("Send while busy = %v, want ErrBusy", err)
	}
	h.engine.Stop(s.ID)
	waitIdle(t, s)
}

func TestUpstreamErrorEndsTheTurnInFailed(t *testing.T) {
	h := newHarness(t, "stream_error.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "go")
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
	h.engine.Send(s.ID, "go")
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
	if _, err := h.engine.Create("/not/a/project", "Alpha-12B"); err != ErrNoProject {
		t.Errorf("Create = %v, want ErrNoProject", err)
	}
	if _, err := h.engine.Create(h.project, ""); err != ErrNoModel {
		t.Errorf("Create without a model = %v", err)
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
	s.LastUsage = &llama.Usage{PromptTokens: 1000, CompletionTokens: 24}
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
	h.engine.Send(s.ID, "carry on")
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
	if last.Kind != "compaction" || !last.InContext || !strings.HasPrefix(last.Content, "Summary of earlier work:") {
		t.Errorf("compaction entry = %+v", last)
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
	h.engine.Send(s.ID, "list")
	waitForCard(t, h, s.ID)
	if err := h.engine.Compact(s.ID); err != ErrBusy {
		t.Errorf("Compact while busy = %v", err)
	}
	h.engine.Stop(s.ID)
	waitIdle(t, s)
}

func TestRebindResetsAccounting(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	s.mu.Lock()
	s.NCtx, s.NCtxSource = 131072, "props"
	s.caps = llama.Caps{Known: true, ReasoningEffort: true}
	s.LastUsage = &llama.Usage{PromptTokens: 100}
	s.mu.Unlock()

	if err := h.engine.Rebind(s.ID, "Delta3.8-27B Q5", 262144, "args"); err != nil {
		t.Fatal(err)
	}
	doc, _, _, caps := s.Snapshot()
	if doc.Model != "Delta3.8-27B Q5" || doc.NCtx != 262144 || doc.NCtxSource != "args" {
		t.Errorf("session = %+v", doc)
	}
	if caps.Known {
		t.Error("caps were not reset")
	}
	if doc.LastUsage != nil {
		t.Error("last usage was carried across models")
	}
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "status" || last.Content != "Model changed to Delta3.8-27B Q5" {
		t.Errorf("entry = %+v", last)
	}
	if last.InContext || !last.Visible {
		t.Errorf("the rebind entry must be visible and out of context: %+v", last)
	}
}

func TestRebindRefusedWhileBusy(t *testing.T) {
	h := newHarness(t, "stream_tool.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list")
	waitForCard(t, h, s.ID)
	if err := h.engine.Rebind(s.ID, "Delta3.8-27B Q5", 1024, "args"); err != ErrBusy {
		t.Errorf("Rebind while busy = %v", err)
	}
	h.engine.Stop(s.ID)
	waitIdle(t, s)
}

func TestPropsProbeCarriesAutoloadFalse(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "hello")
	waitIdle(t, s)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.replay.mu.Lock()
		n := len(h.replay.Queries)
		h.replay.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.replay.mu.Lock()
	queries := append([]string(nil), h.replay.Queries...)
	h.replay.mu.Unlock()
	if len(queries) == 0 {
		t.Fatal("the props probe never ran")
	}
	for _, q := range queries {
		if !strings.Contains(q, "autoload=false") {
			t.Errorf("model-scoped GET %q is missing autoload=false", q)
		}
	}
}

func TestUnansweredToolCallIsRepaired(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "go", InContext: true, Visible: true})
	s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant", InContext: true, Visible: true,
		ToolCalls: []llama.ToolCall{{ID: "call_x", Type: "function",
			Function: llama.FunctionCall{Name: "run_shell", Arguments: `{"command":"ls"}`}}}})
	msgs := s.contextMessages("darwin")
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
	h.engine.Send(s.ID, "list")
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
	h.engine.Send(s.ID, "loop forever")
	waitIdle(t, s)

	if h.replay.Count() != 3 {
		t.Errorf("model calls = %d, want 3", h.replay.Count())
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

// TestEndToEndTurn drives a read, a denial, a write and a forced compaction
// through one engine and asserts what landed on disk.
func TestEndToEndTurn(t *testing.T) {
	h := newHarness(t, "stream_read.txt", "stream_tool.txt", "stream_write.txt", "stream_summary.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "read, run, write")

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
