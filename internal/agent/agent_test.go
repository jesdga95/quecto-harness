package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/search"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
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
	url     string
	agent   config.Agent
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
	engine := NewEngine(ctx, cfg, st, blobs, Clients{Pool: llm.NewPool()}, search.NewProviders(), broker, rec)
	// A retry waits a moment in a test, so a failure fixture is not a
	// fifteen second test.
	engine.retry = []time.Duration{time.Millisecond, time.Millisecond}
	// The temporary home goes away when the test ends, so nothing the engine
	// started may still be writing sessions into it.
	t.Cleanup(func() {
		cancel()
		engine.Wait(5 * time.Second)
	})

	h := &harness{engine: engine, cfg: cfg, broker: broker, rec: rec,
		replay: rp, store: st, project: real, cancel: cancel, url: srv.URL}
	h.agent = h.addAgent(t, "Local", srv.URL, "Alpha-12B")
	if _, err := cfg.SelectAgent(h.agent.ID); err != nil {
		t.Fatal(err)
	}
	return h
}

// addAgent stores an agent without selecting it, so a test can have two
// addresses to move between.
func (h *harness) addAgent(t *testing.T, name, url, model string) config.Agent {
	t.Helper()
	_, a, err := h.cfg.AddAgent(config.Agent{
		Name: name, Enabled: true, URL: url, Model: model,
		Effort: config.EffortMedium, PreserveReasoning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// setAgent applies an edit to the harness's own agent and moves live sessions
// onto the result, the way the HTTP route does.
func (h *harness) setAgent(t *testing.T, fn func(*config.Agent)) config.Agent {
	t.Helper()
	_, a, err := h.cfg.UpdateAgent(h.agent.ID, fn)
	if err != nil {
		t.Fatal(err)
	}
	h.agent = a
	h.engine.SetAgent()
	return a
}

func (h *harness) session(t *testing.T) *Session {
	return h.sessionWith(t, "Alpha-12B")
}

func (h *harness) sessionWith(t *testing.T, model string) *Session {
	t.Helper()
	h.setAgent(t, func(a *config.Agent) { a.Model = model })
	s, err := h.engine.Create(h.project)
	if err != nil {
		t.Fatal(err)
	}
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

// waitState blocks until the session reaches one state, which is how a test
// catches a turn in the middle of a request it means to stop.
func waitState(t *testing.T, s *Session, want State) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if s.State() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("session stayed in %s, want %s", s.State(), want)
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
	// A session created by its first message has never read its model, so the
	// turn reads it, which is the loading in the middle of the preflight.
	want := []string{"preflight", "loading", "preflight", "streaming", "final", "idle"}
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
	// Input and cache are sums too: 410 and 3950 sent, 384 and 0 reused. A
	// cache that reused nothing is still a count, so the field is present.
	if last.Meta["input_tokens"] != 4360 {
		t.Errorf("input_tokens = %v, want 4360", last.Meta["input_tokens"])
	}
	if last.Meta["cached_tokens"] != 384 {
		t.Errorf("cached_tokens = %v, want 384", last.Meta["cached_tokens"])
	}
	for _, key := range []string{"llm_ms", "gen_ms", "first_ms", "tool_ms"} {
		if ms, ok := last.Meta[key].(int64); !ok || ms < 0 {
			t.Errorf("%s = %v", key, last.Meta[key])
		}
	}
	if ms, ok := last.Meta["ms"].(int64); !ok || ms < 0 {
		t.Errorf("ms = %v", last.Meta["ms"])
	}
	// Each request keeps its own prompt and output, in the order they were made.
	each, ok := last.Meta["per_request"].([]map[string]any)
	if !ok || len(each) != 2 {
		t.Fatalf("per_request = %#v, want two requests", last.Meta["per_request"])
	}
	for i, want := range []struct{ window, tokens int }{{410, 22}, {3950, 48}} {
		if each[i]["window_tokens"] != want.window || each[i]["tokens"] != want.tokens {
			t.Errorf("request %d = %v, want window %d and tokens %d", i+1, each[i], want.window, want.tokens)
		}
	}
	// The rate is measured from the first token of each response, so a replay
	// served from memory reports a large one rather than none at all.
	if rate, ok := last.Meta["tok_per_s"].(float64); !ok || rate <= 0 {
		t.Errorf("tok_per_s = %v", last.Meta["tok_per_s"])
	}
	// The server's own rate rides beside it. Only the second replay reported
	// timings, and it said 35 tokens at 52.94 per second, so the turn carries
	// that pair and nothing from the request that said nothing.
	if n, ok := last.Meta["server_tokens"].(int); !ok || n != 35 {
		t.Errorf("server_tokens = %v, want the 35 the server counted", last.Meta["server_tokens"])
	}
	srv, ok := last.Meta["server_tok_per_s"].(float64)
	if !ok || srv < 52.9 || srv > 52.99 {
		t.Errorf("server_tok_per_s = %v, want the 52.94 the server reported", last.Meta["server_tok_per_s"])
	}
	if ms, ok := last.Meta["server_gen_ms"].(int64); !ok || ms < 660 || ms > 662 {
		t.Errorf("server_gen_ms = %v, want the 661 ms 35 tokens at 52.94 is", last.Meta["server_gen_ms"])
	}
	if _, ok := each[0]["server_tok_per_s"]; ok {
		t.Errorf("request 1 carries server_tok_per_s = %v, want it absent: that replay reported no timings",
			each[0]["server_tok_per_s"])
	}
	if r, ok := each[1]["server_tok_per_s"].(float64); !ok || r < 52.9 || r > 52.99 {
		t.Errorf("request 2 server_tok_per_s = %v, want 52.94", each[1]["server_tok_per_s"])
	}

	done := h.rec.ofType("done")
	if len(done) != 1 {
		t.Fatalf("done events = %d", len(done))
	}
	if done[0].Fields["tokens"] != 70 {
		t.Errorf("done tokens = %v, want 70", done[0].Fields["tokens"])
	}
}

// A tool call the server left in the text ends the turn the way an answer
// does, so the transcript says the call was never run.
func TestAnUnparsedToolCallIsNoted(t *testing.T) {
	h := newHarness(t, "stream_leaked_tool_call.txt")
	s := h.session(t)
	if _, err := h.engine.Send(s.ID, "write the tests", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "status" || !strings.Contains(last.Content, "did not parse") {
		t.Fatalf("last entry = %+v, want a status naming the unparsed call", last)
	}
	if last.InContext {
		t.Error("the note was sent back to the model")
	}
	if notes := h.rec.ofType("status_entry"); len(notes) != 1 {
		t.Errorf("status_entry events = %d, want 1", len(notes))
	}
}

// An answer that carries no tool call syntax is left alone.
func TestAPlainAnswerIsNotNoted(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is here", nil)
	waitIdle(t, s)

	if notes := h.rec.ofType("status_entry"); len(notes) != 0 {
		t.Errorf("status_entry events = %d, want none", len(notes))
	}
}

// preserve_reasoning decides whether earlier thinking goes back, on every
// protocol rather than only the one with a template argument for it.
func TestPreserveReasoningDecidesWhatGoesBack(t *testing.T) {
	for _, keep := range []bool{true, false} {
		name := "off"
		if keep {
			name = "on"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
			s := h.session(t)
			h.setAgent(t, func(a *config.Agent) { a.PreserveReasoning = keep })
			h.engine.Send(s.ID, "list the files here", nil)

			id := waitForCard(t, h, s.ID)
			if err := h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce}); err != nil {
				t.Fatal(err)
			}
			waitIdle(t, s)

			// The second request is the first one with an assistant turn
			// behind it, and that turn did its thinking aloud.
			raw, err := json.Marshal(h.replay.Body(1)["messages"])
			if err != nil {
				t.Fatal(err)
			}
			if sent := strings.Contains(string(raw), "reasoning_content"); sent != keep {
				t.Errorf("reasoning_content sent = %v, want %v", sent, keep)
			}
		})
	}
}

// How the model stopped and what was lost reading it are gone from the stream
// by the time a session is read back, so the entry keeps both.
func TestOutcomeLandsOnTheEntry(t *testing.T) {
	h := newHarness(t, "stream_malformed.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "go", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if last.Meta["finish_reason"] != "stop" {
		t.Errorf("finish_reason = %v, want stop", last.Meta["finish_reason"])
	}
	if last.Meta["dropped"] != 2 {
		t.Errorf("dropped = %v, want 2", last.Meta["dropped"])
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
	h.setAgent(t, func(a *config.Agent) { a.Effort = config.EffortHigh })
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
	if want := len(tools.Schemas(topBrief().offer)); len(sent) != want {
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
	h.setAgent(t, func(a *config.Agent) { a.PreserveReasoning = false })
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

// The deadline is published before the command starts, since a long command
// and a stuck one are the same row until the limit is on screen.
func TestToolStartCarriesTheShellDeadline(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	id := waitForCard(t, h, s.ID)
	h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce})
	waitIdle(t, s)

	started := h.rec.ofType("tool_start")
	if len(started) != 1 {
		t.Fatalf("tool_start events = %d, want one", len(started))
	}
	if got := started[0].Fields["name"]; got != "bash" {
		t.Errorf("name = %v, want the call that started", got)
	}
	if got := started[0].Fields["timeout_s"]; got != 120 {
		t.Errorf("timeout_s = %v, want the configured default", got)
	}
}

// A denied call never runs, so nothing says it started.
func TestDeniedToolNeverStarts(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)
	id := waitForCard(t, h, s.ID)
	h.broker.Resolve(id, perms.Decision{Kind: perms.DecideDeny, Reason: "not this one"})
	waitIdle(t, s)

	if n := len(h.rec.ofType("tool_start")); n != 0 {
		t.Errorf("tool_start events = %d, want none", n)
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
	if !strings.HasPrefix(tool.Content, "denied by user: wrong directory\n") {
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

// An upstream failure is retried, each attempt noted, and the turn fails once
// the retries are spent.
func TestUpstreamErrorEndsTheTurnInFailed(t *testing.T) {
	h := newHarness(t, "stream_error.txt", "stream_error.txt", "stream_error.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "go", nil)
	waitIdle(t, s)

	errs := h.rec.ofType("error")
	if len(errs) == 0 || !strings.Contains(errs[0].Fields["message"].(string), "context shift is disabled") {
		t.Errorf("error events = %+v", errs)
	}
	if n := h.replay.Count(); n != 3 {
		t.Errorf("requests = %d, want the first and two retries", n)
	}
	retries := 0
	doc, _, _, _ := s.Snapshot()
	for _, e := range doc.Entries {
		if e.Kind == "status" && strings.Contains(e.Content, "Trying again") {
			retries++
		}
	}
	if retries != 2 {
		t.Errorf("retry notes = %d, want 2", retries)
	}
	// The message belongs to the request that failed, not to the session.
	for i, e := range doc.Entries {
		if e.Kind == "error" {
			t.Errorf("entry %d kept the failure: %+v", i, e)
		}
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
	h.setAgent(t, func(a *config.Agent) { a.Model = "" })
	if _, err := h.engine.Create(h.project); err != ErrNoModel {
		t.Errorf("Create with no model chosen = %v", err)
	}
	h.setAgent(t, func(a *config.Agent) { a.URL = "" })
	if _, err := h.engine.Create(h.project); err != ErrNoAddress {
		t.Errorf("Create with no address = %v", err)
	}
	if _, err := h.cfg.RemoveAgent(h.agent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Create(h.project); err != ErrNoAgent {
		t.Errorf("Create with no agent = %v", err)
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

func TestUnknownWindowNeverCompacts(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	// A model server that reported no window, with the read already done so
	// the turn does not take one from the fixture.
	s.mu.Lock()
	s.NCtx, s.NCtxSource, s.prepared = 0, "", true
	nctx, source := s.window(h.agent)
	s.mu.Unlock()
	if nctx != 0 || source != "" {
		t.Fatalf("window = %d/%s, want unknown", nctx, source)
	}

	h.cfg.Update(func(c *config.Config) { c.CompactAt = 0.05 })
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: strings.Repeat("x", 40000), InContext: true, Visible: true})
	s.mu.Unlock()
	h.engine.Send(s.ID, "carry on", nil)
	waitIdle(t, s)

	if n := len(h.rec.ofType("compacted")); n != 0 {
		t.Errorf("compactions = %d, want none against a window nothing named", n)
	}
}

func TestContextOverrideArmsCompaction(t *testing.T) {
	h := newHarness(t, "stream_summary.txt", "stream_plain.txt")
	s := h.session(t)
	// The agent names a window smaller than the one the model reports, so a
	// fill that fits the report does not fit the override.
	window := 8192
	h.setAgent(t, func(a *config.Agent) { a.MaxContext = &window })
	s.mu.Lock()
	nctx, source := s.window(h.agent)
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: strings.Repeat("x", 40000), InContext: true, Visible: true})
	s.mu.Unlock()
	if nctx != 8192 || source != "agent" {
		t.Fatalf("window = %d/%s, want the override", nctx, source)
	}

	h.engine.Send(s.ID, "carry on", nil)
	waitIdle(t, s)

	if n := len(h.rec.ofType("compacted")); n != 1 {
		t.Errorf("compactions = %d, want one against the override", n)
	}
}

// earlierWork is a history long enough to be worth compacting.
var earlierWork = strings.Repeat("Earlier work. ", 300)

// A compaction is refused while there is less to summarise than a summary
// would take: on a session with nothing in it, and on one holding only the
// last summary.
func TestCompactRefusesTooLittle(t *testing.T) {
	h := newHarness(t, "stream_summary.txt")
	s := h.session(t)
	if err := h.engine.Compact(s.ID); err != ErrTooShort {
		t.Errorf("Compact on an empty session = %v", err)
	}

	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: earlierWork, InContext: true, Visible: true})
	s.mu.Unlock()
	if !s.Compactable() {
		t.Fatal("a long history is not compactable")
	}
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if s.Compactable() {
		t.Error("the summary alone is compactable")
	}
	if err := h.engine.Compact(s.ID); err != ErrTooShort {
		t.Errorf("Compact over the summary alone = %v", err)
	}
	if n := h.replay.Count(); n != 1 {
		t.Errorf("requests = %d, want the one compaction", n)
	}
}

func TestCompactionRewritesInContextOnly(t *testing.T) {
	h := newHarness(t, "stream_summary.txt")
	s := h.session(t)
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: earlierWork, InContext: true, Visible: true})
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
	msgs := s.contextMessages(config.Agent{}, "darwin", "", topBrief(), nil)
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

	// The tools and the effort are the turn's, so the prompt shares its head
	// with the requests before it and the server's cache still holds.
	body := h.replay.Body(0)
	if _, ok := body["tools"]; !ok {
		t.Error("the compaction request dropped the tools")
	}
	if body["reasoning_budget"] == 0.0 {
		t.Error("the compaction request turned thinking off, which the turns before it left on")
	}
	if len(h.rec.ofType("compacted")) != 1 {
		t.Error("no compacted event")
	}
}

func compactionMessage(t *testing.T, s *Session) string {
	t.Helper()
	s.mu.Lock()
	msgs := s.contextMessages(config.Agent{}, "darwin", "", topBrief(), nil)
	s.mu.Unlock()
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, "Summary of earlier work:") {
			return m.Content
		}
	}
	t.Fatal("no compaction message in the context")
	return ""
}

