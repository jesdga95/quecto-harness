package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// titleTimeout and titleTokens bound the naming request, since it stands
// between the message and the first token of the reply. The token cap leaves
// room for a short pass of thinking ahead of the title. titleChars is the most
// of the opening message the request carries.
const (
	titleTimeout = 30 * time.Second
	titleTokens  = 512
	titleChars   = 2000
)

// loadTimeout bounds a model load. It is generous because the work is a large
// file being read into memory on another machine.
const loadTimeout = 10 * time.Minute

// maxAttachments bounds what one message can carry, since every attachment is
// inlined into the prompt.
const maxAttachments = 5

var (
	ErrBusy       = errors.New("The session is busy: stop the current turn first")
	ErrNotFound   = errors.New("No session with that id")
	ErrEmptyText  = errors.New("The message is empty: type something or attach a file")
	ErrNotLast    = errors.New("That is not the last message: only the last one can be sent again")
	ErrNoProject  = errors.New("That folder is not in the project list: add it first")
	ErrNoAgent    = errors.New("No agent is selected: create one in the agents dialog")
	ErrAgentsOff  = errors.New("Every agent is disabled: enable one in the agents dialog")
	ErrAgentOff   = errors.New("The agent is disabled: enable it in the agents dialog")
	ErrNoAddress  = errors.New("The agent has no address: add it in the agents dialog")
	ErrNoModel    = errors.New("The agent has no model: choose one in the agents dialog")
	ErrCompacting = errors.New("The conversation is being compacted: that finishes on its own")
	ErrTooShort   = errors.New("Not enough content to compact: keep working and try again later")
	ErrNoShell    = errors.New("That command is not running in the foreground any more")
)

// Engine owns every live session and dispatches turns.
type Engine struct {
	cfg      *config.Store
	store    *store.Sessions
	files    *store.Files
	backends Backends
	searches Searches
	perms    *perms.Broker
	asks     *asker
	hub      Publisher
	procs    *Registry

	ctx context.Context
	os  string
	// retry is the wait before each retry of a request that failed for a
	// reason that says nothing about the request itself.
	retry []time.Duration

	mu   sync.Mutex
	live map[string]*Session
	wg   sync.WaitGroup
}

func NewEngine(ctx context.Context, cfg *config.Store, st *store.Sessions, files *store.Files,
	backends Backends, searches Searches, pb *perms.Broker, hub Publisher) *Engine {
	e := &Engine{
		cfg: cfg, store: st, files: files, backends: backends, searches: searches,
		perms: pb, asks: newAsker(), hub: hub,
		ctx: ctx, os: runtime.GOOS, live: map[string]*Session{},
		retry: []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second},
	}
	e.procs = NewRegistry(
		func(session string, event Event) { e.publish(session, event) },
		func(session string, text string) {
			s, err := e.Get(session)
			if err != nil {
				return
			}
			if _, err := e.deliverFrom(s, sender{Name: "Background process"}, text, nil); err != nil {
				e.publish(session, ev("error", map[string]any{"kind": "internal", "message": err.Error()}))
			}
		},
	)
	return e
}

// usable reports what stops an agent answering, so a message is refused where
// it was typed rather than part way into a turn.
func usable(a config.Agent) error {
	switch {
	case !a.Enabled:
		return ErrAgentOff
	case a.URL == "":
		return ErrNoAddress
	case a.Model == "":
		return ErrNoModel
	}
	return nil
}

// active is the agent every session answers with, checked before it is used.
func (e *Engine) active() (config.Agent, error) {
	cfg := e.cfg.Config()
	a, ok := cfg.Agent()
	if !ok {
		return config.Agent{}, noAgent(cfg)
	}
	return a, usable(a)
}

// noAgent separates a harness with no agents from one whose agents are all
// disabled, since what the reader does about the two is not the same.
func noAgent(cfg config.Config) error {
	if len(cfg.Agents) > 0 {
		return ErrAgentsOff
	}
	return ErrNoAgent
}

// today is the calendar day the prompt runs on, in the host's zone. It is
// read per request but changes at most once a day, so the system message it
// opens stays token for token across the day and the prefill cache of a
// running conversation survives it. The time of day is left to bash,
// since anything finer would put a new token at the front of every request
// and re-prefill the whole conversation each turn.
func (e *Engine) today() string {
	return time.Now().Format("2006-01-02")
}

// messages builds the prompt for a session. The image bytes are read between
// the two locks, so nothing does file IO while the session is held.
func (e *Engine) messages(s *Session, a config.Agent, b brief) []llm.Message {
	return e.messagesUntil(s, a, b, -1)
}

// messagesUntil is messages over the entries before end, or over all of them
// when end is negative.
func (e *Engine) messagesUntil(s *Session, a config.Agent, b brief, end int) []llm.Message {
	s.mu.Lock()
	refs := s.imageRefs()
	s.mu.Unlock()

	images := map[string]llm.Part{}
	for _, id := range refs {
		raw, err := e.files.Read(id)
		if err != nil {
			// A file that has gone is left out rather than failing the turn:
			// the message still says an image was attached.
			continue
		}
		images[id] = llm.ImagePart(mimeOf(raw), raw)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if end < 0 || end > len(s.Entries) {
		end = len(s.Entries)
	}
	return s.contextMessagesUntil(a, e.os, e.today(), b, images, end)
}

// mimeOf reads the type back off the bytes, so the data URI says what the
// file is rather than what an entry claimed months ago.
func mimeOf(raw []byte) string {
	head := raw
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.SplitN(http.DetectContentType(head), ";", 2)[0]
}

func (e *Engine) publish(sessionID string, event Event) {
	if e.hub != nil {
		e.hub.Publish(sessionID, event)
	}
}

// setState records the state and tells the browser, which drives the spinner
// and the Stop button off this one event.
// setState publishes the state the session moved to. A state it is already in
// is not republished, so a step that sets the same one twice reads as one
// change on the stream.
func (e *Engine) setState(s *Session, st State) {
	s.mu.Lock()
	same := s.state == st
	s.state = st
	s.mu.Unlock()
	if same {
		return
	}
	e.announce(s, st)
}

// announce publishes a state to the session's own stream and, for a subagent
// run, to the launcher watching it. Every state a browser draws goes through
// here, or the launcher is left holding the last one that did.
func (e *Engine) announce(s *Session, st State) {
	e.publish(s.ID, ev("status", map[string]any{"state": string(st)}))
	parent := s.Parent
	if parent == "" {
		return
	}
	s.mu.Lock()
	fill := s.computeFill()
	s.mu.Unlock()
	e.publish(parent, ev("child", map[string]any{"session": s.ID, "state": string(st), "fill": fill}))
	// A subagent's card is drawn in its launcher, so the session list shows
	// the launcher as waiting on a person while the card is open, and as
	// itself again once it is not.
	switch {
	case st == StateDeciding:
		e.publish(parent, ev("listed", map[string]any{"state": string(StateDeciding)}))
	case s.lastAnnounced() == StateDeciding:
		if p, err := e.Get(parent); err == nil {
			e.publish(parent, ev("listed", map[string]any{"state": string(p.State())}))
		}
	}
	s.mu.Lock()
	s.announced = st
	s.mu.Unlock()
}

// save writes the session and waits until the file holds it, flushed to the
// disk.
func (e *Engine) save(s *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.markLocked(s, true)
	e.waitSavedLocked(s)
}

// saveSoon writes the session in the background without a flush, so the turn
// does not wait on the disk between steps. Writes coalesce: the writer takes
// the latest state each time round. finish flushes when the turn ends.
func (e *Engine) saveSoon(s *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.markLocked(s, false)
}

// markLocked records a change and starts the writer if it is not running. A
// session that has been deleted is never written back out. The caller holds
// the lock.
func (e *Engine) markLocked(s *Session, durable bool) {
	if s.gone {
		return
	}
	s.dirty = true
	s.durable = s.durable || durable
	if !s.saving {
		s.saving = true
		go e.writeSession(s)
	}
}

// waitSavedLocked blocks until the writer is idle. The caller holds the lock.
func (e *Engine) waitSavedLocked(s *Session) {
	if s.idle == nil {
		s.idle = sync.NewCond(&s.mu)
	}
	for s.saving {
		s.idle.Wait()
	}
}

// writeSession writes the session until nothing is left unwritten. Each write
// is a snapshot taken under the lock, so the file never goes back to an
// older state.
func (e *Engine) writeSession(s *Session) {
	s.mu.Lock()
	for s.dirty && !s.gone {
		durable := s.durable
		s.dirty, s.durable = false, false
		doc := s.doc()
		s.mu.Unlock()
		write := e.store.SaveNoSync
		if durable {
			write = e.store.Save
		}
		if err := write(doc); err != nil {
			e.publish(s.ID, ev("error", map[string]any{"kind": "save", "message": err.Error()}))
		}
		s.mu.Lock()
	}
	s.saving = false
	if s.idle != nil {
		s.idle.Broadcast()
	}
	s.mu.Unlock()
}

// Create starts a session in a project the user has added, bound to the agent
// answering now. Nothing is read from the model server here. A session is
// created by the message that starts its first turn, and that turn reads the
// model before it sends anything.
func (e *Engine) Create(project string) (*Session, error) {
	if !e.cfg.HasProject(project) {
		return nil, ErrNoProject
	}
	a, err := e.active()
	if err != nil {
		return nil, err
	}
	s := newSession(store.NewID(), project, a.ID, a.Model, e.cfg.Config().Toolset)
	e.mu.Lock()
	e.live[s.ID] = s
	e.mu.Unlock()
	e.save(s)
	return s, nil
}

// Open resolves a session and marks its model unread, which is what opening
// one in the browser asks for. Nothing is sent: a model server restarted
// between two opens may hold nothing, so what the harness read before is a
// guess, and the turn the next message starts reads it again.
//
// Every other way of reaching a session goes through Get and marks nothing.
func (e *Engine) Open(id string) (*Session, error) {
	s, err := e.Get(id)
	if err != nil {
		return nil, err
	}
	s.reopen()
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
	// A session read from disk runs nothing, so calls still unanswered were
	// cut off by a harness that stopped. They are closed before anything
	// else can append.
	repaired := s.closeInterrupted()
	e.mu.Lock()
	if existing, ok := e.live[id]; ok {
		e.mu.Unlock()
		return existing, nil
	}
	e.live[id] = s
	e.mu.Unlock()
	if repaired {
		e.save(s)
	}
	return s, nil
}

func (e *Engine) State(id string) State {
	s, err := e.Get(id)
	if err != nil {
		return StateIdle
	}
	return s.State()
}

// LiveFill is a session's context as it stands now, without reading one back
// from disk. ok is false for a session nothing holds.
func (e *Engine) LiveFill(id string) (fill int, ok bool) {
	e.mu.Lock()
	s, held := e.live[id]
	e.mu.Unlock()
	if !held {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.computeFill(), true
}

// LiveState is what a session is doing, without reading one back from disk to
// answer. Nothing runs a session that is not in memory, so an id nothing holds
// is idle.
func (e *Engine) LiveState(id string) State {
	e.mu.Lock()
	s, ok := e.live[id]
	e.mu.Unlock()
	if !ok {
		return StateIdle
	}
	return s.State()
}

// Busy maps every session that is not idle to what it is doing.
func (e *Engine) Busy() map[string]State {
	e.mu.Lock()
	held := make([]*Session, 0, len(e.live))
	for _, s := range e.live {
		held = append(held, s)
	}
	e.mu.Unlock()
	out := map[string]State{}
	for _, s := range held {
		if st := s.State(); st != StateIdle {
			out[s.ID] = st
		}
	}
	// A subagent's card is drawn in its launcher, which is listed as waiting
	// on a person while it is open.
	for _, s := range held {
		if s.Parent != "" && out[s.ID] == StateDeciding {
			out[s.Parent] = StateDeciding
		}
	}
	return out
}

// Sent reports what became of a message: the entry it was appended as, or the
// queue it joined behind a turn that was already running.
type Sent struct {
	Queued bool
	Seq    int
	Steer  *Steer
}

// Send starts a turn with the message, or queues it against the one running.
// It returns as soon as the goroutine is running; everything else arrives on
// the stream. Attached files are read here, so a file that cannot be read
// refuses the message rather than reaching the model half sent.
func (e *Engine) Send(id, text string, files []string) (Sent, error) {
	if strings.TrimSpace(text) == "" && len(files) == 0 {
		return Sent{}, ErrEmptyText
	}
	s, err := e.Get(id)
	if err != nil {
		return Sent{}, err
	}
	attachments, err := e.readAttachments(files)
	if err != nil {
		return Sent{}, err
	}
	return e.deliver(s, text, attachments)
}

// deliver starts a turn with the message, or queues it. Every path that
// delivers a message goes through here, so a turn that starts between the
// caller's check and this one queues the message rather than refusing it.
func (e *Engine) deliver(s *Session, text string, attachments []store.Attachment) (Sent, error) {
	return e.deliverFrom(s, sender{}, text, attachments)
}

// deliverFrom is deliver with a sender. The entry keeps the message as it was
// written; the sender is put in front of it where the prompt is built, since
// that is the only place it means anything.
func (e *Engine) deliverFrom(s *Session, from sender, text string, attachments []store.Attachment) (Sent, error) {
	s.mu.Lock()
	if s.state != StateIdle {
		if err := s.queue(from, text, attachments); err != nil {
			s.mu.Unlock()
			return Sent{}, err
		}
		s.mu.Unlock()
		held := s.Steer()
		e.publish(s.ID, ev("queued", steerFields(held)))
		return Sent{Queued: true, Steer: held}, nil
	}
	s.state = StatePreflight
	ctx, cancel := context.WithCancel(e.ctx)
	s.cancel = cancel
	entry := s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: text,
		From: from.Name, FromSession: from.Session,
		Attachments: attachments, InContext: true, Visible: true,
		Meta: map[string]any{"mode": string(s.Mode)}})
	if s.Title == "" {
		s.Title = store.Title(text)
	}
	s.mu.Unlock()

	e.save(s)
	// A message from another agent was typed into no composer, so nothing has
	// drawn it and the turn it starts would answer a message the page cannot
	// see. One from a person is already on the page it was typed into.
	if from.Name != "" {
		e.publishMessage(s, entry)
	}
	e.announce(s, StatePreflight)
	e.startTurn(ctx, s, cancel)
	return Sent{Seq: entry.Seq}, nil
}

