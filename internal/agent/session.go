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

	"quecto-harness/internal/llama"
	"quecto-harness/internal/prompts"
	"quecto-harness/internal/store"
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

// assumedNCtx is the last resort when nothing reported a context size.
// Automatic compaction stays armed against it and the meter marks it a guess.
const assumedNCtx = 32768

// Session is one conversation with one model in one project. It is the
// in-memory authority; store holds the same data on disk.
type Session struct {
	mu sync.Mutex

	ID      string
	Project string
	Model   string
	Created time.Time
	Updated time.Time
	Title   string
	Named   bool

	Entries []store.Entry

	NCtx       int
	NCtxSource string
	LastUsage  *llama.Usage
	// LastToolTokens is the tool schema cost inside LastUsage. A turn that
	// spent its call budget sends no tools, and the breakdown says so rather
	// than charging for schemas the prompt did not carry.
	LastToolTokens int
	ContextBase    *store.ContextBase
	Compactions    int
	Todos          []store.Todo
	Baselines      []store.Baseline

	// steer is the message waiting for the running turn to pick it up. It is
	// not part of the document: a message that outlives the turn it was typed
	// against is sent as an ordinary one instead.
	steer *Steer

	caps           llama.Caps
	lastUsageEntry int
	// lastSentTools reports whether the request behind LastUsage carried the
	// tool schemas. A turn that spent its call budget sends none.
	lastSentTools bool
	// baseProbed stops the measurement being retried every turn against a
	// server that will not answer it.
	baseProbed bool
	// prepared reports that the model has been read: its window and its chat
	// template are known and a request can be shaped for it.
	prepared bool

	state   State
	cancel  context.CancelFunc
	turnSeq int
}

// repoint moves the session to a model and forgets everything the previous
// model's tokenizer implied. It returns the entry that records the change,
// which is visible so the transcript shows where it happened and out of
// context so the model is never told about it. The caller holds the lock.
func (s *Session) repoint(model string) store.Entry {
	s.Model = model
	s.NCtx, s.NCtxSource = 0, ""
	s.caps = llama.Caps{}
	s.prepared = false
	s.LastUsage = nil
	s.LastToolTokens = 0
	s.lastSentTools = false
	s.ContextBase = nil
	s.baseProbed = false
	s.lastUsageEntry = -1
	return s.appendEntry(store.Entry{Kind: "status", Content: "Model changed to " + model,
		InContext: false, Visible: true})
}

func newSession(id, project, model string) *Session {
	now := time.Now().UTC()
	return &Session{
		ID: id, Project: project, Model: model,
		Created: now, Updated: now,
		state: StateIdle, lastUsageEntry: -1,
	}
}

func fromDoc(d *store.Session) *Session {
	return &Session{
		ID: d.ID, Project: d.Project, Model: d.Model,
		Created: d.Created, Updated: d.Updated, Title: d.Title, Named: d.Named,
		Entries:        append([]store.Entry(nil), d.Entries...),
		NCtx:           d.NCtx,
		NCtxSource:     d.NCtxSource,
		LastUsage:      d.LastUsage,
		LastToolTokens: d.LastToolTokens,
		ContextBase:    d.ContextBase,
		Compactions:    d.Compactions,
		Todos:          append([]store.Todo(nil), d.Todos...),
		Baselines:      append([]store.Baseline(nil), d.Baselines...),
		state:          StateIdle,
		// A reloaded session has no usage anchor, so the next fill is
		// estimated from the entries rather than trusted from a past turn.
		lastUsageEntry: -1,
	}
}

// doc renders the on-disk shape. The caller holds the session lock.
func (s *Session) doc() *store.Session {
	return &store.Session{
		ID: s.ID, Project: s.Project, Model: s.Model, Title: s.Title, Named: s.Named,
		Created: s.Created, Updated: s.Updated,
		NCtx: s.NCtx, NCtxSource: s.NCtxSource,
		Compactions: s.Compactions, LastUsage: s.LastUsage,
		LastToolTokens: s.LastToolTokens, ContextBase: s.ContextBase,
		Todos:     append([]store.Todo(nil), s.Todos...),
		Baselines: append([]store.Baseline(nil), s.Baselines...),
		Entries:   append([]store.Entry(nil), s.Entries...),
	}
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Snapshot copies what a handler needs without holding the lock afterwards.
func (s *Session) Snapshot() (*store.Session, State, int, llama.Caps) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc(), s.state, s.computeFill(), s.caps
}

