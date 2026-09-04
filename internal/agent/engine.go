package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llama"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/prompts"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// maxIterations bounds a runaway turn. Reaching it ends the turn in limit.
const maxIterations = 50

var (
	ErrBusy      = errors.New("the session is busy: stop the current turn first")
	ErrNotFound  = errors.New("no session with that id")
	ErrEmptyText = errors.New("the message is empty: type something to send")
	ErrNoProject = errors.New("that folder is not in the project list: add it first")
	ErrNoModel   = errors.New("no model is selected: pick one from the model list")
)

// Engine owns every live session and dispatches turns.
type Engine struct {
	cfg      *config.Store
	store    *store.Sessions
	clientMu sync.RWMutex
	client   *llama.Client
	perms    *perms.Broker
	hub      Publisher

	ctx context.Context
	os  string

	mu   sync.Mutex
	live map[string]*Session
	wg   sync.WaitGroup
}

func NewEngine(ctx context.Context, cfg *config.Store, st *store.Sessions, cl *llama.Client,
	pb *perms.Broker, hub Publisher) *Engine {
	return &Engine{
		cfg: cfg, store: st, client: cl, perms: pb, hub: hub,
		ctx: ctx, os: runtime.GOOS, live: map[string]*Session{},
	}
}

func (e *Engine) publish(sessionID string, event Event) {
	if e.hub != nil {
		e.hub.Publish(sessionID, event)
	}
}

// setState records the state and tells the browser, which drives the spinner
// and the Stop button off this one event.
func (e *Engine) setState(s *Session, st State) {
	s.mu.Lock()
	s.state = st
	s.mu.Unlock()
	e.publish(s.ID, ev("status", map[string]any{"state": string(st)}))
}

func (e *Engine) save(s *Session) {
	s.mu.Lock()
	doc := s.doc()
	s.mu.Unlock()
	if err := e.store.Save(doc); err != nil {
		e.publish(s.ID, ev("error", map[string]any{"kind": "save", "message": err.Error()}))
	}
}

// Create starts a session in a project the user has added, bound to a model.
// SetClient re-points the engine after the server address changes. A turn
// already running keeps the client it started with.
func (e *Engine) SetClient(cl *llama.Client) {
	e.clientMu.Lock()
	e.client = cl
	e.clientMu.Unlock()
}

// Llama is the client a turn would use right now.
func (e *Engine) Llama() *llama.Client { return e.llama() }

func (e *Engine) llama() *llama.Client {
	e.clientMu.RLock()
	defer e.clientMu.RUnlock()
	return e.client
}

func (e *Engine) Create(project, model string) (*Session, error) {
	if !e.cfg.HasProject(project) {
		return nil, ErrNoProject
	}
	if model == "" {
		model = e.cfg.Config().Model
	}
	if model == "" {
		return nil, ErrNoModel
	}
	s := newSession(store.NewID(), project, model)
	e.mu.Lock()
	e.live[s.ID] = s
	e.mu.Unlock()
	e.save(s)
	e.probeProps(s, true)
	return s, nil
}

// Get resolves a live session, falling back to the file, so there is only
// ever one *Session per id in the process.
func (e *Engine) Get(id string) (*Session, error) {
	e.mu.Lock()
	if s, ok := e.live[id]; ok {
		e.mu.Unlock()
		return s, nil
	}
	e.mu.Unlock()

	doc, err := e.store.Load(id)
	if err != nil {
		return nil, ErrNotFound
	}
	s := fromDoc(doc)
	s.turnSeq = 0
	for _, entry := range doc.Entries {
		if entry.Seq > s.turnSeq {
			s.turnSeq = entry.Seq
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, ok := e.live[id]; ok {
		return existing, nil
	}
	e.live[id] = s
	return s, nil
}

func (e *Engine) State(id string) State {
	s, err := e.Get(id)
	if err != nil {
		return StateIdle
	}
	return s.State()
}

// Send appends the user entry and starts the turn goroutine. It returns as
// soon as the goroutine is running; everything else arrives on the stream.
func (e *Engine) Send(id, text string) error {
	if strings.TrimSpace(text) == "" {
		return ErrEmptyText
	}
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.state != StateIdle {
		s.mu.Unlock()
		return ErrBusy
	}
	s.state = StatePreflight
	ctx, cancel := context.WithCancel(e.ctx)
	s.cancel = cancel
	s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: text, InContext: true, Visible: true})
	if s.Title == "" {
		s.Title = store.Title(text)
	}
	s.mu.Unlock()

	e.save(s)
	e.publish(s.ID, ev("status", map[string]any{"state": string(StatePreflight)}))

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		e.run(ctx, s)
	}()
	return nil
}