// absorbSteer appends the queued message, so the turn carries on with it in
// the history. It reports whether there was one.
func (e *Engine) absorbSteer(s *Session) bool {
	s.mu.Lock()
	held := s.steer
	if held == nil {
		s.mu.Unlock()
		return false
	}
	s.steer = nil
	entry := s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: held.Text,
		From: held.From.Name, FromSession: held.From.Session,
		Attachments: held.Attachments, InContext: true, Visible: true,
		Meta: map[string]any{"mode": string(s.Mode), "steer": true}})
	s.mu.Unlock()

	e.save(s)
	e.publishMessage(s, entry)
	return true
}

// publishMessage draws a message no browser has drawn for itself. A message
// typed into a composer is on the page before the harness has it; one taken
// off the queue is not.
func (e *Engine) publishMessage(s *Session, entry store.Entry) {
	e.publish(s.ID, ev("message", map[string]any{"seq": entry.Seq, "text": entry.Content,
		"from": entry.From, "attachments": AttachmentFields(entry.Attachments)}))
}

// flushSteer sends a message the turn ended without picking up. A stop leaves
// one behind, and the message was typed to be delivered.
func (e *Engine) flushSteer(s *Session) {
	s.mu.Lock()
	held := s.steer
	s.steer = nil
	s.mu.Unlock()
	if held == nil {
		return
	}
	sent, err := e.deliverFrom(s, held.From, held.Text, held.Attachments)
	if err != nil {
		e.publish(s.ID, ev("error", map[string]any{"kind": "internal", "message": err.Error()}))
		return
	}
	if !sent.Queued {
		e.publishMessage(s, store.Entry{Seq: sent.Seq, Content: held.Text, From: held.From.Name,
			Attachments: held.Attachments})
	}
}

// steerFields renders a queued message for the browser.
func steerFields(held *Steer) map[string]any {
	if held == nil {
		return map[string]any{"text": "", "attachments": []map[string]any{}}
	}
	return map[string]any{"text": held.Text, "from": held.From.Name,
		"attachments": AttachmentFields(held.Attachments)}
}

// AttachmentFields is what a browser needs to draw an attachment. The content
// of a text file is left out: the page has no use for it and it is already in
// the prompt.
func AttachmentFields(attachments []store.Attachment) []map[string]any {
	out := []map[string]any{}
	for _, a := range attachments {
		out = append(out, map[string]any{
			"path": a.Path, "kind": a.Kind, "bytes": a.Bytes, "file": a.File,
		})
	}
	return out
}

// Resend replaces the last message and runs its turn again, returning the
// sequence number of the entry that replaced it. text nil sends the message
// as it stands. The attachments ride along either way: an edit changes the
// words, not the files.
func (e *Engine) Resend(id string, seq int, text *string) (int, error) {
	s, err := e.Get(id)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if s.state != StateIdle {
		s.mu.Unlock()
		return 0, ErrBusy
	}
	i := s.lastUserEntry()
	if i < 0 || s.Entries[i].Seq != seq {
		s.mu.Unlock()
		return 0, ErrNotLast
	}
	previous := s.Entries[i]
	content, edited := previous.Content, previous.Edited
	if text != nil && *text != previous.Content {
		content, edited = *text, true
	}
	if strings.TrimSpace(content) == "" && len(previous.Attachments) == 0 {
		s.mu.Unlock()
		return 0, ErrEmptyText
	}
	// Nothing in the file store is left unreferenced by this: the only
	// message in the discarded range is the one whose files carry over.
	s.rewindTo(i)
	entry := s.appendEntry(store.Entry{Kind: "user", Role: "user", Content: content,
		Attachments: previous.Attachments, Edited: edited, InContext: true, Visible: true})
	// A session takes its name from its opening message, so a rewritten one
	// renames it unless the name was typed.
	if s.firstTurn() {
		s.Title = store.Title(content)
	}
	s.state = StatePreflight
	ctx, cancel := context.WithCancel(e.ctx)
	s.cancel = cancel
	s.mu.Unlock()

	e.save(s)
	// The transcript every browser holds ends in entries that are gone, so
	// they refetch rather than being patched.
	e.publish(s.ID, ev("resync", nil))
	e.announce(s, StatePreflight)
	e.startTurn(ctx, s, cancel)
	return entry.Seq, nil
}

// startTurn runs one turn in the background. The session is already in
// preflight with its cancel function set. A list whose every step is completed
// is cleared first; one with work left carries into the turn.
func (e *Engine) startTurn(ctx context.Context, s *Session, cancel context.CancelFunc) {
	s.mu.Lock()
	done, _ := tallyTodos(s.Todos)
	finished := len(s.Todos) > 0 && done == len(s.Todos)
	if finished {
		s.Todos = nil
	}
	s.mu.Unlock()
	if finished {
		e.publish(s.ID, ev("todos", map[string]any{"todos": []store.Todo{}}))
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		e.run(ctx, s)
		if s.Parent != "" {
			e.report(s)
		}
	}()
}

