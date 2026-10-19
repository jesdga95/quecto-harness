// Package agent owns the order of operations in a turn and is the only
// package that mutates a session.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

type State string

const (
	StateIdle       State = "idle"
	StateLoading    State = "loading"
	StatePreflight  State = "preflight"
	StateCompacting State = "compacting"
	StateStreaming  State = "streaming"
	StateDeciding   State = "deciding"
	StateExecuting  State = "executing"
	StateFinal      State = "final"
	StateStopped    State = "stopped"
	StateFailed     State = "failed"
)

// Session is one conversation with one model in one project. It is the
// in-memory authority; store holds the same data on disk.
type Session struct {
	mu sync.Mutex

	ID      string
	Project string
	AgentID string
	// Parent names the session that launched this one. A session with one is
	// a subagent run: it stays on its own agent, is offered a different set of
	// tools, and reports to its launcher when a turn of it ends.
	Parent string
	Model  string
	// Mode is the work mode the next turn runs in. A turn reads it once, at
	// its start, and holds it for the turn.
	Mode config.WorkMode
	// Toolset is fixed at creation and never written after.
	Toolset config.Toolset
	// Plan is the last plan the user approved, which execute mode carries in
	// its system prompt.
	Plan *store.Plan
	// Draft is the plan being written in plan mode.
	Draft   *store.Plan
	Created time.Time
	Updated time.Time
	Title   string
	Named   bool

	Entries []store.Entry

	NCtx        int
	NCtxSource  string
	ContextBase *store.ContextBase
	Compactions int
	Todos       []store.Todo
	Baselines   []store.Baseline

	// steer is the message waiting for the running turn to pick it up. It is
	// not part of the document: a message that outlives the turn it was typed
	// against is sent as an ordinary one instead.
	steer *Steer

	caps llm.Caps
	// usageSince is the sequence number of the first entry this model could
	// have measured. A report an earlier model made is no measure of this
	// model's context, so it anchors nothing.
	usageSince int
	// rulesNote is the last warning that the project's rules are not in the
	// prompt, so a file that is still too large is not named again on every
	// turn. It is not in the document: a restart re-warns once.
	rulesNote string
	// prepared reports that the model has been read: its window and its chat
	// template are known and a request can be shaped for it.
	prepared bool
	// approved is a plan the user approved during the running turn, which the
	// turn switches to execute mode on once its tool calls are answered.
	approved *store.Plan

	// seen is what this session has read, which write and edit check a file
	// against. It is not in the document: after a restart a file is read again
	// before it is changed.
	seen *tools.Seen

	// waiter receives the result of a subagent's first turn when the call that
	// launched it waits for one. launches holds the subagents a turn started
	// ahead of the calls that wait on them.
	waiter   chan string
	launches map[string]*launch

	// live is the reply the request under way has streamed so far. It is nil
	// outside a request and cleared in the same hold that appends the entry
	// the reply becomes.
	live *Reply

	state State
	// announced is the last state published for a subagent run, which is how
	// its launcher's listing knows a card has closed.
	announced State
	cancel    context.CancelFunc
	turnSeq   int
	// gone is set when the session has been deleted, so nothing still in
	// flight writes its file again.
	gone bool
	// dirty is a change the file does not hold yet, durable asks for the next
	// write to be flushed to the disk, and saving is true while the writer
	// runs. idle is signalled each time the writer stops.
	dirty, durable, saving bool
	idle                   *sync.Cond
}

// repoint moves the session to an agent and a model and forgets everything the
// previous model's tokenizer implied. The transcript says nothing about it: the
// chip already shows which agent a session is on. The caller holds the lock.
func (s *Session) repoint(agentID, model string) {
	s.AgentID = agentID
	s.Model = model
	s.forgetModel()
	s.ContextBase = nil
	// What the previous model reported is no measure of this one's context.
	s.usageSince = s.turnSeq + 1
}

// forgetModel drops what was read from the model server, so the next turn
// reads it again. The caller holds the lock.
func (s *Session) forgetModel() {
	s.NCtx, s.NCtxSource = 0, ""
	s.caps = llm.Caps{}
	s.prepared = false
}

// forget marks the session deleted, so nothing still running writes it back.
func (s *Session) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gone = true
}

// reopen marks the model unread, so that the next turn reads it again. A turn
// under way is left alone: it has the model it is talking to.
func (s *Session) reopen() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateIdle {
		s.prepared = false
	}
}

