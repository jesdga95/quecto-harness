// Package agent owns the order of operations in a turn and is the only
// package that mutates a session.
package agent

import (
	"context"
	"encoding/json"
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

	NCtx        int
	NCtxSource  string
	LastUsage   *llama.Usage
	Compactions int
	Todos       []store.Todo

	caps           llama.Caps
	lastUsageEntry int
	state          State
	cancel         context.CancelFunc
	turnSeq        int
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
		Entries:     append([]store.Entry(nil), d.Entries...),
		NCtx:        d.NCtx,
		NCtxSource:  d.NCtxSource,
		LastUsage:   d.LastUsage,
		Compactions: d.Compactions,
		Todos:       append([]store.Todo(nil), d.Todos...),
		state:       StateIdle,
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
		Todos:   append([]store.Todo(nil), s.Todos...),
		Entries: append([]store.Entry(nil), s.Entries...),
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

// contextMessages is the system prompt followed by every in-context entry,
// with the invariants the wire format requires repaired.
func (s *Session) contextMessages(osName string) []llama.Message {
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
			msgs = append(msgs, llama.Message{Role: "user", Content: userContent(e)})
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

// userContent is the message as typed, with each attached file under it.
func userContent(e store.Entry) string {
	if len(e.Attachments) == 0 {
		return e.Content
	}
	var b strings.Builder
	b.WriteString(e.Content)
	for _, a := range e.Attachments {
		b.WriteString("\n\n")
		b.WriteString(prompts.Attachment(a.Path, a.Content))
	}
	return b.String()
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