// Stop cancels the turn. It never appends: the loop reaches stopped and
// writes the marker itself, so entry order is one goroutine's order.
func (e *Engine) Stop(id string) (State, error) {
	s, err := e.Get(id)
	if err != nil {
		return StateIdle, err
	}
	s.mu.Lock()
	cancel := s.cancel
	state := s.state
	s.mu.Unlock()
	if state == StateIdle || cancel == nil {
		return StateIdle, nil
	}
	cancel()
	e.perms.CancelSession(id)
	return StateStopped, nil
}

// Compact rewrites the in-context set on an idle session.
func (e *Engine) Compact(id string) error {
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.state != StateIdle {
		s.mu.Unlock()
		return ErrBusy
	}
	s.state = StateCompacting
	ctx, cancel := context.WithCancel(e.ctx)
	s.cancel = cancel
	s.mu.Unlock()

	e.publish(s.ID, ev("status", map[string]any{"state": string(StateCompacting)}))
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		if err := e.compact(ctx, s); err != nil {
			e.fail(s, err)
			return
		}
		e.finish(s, StateIdle, "", 0)
	}()
	return nil
}

// Rebind points an idle session at another model and forgets everything the
// previous model's tokenizer implied.
func (e *Engine) Rebind(id, model string, hint int, hintSource string) error {
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.state != StateIdle {
		s.mu.Unlock()
		return ErrBusy
	}
	s.Model = model
	s.NCtx, s.NCtxSource = hint, hintSource
	s.caps = llama.Caps{}
	s.LastUsage = nil
	s.lastUsageEntry = -1
	entry := s.appendEntry(store.Entry{Kind: "status", Content: "model changed to " + model,
		InContext: false, Visible: true})
	s.mu.Unlock()

	e.save(s)
	e.publish(s.ID, ev("status_entry", map[string]any{"seq": entry.Seq, "content": entry.Content}))
	e.probeProps(s, true)
	return nil
}

// Rename sets the session title. An empty title restores the one derived from
// the first user message.
func (e *Engine) Rename(id, title string) error {
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.Title = title
	s.mu.Unlock()
	e.save(s)
	return nil
}

func (e *Engine) Delete(id string) error {
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	if s.State() != StateIdle {
		return ErrBusy
	}
	if err := e.store.Delete(id); err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.live, id)
	e.mu.Unlock()
	return nil
}

// Wait blocks until the running turns finish or the deadline passes. Turns
// that miss it are abandoned with whatever was last flushed.
func (e *Engine) Wait(d time.Duration) {
	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// probeProps fills n_ctx and caps from a model-scoped read that carries
// autoload=false. onlyIfLoaded keeps it off a model the router would have to
// start.
func (e *Engine) probeProps(s *Session, onlyIfLoaded bool) {
	s.mu.Lock()
	model, source := s.Model, s.NCtxSource
	s.mu.Unlock()
	if model == "" || source == "props" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
		defer cancel()
		if onlyIfLoaded {
			models, err := e.llama().Models(ctx)
			if err != nil {
				return
			}
			loaded := false
			for _, m := range models {
				if m.ID == model && m.Status == llama.StatusLoaded {
					loaded = true
				}
			}
			if !loaded {
				return
			}
		}
		props, err := e.llama().Props(ctx, model)
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.Model == model {
			if props.NCtx > 0 {
				s.NCtx, s.NCtxSource = props.NCtx, "props"
			}
			s.caps = props.Caps
		}
		s.mu.Unlock()
		e.save(s)
	}()
}