// readAttachments reads every attached file at once, so the message is either
// sent with all of them or not sent at all. Text is kept with the entry;
// an image goes to the file store and the entry keeps its id.
func (e *Engine) readAttachments(paths []string) ([]store.Attachment, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > maxAttachments {
		return nil, fmt.Errorf("Attach %d files: at most %d fit in one message", len(paths), maxAttachments)
	}
	out := make([]store.Attachment, 0, len(paths))
	for _, p := range paths {
		read, err := tools.ReadAttachment(p)
		if err != nil {
			return nil, err
		}
		a := store.Attachment{Path: p, Kind: read.Kind, Mime: read.Mime,
			Bytes: read.Bytes, Content: read.Content}
		if read.Kind == "image" {
			id, size, err := e.files.Put(p)
			if err != nil {
				return nil, err
			}
			a.File, a.Bytes, a.Content = id, size, ""
		}
		out = append(out, a)
	}
	return out, nil
}

// Stop cancels the turn. It never appends: the loop reaches stopped and
// writes the marker itself, so entry order is one goroutine's order.
//
// Cancelling closes the connection, which is the only signal the HTTP API has
// for it. A server part way through a prompt is writing nothing and does not
// see the close, so it finishes that work and the next request queues behind
// it. What a stop reaches is this harness.
//
// A compaction is refused for that reason: it draws nothing to take back, and
// stopping it would leave the session where it started with the server busy
// for the same length of time either way.
func (e *Engine) Stop(id string) (State, error) {
	s, err := e.Get(id)
	if err != nil {
		return StateIdle, err
	}
	// A background command outlives the turn that started it, so a stop ends
	// them whether or not a turn is running. It goes first, so a turn that an
	// exit notice started just before is read below and stopped with the rest.
	e.StopProcesses(id, true)
	s.mu.Lock()
	cancel := s.cancel
	state := s.state
	s.mu.Unlock()
	if state == StateIdle || cancel == nil {
		return StateIdle, nil
	}
	if state == StateCompacting {
		return state, ErrCompacting
	}
	cancel()
	e.perms.CancelSession(id)
	e.asks.cancelSession(id)
	for _, child := range e.liveChildren(id) {
		if _, err := e.Stop(child.ID); err != nil {
			e.publish(id, ev("error", map[string]any{"kind": "internal", "message": err.Error()}))
		}
	}
	return StateStopped, nil
}

// Compact rewrites the in-context set on an idle session.
func (e *Engine) Compact(id string) error {
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	// The agent is resolved before the session is marked busy, so a harness
	// with none to answer with refuses here rather than inside the goroutine.
	a, err := e.point(s)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.state != StateIdle {
		s.mu.Unlock()
		return ErrBusy
	}
	if !s.compactable() {
		s.mu.Unlock()
		return ErrTooShort
	}
	s.state = StateCompacting
	ctx, cancel := context.WithCancel(e.ctx)
	s.cancel = cancel
	s.mu.Unlock()

	e.announce(s, StateCompacting)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		if err := e.compact(ctx, s, e.backends.For(a), a, e.briefFor(s, e.cfg.Config()), e.turnCaps(s), true); err != nil {
			e.endWithError(s, err, 0, turnClock{}, nil)
			return
		}
		e.finish(s, StateIdle, "", 0, nil)
		// A message typed while the summary was being written has no turn to
		// join, so it starts one of its own.
		e.flushSteer(s)
	}()
	return nil
}

// SetAgent moves every live session onto the agent answering now. One agent
// answers for the whole harness, so a change of agent moves all of them.
//
// Nothing is sent to the model server. Making a model ready can cost minutes
// of another machine's memory, so it waits for a message that needs the model
// rather than following a choice made in a menu.
func (e *Engine) SetAgent() {
	a, _ := e.cfg.Config().Agent()
	for _, s := range e.liveSessions() {
		if s.Parent != "" {
			continue
		}
		e.pointTo(s, a.ID, a.Model)
	}
}

// Forget drops what a session read from one agent, for when that agent's
// address or key changed. What answered before may not be what answers next,
// so the window and the template capabilities are read again.
func (e *Engine) Forget(agentID string) {
	for _, s := range e.liveSessions() {
		s.mu.Lock()
		hit := s.AgentID == agentID
		if hit {
			s.forgetModel()
		}
		s.mu.Unlock()
		if hit {
			e.save(s)
		}
	}
}

func (e *Engine) liveSessions() []*Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	live := make([]*Session, 0, len(e.live))
	for _, s := range e.live {
		live = append(live, s)
	}
	return live
}

// pointTo moves a session onto an agent and a model, forgetting everything the
// last one's tokenizer implied.
func (e *Engine) pointTo(s *Session, agentID, model string) {
	s.mu.Lock()
	changed := s.AgentID != agentID || s.Model != model
	if changed {
		s.repoint(agentID, model)
	}
	s.mu.Unlock()
	if changed {
		e.save(s)
	}
}

// point moves a session onto the agent answering now and answers with it. A
// subagent was launched on an agent by name and stays on it, so the chip
// moving does not repoint work already under way.
func (e *Engine) point(s *Session) (config.Agent, error) {
	s.mu.Lock()
	child, mine := s.Parent != "", s.AgentID
	s.mu.Unlock()
	if child {
		a, ok := e.cfg.Config().AgentByID(mine)
		if !ok {
			return config.Agent{}, ErrNoAgent
		}
		return a, usable(a)
	}
	a, err := e.active()
	if err != nil {
		return config.Agent{}, err
	}
	e.pointTo(s, a.ID, a.Model)
	return a, nil
}

// Rename sets the session title. An empty title hands it back to the harness,
// which titles it from the first message.
func (e *Engine) Rename(id, title string) error {
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.Title = title
	s.Named = strings.TrimSpace(title) != ""
	if !s.Named {
		s.Title = store.Title(s.firstUserMessage())
	}
	s.mu.Unlock()
	e.save(s)
	return nil
}

func (e *Engine) Delete(id string) error {
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	// A turn is stopped first. The session is marked gone as it goes, so one
	// that starts between this check and the delete does not write the file
	// back out.
	if s.State() != StateIdle {
		return ErrBusy
	}
	s.forget()
	// A write already under way finishes before the file is removed, or it
	// would put the file back.
	s.mu.Lock()
	e.waitSavedLocked(s)
	s.mu.Unlock()
	e.StopProcesses(id, true)
	doc, _, _, _ := s.Snapshot()
	if err := e.store.Delete(id); err != nil {
		return err
	}
	// The files an attachment left behind belong to the session, so they go
	// with it rather than sitting in the store with nothing pointing at them.
	for _, entry := range doc.Entries {
		for _, a := range entry.Attachments {
			if a.File != "" {
				e.files.Remove(a.File)
			}
		}
	}
	e.mu.Lock()
	delete(e.live, id)
	e.mu.Unlock()
	for _, child := range e.store.Children(id) {
		if err := e.Delete(child.ID); err != nil {
			return err
		}
	}
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
	for _, s := range e.liveSessions() {
		e.procs.StopAll(s.ID, true)
	}
}

// titleSession names the conversation from its opening message, before the
// turn answers it.
//
// The model server holds one prompt at a time, so the request opens with the
// turn's own system message, tools and effort. It reads only its last message,
// and the turn after it finds the head of its prompt still cached. A title
// asked for after the turn would evict the conversation instead.
//
// A failure is not an error: the session keeps the title cut from the message.
func (e *Engine) titleSession(ctx context.Context, s *Session, back Backend, a config.Agent, b brief, caps llm.Caps) {
	s.mu.Lock()
	first, text := s.firstTurn(), s.firstUserMessage()
	s.mu.Unlock()
	if !first || strings.TrimSpace(text) == "" {
		return
	}
	if len(text) > titleChars {
		text = strings.ToValidUTF8(text[:titleChars], "")
	}
	tctx, cancel := context.WithTimeout(ctx, titleTimeout)
	defer cancel()
	msgs := append(e.messagesUntil(s, a, b, 0), llm.Message{Role: "user", Content: prompts.Title(text)})
	res, err := back.Chat(tctx, llm.ChatRequest{
		Model: a.Model, Messages: msgs, Tools: tools.Schemas(b.offer), Effort: a.Effort,
		PreserveReasoning: a.PreserveReasoning,
		Temperature:       a.Temperature, TopP: a.TopP, TopK: a.TopK,
		RepeatPenalty: a.RepeatPenalty, Seed: a.Seed, Caps: caps,
		MaxTokens: titleTokens,
	}, nil)
	if err != nil {
		return
	}
	title := cleanTitle(res.Content)
	if title == "" {
		return
	}
	s.mu.Lock()
	// A rename while the request was in flight wins, since it says more about
	// the session than the model does.
	if !s.firstTurn() {
		s.mu.Unlock()
		return
	}
	s.Title = title
	s.mu.Unlock()
	e.save(s)
	e.publish(s.ID, ev("title", map[string]any{"title": title}))
}

// cleanTitle takes the first line of a reply and strips what a model wraps a
// title in. An empty result leaves the title cut from the first message.
func cleanTitle(reply string) string {
	line := strings.TrimSpace(reply)
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.Trim(line, " \t\"'`*#.")
	return store.Title(line)
}

// ensureReady points the session at the agent answering now and makes sure its
// model has been read, inside the turn about to use it. It is the only place a
// model is read, so a message is what makes one ready. It answers with the
// agent, which is the snapshot the rest of the turn is shaped by.
func (e *Engine) ensureReady(ctx context.Context, s *Session) (config.Agent, error) {
	a, err := e.point(s)
	if err != nil {
		return config.Agent{}, err
	}
	s.mu.Lock()
	done := s.prepared
	s.mu.Unlock()
	if done {
		return a, nil
	}
	e.setState(s, StateLoading)
	// loadTimeout applies to the read alone. The turn around it has no
	// deadline of its own.
	ctx, cancel := context.WithTimeout(ctx, loadTimeout)
	defer cancel()
	r, err := e.backends.For(a).Prepare(ctx, a.Model)
	if err != nil {
		return config.Agent{}, err
	}
	e.apply(s, a, r)
	return a, nil
}