const carriedTodos = "Your todo list when this summary was written:\n1 of 3 done, 1 in hand\n- [x] Read the routes\n- [>] Add the handler\n- [ ] Wire the panel\n"

func TestCompactionCarriesThePlanOfTheRequest(t *testing.T) {
	h := newHarness(t, "stream_todos.txt", "stream_plain.txt", "stream_summary.txt", "stream_todos_finished.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, earlierWork, nil)
	waitIdle(t, s)
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	sent := compactionMessage(t, s)
	if !strings.Contains(sent, carriedTodos) {
		t.Errorf("compaction message does not carry the plan:\n%s", sent)
	}
	doc, _, _, _ := s.Snapshot()
	var entry store.Entry
	for _, e := range doc.Entries {
		if e.Kind == "compaction" {
			entry = e
		}
	}
	todos, _ := entry.Meta["todos"].(string)
	if todos == "" {
		t.Fatalf("compaction entry carries no plan: %+v", entry.Meta)
	}
	// The estimate is the summary with its plan, plus the messages the
	// compaction kept.
	s.mu.Lock()
	chars := s.estimateChars(-1)
	kept := 0
	for _, e := range s.Entries {
		if e.InContext && e.Kind != "compaction" {
			kept += entryChars(e)
		}
	}
	s.mu.Unlock()
	if want := len(entry.Content) + len(todos) + kept; chars != want {
		t.Errorf("estimate = %d chars, want %d with the carried plan", chars, want)
	}

	h.engine.Send(s.ID, "carry on", nil)
	waitIdle(t, s)
	doc, _, _, _ = s.Snapshot()
	compaction, written := -1, false
	for i, e := range doc.Entries {
		if e.Kind == "compaction" {
			compaction = i
		}
		if compaction >= 0 && i > compaction && e.Kind == "tool" && e.ToolName == "todo_write" {
			written = true
		}
	}
	if !written {
		t.Fatal("no todo_write ran after the compaction")
	}
	if done, _ := tallyTodos(doc.Todos); done != len(doc.Todos) {
		t.Fatalf("todos = %+v, want every step done", doc.Todos)
	}
	if again := compactionMessage(t, s); again != sent {
		t.Errorf("the compaction message changed with the plan:\n%s\nwant\n%s", again, sent)
	}
}

func TestCompactionLeavesAPlanFromAnEarlierRequest(t *testing.T) {
	h := newHarness(t, "stream_todos.txt", "stream_plain.txt", "stream_plain.txt", "stream_summary.txt")
	s := h.session(t)
	h.engine.Send(s.ID, earlierWork, nil)
	waitIdle(t, s)
	h.engine.Send(s.ID, "now something else", nil)
	waitIdle(t, s)
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	if sent := compactionMessage(t, s); strings.Contains(sent, "todo list") {
		t.Errorf("a plan from an earlier request was carried:\n%s", sent)
	}
}

func TestCompactionLeavesAPlanAFailedWriteDidNotReplace(t *testing.T) {
	h := newHarness(t, "stream_todos.txt", "stream_plain.txt", "stream_todos_broken.txt", "stream_plain.txt", "stream_summary.txt")
	s := h.session(t)
	h.engine.Send(s.ID, earlierWork, nil)
	waitIdle(t, s)
	h.engine.Send(s.ID, "now something else", nil)
	waitIdle(t, s)
	doc, _, _, _ := s.Snapshot()
	var last store.Entry
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.ToolName == "todo_write" {
			last = e
		}
	}
	if !strings.Contains(last.Content, "not valid JSON") {
		t.Fatalf("the second todo_write did not fail: %+v", last)
	}
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	if sent := compactionMessage(t, s); strings.Contains(sent, "todo list") {
		t.Errorf("a failed todo_write carried the earlier plan:\n%s", sent)
	}
}