// SetModelHint records a context hint from the model list for a session whose
// model has never been loaded.
func (e *Engine) SetModelHint(s *Session, hint int, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.NCtxSource == "props" || hint <= 0 {
		return
	}
	s.NCtx, s.NCtxSource = hint, source
}

func (e *Engine) fail(s *Session, err error) {
	kind := string(llama.Kind(err))
	if kind == "" {
		kind = "internal"
	}
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "error", Content: err.Error(), InContext: false, Visible: true})
	s.mu.Unlock()
	e.setState(s, StateFailed)
	e.save(s)
	e.publish(s.ID, ev("error", map[string]any{"kind": kind, "message": err.Error()}))
	e.finish(s, StateIdle, "error", 0)
}

// finish publishes done and returns the session to idle, which is the only
// state a new message is accepted in.
func (e *Engine) finish(s *Session, next State, finishReason string, iterations int) {
	s.mu.Lock()
	s.state = next
	s.cancel = nil
	s.mu.Unlock()
	e.publish(s.ID, ev("done", map[string]any{"finish_reason": finishReason, "iterations": iterations}))
	e.publish(s.ID, ev("status", map[string]any{"state": string(next)}))
}

func (e *Engine) run(ctx context.Context, s *Session) {
	cfg := e.cfg.Config()
	runner, err := tools.NewRunner(s.Project, cfg)
	if err != nil {
		e.fail(s, err)
		return
	}

	for iteration := 0; iteration < maxIterations; iteration++ {
		e.setState(s, StatePreflight)

		s.mu.Lock()
		fill := s.computeFill()
		nctx, _ := s.effectiveNCtx()
		s.mu.Unlock()
		if float64(fill) >= cfg.CompactAt*float64(nctx) {
			e.setState(s, StateCompacting)
			if err := e.compact(ctx, s); err != nil {
				e.endWithError(s, err, iteration)
				return
			}
		}

		e.setState(s, StateStreaming)
		s.mu.Lock()
		req := llama.ChatRequest{
			Model:    s.Model,
			Messages: s.contextMessages(e.os),
			Tools:    tools.Schemas(),
			Effort:   cfg.Effort,
			Caps:     s.caps,
		}
		s.mu.Unlock()

		res, err := e.llama().Chat(ctx, req, func(event llama.Event) error {
			e.forward(s, event)
			return nil
		})
		if err != nil {
			e.endWithError(s, err, iteration)
			return
		}
		if res.Dropped > 0 {
			e.publish(s.ID, ev("error", map[string]any{"kind": "malformed",
				"message": fmt.Sprintf("the model server sent %d frames that could not be read: some output was lost", res.Dropped)}))
		}
		if res.Usage != nil {
			s.mu.Lock()
			s.LastUsage = res.Usage
			s.mu.Unlock()
		}

		if len(res.ToolCalls) == 0 {
			s.mu.Lock()
			s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant",
				Content: res.Content, Reasoning: res.Reasoning, InContext: true, Visible: true})
			s.lastUsageEntry = len(s.Entries) - 1
			s.mu.Unlock()
			e.setState(s, StateFinal)
			e.save(s)
			e.probeProps(s, false)
			e.publishUsage(s)
			e.finish(s, StateIdle, res.FinishReason, iteration+1)
			return
		}

		s.mu.Lock()
		s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant",
			Content: res.Content, Reasoning: res.Reasoning, ToolCalls: res.ToolCalls,
			InContext: true, Visible: true})
		s.lastUsageEntry = len(s.Entries) - 1
		s.mu.Unlock()
		e.save(s)
		e.publishUsage(s)
		e.probeProps(s, false)

		for i, call := range res.ToolCalls {
			e.setState(s, StateDeciding)
			decision, record, stopped := e.decide(ctx, s, cfg, call)
			if stopped {
				e.stopWith(s, call, record)
				return
			}
			e.setState(s, StateExecuting)
			entry := e.execute(ctx, s, runner, call, decision, record, i)
			e.save(s)
			e.publish(s.ID, ev("tool_output", map[string]any{
				"index": i, "id": call.ID, "name": call.Function.Name,
				"output": entry.Content, "exit_code": entry.ExitCode, "truncated": entry.Truncated,
			}))
			if ctx.Err() != nil {
				e.markStopped(s)
				return
			}
		}
	}

	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "error",
		Content: "turn limit reached after 50 model calls", InContext: false, Visible: true})
	s.mu.Unlock()
	e.setState(s, StateLimit)
	e.save(s)
	e.publish(s.ID, ev("error", map[string]any{"kind": "limit",
		"message": "turn limit reached after 50 model calls: send a narrower instruction"}))
	e.finish(s, StateIdle, "limit", maxIterations)
}

