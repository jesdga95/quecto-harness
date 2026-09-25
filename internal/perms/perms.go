// Package perms decides whether a tool call may run. It never executes one.
// Splitting the decision from the execution is what lets the turn loop block
// on a channel while an HTTP handler answers from another goroutine.
package perms

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"time"

	"quecto-harness/internal/config"
)

type Action string

const (
	ActionAllow  Action = "allow"
	ActionPrompt Action = "prompt"
)

type DecisionKind string

const (
	DecideOnce   DecisionKind = "once"
	DecideAlways DecisionKind = "always"
	DecideDeny   DecisionKind = "deny"
)

var (
	ErrNoSuchRequest   = errors.New("That decision is no longer pending: the turn moved on")
	ErrSessionCanceled = errors.New("the session was stopped")
)

// Request is one pending question about one tool call.
type Request struct {
	ID string `json:"id"`
	// SessionID is the session blocked on this and Route is the one drawing
	// the card, which differ only when a subagent asked. Agent names who
	// asked, for the label.
	SessionID string    `json:"session"`
	Route     string    `json:"route,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Project   string    `json:"project"`
	Tool      string    `json:"tool"`
	Command   string    `json:"command,omitempty"`
	Path      string    `json:"path,omitempty"`
	Prefix    string    `json:"prefix"`
	Args      string    `json:"args"`
	Created   time.Time `json:"created"`
}

// route is where the card is drawn. A request that names none is drawn in the
// session it blocks.
func (r Request) route() string {
	if r.Route != "" {
		return r.Route
	}
	return r.SessionID
}

// Decision is the answer. Prefix is used when Kind is always, Reason when it
// is deny.
type Decision struct {
	Kind   DecisionKind `json:"decision"`
	Prefix string       `json:"prefix,omitempty"`
	Reason string       `json:"reason,omitempty"`
	// Note carries a problem that did not change the outcome, such as an
	// allowlist that could not be saved.
	Note string `json:"-"`
}

// Broker turns a policy question into a pending request and blocks until the
// UI answers or the session is cancelled.
type Broker struct {
	cfg  *config.Store
	emit func(sessionID string, r Request)

	mu      sync.Mutex
	wait    map[string]chan Decision
	pending map[string]Request
	// byS is keyed on the session blocked on a request and byRoute on the one
	// drawing it, so a card is cancelled by the first and replayed by the
	// second.
	byS     map[string][]string
	byRoute map[string][]string
}

func New(cfg *config.Store, emit func(string, Request)) *Broker {
	return &Broker{
		cfg:     cfg,
		emit:    emit,
		wait:    map[string]chan Decision{},
		pending: map[string]Request{},
		byS:     map[string][]string{},
		byRoute: map[string][]string{},
	}
}

// Evaluate applies the static policy. It never blocks and never asks. ModeFull
// is answered before the tool name is read, so nothing about the call changes
// the answer.
func (b *Broker) Evaluate(project, tool, command string, mode config.Mode) Action {
	if mode == config.ModeFull {
		return ActionAllow
	}
	auto := mode == config.ModeAllowlist
	switch tool {
	case "read_file", "find", "write_todos", "ask_question", "submit_plan", "run_agent", "send_message":
		// find reads inside the project and nothing else, on the same guard
		// read_file uses. write_todos changes the session's own plan and
		// touches nothing on the machine, so there is nothing to decide.
		// ask_question and submit_plan put their own card to the user, and a
		// card in front of either would ask twice. run_agent and send_message touch nothing on the
		// machine; what the agent they reach then does draws its own card.
		return ActionAllow
	case "write_file", "replace_in_file":
		if auto {
			return ActionAllow
		}
		return ActionPrompt
	case "web_search":
		// The query leaves the machine and the call is billed, so it is shown
		// before it is sent. The allowlist holds shell prefixes and has nothing
		// to say about a query, so allowlist mode allows it outright.
		if auto {
			return ActionAllow
		}
		return ActionPrompt
	case "run_shell":
		if !auto {
			return ActionPrompt
		}
		if hasOperator(command) {
			return ActionPrompt
		}
		if _, ok := b.cfg.AllowMatch(project, command); ok {
			return ActionAllow
		}
		return ActionPrompt
	}
	return ActionPrompt
}

// Ask registers a pending request, publishes it and blocks. The channel is
// buffered so Resolve never waits for the asking goroutine to be scheduled.
func (b *Broker) Ask(ctx context.Context, r Request) (Decision, error) {
	if r.ID == "" {
		r.ID = NewRequestID()
	}
	if r.Created.IsZero() {
		r.Created = time.Now()
	}
	ch := make(chan Decision, 1)
	b.mu.Lock()
	b.wait[r.ID] = ch
	b.pending[r.ID] = r
	b.byS[r.SessionID] = append(b.byS[r.SessionID], r.ID)
	b.byRoute[r.route()] = append(b.byRoute[r.route()], r.ID)
	b.mu.Unlock()

	if b.emit != nil {
		b.emit(r.route(), r)
	}

	select {
	case <-ctx.Done():
		b.forget(r.ID, r.SessionID)
		return Decision{}, ctx.Err()
	case d, ok := <-ch:
		if !ok {
			return Decision{}, ErrSessionCanceled
		}
		return d, nil
	}
}

// Resolve answers a pending request. An unknown id is what a second click on
// the card produces.
func (b *Broker) Resolve(id string, d Decision) error {
	b.mu.Lock()
	ch, ok := b.wait[id]
	req := b.pending[id]
	if ok {
		delete(b.wait, id)
		delete(b.pending, id)
		b.dropIDLocked(req, id)
	}
	b.mu.Unlock()
	if !ok {
		return ErrNoSuchRequest
	}
	if d.Kind == DecideAlways {
		if err := b.cfg.AddAllow(req.Project, d.Prefix); err != nil {
			d.Kind = DecideOnce
			d.Note = "save allowlist: " + err.Error() + ": the command ran, but the rule was not kept"
		}
	}
	ch <- d
	return nil
}

// Lookup returns a pending request, which is how a handler checks that a
// submitted prefix matches the command being decided.
func (b *Broker) Lookup(id string) (Request, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.pending[id]
	return r, ok
}

// Pending lists the unanswered requests a session draws, for replay after a
// browser reconnect. A subagent's card is among its root's.
func (b *Broker) Pending(sessionID string) []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Request
	for _, id := range b.byRoute[sessionID] {
		if r, ok := b.pending[id]; ok {
			out = append(out, r)
		}
	}
	return out
}

func (b *Broker) CancelSession(sessionID string) {
	b.mu.Lock()
	ids := b.byS[sessionID]
	delete(b.byS, sessionID)
	var chans []chan Decision
	for _, id := range ids {
		if ch, ok := b.wait[id]; ok {
			chans = append(chans, ch)
			delete(b.wait, id)
			b.dropRouteLocked(b.pending[id], id)
			delete(b.pending, id)
		}
	}
	b.mu.Unlock()
	for _, ch := range chans {
		close(ch)
	}
}

func (b *Broker) CancelAll() {
	b.mu.Lock()
	var chans []chan Decision
	for id, ch := range b.wait {
		chans = append(chans, ch)
		delete(b.wait, id)
		delete(b.pending, id)
	}
	b.byS = map[string][]string{}
	b.byRoute = map[string][]string{}
	b.mu.Unlock()
	for _, ch := range chans {
		close(ch)
	}
}

func (b *Broker) forget(id, sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	req := b.pending[id]
	delete(b.wait, id)
	delete(b.pending, id)
	if req.SessionID == "" {
		req.SessionID = sessionID
	}
	b.dropIDLocked(req, id)
}

// dropIDLocked takes a request out of both indexes. The caller holds the lock.
func (b *Broker) dropIDLocked(r Request, id string) {
	b.byS[r.SessionID] = without(b.byS[r.SessionID], id)
	b.dropRouteLocked(r, id)
}

func (b *Broker) dropRouteLocked(r Request, id string) {
	b.byRoute[r.route()] = without(b.byRoute[r.route()], id)
}

func without(ids []string, id string) []string {
	for i, v := range ids {
		if v == id {
			return append(ids[:i:i], ids[i+1:]...)
		}
	}
	return ids
}

// operators are the shell metacharacters that let one approved command start
// another, or escape the process group the timeout relies on.
var operators = []string{";", "&", "|", ">", "<", "`", "$(", "\n", "\r"}

func hasOperator(cmd string) bool {
	for _, op := range operators {
		if strings.Contains(cmd, op) {
			return true
		}
	}
	return false
}

// DefaultPrefix is the first two whitespace-separated fields of the command.
func DefaultPrefix(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) > 2 {
		fields = fields[:2]
	}
	return strings.Join(fields, " ")
}

// DenialContent is what the model is told about a refused call.
func DenialContent(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "denied by user"
	}
	return "denied by user: " + reason
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func NewRequestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "perm_" + idEncoding.EncodeToString(b)[:12]
}