func TestCompactionCarriesThePlanPastASteer(t *testing.T) {
	h := newHarness(t, "stream_todos.txt", "stream_plain.txt", "stream_summary.txt")
	h.replay.Delay = 8 * time.Millisecond
	s := h.session(t)
	h.engine.Send(s.ID, earlierWork, nil)
	queueDuringTurn(t, h, s, "also check the tests")
	waitIdle(t, s)
	doc, _, _, _ := s.Snapshot()
	var steer store.Entry
	for _, e := range doc.Entries {
		if e.Content == "also check the tests" {
			steer = e
		}
	}
	if steer.Meta["steer"] != true {
		t.Fatalf("the steer was not marked: %+v", steer)
	}
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	if sent := compactionMessage(t, s); !strings.Contains(sent, carriedTodos) {
		t.Errorf("a steer dropped the plan:\n%s", sent)
	}
}

func TestCompactionCarriesThePlanPastAnAgentMessage(t *testing.T) {
	h := newHarness(t, "stream_todos.txt", "stream_plain.txt", "stream_summary.txt")
	s := h.session(t)
	h.engine.Send(s.ID, earlierWork, nil)
	waitIdle(t, s)
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "the routes are read",
		From: "Reviewer", FromSession: "sess_reviewer", InContext: true, Visible: true})
	s.mu.Unlock()
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	if sent := compactionMessage(t, s); !strings.Contains(sent, carriedTodos) {
		t.Errorf("a message from another agent dropped the plan:\n%s", sent)
	}
}