// apply writes what a model answered onto a session, unless it has moved to
// another agent or model since the read began.
func (e *Engine) apply(s *Session, a config.Agent, r llm.Ready) {
	s.mu.Lock()
	if s.AgentID != a.ID || s.Model != a.Model {
		s.mu.Unlock()
		return
	}
	if r.NCtx > 0 {
		s.NCtx, s.NCtxSource = r.NCtx, r.Source
	}
	s.caps = r.Caps
	s.prepared = true
	s.mu.Unlock()
	e.save(s)
	e.publishUsage(s)
}

// turnCaps is what the model's template said it takes, read once for a turn.
func (e *Engine) turnCaps(s *Session) llm.Caps {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caps
}

// schemaTokens is the estimated cost of the tool schemas a request sends, kept
// with its record so a context breakdown can subtract the figure that stood
// when the report was made.
func (e *Engine) schemaTokens(s *Session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ContextBase == nil {
		return 0
	}
	return s.ContextBase.Tools
}

// estimateBase estimates the token cost of the fixed parts of a prompt from
// their size, at charsPerToken characters per token. The system message
// renders once with and once without the project's rules, so the step between
// the two is the rules' cost, and the schemas' cost is their marshalled size
// the same way. A turn re-takes it, since re-estimating is free and the prompt
// it reads is the one the next request is about to send.
func (e *Engine) estimateBase(s *Session, a config.Agent, b brief) {
	s.mu.Lock()
	baseBrief := b
	baseBrief.rules, baseBrief.rulesName = "", ""
	baseChars := len(s.systemMessage(a, e.os, e.today(), baseBrief).Content)
	sysChars := len(s.systemMessage(a, e.os, e.today(), b).Content)
	schemaChars := 0
	if raw, err := json.Marshal(tools.Schemas(b.offer)); err == nil {
		schemaChars = len(raw)
	}
	s.ContextBase = &store.ContextBase{
		System: baseChars / charsPerToken,
		Rules:  (sysChars - baseChars) / charsPerToken,
		Tools:  schemaChars / charsPerToken,
	}
	s.mu.Unlock()
	e.saveSoon(s)
}

// fail ends the turn on the stream. The message is not kept: it says what went
// wrong with one request, which is of no use to a reader of the session later
// and of none at all to the model. What the turn's calls spent is kept, since
// the reader paid for them.
func (e *Engine) fail(s *Session, err error, clock turnClock) {
	kind := string(llm.Kind(err))
	if kind == "" {
		kind = "internal"
	}
	e.setState(s, StateFailed)
	e.publish(s.ID, ev("error", map[string]any{"kind": kind, "message": err.Error()}))
	e.endTurn(s, StateIdle, "error", 0, clock)
	e.flushSteer(s)
}

// turnClock is what one turn spent that no model call records: when it
// started and how long its tool calls took. The token counts, the call count
// and every timing of a request live on the entries those calls produced, so
// the totals are summed from there rather than from a counter a stop can leave
// half-written.
type turnClock struct {
	started time.Time
	// tools is the time spent running tool calls, which is outside every
	// request and so is in no call's record.
	tools time.Duration
	agent string
	model string
}

// known reports a clock a turn is actually running. A compaction asked for on
// its own has none, and writes no card.
func (c turnClock) known() bool { return !c.started.IsZero() }

// turnCard is what this turn spent, summed over the calls its entries
// recorded after the message it answers. nil says it recorded none.
func (e *Engine) turnCard(s *Session, clock turnClock) map[string]any {
	if !clock.known() {
		return nil
	}
	s.mu.Lock()
	u := turnUsageOf(s.Entries, turnStart(s.Entries))
	s.mu.Unlock()
	return u.fields(time.Since(clock.started), clock.tools, clock.agent, clock.model)
}

// noteCard records what the turn spent on the reply that ended it, so a
// reloaded page draws the line the stream drew.
func (e *Engine) noteCard(s *Session, card map[string]any) {
	if card == nil {
		return
	}
	s.mu.Lock()
	i := turnEnd(s.Entries, turnStart(s.Entries))
	if i >= 0 {
		if s.Entries[i].Meta == nil {
			s.Entries[i].Meta = map[string]any{}
		}
		for k, v := range card {
			s.Entries[i].Meta[k] = v
		}
	}
	s.mu.Unlock()
	e.saveSoon(s)
}

// endTurn writes the turn's totals where a reloaded page reads them, publishes
// them with the done event, and returns the session to idle, which is the only
// state a new message is accepted in.
func (e *Engine) endTurn(s *Session, next State, finishReason string, iterations int, clock turnClock) {
	card := e.turnCard(s, clock)
	e.noteCard(s, card)
	e.finish(s, next, finishReason, iterations, card)
}

// carriesOutput reports whether an event is part of the generation. Usage and
// the finish reason arrive after the last token and would put the start of
// generation at the end of the stream.
func carriesOutput(kind llm.EventKind) bool {
	switch kind {
	case llm.EventContent, llm.EventReasoning, llm.EventToolCall:
		return true
	}
	return false
}

// finish publishes done and returns the session to idle, which is the only
// state a new message is accepted in. card holds the turn's totals and is nil
// for a turn that recorded no model call.
func (e *Engine) finish(s *Session, next State, finishReason string, iterations int, card map[string]any) {
	// The steps of the turn were written without a flush. The file reaches
	// the disk before the session reads as idle.
	e.save(s)
	s.mu.Lock()
	s.state = next
	s.cancel = nil
	s.mu.Unlock()
	fields := map[string]any{"finish_reason": finishReason, "iterations": iterations}
	for k, v := range card {
		fields[k] = v
	}
	e.publish(s.ID, ev("done", fields))
	e.announce(s, next)
}