// endWithError separates a cancellation, which is a stop, from a failure.
func (e *Engine) endWithError(s *Session, err error, iteration int) {
	if llama.Kind(err) == llama.KindCanceled || errors.Is(err, context.Canceled) {
		e.markStopped(s)
		return
	}
	e.fail(s, err)
}

func (e *Engine) markStopped(s *Session) {
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "status", Content: "stopped", InContext: false, Visible: true})
	s.mu.Unlock()
	e.setState(s, StateStopped)
	e.save(s)
	e.finish(s, StateIdle, "stopped", 0)
}

// stopWith records the refused call so the assistant message keeps its pair
// before the turn ends.
func (e *Engine) stopWith(s *Session, call llama.ToolCall, record *store.PermissionRecord) {
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "tool", Role: "tool", ToolCallID: call.ID,
		ToolName: call.Function.Name, Content: "stopped by user",
		Permission: record, InContext: true, Visible: true})
	s.mu.Unlock()
	e.save(s)
	e.markStopped(s)
}

func (e *Engine) forward(s *Session, event llama.Event) {
	switch event.Kind {
	case llama.EventContent:
		e.publish(s.ID, ev("delta", map[string]any{"text": event.Text}))
	case llama.EventReasoning:
		e.publish(s.ID, ev("reasoning", map[string]any{"text": event.Text}))
	case llama.EventToolCall:
		e.publish(s.ID, ev("tool_call", map[string]any{
			"index": event.Index, "id": event.ID, "name": event.Name, "arguments": event.Arguments}))
	}
}

func (e *Engine) publishUsage(s *Session) {
	s.mu.Lock()
	u := s.LastUsage
	fill := s.computeFill()
	nctx, _ := s.effectiveNCtx()
	s.mu.Unlock()
	if u == nil {
		return
	}
	e.publish(s.ID, ev("usage", map[string]any{
		"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
		"cached_tokens": u.CachedTokens, "fill": fill, "n_ctx": nctx}))
}

// decide applies the policy and, when it says prompt, blocks on the browser.
// The third result reports that the session was stopped while waiting.
func (e *Engine) decide(ctx context.Context, s *Session, cfg config.Config, call llama.ToolCall) (perms.Decision, *store.PermissionRecord, bool) {
	tool := call.Function.Name
	command, path := argSummary(call)
	record := &store.PermissionRecord{Tool: tool, Command: command, Path: path}

	if e.perms.Evaluate(s.Project, tool, command, cfg.Auto) == perms.ActionAllow {
		record.Decision = "auto"
		return perms.Decision{Kind: perms.DecideOnce}, record, false
	}
	// The id is minted here rather than inside Ask, so the record kept with the
	// tool entry carries the same id the card was answered with.
	req := perms.Request{
		ID:        perms.NewRequestID(),
		SessionID: s.ID, Project: s.Project, Tool: tool,
		Command: command, Path: path,
		Prefix: perms.DefaultPrefix(command),
		Args:   string(call.Function.Arguments),
	}
	d, err := e.perms.Ask(ctx, req)
	if err != nil {
		record.Decision = "stopped"
		return perms.Decision{}, record, true
	}
	record.ID = req.ID
	record.Decision = string(d.Kind)
	record.Prefix = d.Prefix
	record.Reason = d.Reason
	if d.Note != "" {
		e.publish(s.ID, ev("error", map[string]any{"kind": "allowlist", "message": d.Note}))
	}
	return d, record, false
}