// approvePlan runs a plan turn to its approval and the execute reply after it,
// which is sent back to the unfinished plan twice.
func approvePlan(t *testing.T, h *harness, s *Session) {
	t.Helper()
	if err := h.engine.SetMode(s.ID, config.WorkPlan); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, earlierWork, nil)
	q := waitForQuestion(t, h, s.ID)
	if err := h.engine.Answer(q.ID, Answer{Options: []int{0}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
}

func TestCompactionCarriesTheApprovedPlansProgress(t *testing.T) {
	h := newHarness(t, "stream_write_plan.txt", "stream_exit_plan_mode.txt", "stream_plain.txt",
		"stream_plain.txt", "stream_plain.txt", "stream_summary.txt")
	s := h.session(t)
	approvePlan(t, h, s)
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	want := "Your todo list when this summary was written:\n0 of 2 done, 1 in hand\n- [>] Add the cache\n- [ ] Test it\n"
	if sent := compactionMessage(t, s); !strings.Contains(sent, want) {
		t.Errorf("the approved plan's progress was not carried:\n%s", sent)
	}
}

func TestCompactionLeavesAnApprovedPlanAfterANewRequest(t *testing.T) {
	h := newHarness(t, "stream_write_plan.txt", "stream_exit_plan_mode.txt", "stream_plain.txt",
		"stream_plain.txt", "stream_plain.txt", "stream_plain.txt", "stream_summary.txt")
	s := h.session(t)
	approvePlan(t, h, s)
	h.engine.Send(s.ID, "now something else", nil)
	waitIdle(t, s)
	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	if sent := compactionMessage(t, s); strings.Contains(sent, "todo list") {
		t.Errorf("an approved plan from an earlier request was carried:\n%s", sent)
	}
}

// A compaction is not stoppable. Stop is refused where it was pressed and the
// summary lands as it would have.
func TestCompactRefusesAStop(t *testing.T) {
	h := newHarness(t, "stream_summary.txt")
	s := h.session(t)
	h.replay.Delay = 200 * time.Millisecond
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: earlierWork, InContext: true, Visible: true})
	s.mu.Unlock()

	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StateCompacting)
	// The refusal has to be the state's, not a race with the request.
	waitForRequests(t, h, 1)
	if state, err := h.engine.Stop(s.ID); err != ErrCompacting {
		t.Errorf("Stop during a compaction = %s, %v", state, err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if doc.Compactions != 1 {
		t.Errorf("compactions = %d, want the compaction to have finished", doc.Compactions)
	}
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "compaction" {
		t.Errorf("last entry = %+v, want the summary", last)
	}
	if len(h.rec.ofType("error")) != 0 || len(h.rec.ofType("status_entry")) != 0 {
		t.Error("the refused stop reached the transcript")
	}
}

// A message typed while a compaction runs has no turn to join, so it starts
// one once the summary has landed.
func TestCompactDeliversQueuedMessage(t *testing.T) {
	h := newHarness(t, "stream_summary.txt", "stream_plain.txt")
	s := h.session(t)
	h.replay.Delay = 200 * time.Millisecond
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: earlierWork, InContext: true, Visible: true})
	s.mu.Unlock()

	if err := h.engine.Compact(s.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StateCompacting)
	sent, err := h.engine.Send(s.ID, "and now this", nil)
	if err != nil || !sent.Queued {
		t.Fatalf("Send during a compaction = %+v, %v", sent, err)
	}
	waitForRequests(t, h, 2)
	waitIdle(t, s)

	if held := s.Steer(); held != nil {
		t.Fatalf("the queued message was left behind: %+v", held)
	}
	doc, _, _, _ := s.Snapshot()
	kinds := []string{}
	for _, e := range doc.Entries {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"user", "compaction", "user", "assistant"}
	if strings.Join(kinds, ",") != strings.Join(want, ",") {
		t.Errorf("entries = %v, want %v", kinds, want)
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

// Choosing a different agent for the harness leaves a conversation on its own
// agent with everything read of its model intact, and sends nothing to the
// model server.
func TestSetAgentLeavesConversationsOnTheirOwnAgent(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	s.mu.Lock()
	s.NCtx, s.NCtxSource = 131072, "props"
	s.LastUsage = &llm.Usage{PromptTokens: 100}
	s.caps = llm.Caps{Known: true, ToolCalls: true}
	s.prepared = true
	s.mu.Unlock()

	other := h.addAgent(t, "Other", h.url, "Delta3.8-27B Q5")
	if _, err := h.cfg.SelectAgent(other.ID); err != nil {
		t.Fatal(err)
	}
	h.engine.SetAgent()
	h.engine.Wait(5 * time.Second)

	doc, state, _, caps := s.Snapshot()
	if doc.Agent == other.ID {
		t.Errorf("session agent = %q, want the conversation left on its own agent", doc.Agent)
	}
	if doc.Model != "Alpha-12B" {
		t.Errorf("session model = %q, want the one it was on", doc.Model)
	}
	if state != StateIdle {
		t.Errorf("state = %s, want a session a choice left ready", state)
	}
	if doc.LastUsage == nil {
		t.Error("last usage was dropped although the conversation never moved")
	}
	if doc.NCtxSource != "props" || doc.NCtx != 131072 || !caps.Known || !caps.ToolCalls {
		t.Errorf("what the model had been read was reset: n_ctx %d from %q caps %+v", doc.NCtx, doc.NCtxSource, caps)
	}
	h.replay.mu.Lock()
	queries := append([]string(nil), h.replay.Queries...)
	h.replay.mu.Unlock()
	if len(queries) != 0 {
		t.Errorf("the choice asked the model server: %v", queries)
	}
	for _, e := range doc.Entries {
		if e.Kind == "status" {
			t.Errorf("the transcript reported a change that moved nothing: %+v", e)
		}
	}
}

// A conversation whose agent was deleted takes the one answering now, and the
// transcript says so once rather than on every turn.
func TestAConversationWhoseAgentIsGoneFallsToTheDefault(t *testing.T) {
	h := newHarness(t)
	other := h.addAgent(t, "Other", h.url, "Delta3.8-27B Q5")
	s := h.sessionWith(t, "Beta-30B")
	if err := h.engine.UseAgent(s.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cfg.RemoveAgent(other.ID); err != nil {
		t.Fatal(err)
	}
	h.engine.SetAgent()
	h.engine.Wait(5 * time.Second)

	doc, _, _, _ := s.Snapshot()
	if doc.Agent != h.agent.ID {
		t.Errorf("session agent = %q, want the agent answering now", doc.Agent)
	}
	var notes []string
	for _, e := range doc.Entries {
		if e.Kind == "status" {
			notes = append(notes, e.Content)
		}
	}
	// A deleted agent leaves no name behind to read, so the note says the agent
	// is gone and names what answers in its place.
	if len(notes) != 1 {
		t.Fatalf("status entries = %v, want one note about the lost agent", notes)
	}
	if !strings.Contains(notes[0], "gone") {
		t.Errorf("note = %q, want it to say the agent is gone", notes[0])
	}
	if !strings.Contains(notes[0], h.agent.Name) {
		t.Errorf("note = %q, want it to name what answers instead", notes[0])
	}
}

// A conversation whose agent was switched off, rather than deleted, is named in
// the note, since it is still in the list to be switched back on.
func TestAConversationWhoseAgentIsDisabledIsToldSo(t *testing.T) {
	h := newHarness(t)
	own := h.addAgent(t, "Own", h.url, "Beta-30B")
	s := h.session(t)
	if err := h.engine.UseAgent(s.ID, own.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.cfg.UpdateAgent(own.ID, func(a *config.Agent) { a.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	h.engine.SetAgent()
	h.engine.Wait(5 * time.Second)

	var notes []string
	doc, _, _, _ := s.Snapshot()
	for _, e := range doc.Entries {
		if e.Kind == "status" {
			notes = append(notes, e.Content)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "Own is disabled") {
		t.Fatalf("status entries = %v, want one naming the agent switched off", notes)
	}
	if doc.Agent != h.agent.ID {
		t.Errorf("session agent = %q, want the agent answering now", doc.Agent)
	}
}

// A conversation given an agent of its own keeps it across a change of the one
// answering now, which is what lets two conversations run on two models.
func TestAConversationKeepsItsOwnAgent(t *testing.T) {
	h := newHarness(t)
	own := h.addAgent(t, "Own", h.url, "Beta-30B")
	s := h.session(t)
	if err := h.engine.UseAgent(s.ID, own.ID); err != nil {
		t.Fatal(err)
	}

	third := h.addAgent(t, "Third", h.url, "Gamma-9B")
	if _, err := h.cfg.SelectAgent(third.ID); err != nil {
		t.Fatal(err)
	}
	h.engine.SetAgent()
	h.engine.Wait(5 * time.Second)

	doc, _, _, _ := s.Snapshot()
	if doc.Agent != own.ID || doc.Model != "Beta-30B" {
		t.Errorf("session = %s/%s, want it left on %s/Beta-30B", doc.Agent, doc.Model, own.ID)
	}
}

// An agent id that names nothing, and one that exists switched off, are refused
// rather than leaving the conversation pointing at nothing.
func TestUseAgentRefusesAnAgentThatCannotAnswer(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	before := s.AgentID

	if err := h.engine.UseAgent(s.ID, "agt_nothing"); err != ErrUnknownAgent {
		t.Errorf("UseAgent of an unknown id = %v, want ErrUnknownAgent", err)
	}
	off := h.addAgent(t, "Off", h.url, "Beta-30B")
	if _, _, err := h.cfg.UpdateAgent(off.ID, func(a *config.Agent) { a.Enabled = false }); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.UseAgent(s.ID, off.ID); err == nil {
		t.Error("UseAgent of a disabled agent = nil, want it refused")
	}
	if doc, _, _, _ := s.Snapshot(); doc.Agent != before {
		t.Errorf("session agent = %q after two refusals, want it left where it was", doc.Agent)
	}
}

// Opening a session sends nothing. It marks the model unread, so the session
// takes a message at once and the turn that message starts is what loads the
// model.
func TestOpeningASessionReadsNothing(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.sessionWith(t, "Delta3.8-27B Q5")
	id := s.ID

	// Forgotten the way a restart forgets it, with the file left behind.
	h.engine.mu.Lock()
	delete(h.engine.live, id)
	h.engine.mu.Unlock()

	opened, err := h.engine.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := opened.State(); got != StateIdle {
		t.Errorf("state = %s, want a session that takes a message", got)
	}
	h.replay.mu.Lock()
	queries := append([]string(nil), h.replay.Queries...)
	h.replay.mu.Unlock()
	if len(queries) != 0 {
		t.Errorf("opening asked the model server: %v", queries)
	}

	if _, err := h.engine.Send(id, "hello", nil); err != nil {
		t.Fatalf("send after an open = %v", err)
	}
	waitIdle(t, opened)

	h.replay.mu.Lock()
	loads := append([]string(nil), h.replay.Loads...)
	h.replay.mu.Unlock()
	if len(loads) != 1 || loads[0] != "Delta3.8-27B Q5" {
		t.Errorf("loads = %v, want the model loaded once by the message", loads)
	}
}

// A session that was not open when the model changed takes it at its next
// message rather than answering with the model it last ran against.
func TestSessionTakesTheModelAtItsNextTurn(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	// Changed behind the session's back, as a session on disk would see it.
	if _, _, err := h.cfg.UpdateAgent(h.agent.ID, func(a *config.Agent) { a.Model = "Delta3.8-27B Q5" }); err != nil {
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
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	// Alpha-12B is loaded in the model list, so reading it must not offer to
	// start it. Delta is not, so preparing it is a load.
	loaded := h.sessionWith(t, "Alpha-12B")
	h.engine.Send(loaded.ID, "hello", nil)
	waitIdle(t, loaded)

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

	unloaded := h.sessionWith(t, "Delta3.8-27B Q5")
	h.engine.Send(unloaded.ID, "hello", nil)
	waitIdle(t, unloaded)

	h.replay.mu.Lock()
	loads = append([]string(nil), h.replay.Loads...)
	h.replay.mu.Unlock()
	if len(loads) != 1 || loads[0] != "Delta3.8-27B Q5" {
		t.Errorf("loads = %v, want the unloaded model loaded once", loads)
	}
}

// The model is read inside the turn the first message starts, so a message
// typed while it loads joins that turn's queue.
func TestMessageTypedWhileTheModelLoadsIsQueued(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	h.replay.Props = 300 * time.Millisecond
	s := h.sessionWith(t, "Delta3.8-27B Q5")

	if _, err := h.engine.Send(s.ID, "first", nil); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StateLoading)
	sent, err := h.engine.Send(s.ID, "second", nil)
	if err != nil {
		t.Fatalf("Send while the model loads = %v", err)
	}
	if !sent.Queued {
		t.Error("the message did not join the turn that was loading the model")
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	var texts []string
	for _, e := range doc.Entries {
		if e.Kind == "user" {
			texts = append(texts, e.Content)
		}
	}
	if strings.Join(texts, ",") != "first,second" {
		t.Errorf("user entries = %v, want both messages in order", texts)
	}
}

func TestUnansweredToolCallIsRepaired(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "go", InContext: true, Visible: true})
	s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant", InContext: true, Visible: true,
		ToolCalls: []llm.ToolCall{{ID: "call_x", Type: "function",
			Function: llm.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}}}})
	msgs := s.contextMessages(config.Agent{}, "darwin", "", topBrief(), nil)
	s.mu.Unlock()

	last := msgs[len(msgs)-1]
	if last.Role != "tool" || last.ToolCallID != "call_x" {
		t.Fatalf("last message = %+v, an unanswered call must be paired", last)
	}
	if last.Content != prompts.NotRun() {
		t.Errorf("content = %q", last.Content)
	}
}

// A session read back from disk with calls left unanswered has them closed:
// one that was marked started may have run, one that was not did not.
func TestInterruptedCallsAreClosedOnLoad(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: "go", InContext: true, Visible: true})
	s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant", InContext: true, Visible: true,
		Meta: map[string]any{"started": []any{"call_a"}},
		ToolCalls: []llm.ToolCall{
			{ID: "call_a", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: `{"command":"make"}`}},
			{ID: "call_b", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}},
		}})
	doc := s.doc()
	s.mu.Unlock()
	if err := h.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	h.engine.mu.Lock()
	delete(h.engine.live, s.ID)
	h.engine.mu.Unlock()

	loaded, err := h.engine.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _, _, _ := loaded.Snapshot()
	results := map[string]string{}
	interrupted := false
	for _, e := range got.Entries {
		if e.Kind == "tool" {
			results[e.ToolCallID] = e.Content
		}
		if e.Kind == "status" && strings.HasPrefix(e.Content, "Interrupted") {
			interrupted = true
		}
	}
	if results["call_a"] != prompts.OutcomeUnknown() || results["call_b"] != prompts.NotRun() {
		t.Errorf("results = %+v", results)
	}
	if !interrupted {
		t.Error("the transcript does not say the turn was interrupted")
	}
	// The repair is saved, so a second load finds nothing to close.
	saved, _ := h.store.Load(s.ID)
	if len(saved.Entries) != len(got.Entries) {
		t.Errorf("saved %d entries, the repaired session has %d", len(saved.Entries), len(got.Entries))
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

// TestToolCallLimitRefusesThenEnds asserts that calls made after the budget is
// spent are answered rather than run, with the tools still sent, and that a
// model calling tools a second time after that ends the turn.
func TestToolCallLimitRefusesThenEnds(t *testing.T) {
	h := newHarness(t, "stream_two_tools.txt", "stream_two_tools.txt", "stream_two_tools.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) {
		c.Mode = config.ModeAllowlist
		c.MaxToolCalls = 2
	})
	h.cfg.AddAllow(h.project, "echo")
	s := h.session(t)
	h.engine.Send(s.ID, "loop forever", nil)
	waitIdle(t, s)

	if h.replay.Count() != 3 {
		t.Errorf("model calls = %d, want 3", h.replay.Count())
	}
	if !strings.Contains(mustJSON(h.replay.Body(2)), `"tools"`) {
		t.Error("the request after the budget was spent carried no tools")
	}
	doc, _, _, _ := s.Snapshot()
	refusals := 0
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.Content == prompts.BudgetSpent() {
			refusals++
		}
	}
	if refusals != 4 {
		t.Errorf("refused calls = %d, want 4 across two replies", refusals)
	}
}