func (e *Engine) run(ctx context.Context, s *Session) {
	// A turn that was stopped part way through a run of subagent calls leaves
	// launches nobody will wait on.
	s.mu.Lock()
	s.launches = nil
	s.mu.Unlock()
	clock := turnClock{started: time.Now()}
	a, err := e.ensureReady(ctx, s)
	if err != nil {
		e.endWithError(s, err, 0, clock, nil)
		return
	}
	clock.agent, clock.model = a.Name, a.Model
	cfg := e.cfg.Config()
	// The agent, the settings and the capabilities are read once and hold for
	// the whole turn, and so does the backend they resolve to. All of them
	// decide how the template renders the prompt, so a change that lands
	// between two steps of one turn would change the prompt under the model and
	// cost a reprocess of everything before it. A change made meanwhile is used
	// by the next turn.
	caps := e.turnCaps(s)
	b := e.briefFor(s, cfg)
	back := e.backends.For(a)
	// A subagent is filed under the task it was given, so nothing is spent
	// asking the model for a name.
	if s.Parent == "" {
		e.titleSession(ctx, s, back, a, b, caps)
	}
	// The estimate reads the same prompt the first request sends.
	e.estimateBase(s, a, b)
	runner, rerr := tools.NewRunner(s.Project, cfg)
	if rerr != nil {
		e.fail(s, rerr, clock)
		return
	}
	runner.Baseline = func(sn tools.Snapshot) { e.recordBaseline(s, sn) }
	runner.Hide(e.cfg.Paths().Home)
	runner.Seen = s.seen
	runner.SpillTo(e.store.SpillDir(s.ID))
	// The provider and the offer both read the same config, so a tool that is
	// sent and a tool that is built cannot disagree within the turn. A
	// provider that fails to build leaves Search nil, and the tool answers
	// with its not-configured sentence rather than failing the turn.
	if cfg.Search.Enabled() {
		acc, _ := cfg.Search.Account()
		if p, err := e.searches.For(acc.Name, acc.URL, acc.Key); err == nil {
			runner.Search = p
		}
	}

	// The tool call budget for this turn. Zero is no budget at all, so the
	// only thing that ends a runaway turn is the Stop button.
	budget := cfg.MaxToolCalls
	calls := 0
	// noted and refused track the spent budget: the note is written once, and a
	// model that calls tools again after being refused ends the turn.
	noted, refused := false, false
	var seen repeats
	// overflowed is set once a request the server found too long has been
	// compacted and sent again, so a second overflow ends the turn.
	overflowed := false
	carried := newCarry()

	for iteration := 0; ; iteration++ {
		e.setState(s, StatePreflight)
		// A message typed during the last step joins the history here, once
		// the work that was already under way has finished. It lands before
		// the context is measured, so one large enough to need it compacts.
		if e.absorbSteer(s) {
			seen.reset()
		}

		s.mu.Lock()
		fill := s.computeFill()
		nctx, _ := s.window(a)
		enough := s.compactable()
		s.mu.Unlock()
		// A window nothing reported and nothing overrode is no limit, so a
		// session against one compacts when the reader asks and not before.
		// A context filled by the prompt itself is sent as it stands.
		if nctx > 0 && float64(fill) >= compactLimit(nctx, cfg.CompactAt, a.MaxTokens) && enough {
			e.setState(s, StateCompacting)
			if err := e.compact(ctx, s, back, a, b, caps, true); err != nil {
				e.endWithError(s, err, iteration, clock, nil)
				return
			}
		}

		// A spent budget refuses further calls rather than ending the turn where
		// it stands, so the last thing the reader sees is an answer written with
		// what the model already found. The tools stay in the request, since
		// dropping them would change the head of the prompt and miss the cache.
		spent := budget > 0 && calls >= budget
		if spent && !noted {
			e.noteLimit(s, calls)
			noted = true
		}

		e.setState(s, StateStreaming)
		msgs := e.messages(s, a, b)
		s.mu.Lock()
		req := llm.ChatRequest{
			Model:             a.Model,
			Messages:          msgs,
			Effort:            a.Effort,
			PreserveReasoning: a.PreserveReasoning,
			Temperature:       a.Temperature,
			TopP:              a.TopP,
			TopK:              a.TopK,
			RepeatPenalty:     a.RepeatPenalty,
			Seed:              a.Seed,
			Caps:              caps,
		}
		// The agent's cap is the reply's. The title request sets its own,
		// since what it asks for is not a reply.
		if a.MaxTokens != nil {
			req.MaxTokens = *a.MaxTokens
		}
		req.Tools = tools.Schemas(b.offer)
		s.live = &Reply{Since: time.Now()}
		s.mu.Unlock()
		// The schemas' estimated cost rides on the record, so the breakdown of
		// a context reads the figure that stood when the report was made.
		schemas := e.schemaTokens(s)

		res, rec, err := e.request(ctx, s, back, req, schemas)
		if llm.Kind(err) == llm.KindOverflow && !overflowed && s.Compactable() {
			// The fill is an estimate, so a window can be overrun before the
			// threshold is reached. Compacting and asking again recovers once.
			overflowed = true
			e.dropLive(s)
			e.setState(s, StateCompacting)
			if err := e.compact(ctx, s, back, a, b, caps, false); err != nil {
				e.endWithError(s, err, iteration, clock, nil)
				return
			}
			continue
		}
		// A server that will not take a picture refuses the whole request, so
		// the pictures the last calls returned become errors in their results
		// and the request goes again. Older ones were accepted already.
		if llm.Kind(err) == llm.KindBadRequest && e.refuseImages(s, err) {
			e.dropLive(s)
			continue
		}
		if err != nil {
			// What the cut call reported rides on the reply it had written, and
			// on the stop marker where it wrote none, so the turn still counts
			// the call it made.
			rec = e.keepPartial(s, rec)
			e.endWithError(s, err, iteration, clock, rec)
			return
		}
		if res.Dropped > 0 {
			e.publish(s.ID, ev("error", map[string]any{"kind": "malformed",
				"message": fmt.Sprintf("The model server sent %d frames that could not be read: some output was lost", res.Dropped)}))
		}

		if len(res.ToolCalls) == 0 {
			s.mu.Lock()
			s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant",
				Content: res.Content, Reasoning: res.Reasoning,
				Meta: outcomeMeta(res), Usage: rec, InContext: true, Visible: true})
			s.live = nil
			s.mu.Unlock()
			e.saveSoon(s)
			e.publishUsage(s)
			// A turn the model meant to continue reads as a finished one, so
			// the transcript says the call was never run.
			if llm.UnparsedToolCall(res.Content) {
				e.noteUnparsed(s)
			}
			// A message that landed while the model was answering makes this
			// the middle of the turn rather than the end of it.
			if e.absorbSteer(s) {
				seen.reset()
				continue
			}
			if ctx.Err() == nil && e.carryOn(s, res, b.offer.Mode, &carried) {
				continue
			}
			e.setState(s, StateFinal)
			e.endTurn(s, StateIdle, res.FinishReason, iteration+1, clock)
			// One that landed between the check above and the session going
			// idle has no turn to join, so it starts one of its own.
			e.flushSteer(s)
			return
		}

		s.mu.Lock()
		s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant",
			Content: res.Content, Reasoning: res.Reasoning, ToolCalls: res.ToolCalls,
			Meta: outcomeMeta(res), Usage: rec, InContext: true, Visible: true})
		s.live = nil
		s.mu.Unlock()
		e.saveSoon(s)
		e.publishUsage(s)

		if spent {
			e.refuseCalls(s, res.ToolCalls)
			// A model that reaches for tools again after being told the budget
			// is spent will not stop on its own, so the turn ends here.
			if refused {
				e.setState(s, StateFinal)
				e.endTurn(s, StateIdle, "tool_limit", iteration+1, clock)
				e.flushSteer(s)
				return
			}
			refused = true
			continue
		}

		for i := 0; i < len(res.ToolCalls); i++ {
			call := res.ToolCalls[i]
			// Calls that only read and need no card, made one after another,
			// run at the same time.
			if run := e.readRun(s, cfg, b.offer, runner, res.ToolCalls, i); run > 1 {
				calls += run
				e.runReads(ctx, s, cfg, a, runner, res.ToolCalls[i:i+run], i, &seen, &clock)
				i += run - 1
				if ctx.Err() != nil {
					e.markStopped(s, clock, nil)
					return
				}
				continue
			}
			calls++
			// A tool the mode does not run or the toolset does not offer is
			// answered here; a tool withheld from this session alone reaches
			// its own tool, which says why it is not taken.
			if tools.Known(call.Function.Name) &&
				(!b.offer.InMode(call.Function.Name) || !b.offer.InSet(call.Function.Name)) {
				entry := e.unavailable(s, call, b.offer)
				e.saveSoon(s)
				e.publish(s.ID, ev("tool_output", map[string]any{
					"index": i, "id": call.ID, "name": call.Function.Name,
					"output": entry.Content, "exit_code": nil, "truncated": false,
				}))
				continue
			}
			note := seen.see(call)
			decision, record, stopped := e.decide(ctx, s, cfg, a, runner, call)
			if stopped {
				e.stopWith(s, call, record, clock)
				return
			}
			e.setState(s, StateExecuting)
			if decision.Kind != perms.DecideDeny {
				e.publishStarted(s, runner, call, i)
			}
			if decision.Kind != perms.DecideDeny {
				e.markStarted(s, call)
				if call.Function.Name == "subagent" {
					e.prelaunch(s, res.ToolCalls, i)
				}
			}
			began := time.Now()
			entry := e.execute(ctx, s, runner, call, decision, record, i)
			if note != "" {
				entry = e.addNote(s, entry, note)
			}
			clock.tools += time.Since(began)
			e.saveSoon(s)
			e.publish(s.ID, ev("tool_output", map[string]any{
				"index": i, "id": call.ID, "name": call.Function.Name,
				"output": entry.Content, "exit_code": entry.ExitCode, "truncated": entry.Truncated,
				"meta": entry.Meta,
			}))
			if ctx.Err() != nil {
				e.markStopped(s, clock, nil)
				return
			}
		}

		// An approved plan turns the rest of this turn into its execution: the
		// next request carries execute's prompt and tools, and the budget
		// starts again for the work the plan describes.
		if e.takeApproval(s) {
			b = e.briefFor(s, cfg)
			calls = 0
			e.estimateBase(s, a, b)
		}
	}

}

// errEmptyReply is a request that ended normally with nothing in it, which a
// retry usually fixes and which a chat template may refuse to render later.
var errEmptyReply = errors.New("The model returned an empty reply: send the message again, or check the model server")

// request sends one request, retrying a failure that says nothing about the
// request itself: a server error, a model still loading, a server that is not
// answering yet, or an empty reply. A stall is not retried, since the server
// is still busy and a new request would queue behind the old one.
//
// It answers with what the call spent whichever way it ended, including one
// this harness cut short: the server's report of the part it did send arrives
// in the stream, and the caller puts that record on the entry the call
// produced. A discarded attempt's record goes on the note saying it was sent
// again, so every call of the turn is counted somewhere.
func (e *Engine) request(ctx context.Context, s *Session, back Backend, req llm.ChatRequest, schemas int) (llm.Result, *store.CallUsage, error) {
	for attempt := 0; ; attempt++ {
		// first is when this request began to produce, which is what the
		// generation rate is measured against. Chat calls on synchronously
		// from its read loop, so no lock is needed here.
		var first time.Time
		sent := time.Now()
		res, err := back.Chat(ctx, req, func(event llm.Event) error {
			if first.IsZero() && carriesOutput(event.Kind) {
				first = time.Now()
			}
			e.forward(s, event)
			return nil
		})
		if err == nil && res.Content == "" && res.Reasoning == "" && len(res.ToolCalls) == 0 {
			err = errEmptyReply
		}
		rec := callUsage(res, sent, first, schemas, stopped(err))
		if err == nil {
			return res, rec, nil
		}
		if !retryable(err) || attempt >= len(e.retry) || ctx.Err() != nil {
			return res, rec, err
		}
		wait := e.retry[attempt]
		e.noteRetry(s, err, wait, attempt+1, rec)
		select {
		case <-ctx.Done():
			return res, rec, ctx.Err()
		case <-time.After(wait):
		}
		e.setState(s, StateStreaming)
	}
}

// stopped reports that a request ended because this harness ended it, which is
// what separates a call whose usage is unknown because it was cut off from one
// whose server simply never reported.
func stopped(err error) bool {
	return llm.Kind(err) == llm.KindCanceled || errors.Is(err, context.Canceled)
}

// retryable reports whether a failed request is worth sending again as it is.
func retryable(err error) bool {
	if errors.Is(err, errEmptyReply) {
		return true
	}
	switch llm.Kind(err) {
	case llm.KindUpstream, llm.KindModelLoading, llm.KindUnreachable:
		return true
	}
	return false
}

// noteRetry records a failed attempt in the transcript and drops what it had
// streamed, so the retry starts from a clean reply. A browser that drew some
// of it is sent back for the transcript. The note carries the attempt's
// record: the call happened, and its cost belongs to the turn.
func (e *Engine) noteRetry(s *Session, err error, wait time.Duration, attempt int, rec *store.CallUsage) {
	text := fmt.Sprintf("%s. Trying again in %s, attempt %d of %d", strings.TrimRight(err.Error(), "."),
		wait.Round(100*time.Millisecond), attempt+1, len(e.retry)+1)
	s.mu.Lock()
	drew := s.live != nil && (s.live.Content != "" || s.live.Reasoning != "" || len(s.live.ToolCalls) > 0)
	s.live = &Reply{Since: time.Now()}
	entry := s.appendEntry(store.Entry{Kind: "status", Content: text, InContext: false, Visible: true, Usage: rec})
	s.mu.Unlock()
	e.save(s)
	if drew {
		e.publish(s.ID, ev("resync", nil))
		return
	}
	e.publish(s.ID, ev("status_entry", map[string]any{"content": entry.Content}))
}

// dropLive forgets the reply a request had streamed.
func (e *Engine) dropLive(s *Session) {
	s.mu.Lock()
	s.live = nil
	s.mu.Unlock()
}