// execute runs the tool or writes the denial, and appends the tool entry that
// pairs with the assistant message.
func (e *Engine) execute(ctx context.Context, s *Session, runner *tools.Runner,
	call llama.ToolCall, d perms.Decision, record *store.PermissionRecord, index int) store.Entry {
	content := ""
	var exitCode *int
	truncated := false

	switch {
	case d.Kind == perms.DecideDeny:
		content = perms.DenialContent(d.Reason)
	default:
		args := json.RawMessage(call.Function.Arguments)
		var res tools.Result
		var err error
		switch call.Function.Name {
		case "read_file":
			res, err = runner.ReadFile(ctx, args)
		case "write_file":
			res, err = runner.WriteFile(ctx, args)
		case "run_shell":
			res, err = runner.RunShell(ctx, args)
		default:
			err = fmt.Errorf("no tool named %s: use read_file, write_file or run_shell", call.Function.Name)
		}
		if err != nil {
			content = err.Error()
		} else {
			content, exitCode, truncated = res.Output, res.ExitCode, res.Truncated
		}
	}

	s.mu.Lock()
	entry := s.appendEntry(store.Entry{Kind: "tool", Role: "tool",
		ToolCallID: call.ID, ToolName: call.Function.Name, Content: content,
		ExitCode: exitCode, Truncated: truncated, Permission: record,
		InContext: true, Visible: true})
	s.mu.Unlock()
	return entry
}

// argSummary pulls the command and the path out of the raw arguments for the
// permission card, tolerating arguments the model malformed.
func argSummary(call llama.ToolCall) (command, path string) {
	var args struct {
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "", ""
	}
	return args.Command, args.Path
}

// compact replaces the in-context set with one summary. A failure leaves the
// history untouched, so a failed compaction never loses context.
func (e *Engine) compact(ctx context.Context, s *Session) error {
	s.mu.Lock()
	before := s.computeFill()
	msgs := append(s.contextMessages(e.os), llama.Message{Role: "user", Content: prompts.Compact()})
	model, caps := s.Model, s.caps
	s.mu.Unlock()

	res, err := e.llama().Chat(ctx, llama.ChatRequest{
		Model: model, Messages: msgs, Effort: llama.EffortOff, Caps: caps}, nil)
	if err != nil {
		return err
	}
	summary := strings.TrimSpace(res.Content)
	if summary == "" {
		return errors.New("the model returned an empty summary: try again or start a new session")
	}

	s.mu.Lock()
	for i := range s.Entries {
		s.Entries[i].InContext = false
	}
	s.Compactions++
	s.appendEntry(store.Entry{Kind: "compaction", Role: "user",
		Content:   "Summary of earlier work:\n" + summary + "\n\nContinue.",
		InContext: true, Visible: true,
		Meta: map[string]any{"before": before, "n": s.Compactions}})
	s.LastUsage = nil
	s.lastUsageEntry = len(s.Entries) - 1
	after := s.computeFill()
	s.Entries[len(s.Entries)-1].Meta["after"] = after
	s.mu.Unlock()

	e.save(s)
	e.publish(s.ID, ev("compacted", map[string]any{"before": before, "after": after, "summary": summary}))
	return nil
}