// Steer is a message typed while a turn was running. There is one per
// session: a second joins the first, since the model reads them as one turn
// either way.
type Steer struct {
	Text        string
	Attachments []store.Attachment
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
func (s *Session) queue(text string, attachments []store.Attachment) error {
	next := &Steer{Text: text, Attachments: attachments}
	if s.steer != nil {
		next.Text = joinMessages(s.steer.Text, text)
		next.Attachments = append(append([]store.Attachment(nil), s.steer.Attachments...), attachments...)
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
		if e.Kind == "user" {
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

// lastUserEntry is the index of the newest message the user sent, or -1.
// The caller holds the lock.
func (s *Session) lastUserEntry() int {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == "user" {
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
	// The anchor pointed into the entries that are gone, so the next fill is
	// estimated from what is left rather than measured against a turn the
	// history no longer contains.
	s.LastUsage = nil
	s.LastToolTokens = 0
	s.lastUsageEntry = -1
	s.lastSentTools = false
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
	for i := range s.Entries {
		switch s.Entries[i].Kind {
		case "user", "assistant", "tool", "compaction":
			s.Entries[i].InContext = i >= from
		default:
			s.Entries[i].InContext = false
		}
	}
}

// restoreTodos rewinds the plan to the newest write_todos the surviving
// entries ran. The call carries the whole plan, so nothing is stored for this
// beyond the entries themselves. The caller holds the lock.
func (s *Session) restoreTodos() {
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind != "tool" || s.Entries[i].ToolName != "write_todos" {
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

// contextMessages is the system prompt followed by every in-context entry,
// with the invariants the wire format requires repaired. images maps a file
// id to the part built from it.
func (s *Session) contextMessages(osName string, images map[string]llama.Part) []llama.Message {
	msgs := []llama.Message{{
		Role:    "system",
		Content: prompts.System(prompts.SystemVars{Project: s.Project, OS: osName}),
	}}
	for i := 0; i < len(s.Entries); i++ {
		e := s.Entries[i]
		if !e.InContext {
			continue
		}
		switch e.Kind {
		case "user":
			msgs = append(msgs, userMessage(e, images))
		case "compaction":
			msgs = append(msgs, llama.Message{Role: "user", Content: prompts.Compaction(e.Content)})
		case "assistant":
			msgs = append(msgs, llama.Message{Role: "assistant", Content: e.Content,
				ReasoningContent: e.Reasoning, ToolCalls: e.ToolCalls})
			msgs = append(msgs, repairToolResults(s.Entries, i, e.ToolCalls)...)
		case "tool":
			// Tool results are emitted beside their assistant, so a tool
			// message that lost its assistant is dropped rather than sent.
		}
	}
	return msgs
}

// userMessage is the message as typed, with each attached text file under it
// and each image beside it. A message with an image is an array of parts,
// which is the only shape that carries one.
func userMessage(e store.Entry, images map[string]llama.Part) llama.Message {
	var b strings.Builder
	b.WriteString(e.Content)
	var parts []llama.Part
	for _, a := range e.Attachments {
		if a.Kind == "image" {
			if part, ok := images[a.File]; ok {
				parts = append(parts, part)
				b.WriteString("\n\n")
				b.WriteString(prompts.Image(a.Path))
			}
			continue
		}
		b.WriteString("\n\n")
		b.WriteString(prompts.Attachment(a.Path, a.Content))
	}
	if len(parts) == 0 {
		return llama.Message{Role: "user", Content: b.String()}
	}
	return llama.Message{Role: "user", Parts: append([]llama.Part{llama.TextPart(b.String())}, parts...)}
}

// repairToolResults returns one tool message per call, synthesising the ones
// a crash left unanswered so the history stays valid.
func repairToolResults(entries []store.Entry, at int, calls []llama.ToolCall) []llama.Message {
	answered := map[string]store.Entry{}
	for j := at + 1; j < len(entries); j++ {
		if entries[j].Kind == "assistant" {
			break
		}
		if entries[j].Kind == "tool" && entries[j].InContext {
			answered[entries[j].ToolCallID] = entries[j]
		}
	}
	out := make([]llama.Message, 0, len(calls))
	for _, c := range calls {
		if e, ok := answered[c.ID]; ok {
			out = append(out, llama.Message{Role: "tool", ToolCallID: c.ID, Name: e.ToolName, Content: e.Content})
			continue
		}
		out = append(out, llama.Message{Role: "tool", ToolCallID: c.ID, Name: c.Function.Name,
			Content: "not run: the session was interrupted"})
	}
	return out
}

// computeFill estimates tokens currently in context. The last usage report is
// authoritative up to the response it came from; entries added since are
// estimated at four characters per token.
func (s *Session) computeFill() int {
	if s.LastUsage == nil || s.lastUsageEntry < 0 {
		return s.estimateChars(-1) / 4
	}
	base := s.LastUsage.PromptTokens + s.LastUsage.CompletionTokens
	return base + s.estimateChars(s.lastUsageEntry)/4
}

// estimateChars sums the in-context characters after index from, or over the
// whole transcript when from is negative.
func (s *Session) estimateChars(from int) int {
	total := 0
	for i, e := range s.Entries {
		if i <= from || !e.InContext {
			continue
		}
		total += len(e.Content) + len(e.Reasoning)
		if len(e.ToolCalls) > 0 {
			if b, err := json.Marshal(e.ToolCalls); err == nil {
				total += len(b)
			}
		}
	}
	return total
}

// effectiveNCtx never reports zero, so the meter always has a denominator.
func (s *Session) effectiveNCtx() (int, string) {
	if s.NCtx > 0 {
		return s.NCtx, s.NCtxSource
	}
	return assumedNCtx, "assumed"
}

// Breakdown splits the context into the three parts a reader can act on. It
// is reported only from a measured base and a real usage report, so a session
// with either missing shows no breakdown rather than a guess.
type Breakdown struct {
	System   int `json:"system"`
	Tools    int `json:"tools"`
	Messages int `json:"messages"`
}

// Breakdown is breakdown under the lock, for a handler.
func (s *Session) Breakdown() *Breakdown {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.breakdown()
}

// breakdown subtracts the fixed parts from the last usage report. Messages is
// the remainder, which is exact because every part of it came from the model
// server rather than from counting characters. The caller holds the lock.
func (s *Session) breakdown() *Breakdown {
	if s.LastUsage == nil || s.ContextBase == nil || s.ContextBase.Model != s.Model {
		return nil
	}
	b := Breakdown{System: s.ContextBase.System, Tools: s.LastToolTokens}
	b.Messages = s.LastUsage.PromptTokens + s.LastUsage.CompletionTokens - b.System - b.Tools
	if b.Messages < 0 {
		return nil
	}
	return &b
}