func newSession(id, project, agentID, model string, set config.Toolset) *Session {
	now := time.Now().UTC()
	return &Session{
		ID: id, Project: project, AgentID: agentID, Model: model,
		Mode: config.WorkExecute, Toolset: set, Created: now, Updated: now,
		state: StateIdle, seen: tools.NewSeen(),
	}
}

func fromDoc(d *store.Session) *Session {
	return &Session{
		ID: d.ID, Project: d.Project, AgentID: d.Agent, Parent: d.Parent, Model: d.Model,
		Mode: d.Mode, Toolset: d.Toolset, Plan: d.Plan, Draft: d.Draft,
		Created: d.Created, Updated: d.Updated, Title: d.Title, Named: d.Named,
		Entries:     append([]store.Entry(nil), d.Entries...),
		NCtx:        d.NCtx,
		NCtxSource:  d.NCtxSource,
		ContextBase: d.ContextBase,
		Compactions: d.Compactions,
		Todos:       append([]store.Todo(nil), d.Todos...),
		Baselines:   append([]store.Baseline(nil), d.Baselines...),
		state:       StateIdle,
		seen:        tools.NewSeen(),
	}
}

// doc renders the on-disk shape. The caller holds the session lock.
// copyPlan copies a plan's fields, which write_plan replaces in place. The
// lists are only ever replaced whole, so they are shared.
func copyPlan(p *store.Plan) *store.Plan {
	if p == nil {
		return nil
	}
	held := *p
	return &held
}

func (s *Session) doc() *store.Session {
	// The entries are copied, not shared: a save marshals this after the lock
	// is dropped, so a field a live turn still mutates under the lock must not
	// be read out from underneath it.
	entries := make([]store.Entry, len(s.Entries))
	for i, e := range s.Entries {
		if e.Meta != nil {
			meta := make(map[string]any, len(e.Meta))
			for k, v := range e.Meta {
				meta[k] = v
			}
			e.Meta = meta
		}
		if e.Usage != nil {
			held := *e.Usage
			e.Usage = &held
		}
		entries[i] = e
	}
	return &store.Session{
		ID: s.ID, Project: s.Project, Agent: s.AgentID, Parent: s.Parent,
		Model: s.Model, Mode: s.Mode, Toolset: s.Toolset, Plan: copyPlan(s.Plan), Draft: copyPlan(s.Draft), Title: s.Title, Named: s.Named,
		Created: s.Created, Updated: s.Updated,
		NCtx: s.NCtx, NCtxSource: s.NCtxSource,
		Compactions: s.Compactions, ContextBase: s.ContextBase,
		Todos:     append([]store.Todo(nil), s.Todos...),
		Baselines: append([]store.Baseline(nil), s.Baselines...),
		Entries:   entries,
	}
}

// takeLaunch hands over the subagent started ahead for a call, if any.
func (s *Session) takeLaunch(callID string) *launch {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.launches[callID]
	delete(s.launches, callID)
	return l
}

// lastAnnounced is the state last published for the session.
func (s *Session) lastAnnounced() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.announced
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Snapshot copies what a handler needs without holding the lock afterwards.
func (s *Session) Snapshot() (*store.Session, State, int, llm.Caps) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc(), s.state, s.computeFill(), s.caps
}

// Reply is what a request has streamed before it lands as an entry.
type Reply struct {
	Content   string      `json:"content"`
	Reasoning string      `json:"reasoning"`
	ToolCalls []ReplyCall `json:"tool_calls"`
	// Since is when the request was sent, which a page opened part way
	// through the wait counts from.
	Since time.Time `json:"since"`
}