// keepPartial keeps what a request had streamed before it was stopped or
// failed, so the text the reader saw is still there on reload and the model
// sees what it said. Tool calls cut off mid-way are dropped, and so is a
// reply with no text, which some chat templates refuse to render. The call's
// record rides on the entry it produced; what is returned is the record that
// found no entry to go on, which the caller offers to the stop marker.
func (e *Engine) keepPartial(s *Session, rec *store.CallUsage) *store.CallUsage {
	s.mu.Lock()
	live := s.live
	s.live = nil
	if live == nil || (strings.TrimSpace(live.Content) == "" && strings.TrimSpace(live.Reasoning) == "") {
		s.mu.Unlock()
		return rec
	}
	s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant",
		Content: live.Content, Reasoning: live.Reasoning,
		Meta:      map[string]any{"interrupted": true},
		Usage:     rec,
		InContext: strings.TrimSpace(live.Content) != "", Visible: true})
	s.mu.Unlock()
	e.save(s)
	return nil
}

// outcomeMeta is what an assistant entry keeps about the request behind it:
// how the model stopped, and how many frames were lost reading it. Neither
// survives the stream, and a session read back is all that is left.
func outcomeMeta(res llm.Result) map[string]any {
	m := map[string]any{}
	if res.FinishReason != "" {
		m["finish_reason"] = res.FinishReason
	}
	if res.Dropped > 0 {
		m["dropped"] = res.Dropped
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// noteUnparsed records that the answer carries a tool call the server left in
// the text, which the reader would otherwise take for the end of the work.
func (e *Engine) noteUnparsed(s *Session) {
	text := "The answer carries a tool call the model server did not parse: nothing was run and the turn ended"
	s.mu.Lock()
	entry := s.appendEntry(store.Entry{Kind: "status", Content: text, InContext: false, Visible: true})
	s.mu.Unlock()
	e.save(s)
	e.publish(s.ID, ev("status_entry", map[string]any{"content": entry.Content}))
}

// noteLimit records that the budget ran out, so the transcript says why the
// model stopped reaching for tools.
func (e *Engine) noteLimit(s *Session, calls int) {
	text := fmt.Sprintf("Tool call limit reached after %d calls: further calls are refused and the model was asked to answer", calls)
	s.mu.Lock()
	entry := s.appendEntry(store.Entry{Kind: "status", Content: text, InContext: false, Visible: true})
	s.mu.Unlock()
	e.save(s)
	e.publish(s.ID, ev("status_entry", map[string]any{"content": entry.Content}))
}

// sideEffects are the tools whose call may have changed something outside the
// session by the time a crash cut it off.
var sideEffects = map[string]bool{"bash": true, "write": true, "edit": true, "subagent": true, "send_message": true}

// markStarted records on the assistant entry that a call with side effects is
// about to run, and saves it first. A harness that stops before the result
// lands then knows the call may have done its work.
func (e *Engine) markStarted(s *Session, call llm.ToolCall) {
	if !sideEffects[call.Function.Name] {
		return
	}
	s.mu.Lock()
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind != "assistant" {
			continue
		}
		if s.Entries[i].Meta == nil {
			s.Entries[i].Meta = map[string]any{}
		}
		started, _ := s.Entries[i].Meta["started"].([]any)
		s.Entries[i].Meta["started"] = append(started, call.ID)
		break
	}
	s.mu.Unlock()
	e.save(s)
}

// addNote puts a note after the result a tool entry carries, so the model
// reads it in the result of the call it is about.
func (e *Engine) addNote(s *Session, entry store.Entry, note string) store.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Seq == entry.Seq {
			s.Entries[i].Content = joinMessages(s.Entries[i].Content, "\n"+note)
			return s.Entries[i]
		}
	}
	return entry
}

// refuseCalls answers every call of a reply made after the budget was spent,
// so each one keeps its pair and the model reads why nothing ran.
func (e *Engine) refuseCalls(s *Session, calls []llm.ToolCall) {
	for i, call := range calls {
		s.mu.Lock()
		entry := s.appendEntry(store.Entry{Kind: "tool", Role: "tool",
			ToolCallID: call.ID, ToolName: call.Function.Name, Content: prompts.BudgetSpent(),
			InContext: true, Visible: true})
		s.mu.Unlock()
		e.publish(s.ID, ev("tool_output", map[string]any{
			"index": i, "id": call.ID, "name": call.Function.Name,
			"output": entry.Content, "exit_code": nil, "truncated": false,
		}))
	}
	e.save(s)
}

// compactLimit is the fill a turn compacts at: the configured share of the
// window, lowered where the agent's reply cap would not fit in what is left.
func compactLimit(nctx int, share float64, maxTokens *int) float64 {
	limit := share * float64(nctx)
	if maxTokens != nil {
		if room := float64(nctx - *maxTokens); room > 0 && room < limit {
			limit = room
		}
	}
	return limit
}

// endWithError separates a cancellation, which is a stop, from a failure. rec
// is a call that ended without a reply of its own, offered to whatever entry
// the turn writes as it stops.
func (e *Engine) endWithError(s *Session, err error, iteration int, clock turnClock, rec *store.CallUsage) {
	if stopped(err) {
		e.markStopped(s, clock, rec)
		return
	}
	e.fail(s, err, clock)
}

// markStopped ends the turn on a stop. The calls it had already made keep their
// cost: a call that streamed no report is counted as a call and named as one
// that could not be counted, rather than left out of the numbers silently.
func (e *Engine) markStopped(s *Session, clock turnClock, rec *store.CallUsage) {
	s.mu.Lock()
	entry := s.appendEntry(store.Entry{Kind: "status", Content: "Stopped",
		InContext: false, Visible: true, Usage: rec})
	s.mu.Unlock()
	e.setState(s, StateStopped)
	e.save(s)
	// A stop between two requests draws nothing of its own, so the marker is
	// published rather than left for the next time the page is loaded.
	e.publish(s.ID, ev("status_entry", map[string]any{"content": entry.Content}))
	e.endTurn(s, StateIdle, "stopped", 0, clock)
	e.flushSteer(s)
}

// stopWith records the refused call so the assistant message keeps its pair
// before the turn ends.
func (e *Engine) stopWith(s *Session, call llm.ToolCall, record *store.PermissionRecord, clock turnClock) {
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "tool", Role: "tool", ToolCallID: call.ID,
		ToolName: call.Function.Name, Content: "stopped by user",
		Permission: record, InContext: true, Visible: true})
	s.mu.Unlock()
	e.save(s)
	e.markStopped(s, clock, nil)
}

// forward publishes one streamed event and folds it into the live reply under
// the same hold of the lock, which is what lets a view read both at once.
func (e *Engine) forward(s *Session, event llm.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != nil {
		s.live.add(event)
	}
	switch event.Kind {
	case llm.EventContent:
		e.publish(s.ID, ev("delta", map[string]any{"text": event.Text}))
	case llm.EventReasoning:
		e.publish(s.ID, ev("reasoning", map[string]any{"text": event.Text}))
	case llm.EventToolCall:
		e.publish(s.ID, ev("tool_call", map[string]any{
			"index": event.Index, "id": event.ID, "name": event.Name, "arguments": event.Arguments}))
	}
}

// publishUsage sends the context meter what it needs: the last report the
// server made, the fill built from it, and the breakdown that reads it.
func (e *Engine) publishUsage(s *Session) {
	s.mu.Lock()
	_, u := s.lastMeasured()
	fill := s.computeFill()
	compactable := s.compactable()
	reported, source := s.NCtx, s.NCtxSource
	agentID := s.AgentID
	parts := s.breakdown()
	s.mu.Unlock()
	if u == nil {
		return
	}
	// The window comes from the agent as it stands now, so an override typed
	// into the dialog moves the meter on the next turn without a reload.
	agent, _ := e.cfg.Config().AgentByID(agentID)
	nctx, _ := agent.Window(reported, source)
	payload := map[string]any{
		"prompt_tokens": u.Prompt, "completion_tokens": u.Completion,
		"cached_tokens": u.Cached, "fill": fill, "n_ctx": nctx, "compactable": compactable}
	if parts != nil {
		payload["breakdown"] = parts
	}
	e.publish(s.ID, ev("usage", payload))
}

// decide applies the policy and, when it says prompt, blocks on the browser.
// The third result reports that the session was stopped while waiting.
func (e *Engine) decide(ctx context.Context, s *Session, cfg config.Config, a config.Agent, runner *tools.Runner, call llm.ToolCall) (perms.Decision, *store.PermissionRecord, bool) {
	tool := call.Function.Name
	command, path := argSummary(call)
	record := &store.PermissionRecord{Tool: tool, Command: command, Path: path}

	policy := cfg.Mode
	if e.perms.Evaluate(s.Project, tool, command, path, runner.Outside(path), policy) == perms.ActionAllow {
		// A call skips the card two ways, and the record keeps them apart.
		// "auto" is a policy that allowed this call, "full" is no policy.
		record.Decision = "auto"
		if policy == config.ModeFull {
			record.Decision = "full"
		}
		return perms.Decision{Kind: perms.DecideOnce}, record, false
	}
	// The id is minted here rather than inside Ask, so the record kept with the
	// tool entry carries the same id the card was asked with, whether the card
	// was answered or the turn was stopped under it.
	req := perms.Request{
		ID:        perms.NewRequestID(),
		SessionID: s.ID, Project: s.Project, Tool: tool,
		Command: command, Path: path,
		Prefix: perms.DefaultPrefix(command),
		Args:   string(call.Function.Arguments),
	}
	// A subagent's card belongs in the conversation the person has open, not
	// in a transcript nobody is reading.
	if root := e.rootOf(s); root != s.ID {
		req.Route, req.Agent = root, a.Name
	}
	record.ID = req.ID
	// Deciding means a person has a card to answer, so a call the policy
	// allows never enters it.
	e.setState(s, StateDeciding)
	d, err := e.perms.Ask(ctx, req)
	if err != nil {
		record.Decision = "stopped"
		e.publishDecided(s, record)
		return perms.Decision{}, record, true
	}
	record.Decision = string(d.Kind)
	record.Prefix = d.Prefix
	record.Reason = d.Reason
	e.publishDecided(s, record)
	if d.Note != "" {
		e.publish(s.ID, ev("error", map[string]any{"kind": "allowlist", "message": d.Note}))
	}
	return d, record, false
}