// TestRepeatedCallsAreNoted asserts that the third identical call in a row
// carries a note in its result, and the first two do not.
func TestRepeatedCallsAreNoted(t *testing.T) {
	h := newHarness(t, "stream_read.txt", "stream_read.txt", "stream_read.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "read it", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	var results []string
	for _, e := range doc.Entries {
		if e.Kind == "tool" {
			results = append(results, e.Content)
		}
	}
	if len(results) != 3 {
		t.Fatalf("tool results = %d, want 3", len(results))
	}
	for i, r := range results {
		noted := strings.Contains(r, "3 read calls in a row")
		if noted != (i == 2) {
			t.Errorf("result %d noted = %v: %q", i, noted, r)
		}
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
	if doc.Todos[0].Status != todoCompleted || doc.Todos[1].Status != todoInProgress || doc.Todos[2].Status != todoPending {
		t.Errorf("statuses = %+v", doc.Todos)
	}
	if doc.Todos[1].Content != "Add the handler" {
		t.Errorf("text = %q", doc.Todos[1].Content)
	}

	var result store.Entry
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.ToolName == "todo_write" {
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

// TestTurnStartClearsAFinishedList asserts that a list with work left carries
// into the next turn and one with every step completed does not.
func TestTurnStartClearsAFinishedList(t *testing.T) {
	h := newHarness(t, "stream_todos.txt", "stream_plain.txt", "stream_plain.txt",
		"stream_todos_finished.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "plan the work", nil)
	waitIdle(t, s)
	h.engine.Send(s.ID, "carry on", nil)
	waitIdle(t, s)
	if doc, _, _, _ := s.Snapshot(); len(doc.Todos) != 3 {
		t.Fatalf("todos = %+v, want the unfinished list carried over", doc.Todos)
	}
	if n := len(h.rec.ofType("todos")); n != 1 {
		t.Errorf("todos events = %d, want only the write", n)
	}

	h.engine.Send(s.ID, "finish it", nil)
	waitIdle(t, s)
	if doc, _, _, _ := s.Snapshot(); len(doc.Todos) != 3 {
		t.Fatalf("todos = %+v, want the finished list kept until the next turn", doc.Todos)
	}
	h.engine.Send(s.ID, "something else", nil)
	waitIdle(t, s)
	if doc, _, _, _ := s.Snapshot(); len(doc.Todos) != 0 {
		t.Errorf("todos = %+v, want the finished list cleared", doc.Todos)
	}
	events := h.rec.ofType("todos")
	if last, _ := events[len(events)-1].Fields["todos"].([]store.Todo); len(events) != 3 || len(last) != 0 {
		t.Errorf("todos events = %+v, want the clear published last", events)
	}
}

// TestWriteTodosRefusesWhatItWouldHaveToGuess asserts that a list with an
// unknown key or status, an empty step or a repeated one is an error rather
// than a list with the doubtful parts changed.
func TestWriteTodosRefusesWhatItWouldHaveToGuess(t *testing.T) {
	for name, tc := range map[string]struct{ args, want string }{
		"old key":      {`{"todos":[{"text":"Read","status":"pending"}]}`, `unknown field "text"`},
		"extra key":    {`{"todos":[{"content":"Read","status":"pending","id":1}]}`, `unknown field "id"`},
		"status":       {`{"todos":[{"content":"Read","status":"complete"}]}`, `status "complete"`},
		"no status":    {`{"todos":[{"content":"Read"}]}`, `status ""`},
		"empty":        {`{"todos":[{"content":"  ","status":"pending"}]}`, "item 1 has no content"},
		"repeated":     {`{"todos":[{"content":"Read","status":"pending"},{"content":"Read","status":"completed"}]}`, "listed twice"},
		"no list":      {`{}`, "todos is missing"},
		"invalid json": {`{"todos":[`, "not valid JSON"},
	} {
		if _, err := parseTodos(json.RawMessage(tc.args)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want one naming %q", name, err, tc.want)
		}
	}
	todos, err := parseTodos(json.RawMessage(`{"todos":[{"content":" Read ","status":"completed"},{"content":"Write","status":"in_progress"}]}`))
	if err != nil || len(todos) != 2 || todos[0] != (store.Todo{Content: "Read", Status: todoCompleted}) {
		t.Errorf("todos = %+v, err = %v", todos, err)
	}
	if todos, err := parseTodos(json.RawMessage(`{"todos":[]}`)); err != nil || len(todos) != 0 {
		t.Errorf("an empty list: todos = %+v, err = %v", todos, err)
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
// TestImageAloneHasNoTextPart asserts that a message with a picture and no
// words sends the picture alone, since a server refuses an empty text part.
func TestImageAloneHasNoTextPart(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, onePixelPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Send(s.ID, "", []string{png}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	msgs, _ := h.replay.Body(0)["messages"].([]any)
	user, _ := msgs[len(msgs)-1].(map[string]any)
	parts, ok := user["content"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("user content = %v", user["content"])
	}
	if part, _ := parts[0].(map[string]any); part["type"] != "image_url" {
		t.Errorf("the only part = %v, want the picture", part)
	}
}

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
	if text["type"] != "text" || text["text"] != "what is this" {
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

// TestReadImageSendsThePicture asserts that read_image keeps a copy in the
// file store and that the next request carries it in the tool message, as the
// bytes that were read rather than the file as it stands.
func TestReadImageSendsThePicture(t *testing.T) {
	h := newHarness(t, "stream_read_image.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	png := filepath.Join(h.project, "shot.png")
	if err := os.WriteFile(png, onePixelPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "look at it", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	var tool store.Entry
	for _, e := range doc.Entries {
		if e.Kind == "tool" {
			tool = e
		}
	}
	if len(tool.Attachments) != 1 || tool.Attachments[0].File == "" || tool.Meta["image"] != tool.Attachments[0].File {
		t.Fatalf("tool entry = %+v", tool)
	}
	if !strings.Contains(tool.Content, "shot.png, PNG") {
		t.Errorf("result = %q", tool.Content)
	}

	// Changing the file afterwards changes nothing the model is sent.
	if err := os.WriteFile(png, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs, _ := h.replay.Body(1)["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || last["tool_call_id"] != "call_i" {
		t.Fatalf("last message = %v", last)
	}
	parts, ok := last["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("tool content = %v", last["content"])
	}
	image, _ := parts[1].(map[string]any)
	url, _ := image["image_url"].(map[string]any)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(onePixelPNG())
	if image["type"] != "image_url" || url["url"] != want {
		t.Errorf("image part = %v", image)
	}
}

// TestRefusedImageIsAToolError asserts that a server refusing the picture
// read_image returned ends neither the loop nor the session: the result says
// why, the picture is dropped, and the request goes again without it.
func TestRefusedImageIsAToolError(t *testing.T) {
	h := newHarness(t, "stream_read_image.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.mu.Lock()
	h.replay.NoVision = true
	h.replay.mu.Unlock()
	s := h.session(t)
	if err := os.WriteFile(filepath.Join(h.project, "shot.png"), onePixelPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "look at it", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	var tool store.Entry
	for _, e := range doc.Entries {
		if e.Kind == "tool" {
			tool = e
		}
	}
	if len(tool.Attachments) != 0 || tool.Meta["error"] != true {
		t.Fatalf("tool entry = %+v", tool)
	}
	if !strings.Contains(tool.Content, "shot.png was not sent") || !strings.Contains(tool.Content, "Vision is disabled") {
		t.Errorf("result = %q", tool.Content)
	}
	last := doc.Entries[len(doc.Entries)-1]
	if last.Kind != "assistant" {
		t.Errorf("the turn ended on %s %q, want the model's reply", last.Kind, last.Content)
	}
	if n := h.replay.Count(); n != 3 {
		t.Errorf("chat requests = %d, want the call, the refusal and the retry", n)
	}
	if carriesImage(h.replay.Body(2)) {
		t.Error("the retry carried the picture again")
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
	if only["role"] != "user" || !strings.Contains(content, "Six words or fewer") {
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
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: earlierWork, InContext: true, Visible: true})
	s.mu.Unlock()
	h.engine.Send(s.ID, "read, run, write", nil)

	// read is allowed by policy, bash is denied, write is
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
	// The compaction keeps the latest messages, which sit before the divider
	// from the entry it names; everything older is out of the context.
	tail := 1 << 30
	for _, e := range doc.Entries {
		if e.Kind == "compaction" {
			if n, ok := metaInt(e.Meta["tail"]); ok {
				tail = n
			}
		}
	}
	sawDivider := false
	for _, e := range doc.Entries {
		if e.Kind == "compaction" {
			sawDivider = true
			continue
		}
		if !sawDivider {
			if e.InContext && e.Seq < tail {
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

func TestContextBreakdownIsEstimatedFromThePrompt(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_title.txt")
	s := h.session(t)
	h.engine.Send(s.ID, "what is in this project", nil)
	waitIdle(t, s)

	// The fixed parts are estimated locally, so the turn sends the title and
	// the reply and nothing else.
	h.replay.mu.Lock()
	order := append([]string(nil), h.replay.Order...)
	h.replay.mu.Unlock()
	if strings.Join(order, ",") != "title,chat" {
		t.Errorf("server order = %v, want the title and the reply alone", order)
	}

	s.mu.Lock()
	baseChars := len(s.systemMessage(h.agent, h.engine.os, h.engine.today(), topBrief()).Content)
	schemaChars := len(mustJSON(tools.Schemas(topBrief().offer)))
	s.mu.Unlock()
	parts := s.Breakdown()
	if parts == nil {
		t.Fatal("no breakdown after the turn")
	}
	if want := baseChars / charsPerToken; parts.System != want {
		t.Errorf("system = %d, want %d", parts.System, want)
	}
	if parts.Rules != 0 {
		t.Errorf("rules = %d, want 0: the project has no rules file", parts.Rules)
	}
	if want := schemaChars / charsPerToken; parts.Tools != want {
		t.Errorf("tools = %d, want %d", parts.Tools, want)
	}
	// The turn reported 3950 prompt and 48 completion tokens.
	if want := 3950 + 48 - parts.System - parts.Tools; parts.Messages != want {
		t.Errorf("messages = %d, want %d", parts.Messages, want)
	}
}

// The agent is read once at the start of a turn. An edit that lands part way
// through must not change the prompt under the model.
func TestTurnKeepsItsAgentSnapshot(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })
	s := h.session(t)
	h.engine.Send(s.ID, "list", nil)

	// The turn is under way once it has asked something, so an edit now lands
	// between two of its steps.
	waitForRequests(t, h, 1)
	h.setAgent(t, func(a *config.Agent) { a.Model = "Delta3.8-27B Q5" })
	waitIdle(t, s)
	if h.replay.Count() < 2 {
		t.Fatalf("the turn made %d requests, want the tool call and the reply after it", h.replay.Count())
	}

	for i := 0; i < h.replay.Count(); i++ {
		if got := h.replay.Body(i)["model"]; got != "Alpha-12B" {
			t.Errorf("request %d asked %v, want the model the turn started with", i, got)
		}
	}
}

func TestAgentSamplingReachesTheRequest(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	temp, top := 0.35, 0.9
	h.setAgent(t, func(a *config.Agent) { a.Temperature, a.TopP = &temp, &top })
	s := h.session(t)
	h.engine.Send(s.ID, "hello", nil)
	waitIdle(t, s)

	body := h.replay.Body(0)
	if body["temperature"] != 0.35 || body["top_p"] != 0.9 {
		t.Errorf("body carried temperature %v top_p %v", body["temperature"], body["top_p"])
	}
}

func TestInstructionsReachTheRequest(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	h.setAgent(t, func(a *config.Agent) { a.Instructions = "Answer in Latin." })
	s := h.session(t)
	h.engine.Send(s.ID, "hello", nil)
	waitIdle(t, s)

	msgs, _ := h.replay.Body(0)["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("no messages in %v", h.replay.Body(0))
	}
	first, _ := msgs[0].(map[string]any)
	system, _ := first["content"].(string)
	if !strings.HasSuffix(system, "Answer in Latin.") {
		t.Errorf("system message did not end with the agent's instructions: %q", system)
	}
	if !strings.Contains(system, "read") {
		t.Errorf("the built-in prompt was lost: %q", system)
	}
}

// rulesMarker is the line the project-rules tests write into the file.
const rulesMarker = "Never use tabs."

func TestProjectRulesReachTheRequest(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	if err := os.WriteFile(filepath.Join(h.project, "AGENTS.md"), []byte(rulesMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "what is in this project", nil)
	waitIdle(t, s)

	msgs, _ := h.replay.Body(0)["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	system, _ := first["content"].(string)
	if !strings.Contains(system, "Rules for this project, from AGENTS.md at its root:\n"+rulesMarker) {
		t.Errorf("the first request does not carry the rules under their label: %q", system)
	}
}

// A file that grows between turns is a different prompt, and the estimate is
// re-taken at the next turn start rather than the old one standing.
func TestProjectRulesReestimatedWhenTheFileGrows(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	if err := os.WriteFile(filepath.Join(h.project, "AGENTS.md"), []byte(rulesMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "one", nil)
	waitIdle(t, s)
	first := s.Breakdown()
	if first == nil || first.Rules == 0 {
		t.Fatalf("breakdown = %+v, want the rules to cost something", first)
	}

	grown := rulesMarker + "\nAnd never use semicolons."
	if err := os.WriteFile(filepath.Join(h.project, "AGENTS.md"), []byte(grown), 0o644); err != nil {
		t.Fatal(err)
	}
	h.engine.Send(s.ID, "two", nil)
	waitIdle(t, s)

	withRules := topBrief()
	withRules.rules, withRules.rulesName = grown, "AGENTS.md"
	s.mu.Lock()
	baseChars := len(s.systemMessage(h.agent, h.engine.os, h.engine.today(), topBrief()).Content)
	sysChars := len(s.systemMessage(h.agent, h.engine.os, h.engine.today(), withRules).Content)
	s.mu.Unlock()
	if parts := s.Breakdown(); parts == nil {
		t.Fatal("no breakdown after the second turn")
	} else if want := (sysChars - baseChars) / charsPerToken; parts.Rules != want {
		t.Errorf("rules = %d, want %d: the estimate follows the grown file", parts.Rules, want)
	} else if parts.Rules < first.Rules {
		t.Errorf("rules = %d, want more than the %d the smaller file cost", parts.Rules, first.Rules)
	}
}

// A file a person named agents.md is read as well, and the label names it as
// it stands on disk.
func TestProjectRulesReadCaseInsensitive(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	if err := os.WriteFile(filepath.Join(h.project, "agents.md"), []byte(rulesMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "hello", nil)
	waitIdle(t, s)

	msgs, _ := h.replay.Body(0)["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	system, _ := first["content"].(string)
	if !strings.Contains(system, rulesMarker) {
		t.Errorf("a lowercase agents.md was not read: %q", system)
	}
	if !strings.Contains(system, "Rules for this project, from agents.md at its root:") {
		t.Errorf("the label does not name the file as found: %q", system)
	}
}

func TestContextBreakdownIncludesProjectRules(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	if err := os.WriteFile(filepath.Join(h.project, "AGENTS.md"), []byte(rulesMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "what is in this project", nil)
	waitIdle(t, s)

	withRules := topBrief()
	withRules.rules, withRules.rulesName = rulesMarker, "AGENTS.md"
	s.mu.Lock()
	baseChars := len(s.systemMessage(h.agent, h.engine.os, h.engine.today(), topBrief()).Content)
	sysChars := len(s.systemMessage(h.agent, h.engine.os, h.engine.today(), withRules).Content)
	s.mu.Unlock()
	parts := s.Breakdown()
	if parts == nil {
		t.Fatal("no breakdown after the turn")
	}
	// The step between the system message with and without the rules, at
	// charsPerToken characters per token, is the rules' cost.
	if want := (sysChars - baseChars) / charsPerToken; parts.Rules != want {
		t.Errorf("rules = %d, want %d", parts.Rules, want)
	}
	// The turn reported 3950 prompt and 48 completion tokens.
	if want := 3950 + 48 - parts.System - parts.Rules - parts.Tools; parts.Messages != want {
		t.Errorf("messages = %d, want %d", parts.Messages, want)
	}
}

func TestProjectRulesOverTheLimitAreNotInjected(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	big := bytes.Repeat([]byte("x "), (tools.MaxAttachment+2048)/2)
	if err := os.WriteFile(filepath.Join(h.project, "AGENTS.md"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "what is in this project", nil)
	waitIdle(t, s)

	want := "AGENTS.md is 258 kb, the limit is 256 kb: it is not in the prompt"
	msgs, _ := h.replay.Body(0)["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	system, _ := first["content"].(string)
	if strings.Contains(system, "Rules for this project") {
		t.Error("an over-limit file was injected into the prompt")
	}
	h.engine.Send(s.ID, "and again", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	notes := 0
	for _, e := range doc.Entries {
		if e.Kind != "status" {
			continue
		}
		if e.Content == want {
			notes++
			continue
		}
		if strings.Contains(e.Content, "AGENTS.md") {
			t.Errorf("unexpected rules note: %q", e.Content)
		}
	}
	if notes != 1 {
		t.Errorf("the over-limit note was drawn %d times over two turns, want once", notes)
	}
	if parts := s.Breakdown(); parts == nil || parts.Rules != 0 {
		t.Errorf("breakdown = %+v, want the rules to cost nothing", parts)
	}
}

// An address that moved may not be answered by the same thing, so what was
// read from it is dropped.
func TestForgetDropsWhatWasReadFromAnAgent(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	s.mu.Lock()
	s.NCtx, s.NCtxSource, s.prepared = 131072, "props", true
	s.LastUsage = &llm.Usage{PromptTokens: 100}
	s.mu.Unlock()

	h.engine.Forget("agt_someone_else")
	if doc, _, _, _ := s.Snapshot(); doc.NCtxSource != "props" {
		t.Error("another agent's change reached this session")
	}

	h.engine.Forget(h.agent.ID)
	doc, _, _, caps := s.Snapshot()
	if doc.NCtxSource != "" || caps.Known {
		t.Errorf("the window and the template survived: %q %+v", doc.NCtxSource, caps)
	}
	if doc.LastUsage == nil {
		t.Error("the usage report was dropped, and it is still what the last turn cost")
	}
}

// topBrief is what a conversation somebody started is shaped with, for a test
// rendering a prompt without running a turn.
// topBrief is what a conversation is offered in a harness holding one agent.
// That agent's own turn takes its only slot, so there is nobody to hand work
// to and subagent is not sent.
func topBrief() brief {
	return brief{offer: tools.Offer{Mode: config.WorkExecute, Ask: true, Message: true}}
}

// A compaction keeps the latest messages word for word, starting on an
// assistant entry so a call stays beside its result, and the summary is sent
// ahead of them. Dropping the summary gives everything back.
func TestCompactionKeepsTheTail(t *testing.T) {
	s := newSession("sess_tail", "/p", "a", "m", config.ToolsetFull)
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: strings.Repeat("x", 4000), InContext: true, Visible: true})
	call := s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant", InContext: true, Visible: true,
		ToolCalls: []llm.ToolCall{{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "read", Arguments: `{"path":"a.go"}`}}}})
	s.appendEntry(store.Entry{Kind: "tool", Role: "tool", ToolCallID: "call_1", ToolName: "read",
		Content: strings.Repeat("r", 300), InContext: true, Visible: true})
	s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant", Content: "latest words", InContext: true, Visible: true})

	start := s.tailStart()
	if start != 1 {
		t.Fatalf("tail starts at %d, want the assistant that made the call", start)
	}
	for i := range s.Entries {
		if i < start {
			s.Entries[i].InContext = false
		}
	}
	s.appendEntry(store.Entry{Kind: "compaction", Role: "user", Content: "the summary", InContext: true, Visible: true,
		Meta: map[string]any{"tail": float64(call.Seq)}})

	msgs := s.contextMessages(config.Agent{}, "darwin", "", topBrief(), nil)
	var roles []string
	for _, m := range msgs[1:] {
		roles = append(roles, m.Role)
	}
	if strings.Join(roles, ",") != "user,assistant,tool,assistant" || !strings.Contains(msgs[1].Content, "the summary") {
		t.Errorf("context = %v, want the summary and then the kept messages", roles)
	}
	if msgs[len(msgs)-1].Content != "latest words" {
		t.Errorf("last message = %q", msgs[len(msgs)-1].Content)
	}

	// A reload rebuilds the same context from the tail the entry names.
	for i := range s.Entries {
		s.Entries[i].InContext = false
	}
	s.restoreContext()
	if s.Entries[0].InContext || !s.Entries[1].InContext || !s.Entries[3].InContext {
		t.Errorf("restored context = %v %v %v", s.Entries[0].InContext, s.Entries[1].InContext, s.Entries[3].InContext)
	}
	s.rewindTo(len(s.Entries) - 1)
	if !s.Entries[0].InContext {
		t.Error("dropping the compaction did not give back what it summarised")
	}
}

// A turn that ends with steps of an approved plan left is sent back to it, and
// stops being sent back once two rounds in a row complete nothing. The nudge is
// in the context and not in the transcript.
func TestTurnCarriesOnWithThePlan(t *testing.T) {
	h := newHarness(t, "stream_write_plan.txt", "stream_exit_plan_mode.txt",
		"stream_plain.txt", "stream_plain.txt", "stream_plain.txt", "stream_plain.txt")
	s := h.session(t)
	approvePlan(t, h, s)

	// Two plan requests, the execute reply, and two rounds sent back.
	if n := h.replay.Count(); n != 5 {
		t.Errorf("requests = %d, want the execute reply and two rounds sent back", n)
	}
	doc, _, _, _ := s.Snapshot()
	nudges, notes := 0, 0
	for _, e := range doc.Entries {
		if e.Kind == "user" && e.Meta["continue"] == true {
			nudges++
			if e.Visible || !e.InContext || !strings.Contains(e.Content, "The next one is: Add the cache") {
				t.Errorf("nudge = %+v", e)
			}
		}
		if e.Kind == "status" && strings.Contains(e.Content, "carrying on") {
			notes++
		}
	}
	if nudges != 2 || notes != 2 {
		t.Errorf("nudges = %d, notes = %d, want 2 of each", nudges, notes)
	}
	s.mu.Lock()
	last := s.lastUserEntry()
	kickoff := last >= 0 && s.Entries[last].Meta["kickoff"] == true
	s.mu.Unlock()
	if !kickoff {
		t.Error("a nudge stands in for the request the turn answers")
	}

	// A later request is not pushed back to the old plan.
	h.engine.Send(s.ID, "now something else", nil)
	waitIdle(t, s)
	if n := h.replay.Count(); n != 6 {
		t.Errorf("requests = %d, want the new request answered once", n)
	}
}

// Reads made together in one reply run at once and land in the order the
// model made them, each paired with its own call.
func TestReadsInOneReplyRunTogether(t *testing.T) {
	h := newHarness(t, "stream_two_reads.txt", "stream_plain.txt", "stream_plain.txt")
	if err := os.WriteFile(filepath.Join(h.project, "other.txt"), []byte("other contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "read both", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	var got []store.Entry
	for _, e := range doc.Entries {
		if e.Kind == "tool" {
			got = append(got, e)
		}
	}
	if len(got) != 2 || got[0].ToolCallID != "call_0" || got[1].ToolCallID != "call_1" {
		t.Fatalf("tool entries = %+v", got)
	}
	if !strings.Contains(got[0].Content, "one") || !strings.Contains(got[1].Content, "other contents") {
		t.Errorf("results = %q, %q", got[0].Content, got[1].Content)
	}
	if len(h.rec.ofType("tool_start")) != 2 || len(h.rec.ofType("tool_output")) != 2 {
		t.Errorf("tool events: %d started, %d output", len(h.rec.ofType("tool_start")), len(h.rec.ofType("tool_output")))
	}
}