// ReplyCall is one tool call as far as its fragments have arrived. Fragments
// join on index, since the id may be empty until the call is nearly whole.
type ReplyCall struct {
	Index     int    `json:"index"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// add folds one streamed event into the reply.
func (r *Reply) add(event llm.Event) {
	switch event.Kind {
	case llm.EventContent:
		r.Content += event.Text
	case llm.EventReasoning:
		r.Reasoning += event.Text
	case llm.EventToolCall:
		var call *ReplyCall
		for i := range r.ToolCalls {
			if r.ToolCalls[i].Index == event.Index {
				call = &r.ToolCalls[i]
			}
		}
		if call == nil {
			r.ToolCalls = append(r.ToolCalls, ReplyCall{Index: event.Index})
			call = &r.ToolCalls[len(r.ToolCalls)-1]
		}
		if call.ID == "" {
			call.ID = event.ID
		}
		if event.Name != "" {
			call.Name = event.Name
		}
		call.Arguments += event.Arguments
	}
}

// View is a session as a browser opening it needs it: the document, the reply
// still streaming, and the mark of the last event already sent.
type View struct {
	Doc   *store.Session
	State State
	Fill  int
	Reply *Reply
	Seq   int64
}

// View reads the session and takes the mark under one hold of the lock.
// Streamed text is published under the same lock, so a stream opened from
// the mark carries on exactly where the reply leaves off.
func (s *Session) View(mark func() int64) View {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Doc: s.doc(), State: s.state, Fill: s.computeFill(), Seq: mark()}
	if s.live != nil {
		held := *s.live
		held.ToolCalls = append([]ReplyCall(nil), s.live.ToolCalls...)
		v.Reply = &held
	}
	return v
}

// Compactable reports whether a compaction asked for now would be taken.
func (s *Session) Compactable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.compactable()
}

// sender is who a message is from: the name a person reads, and the session a
// reply is addressed to. Both empty is a person.
type sender struct {
	Name    string
	Session string
}

// Steer is a message typed while a turn was running. There is one per
// session: a second joins the first, since the model reads them as one turn
// either way.
type Steer struct {
	Text        string
	Attachments []store.Attachment
	// From is the agent that sent it, empty for the person. Two messages from
	// different senders join with no sender, since the joined text names each
	// of them already.
	From sender
}

// Steer is the message waiting for the running turn, or nil.
func (s *Session) Steer() *Steer {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.steer == nil {
		return nil
	}
	held := *s.steer
	held.Attachments = append([]store.Attachment(nil), s.steer.Attachments...)
	return &held
}

// queue adds a message to the one waiting, on a line of its own. A set of
// attachments that would
// take the joined message over the cap is refused whole, so the queue is
// never left holding half of what was typed. The caller holds the lock.
func (s *Session) queue(from sender, text string, attachments []store.Attachment) error {
	next := &Steer{Text: text, Attachments: attachments, From: from}
	if s.steer != nil {
		next.Text = joinMessages(s.steer.Text, text)
		next.Attachments = append(append([]store.Attachment(nil), s.steer.Attachments...), attachments...)
		if s.steer.From != from {
			next.From = sender{}
		}
	}
	if len(next.Attachments) > maxAttachments {
		return fmt.Errorf("Attach %d files: at most %d fit in one message", len(next.Attachments), maxAttachments)
	}
	s.steer = next
	return nil
}

// joinMessages puts two messages on their own lines.
func joinMessages(first, second string) string {
	if first == "" {
		return second
	}
	if second == "" {
		return first
	}
	return first + "\n" + second
}

// appendEntry numbers and stamps one entry. The caller holds the lock.
func (s *Session) appendEntry(e store.Entry) store.Entry {
	s.turnSeq++
	e.Seq = s.turnSeq
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	s.Entries = append(s.Entries, e)
	s.Updated = e.At
	return e
}

// firstTurn reports that one user message has been sent and no title was
// typed, which is when a title is worth asking the model for. The caller holds
// the lock.
func (s *Session) firstTurn() bool {
	if s.Named {
		return false
	}
	users := 0
	for _, e := range s.Entries {
		if e.Kind == "user" && e.Meta["continue"] != true {
			users++
		}
	}
	return users == 1
}

// firstUserMessage is the text the fallback title is cut from. The caller
// holds the lock.
func (s *Session) firstUserMessage() string {
	for _, e := range s.Entries {
		if e.Kind == "user" {
			return e.Content
		}
	}
	return ""
}

// lastUserEntry is the index of the newest message the user sent, or -1. The
// harness's own nudge to carry on with a plan is not one of them. The caller
// holds the lock.
func (s *Session) lastUserEntry() int {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == "user" && s.Entries[i].Meta["continue"] != true {
			return i
		}
	}
	return -1
}

// rewindTo drops the entry at i and everything after it, then repairs what
// those entries had changed. Sequence numbers are not reused, so an id built
// from one never names two different entries. The caller holds the lock.
func (s *Session) rewindTo(i int) {
	s.Entries = s.Entries[:i:i]
	s.restoreContext()
	s.restoreTodos()
}

// restoreContext rebuilds the in-context set: everything from the newest
// surviving compaction onward, with the kinds that are never sent left out.
// Dropping a compaction therefore gives back what it summarised. The caller
// holds the lock.
func (s *Session) restoreContext() {
	from := 0
	s.Compactions = 0
	for i, e := range s.Entries {
		if e.Kind == "compaction" {
			from = i
			s.Compactions++
		}
	}
	// A compaction keeps the messages it did not summarise, which sit before
	// it in the transcript and start at the entry it names.
	if from > 0 {
		if tail, ok := metaInt(s.Entries[from].Meta["tail"]); ok {
			for i := 0; i < from; i++ {
				if s.Entries[i].Seq == tail {
					from = i
					break
				}
			}
		}
	}
	for i := range s.Entries {
		switch s.Entries[i].Kind {
		case "user", "assistant", "tool", "compaction":
			s.Entries[i].InContext = i >= from
		default:
			s.Entries[i].InContext = false
		}
	}
}

// restoreTodos rewinds the plan to the newest todo_write the surviving
// entries ran. The call carries the whole plan, so nothing is stored for this
// beyond the entries themselves. The caller holds the lock.
func (s *Session) restoreTodos() {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == "tool" && s.Entries[i].ToolName == "exit_plan_mode" && s.Entries[i].Meta["approved"] == true {
			if s.Plan != nil {
				s.Todos = seedTodos(*s.Plan)
				return
			}
			continue
		}
		if s.Entries[i].Kind != "tool" || s.Entries[i].ToolName != "todo_write" {
			continue
		}
		args, ok := s.argumentsOf(i, s.Entries[i].ToolCallID)
		if !ok {
			continue
		}
		// A call the tool refused left the plan alone, so it does not count
		// as the state to go back to.
		todos, err := parseTodos(args)
		if err != nil {
			continue
		}
		s.Todos = todos
		return
	}
	s.Todos = nil
}

// closeInterrupted answers every call of the last assistant entry that has no
// result, and says so in the transcript. A call marked as started may have
// done its work, so its result says to check before running it again. It
// reports whether anything was added. The caller holds no lock and nothing
// else holds the session yet.
func (s *Session) closeInterrupted() bool {
	last := -1
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == "assistant" {
			last = i
			break
		}
	}
	if last < 0 || len(s.Entries[last].ToolCalls) == 0 {
		return false
	}
	answered := map[string]bool{}
	for _, e := range s.Entries[last+1:] {
		if e.Kind == "tool" {
			answered[e.ToolCallID] = true
		}
	}
	started := map[string]bool{}
	marked, _ := s.Entries[last].Meta["started"].([]any)
	for _, id := range marked {
		if id, ok := id.(string); ok {
			started[id] = true
		}
	}
	added := false
	for _, c := range s.Entries[last].ToolCalls {
		if answered[c.ID] {
			continue
		}
		content := prompts.NotRun()
		if started[c.ID] {
			content = prompts.OutcomeUnknown()
		}
		s.appendEntry(store.Entry{Kind: "tool", Role: "tool", ToolCallID: c.ID,
			ToolName: c.Function.Name, Content: content, InContext: true, Visible: true,
			Meta: map[string]any{"error": true}})
		added = true
	}
	if added {
		s.appendEntry(store.Entry{Kind: "status", Content: "Interrupted: the harness stopped while this turn was running",
			InContext: false, Visible: true})
	}
	return added
}

// argumentsOf returns the arguments of the call a tool entry answers. Tool
// entries follow their assistant with only sibling results between, so the
// first assistant above the entry is the one that made the call. The caller
// holds the lock.
func (s *Session) argumentsOf(at int, id string) (json.RawMessage, bool) {
	for i := at - 1; i >= 0; i-- {
		if s.Entries[i].Kind != "assistant" {
			continue
		}
		for _, c := range s.Entries[i].ToolCalls {
			if c.ID == id {
				return json.RawMessage(c.Function.Arguments), true
			}
		}
		return nil, false
	}
	return nil, false
}

// imageRefs lists the file ids the in-context messages need, so the bytes are
// read before the lock is taken rather than under it.
func (s *Session) imageRefs() []string {
	var out []string
	for _, e := range s.Entries {
		if !e.InContext {
			continue
		}
		for _, a := range e.Attachments {
			if a.File != "" {
				out = append(out, a.File)
			}
		}
	}
	return out
}

// systemMessage is the system prompt as this agent renders it. Everything that
// sends one goes through here, so a turn, a compaction and the estimate of the
// fixed context all carry the same text. The caller holds the lock.
func (s *Session) systemMessage(a config.Agent, osName, date string, b brief) llm.Message {
	names := tools.Names(b.offer)
	content := prompts.System(prompts.SystemVars{
		Project: s.Project, OS: osName, Date: date,
		CanAsk: b.offer.Ask, CanMessage: b.offer.Message, CanSearch: b.offer.Search,
		Agents: b.agents, Launcher: b.launcher,
		Rules: b.rules, RulesName: b.rulesName,
		Instructions: a.Instructions,
		Tools:        names,
		NotAllTools:  len(names) < tools.AllCount(),
	})
	return llm.Message{Role: "system", Content: content}
}

// contextMessages is the system prompt followed by every in-context entry,
// with the invariants the wire format requires repaired. images maps a file
// id to the part built from it.
func (s *Session) contextMessages(a config.Agent, osName, date string, b brief, images map[string]llm.Part) []llm.Message {
	return s.contextMessagesUntil(a, osName, date, b, images, len(s.Entries))
}

// contextMessagesUntil is contextMessages over the entries before end. The
// summary goes first, ahead of the messages it kept word for word, which sit
// before it in the transcript. The caller holds the lock.
func (s *Session) contextMessagesUntil(a config.Agent, osName, date string, b brief, images map[string]llm.Part, end int) []llm.Message {
	msgs := []llm.Message{s.systemMessage(a, osName, date, b)}
	summary := -1
	for i := 0; i < end; i++ {
		if s.Entries[i].Kind == "compaction" && s.Entries[i].InContext {
			summary = i
		}
	}
	// mode is the mode the messages so far were sent in. A message sent in
	// another carries that mode's rules in front of it, so the rules enter the
	// history where the mode changed and the head of the prompt never moves.
	mode := config.WorkExecute
	if summary >= 0 {
		e := s.Entries[summary]
		todos, _ := e.Meta["todos"].(string)
		carry, _ := e.Meta["carry"].(string)
		last, _ := e.Meta["last_user"].(string)
		_, kept := e.Meta["tail"]
		mode = entryMode(e)
		msgs = append(msgs, llm.Message{Role: "user", Content: prompts.Compaction(prompts.CompactionVars{
			Summary: e.Content, Todos: todos, Carry: carry, LastUser: last, Kept: kept})})
	}
	for i := 0; i < end; i++ {
		e := s.Entries[i]
		if !e.InContext {
			continue
		}
		switch e.Kind {
		case "user":
			switched := ""
			if m := entryMode(e); e.Meta["kickoff"] == true {
				mode = m
			} else if m != mode {
				switched, mode = modeRules(m, b), m
			}
			msgs = append(msgs, userMessage(e, images, switched))
		case "compaction":
			// Sent above, ahead of the messages it kept.
		case "assistant":
			// Thinking from an earlier turn goes back only when the agent asks
			// for it, whatever the protocol does with the field afterwards.
			reasoning := ""
			if a.PreserveReasoning {
				reasoning = e.Reasoning
			}
			msgs = append(msgs, llm.Message{Role: "assistant", Content: e.Content,
				ReasoningContent: reasoning, ToolCalls: e.ToolCalls})
			msgs = append(msgs, repairToolResults(s.Entries, i, e.ToolCalls, images)...)
		case "tool":
			// Tool results are emitted beside their assistant, so a tool
			// message that lost its assistant is dropped rather than sent.
		}
	}
	return msgs
}

// modeAt is the mode the messages a compaction summarises were sent in: that
// of the latest message before the ones it keeps. The caller holds the lock.
func (s *Session) modeAt(tailSeq int, kept bool) config.WorkMode {
	mode := config.WorkExecute
	for _, e := range s.Entries {
		if kept && e.Seq >= tailSeq {
			break
		}
		if e.Kind == "user" {
			mode = entryMode(e)
		}
	}
	return mode
}

// carryFor is what a compaction carries past the messages it summarises:
// plan mode's rules while in it, or the approved plan when the message that
// approved it is not among the ones kept. The caller holds the lock.
func (s *Session) carryFor(mode config.WorkMode, b brief, tailSeq int, kept bool) string {
	if mode == config.WorkPlan {
		return prompts.ModePlan(b.offer.Ask, b.offer.Search)
	}
	if s.Plan == nil {
		return ""
	}
	for _, e := range s.Entries {
		if kept && e.Seq >= tailSeq && e.Meta["kickoff"] == true {
			return ""
		}
	}
	return prompts.PlanCarried(renderPlan(*s.Plan))
}

// entryMode is the mode an entry was written in, execute when it names none.
func entryMode(e store.Entry) config.WorkMode {
	if m, _ := e.Meta["mode"].(string); m == string(config.WorkPlan) {
		return config.WorkPlan
	}
	return config.WorkExecute
}

// modeRules is what the first message sent in a mode carries in front of it.
func modeRules(m config.WorkMode, b brief) string {
	if m == config.WorkPlan {
		return prompts.ModePlan(b.offer.Ask, b.offer.Search)
	}
	return prompts.ModeExecute()
}

// userMessage is the message as typed, with each attached text file under it
// and each image beside it, and rules for a mode it was the first sent in
// ahead of it. A message with an image is an array of parts, which is the only
// shape that carries one.
func userMessage(e store.Entry, images map[string]llm.Part, rules string) llm.Message {
	var b strings.Builder
	if rules != "" {
		b.WriteString(rules)
		b.WriteString("\n\n")
	}
	// A message from a person and one from another agent arrive on the same
	// role, so the sender is named in the text or the model cannot tell them
	// apart.
	if e.From != "" {
		b.WriteString(prompts.Message(e.From, e.FromSession, e.Content))
	} else {
		b.WriteString(e.Content)
	}
	var parts []llm.Part
	for _, a := range e.Attachments {
		if a.Kind == "image" {
			// The picture goes as a part, so the model sees it. A path is not
			// said: a paste lives outside the project the tools can read, and
			// the picture itself is already in the message.
			if part, ok := images[a.File]; ok {
				parts = append(parts, part)
			}
			continue
		}
		b.WriteString("\n\n")
		b.WriteString(prompts.Attachment(a.Path, a.Content))
	}
	if len(parts) == 0 {
		return llm.Message{Role: "user", Content: b.String()}
	}
	return llm.Message{Role: "user", Parts: withText(b.String(), parts)}
}

// withText puts the text ahead of the pictures. Empty text is left out rather
// than sent as an empty part, which a server may refuse.
func withText(text string, parts []llm.Part) []llm.Part {
	if text == "" {
		return parts
	}
	return append([]llm.Part{llm.TextPart(text)}, parts...)
}

// repairToolResults returns one tool message per call, synthesising the ones
// a crash left unanswered so the history stays valid.
func repairToolResults(entries []store.Entry, at int, calls []llm.ToolCall, images map[string]llm.Part) []llm.Message {
	answered := map[string]store.Entry{}
	for j := at + 1; j < len(entries); j++ {
		if entries[j].Kind == "assistant" {
			break
		}
		if entries[j].Kind == "tool" && entries[j].InContext {
			answered[entries[j].ToolCallID] = entries[j]
		}
	}
	out := make([]llm.Message, 0, len(calls))
	for _, c := range calls {
		if e, ok := answered[c.ID]; ok {
			out = append(out, toolMessage(c.ID, e, images))
			continue
		}
		out = append(out, llm.Message{Role: "tool", ToolCallID: c.ID, Name: c.Function.Name,
			Content: prompts.NotRun()})
	}
	return out
}

// toolMessage is one result as the model reads it. A result with a picture is
// an array of parts, its text first; a picture whose file has gone is left
// out, and the text still names it.
func toolMessage(id string, e store.Entry, images map[string]llm.Part) llm.Message {
	m := llm.Message{Role: "tool", ToolCallID: id, Name: e.ToolName, Content: e.Content}
	var parts []llm.Part
	for _, a := range e.Attachments {
		if part, ok := images[a.File]; ok && a.Kind == "image" {
			parts = append(parts, part)
		}
	}
	if len(parts) > 0 {
		m.Parts = withText(e.Content, parts)
	}
	return m
}

// charsPerToken is the rough ratio a token cost is estimated at wherever a
// usage report does not cover it.
const charsPerToken = 4

// lastMeasured is the newest call that measured what is in context now, with
// its entry index, or -1 where no such call is kept. A report the previous
// model made measures that model's context, so it counts only from the entry
// this model starts at. The caller holds the lock.
func (s *Session) lastMeasured() (int, *store.CallUsage) {
	i, u := store.LastMeasured(s.Entries)
	if u != nil && s.Entries[i].Seq < s.usageSince {
		return -1, nil
	}
	return i, u
}

// computeFill estimates tokens currently in context. The last call that
// reported counts is authoritative up to the reply it produced; entries added
// since are estimated at charsPerToken characters per token. With no such
// call, the fixed parts of the prompt are added where they are known.
func (s *Session) computeFill() int {
	i, u := s.lastMeasured()
	if u == nil {
		fill := s.estimateChars(-1) / charsPerToken
		if b := s.ContextBase; b != nil {
			fill += b.System + b.Rules + b.Tools
		}
		return fill
	}
	return u.Prompt + u.Completion + s.estimateChars(i)/charsPerToken
}

// compactFloor is the fewest characters since the last summary that a
// compaction takes. The summary may run to 600 words, so compacting less than
// this would not shrink the context.
const compactFloor = 4000

// tailChars and tailShare bound what a compaction keeps word for word rather
// than summarising: the smaller of a fixed size and a share of the context.
// Everything kept is read again after the summary.
const (
	tailChars = 16000
	tailShare = 0.1
)

// compactable reports whether enough has been said since the last summary to
// be worth summarising again. The messages the last one kept count, since
// the next one summarises them.
func (s *Session) compactable() bool {
	total := 0
	for _, e := range s.Entries {
		if e.InContext && e.Kind != "compaction" {
			total += entryChars(e)
		}
	}
	return total >= compactFloor
}

// tailStart is where the messages a compaction keeps begin: the earliest
// assistant entry whose run to the end fits in the share. Starting on an
// assistant keeps each call beside its results, and puts a reply rather than
// a second user message after the summary. It is len(s.Entries) when nothing
// fits. The caller holds the lock.
func (s *Session) tailStart() int {
	budget := min(tailChars, int(tailShare*float64(s.estimateChars(-1))))
	start, size := len(s.Entries), 0
	for i := len(s.Entries) - 1; i >= 0; i-- {
		e := s.Entries[i]
		if e.Kind == "compaction" && e.InContext {
			break
		}
		if !e.InContext {
			continue
		}
		size += entryChars(e)
		if size > budget {
			break
		}
		if e.Kind == "assistant" {
			start = i
		}
	}
	return start
}

// estimateChars sums the in-context characters after index from, or over the
// whole transcript when from is negative.
func (s *Session) estimateChars(from int) int {
	total := 0
	for i, e := range s.Entries {
		if i <= from || !e.InContext {
			continue
		}
		total += entryChars(e)
	}
	return total
}

// entryChars is the size of one entry as the prompt carries it.
func entryChars(e store.Entry) int {
	total := len(e.Content) + len(e.Reasoning)
	if todos, ok := e.Meta["todos"].(string); ok && e.Kind == "compaction" {
		total += len(todos)
	}
	if len(e.ToolCalls) > 0 {
		if b, err := json.Marshal(e.ToolCalls); err == nil {
			total += len(b)
		}
	}
	return total
}

// metaInt reads a number kept in an entry's meta, which is an int in memory
// and a float64 once the session has been read back from disk.
func metaInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	}
	return 0, false
}

// window is the size this session's context is measured against: the agent's
// override, else what the model server reported. Zero is a window nothing
// named, which is no limit. The caller holds the lock.
func (s *Session) window(a config.Agent) (int, string) {
	return a.Window(s.NCtx, s.NCtxSource)
}

// Breakdown splits the context into the four parts a reader can act on. The
// fixed parts are estimated and Messages is the remainder of a real usage
// report, so a session with no usage yet shows no breakdown at all.
type Breakdown struct {
	System   int `json:"system"`
	Rules    int `json:"rules"`
	Tools    int `json:"tools"`
	Messages int `json:"messages"`
}

// Breakdown is breakdown under the lock, for a handler.
func (s *Session) Breakdown() *Breakdown {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.breakdown()
}

// breakdown subtracts the fixed parts from the last call that reported counts.
// Messages is the remainder, which is the only part that came from the model
// server rather than from counting characters. The caller holds the lock.
func (s *Session) breakdown() *Breakdown {
	_, u := s.lastMeasured()
	if u == nil || s.ContextBase == nil {
		return nil
	}
	b := Breakdown{System: s.ContextBase.System, Rules: s.ContextBase.Rules, Tools: u.ToolTokens}
	b.Messages = u.Prompt + u.Completion - b.System - b.Rules - b.Tools
	if b.Messages < 0 {
		return nil
	}
	return &b
}