// publishDecided closes a card that is open in the browser. The tab that
// answered it already knows, but a stop answers it for every tab and nothing
// else tells them, which leaves the composer waiting on a card that is gone.
func (e *Engine) publishDecided(s *Session, r *store.PermissionRecord) {
	if r.ID == "" {
		return
	}
	e.publish(s.ID, ev("permission_done", map[string]any{
		"id": r.ID, "decision": r.Decision, "prefix": r.Prefix, "reason": r.Reason}))
}

// publishStarted marks the moment a call begins running. A shell call carries
// the deadline it was given, since a command that is slow and one that is
// stuck look alike until the limit is on screen.
func (e *Engine) publishStarted(s *Session, runner *tools.Runner, call llm.ToolCall, index int) {
	fields := map[string]any{"index": index, "id": call.ID, "name": call.Function.Name}
	if call.Function.Name == "bash" {
		raw := json.RawMessage(call.Function.Arguments)
		if runner.ShellBackground(raw) {
			fields["background"] = true
		} else {
			fields["timeout_s"] = int(runner.ShellTimeout(raw) / time.Second)
		}
	}
	e.publish(s.ID, ev("tool_start", fields))
}

// execute runs the tool or writes the denial, and appends the tool entry that
// pairs with the assistant message.
func (e *Engine) execute(ctx context.Context, s *Session, runner *tools.Runner,
	call llm.ToolCall, d perms.Decision, record *store.PermissionRecord, index int) store.Entry {
	return e.appendTool(s, call, record, e.runTool(ctx, s, runner, call, d))
}

// outcome is what one tool call produced, before it is appended.
type outcome struct {
	content   string
	exitCode  *int
	truncated bool
	meta      map[string]any
	// images are the pictures the result carries, already in the file store.
	images []store.Attachment
}

// readTools read and change nothing, so a run of them in one reply can go at
// once.
var readTools = map[string]bool{"read": true, "read_image": true, "grep": true, "web_search": true}

// readRun is how many calls from i on only read and would run without a card,
// so they can run at the same time.
func (e *Engine) readRun(s *Session, cfg config.Config, offer tools.Offer, runner *tools.Runner, calls []llm.ToolCall, i int) int {
	n := 0
	for _, call := range calls[i:] {
		name := call.Function.Name
		if !readTools[name] || !offer.InMode(name) || !offer.InSet(name) {
			break
		}
		command, path := argSummary(call)
		if e.perms.Evaluate(s.Project, name, command, path, runner.Outside(path), cfg.Mode) != perms.ActionAllow {
			break
		}
		n++
	}
	return n
}

// runReads runs calls that only read at the same time, and appends their
// results in the order the model made them.
func (e *Engine) runReads(ctx context.Context, s *Session, cfg config.Config, a config.Agent,
	runner *tools.Runner, calls []llm.ToolCall, first int, seen *repeats, clock *turnClock) {
	notes := make([]string, len(calls))
	records := make([]*store.PermissionRecord, len(calls))
	for k, call := range calls {
		notes[k] = seen.see(call)
		// The policy allows each of these, so deciding asks nobody.
		_, records[k], _ = e.decide(ctx, s, cfg, a, runner, call)
		e.publishStarted(s, runner, call, first+k)
	}
	e.setState(s, StateExecuting)
	outs := make([]outcome, len(calls))
	began := time.Now()
	var wg sync.WaitGroup
	for k, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outs[k] = e.runTool(ctx, s, runner, call, perms.Decision{Kind: perms.DecideOnce})
		}()
	}
	wg.Wait()
	clock.tools += time.Since(began)
	for k, call := range calls {
		entry := e.appendTool(s, call, records[k], outs[k])
		if notes[k] != "" {
			entry = e.addNote(s, entry, notes[k])
		}
		e.saveSoon(s)
		e.publish(s.ID, ev("tool_output", map[string]any{
			"index": first + k, "id": call.ID, "name": call.Function.Name,
			"output": entry.Content, "exit_code": entry.ExitCode, "truncated": entry.Truncated,
			"meta": entry.Meta,
		}))
	}
}

// runTool runs one call, or writes its denial.
func (e *Engine) runTool(ctx context.Context, s *Session, runner *tools.Runner, call llm.ToolCall, d perms.Decision) outcome {
	var o outcome
	switch {
	case d.Kind == perms.DecideDeny:
		o.content = perms.DenialContent(d.Reason)
	default:
		args := json.RawMessage(call.Function.Arguments)
		var res tools.Result
		var err error
		switch call.Function.Name {
		case "read":
			res, err = runner.ReadFile(ctx, args)
		case "read_image":
			res, err = runner.ReadImage(ctx, args)
		case "grep":
			res, err = runner.Find(ctx, args)
		case "web_search":
			res, err = runner.WebSearch(ctx, args)
		case "write":
			res, err = runner.WriteFile(ctx, args)
		case "edit":
			res, err = runner.ReplaceInFile(ctx, args)
		case "bash":
			res, err = e.runBash(ctx, s, runner, call)
		case "processes":
			res, err = e.listProcesses(s)
		case "todo_write":
			res, err = e.writeTodos(s, args)
		case "ask_user_question":
			res, err = e.askQuestion(ctx, s, call)
		case "subagent":
			res, err = e.runAgent(ctx, s, call)
		case "send_message":
			res, err = e.sendMessage(s, call)
		case "write_plan":
			res, err = e.writePlan(s, args)
		case "exit_plan_mode":
			res, err = e.submitPlan(ctx, s, call)
		default:
			err = fmt.Errorf("no tool named %s: use read, read_image, grep, web_search, write, edit, bash, processes, todo_write, ask_user_question, write_plan, exit_plan_mode, subagent or send_message", call.Function.Name)
		}
		if err != nil {
			// The flag is what lets the row say the call failed, since only
			// bash has an exit code to say it with.
			o.content, o.meta = err.Error(), map[string]any{"error": true}
		} else {
			o.content, o.exitCode, o.truncated, o.meta = res.Output, res.ExitCode, res.Truncated, res.Meta
		}
		if err == nil && res.Image != nil {
			e.keepImage(&o, res.Image)
		}
	}
	return o
}

// keepImage copies a result's picture into the file store, so every later
// request sends the bytes that were read rather than the file as it stands.
// The id goes in the meta as well, which is how the transcript finds it.
func (e *Engine) keepImage(o *outcome, img *tools.Image) {
	id, size, err := e.files.Put(img.Abs)
	if err != nil {
		o.content = fmt.Sprintf("%s could not be kept for the model: %s", img.Path, err)
		o.meta = map[string]any{"error": true}
		return
	}
	o.images = append(o.images, store.Attachment{Path: img.Path, Kind: "image", Mime: img.Mime, Bytes: size, File: id})
	if o.meta == nil {
		o.meta = map[string]any{}
	}
	o.meta["image"] = id
}

// runBash runs a shell call. A call marked background starts the command and
// answers with the process the browser can watch, since the output is not in
// yet.
func (e *Engine) runBash(ctx context.Context, s *Session, runner *tools.Runner, call llm.ToolCall) (tools.Result, error) {
	raw := json.RawMessage(call.Function.Arguments)
	var args tools.RunShellArgs
	if err := tools.DecodeArgs("bash", raw, &args); err != nil {
		return tools.Result{}, err
	}
	if !args.Background {
		return e.watchBash(ctx, s, runner, call, args, runner.ShellTimeout(raw))
	}
	pr, err := e.procs.Start(e.ctx, runner, s.ID, call.ID, args)
	if err != nil {
		return tools.Result{}, err
	}
	out := fmt.Sprintf("Running in the background as %s (pid %d). The tail is in the Processes panel.", pr.ID, pr.PID)
	if pr.Spill != "" {
		out += " The whole output is at " + pr.Spill + "."
	}
	return tools.Result{
		Output: out,
		Meta:   map[string]any{"command": args.Command, "process": pr.ID, "background": true, "spill": pr.Spill},
	}, nil
}

// Actions the person can take on a bash call that is still being waited on.
const (
	shellBackground = "background"
	shellStop       = "stop"
)

// watchBash runs a bash call and waits on it until it ends, its timeout
// passes, or it has run for the configured time, when it moves to the
// background and the call returns what it has printed so far. The person can
// move it or stop it sooner.
func (e *Engine) watchBash(ctx context.Context, s *Session, runner *tools.Runner, call llm.ToolCall, args tools.RunShellArgs, timeout time.Duration) (tools.Result, error) {
	pr, err := e.procs.Watch(e.ctx, runner, s.ID, call.ID, args)
	if err != nil {
		return tools.Result{}, err
	}
	actions := make(chan string, 1)
	s.mu.Lock()
	if s.shells == nil {
		s.shells = map[string]chan string{}
	}
	s.shells[call.ID] = actions
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.shells, call.ID)
		s.mu.Unlock()
	}()

	began := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var after <-chan time.Time
	if wait := runner.BackgroundAfter; wait > 0 && wait < timeout {
		t := time.NewTimer(wait)
		defer t.Stop()
		after = t.C
	}
	end := func(timedOut, stopped bool) (tools.Result, error) {
		<-pr.done
		res := pr.handle.Result(runner.OutputLimit, pr.code, timedOut, stopped)
		if !timedOut && !stopped && pr.code == 0 {
			runner.NoteShellReads(args.Command)
		}
		return res, nil
	}
	moved := func(why string) (tools.Result, error) {
		if !e.procs.Promote(pr) {
			return end(false, false)
		}
		out, _ := pr.handle.Output(runner.OutputLimit)
		return tools.Result{
			Output: prompts.BashMoved(why, pr.view.ID, pr.view.PID, strings.TrimRight(out, "\n")),
			Meta:   map[string]any{"command": args.Command, "process": pr.view.ID, "background": true, "spill": pr.handle.SpillPath()},
		}, nil
	}
	select {
	case <-pr.done:
		return end(false, false)
	case <-ctx.Done():
		pr.handle.Stop()
		return end(false, true)
	case <-deadline.C:
		pr.handle.Stop()
		return end(true, false)
	case <-after:
		return moved(fmt.Sprintf("Still running after %ds", int(runner.BackgroundAfter/time.Second)))
	case act := <-actions:
		if act == shellStop {
			pr.handle.StopBecause("stopped by the user")
			return end(false, true)
		}
		return moved(fmt.Sprintf("The user moved it to the background after %ds", int(time.Since(began)/time.Second)))
	}
}

// ShellAction moves a bash call that is still being waited on to the
// background, or stops it.
func (e *Engine) ShellAction(id, callID, action string) error {
	if action != shellBackground && action != shellStop {
		return fmt.Errorf("Unknown action %q: use background or stop", action)
	}
	s, err := e.Get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	actions := s.shells[callID]
	s.mu.Unlock()
	if actions == nil {
		return ErrNoShell
	}
	select {
	case actions <- action:
	default:
	}
	return nil
}

// listProcesses reports the background commands of a session and the last
// lines of each.
func (e *Engine) listProcesses(s *Session) (tools.Result, error) {
	procs := e.procs.List(s.ID)
	if len(procs) == 0 {
		return tools.Result{Output: "No commands are running in the background."}, nil
	}
	var b strings.Builder
	for _, pr := range procs {
		b.WriteString(fmt.Sprintf("%s: %s (pid %d) %s", pr.ID, pr.Command, pr.PID, pr.Status))
		if pr.Status == "running" {
			b.WriteString(fmt.Sprintf(", running for %s, %d bytes",
				time.Since(time.UnixMilli(pr.StartedAt)).Truncate(time.Second), pr.Bytes))
		} else if pr.ExitCode != nil {
			b.WriteString(fmt.Sprintf(", exit %d, %d bytes", *pr.ExitCode, pr.Bytes))
		}
		b.WriteString("\n")
		tail := pr.Tail
		if len(tail) > 20 {
			tail = tail[len(tail)-20:]
		}
		for _, line := range tail {
			b.WriteString("  " + line + "\n")
		}
	}
	return tools.Result{Output: strings.TrimRight(b.String(), "\n")}, nil
}

// appendTool appends the tool entry that pairs a call with its result.
func (e *Engine) appendTool(s *Session, call llm.ToolCall, record *store.PermissionRecord, o outcome) store.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendEntry(store.Entry{Kind: "tool", Role: "tool",
		ToolCallID: call.ID, ToolName: call.Function.Name, Content: o.content,
		ExitCode: o.exitCode, Truncated: o.truncated, Permission: record, Meta: o.meta,
		Attachments: o.images, InContext: true, Visible: true})
}

// refuseImages takes the pictures off the results that follow the last
// assistant entry, which the refused request was the first to carry, and says
// in each result why it was not seen. It reports whether there were any.
func (e *Engine) refuseImages(s *Session, err error) bool {
	s.mu.Lock()
	var dropped []string
	for i := len(s.Entries) - 1; i >= 0 && s.Entries[i].Kind != "assistant"; i-- {
		entry := &s.Entries[i]
		if entry.Kind != "tool" || len(entry.Attachments) == 0 {
			continue
		}
		for _, a := range entry.Attachments {
			dropped = append(dropped, a.File)
		}
		entry.Content = fmt.Sprintf("%s was not sent: %s", entry.Attachments[0].Path, err)
		entry.Attachments = nil
		entry.Meta = map[string]any{"error": true}
	}
	s.mu.Unlock()
	if len(dropped) == 0 {
		return false
	}
	for _, id := range dropped {
		e.files.Remove(id)
	}
	e.save(s)
	e.publish(s.ID, ev("resync", nil))
	return true
}

// recordBaseline keeps one file as it stood before this session first wrote
// to it. Later writes to the same path are ignored, so the comparison stays
// against the state the session started from.
func (e *Engine) recordBaseline(s *Session, sn tools.Snapshot) {
	if sn.Path == "" {
		return
	}
	s.mu.Lock()
	for _, b := range s.Baselines {
		if b.Path == sn.Path {
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()

	b := store.Baseline{Path: sn.Path, State: baselineState(sn.State), At: time.Now().UTC()}
	if b.State == store.BaselineText {
		b.Blob = store.BlobName(sn.Path)
		if err := e.store.WriteBlob(s.ID, b.Blob, sn.Content); err != nil {
			// Nothing was kept, so the file is left out of the change list
			// rather than listed against a copy that is not there.
			e.publish(s.ID, ev("error", map[string]any{"kind": "save", "message": err.Error()}))
			return
		}
	}

	s.mu.Lock()
	// The lock was dropped for the copy, so the path is checked once more.
	for _, have := range s.Baselines {
		if have.Path == b.Path {
			s.mu.Unlock()
			return
		}
	}
	s.Baselines = append(s.Baselines, b)
	s.mu.Unlock()
	e.saveSoon(s)
}

// baselineState maps what the tool saw onto what the session stores.
func baselineState(snapshot string) string {
	switch snapshot {
	case tools.SnapshotText:
		return store.BaselineText
	case tools.SnapshotBinary:
		return store.BaselineBinary
	case tools.SnapshotLarge:
		return store.BaselineLarge
	default:
		return store.BaselineNew
	}
}

// argSummary pulls the command and the path out of the raw arguments for the
// permission card, tolerating arguments the model malformed. The card shows
// what the call is about, and for a search that is the query, which the card
// reads as the command.
func argSummary(call llm.ToolCall) (command, path string) {
	var args struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		Query   string `json:"query"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "", ""
	}
	if args.Command == "" {
		return args.Query, args.Path
	}
	return args.Command, args.Path
}

// compact replaces the in-context set with one summary of everything but the
// latest messages, which it keeps word for word. A failure leaves the history
// untouched, so a failed compaction never loses context.
//
// The request carries the turn's tools and effort, which a chat template
// renders at the head of the prompt. With whole set it sends the conversation
// as the server holds it and adds the instruction after, so only the
// instruction is read. Without it the request stops where the kept messages
// begin, which is what a window that has overflowed can still take. A tool
// call is ignored.
func (e *Engine) compact(ctx context.Context, s *Session, back Backend, a config.Agent, b brief, caps llm.Caps, whole bool) error {
	s.mu.Lock()
	before := s.computeFill()
	start := s.tailStart()
	kept := start < len(s.Entries)
	tailSeq := 0
	if kept {
		tailSeq = s.Entries[start].Seq
	}
	s.mu.Unlock()
	end := start
	if whole {
		end = -1
	}
	// A summary of the whole conversation covers the kept messages too, so
	// it is not told they follow it.
	msgs := append(e.messagesUntil(s, a, b, end), llm.Message{Role: "user", Content: prompts.Compact(kept && !whole)})

	// A summary is a model call like any other, and is kept on the entry it
	// produced so the turn that asked for it counts it. Its own prompt counted
	// the messages the summary replaces, so it is marked and never read as a
	// measure of what is in context afterwards.
	sent := time.Now()
	res, err := back.Chat(ctx, llm.ChatRequest{
		Model: a.Model, Messages: msgs, Tools: tools.Schemas(b.offer), Effort: a.Effort,
		PreserveReasoning: a.PreserveReasoning,
		Temperature:       a.Temperature, TopP: a.TopP, TopK: a.TopK,
		RepeatPenalty: a.RepeatPenalty, Seed: a.Seed, Caps: caps}, nil)
	if err != nil {
		return err
	}
	rec := callUsage(res, sent, time.Time{}, e.schemaTokens(s), false)
	rec.Compaction = true
	summary := strings.TrimSpace(res.Content)
	if llm.CutOff(res.FinishReason) {
		return errors.New("The summary was cut off at the token limit: raise the agent's max tokens, or compact sooner")
	}
	if summary == "" && len(res.ToolCalls) > 0 {
		return errors.New("The model called a tool instead of writing a summary: try again or start a new session")
	}
	if summary == "" {
		return errors.New("The model returned an empty summary: try again or start a new session")
	}

	s.mu.Lock()
	// The kept messages are found again by sequence number, since the turn
	// may have appended since the request was made.
	for i := range s.Entries {
		if s.Entries[i].Seq < tailSeq || !kept {
			s.Entries[i].InContext = false
		}
	}
	s.Compactions++
	meta := map[string]any{"before": before, "n": s.Compactions}
	if kept {
		meta["tail"] = tailSeq
	}
	// The summarised messages may hold what the work still needs: the rules
	// of the mode they were sent in, or the plan being carried out. Both are
	// kept as they stood, so the message does not change under the cache.
	mode := s.modeAt(tailSeq, kept)
	meta["mode"] = string(mode)
	if carry := s.carryFor(mode, b, tailSeq, kept); carry != "" {
		meta["carry"] = carry
	}
	// The plan is kept as it stood rather than read live, so the message
	// does not change under the prompt cache as steps are marked.
	if s.carriesTodos() {
		meta["todos"] = renderTodos(s.Todos)
	}
	// The last message the user sent is carried in the summary unless it is
	// one of the messages kept.
	for i := len(s.Entries) - 1; i >= 0; i-- {
		if s.Entries[i].Kind == "user" {
			if !kept || s.Entries[i].Seq < tailSeq {
				meta["last_user"] = s.Entries[i].Content
			}
			break
		}
	}
	s.appendEntry(store.Entry{Kind: "compaction", Role: "user",
		Content: summary, InContext: true, Visible: true, Meta: meta, Usage: rec})
	after := s.computeFill()
	s.Entries[len(s.Entries)-1].Meta["after"] = after
	s.mu.Unlock()

	e.save(s)
	e.publish(s.ID, ev("compacted", map[string]any{"before": before, "after": after, "summary": summary}))
	return nil
}
