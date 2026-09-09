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

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/prompts"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// titleTimeout and titleTokens bound the naming request, since it stands
// between the message and the first token of the reply.
const (
	titleTimeout = 30 * time.Second
	titleTokens  = 32
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
	ErrNoAddress  = errors.New("The agent has no address: add it in the agents dialog")
	ErrNoModel    = errors.New("The agent has no model: choose one in the agents dialog")
	ErrCompacting = errors.New("The conversation is being compacted: that finishes on its own")
)

// Engine owns every live session and dispatches turns.
type Engine struct {
	cfg      *config.Store
	store    *store.Sessions
	files    *store.Files
	backends Backends
	perms    *perms.Broker
	asks     *asker
	hub      Publisher

	ctx context.Context
	os  string

	mu   sync.Mutex
	live map[string]*Session
	wg   sync.WaitGroup
}

func NewEngine(ctx context.Context, cfg *config.Store, st *store.Sessions, files *store.Files,
	backends Backends, pb *perms.Broker, hub Publisher) *Engine {
	return &Engine{
		cfg: cfg, store: st, files: files, backends: backends, perms: pb, asks: newAsker(), hub: hub,
		ctx: ctx, os: runtime.GOOS, live: map[string]*Session{},
	}
}

// usable reports what stops an agent answering, so a message is refused where
// it was typed rather than part way into a turn.
func usable(a config.Agent) error {
	switch {
	case a.URL == "":
		return ErrNoAddress
	case a.Model == "":
		return ErrNoModel
	}
	return nil
}

// active is the agent every session answers with, checked before it is used.
func (e *Engine) active() (config.Agent, error) {
	a, ok := e.cfg.Config().Agent()
	if !ok {
		return config.Agent{}, ErrNoAgent
	}
	return a, usable(a)
}

// messages builds the prompt for a session. The image bytes are read between
// the two locks, so nothing does file IO while the session is held.
func (e *Engine) messages(s *Session, a config.Agent, b brief) []llm.Message {
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
	return s.contextMessages(a, e.os, b, images)
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
	e.publish(s.ID, ev("status", map[string]any{"state": string(st)}))
	if parent := s.Parent; parent != "" {
		e.publish(parent, ev("child", map[string]any{"session": s.ID, "state": string(st)}))
	}
}

func (e *Engine) save(s *Session) {
	s.mu.Lock()
	// A read that lands after the session was deleted must not write the file
	// back out.
	if s.gone {
		s.mu.Unlock()
		return
	}
	doc := s.doc()
	s.mu.Unlock()
	if err := e.store.Save(doc); err != nil {
		e.publish(s.ID, ev("error", map[string]any{"kind": "save", "message": err.Error()}))
	}
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
	s := newSession(store.NewID(), project, a.ID, a.Model)
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
	// A model that cannot read pictures refuses the message where it was
	// typed, rather than when the turn picks the message up. The check is
	// against the agent answering now, which is the one that will take it.
	if a, err := e.active(); err == nil {
		if err := e.checkImages(a, attachments); err != nil {
			return Sent{}, err
		}
	}
	return e.deliver(s, text, attachments)
}

// deliver starts a turn with the message, or queues it. Every path that
// delivers a message goes through here, so a turn that starts between the
// caller's check and this one queues the message rather than refusing it.
func (e *Engine) deliver(s *Session, text string, attachments []store.Attachment) (Sent, error) {
	return e.deliverFrom(s, "", text, attachments)
}

// deliverFrom is deliver with a sender. The entry keeps the message as it was
// written; the sender's name is put in front of it where the prompt is built,
// since that is the only place it means anything.
func (e *Engine) deliverFrom(s *Session, from, text string, attachments []store.Attachment) (Sent, error) {
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
		From: from, Attachments: attachments, InContext: true, Visible: true})
	if s.Title == "" {
		s.Title = store.Title(text)
	}
	s.mu.Unlock()

	e.save(s)
	e.publish(s.ID, ev("status", map[string]any{"state": string(StatePreflight)}))
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
		From: held.From, Attachments: held.Attachments, InContext: true, Visible: true})
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
		e.publishMessage(s, store.Entry{Seq: sent.Seq, Content: held.Text, From: held.From,
			Attachments: held.Attachments})
	}
}

// steerFields renders a queued message for the browser.
func steerFields(held *Steer) map[string]any {
	if held == nil {
		return map[string]any{"text": "", "attachments": []map[string]any{}}
	}
	return map[string]any{"text": held.Text, "from": held.From,
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
	i := s.lastUserEntry()
	if i < 0 || s.Entries[i].Seq != seq {
		s.mu.Unlock()
		return 0, ErrNotLast
	}
	attachments := s.Entries[i].Attachments
	s.mu.Unlock()

	// The message was accepted once, but the harness may have been pointed at
	// another agent since, and its model may not read pictures.
	if a, err := e.active(); err == nil {
		if err := e.checkImages(a, attachments); err != nil {
			return 0, err
		}
	}

	s.mu.Lock()
	if s.state != StateIdle {
		s.mu.Unlock()
		return 0, ErrBusy
	}
	// The lock was dropped for the check, so the message is found again.
	i = s.lastUserEntry()
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
	e.publish(s.ID, ev("status", map[string]any{"state": string(StatePreflight)}))
	e.startTurn(ctx, s, cancel)
	return entry.Seq, nil
}

// startTurn runs one turn in the background. The session is already in
// preflight with its cancel function set.
func (e *Engine) startTurn(ctx context.Context, s *Session, cancel context.CancelFunc) {
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

// checkImages asks the model server what the model takes before an image is
// sent to it, since a model with no vision answers a request carrying one
// with an error that says nothing about the picture.
func (e *Engine) checkImages(a config.Agent, attachments []store.Attachment) error {
	images := false
	for _, a := range attachments {
		if a.Kind == "image" {
			images = true
		}
	}
	if !images {
		return nil
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	models, err := e.backends.For(a).Models(ctx)
	if err != nil {
		return fmt.Errorf("Ask %s what %s takes: %w", a.Name, a.Model, err)
	}
	for _, m := range models {
		if m.ID != a.Model {
			continue
		}
		for _, mod := range m.Modalities {
			if mod == "image" {
				return nil
			}
		}
		return fmt.Errorf("%s does not take images: choose a model that does", a.Model)
	}
	return fmt.Errorf("%s is not on %s: choose another model for this agent", a.Model, a.Name)
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
	s.state = StateCompacting
	ctx, cancel := context.WithCancel(e.ctx)
	s.cancel = cancel
	s.mu.Unlock()

	e.publish(s.ID, ev("status", map[string]any{"state": string(StateCompacting)}))
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer cancel()
		if err := e.compact(ctx, s, e.backends.For(a), a, e.briefFor(s, e.cfg.Config()), e.turnCaps(s)); err != nil {
			e.endWithError(s, err, 0)
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
}

// titleSession names the conversation from its opening message, before the
// turn answers it.
//
// It goes first because the model server holds one prompt at a time. A title
// request made after the turn would evict what the turn left cached, and the
// next message would reprocess the conversation from the start. Made here, it
// runs against a slot that holds nothing worth keeping, and it carries the
// opening message alone rather than the conversation, so it is a few hundred
// tokens either way.
//
// A failure is not an error: the session keeps the title cut from the message.
func (e *Engine) titleSession(ctx context.Context, s *Session, back Backend, a config.Agent, caps llm.Caps) {
	s.mu.Lock()
	first, text := s.firstTurn(), s.firstUserMessage()
	s.mu.Unlock()
	if !first || strings.TrimSpace(text) == "" {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, titleTimeout)
	defer cancel()
	res, err := back.Chat(tctx, llm.ChatRequest{
		Model:       a.Model,
		Messages:    []llm.Message{{Role: "user", Content: prompts.Title(text)}},
		Effort:      llm.EffortOff,
		Temperature: a.Temperature,
		TopP:        a.TopP,
		Caps:        caps,
		MaxTokens:   titleTokens,
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

// probeContextBase measures the fixed parts of a prompt against the model that
// will read them. Two completions capped at one token carry the system message
// and differ only in whether the tool schemas are attached, so one prompt count
// is the system cost and the difference is the schema cost. Everything else in
// the context is then the remainder of a real usage report.
//
// It runs after a turn has streamed rather than beside the preparation, so a
// session that is ready to type into is not waiting on a measurement.
func (e *Engine) probeContextBase(s *Session, a config.Agent, b brief) {
	s.mu.Lock()
	caps := s.caps
	system := s.systemMessage(a, e.os, b)
	done := s.baseProbed || s.ContextBase.Fits(a.Model, s.sysChars)
	s.mu.Unlock()
	if a.Model == "" || done {
		return
	}
	chars := len(system.Content)
	// Counted like a turn, so a shutdown waits for the write it ends in
	// rather than racing it.
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
		defer cancel()
		count := func(schemas []llm.Tool) (int, bool) {
			res, err := e.backends.For(a).Chat(ctx, llm.ChatRequest{
				Model: a.Model, Messages: []llm.Message{system}, Tools: schemas,
				Effort: llm.EffortOff, Caps: caps, MaxTokens: 1}, nil)
			if err != nil || res.Usage == nil || res.Usage.PromptTokens <= 0 {
				return 0, false
			}
			return res.Usage.PromptTokens, true
		}
		bare, okBare := count(nil)
		armed, okArmed := count(tools.Schemas(b.offer))

		s.mu.Lock()
		s.baseProbed = true
		stale := s.Model != a.Model || s.sysChars != chars
		measured := okBare && okArmed && armed >= bare && !stale
		if measured {
			s.ContextBase = &store.ContextBase{Model: a.Model, Chars: chars, System: bare, Tools: armed - bare}
			// The turn that triggered the probe already reported its usage, so
			// it is attributed here rather than a turn later.
			if s.LastUsage != nil && s.lastSentTools {
				s.LastToolTokens = s.ContextBase.Tools
			}
		}
		s.mu.Unlock()
		if !measured {
			return
		}
		e.save(s)
		e.publishUsage(s)
	}()
}

// fail ends the turn on the stream. The message is not kept: it says what went
// wrong with one request, which is of no use to a reader of the session later
// and of none at all to the model.
func (e *Engine) fail(s *Session, err error) {
	kind := string(llm.Kind(err))
	if kind == "" {
		kind = "internal"
	}
	e.setState(s, StateFailed)
	e.publish(s.ID, ev("error", map[string]any{"kind": kind, "message": err.Error()}))
	e.finish(s, StateIdle, "error", 0, nil)
	e.flushSteer(s)
}

// turnCost is what one turn spent, summed over every request it made. Elapsed
// covers the tool calls between those requests; generating does not, so the
// rate below is the model's and not the harness's.
type turnCost struct {
	started    time.Time
	requests   int
	tokens     int
	generating time.Duration
}

// add folds in one finished request. first is when its stream began to
// produce, which is zero for a request that produced nothing.
func (c *turnCost) add(res llm.Result, first time.Time) {
	c.requests++
	if res.Usage != nil {
		c.tokens += res.Usage.CompletionTokens
	}
	if !first.IsZero() {
		c.generating += time.Since(first)
	}
}

// fields renders the numbers for the transcript. The rate is measured rather
// than read from the response, since only some servers report one.
func (c *turnCost) fields() map[string]any {
	rate := 0.0
	if secs := c.generating.Seconds(); secs > 0 {
		rate = float64(c.tokens) / secs
	}
	return map[string]any{
		"tokens":    c.tokens,
		"ms":        time.Since(c.started).Milliseconds(),
		"tok_per_s": rate,
		"requests":  c.requests,
	}
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
// state a new message is accepted in. cost is nil for a turn that ended
// before it could be counted.
func (e *Engine) finish(s *Session, next State, finishReason string, iterations int, cost map[string]any) {
	s.mu.Lock()
	s.state = next
	s.cancel = nil
	s.mu.Unlock()
	fields := map[string]any{"finish_reason": finishReason, "iterations": iterations}
	for k, v := range cost {
		fields[k] = v
	}
	e.publish(s.ID, ev("done", fields))
	e.publish(s.ID, ev("status", map[string]any{"state": string(next)}))
}

// noteCost records what the turn spent on the assistant entry that ended it,
// so a reloaded page draws the line the stream drew.
func (e *Engine) noteCost(s *Session, cost map[string]any) {
	s.mu.Lock()
	i := s.lastUsageEntry
	if i >= 0 && i < len(s.Entries) && s.Entries[i].Kind == "assistant" {
		if s.Entries[i].Meta == nil {
			s.Entries[i].Meta = map[string]any{}
		}
		for k, v := range cost {
			s.Entries[i].Meta[k] = v
		}
	}
	s.mu.Unlock()
	e.save(s)
}

func (e *Engine) run(ctx context.Context, s *Session) {
	a, err := e.ensureReady(ctx, s)
	if err != nil {
		e.endWithError(s, err, 0)
		return
	}
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
		e.titleSession(ctx, s, back, a, caps)
	}
	runner, rerr := tools.NewRunner(s.Project, cfg)
	if rerr != nil {
		e.fail(s, rerr)
		return
	}
	runner.Baseline = func(sn tools.Snapshot) { e.recordBaseline(s, sn) }

	// The tool call budget for this turn. Zero is no budget at all, so the
	// only thing that ends a runaway turn is the Stop button.
	budget := cfg.MaxToolCalls
	calls := 0
	cost := turnCost{started: time.Now()}

	for iteration := 0; ; iteration++ {
		e.setState(s, StatePreflight)
		// A message typed during the last step joins the history here, once
		// the work that was already under way has finished. It lands before
		// the context is measured, so one large enough to need it compacts.
		e.absorbSteer(s)

		s.mu.Lock()
		fill := s.computeFill()
		nctx, _ := s.window(a)
		s.mu.Unlock()
		// A window nothing reported and nothing overrode is no limit, so a
		// session against one compacts when the reader asks and not before.
		if nctx > 0 && float64(fill) >= cfg.CompactAt*float64(nctx) {
			e.setState(s, StateCompacting)
			if err := e.compact(ctx, s, back, a, b, caps); err != nil {
				e.endWithError(s, err, iteration)
				return
			}
		}

		// A spent budget takes the tools out of the request rather than ending
		// the turn where it stands, so the last thing the reader sees is an
		// answer written with what the model already found.
		spent := budget > 0 && calls >= budget
		if spent {
			e.noteLimit(s, calls)
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
		// The agent's cap is the reply's. The title request and the context
		// measurement set their own, since what they ask for is not a reply.
		if a.MaxTokens != nil {
			req.MaxTokens = *a.MaxTokens
		}
		if !spent {
			req.Tools = tools.Schemas(b.offer)
		}
		s.mu.Unlock()

		// first is when this request began to produce, which is what the
		// generation rate is measured against. Chat calls on synchronously
		// from its read loop, so no lock is needed here.
		var first time.Time
		res, err := back.Chat(ctx, req, func(event llm.Event) error {
			if first.IsZero() && carriesOutput(event.Kind) {
				first = time.Now()
			}
			e.forward(s, event)
			return nil
		})
		if err != nil {
			e.endWithError(s, err, iteration)
			return
		}
		cost.add(res, first)
		if res.Dropped > 0 {
			e.publish(s.ID, ev("error", map[string]any{"kind": "malformed",
				"message": fmt.Sprintf("The model server sent %d frames that could not be read: some output was lost", res.Dropped)}))
		}
		if res.Usage != nil {
			s.mu.Lock()
			s.LastUsage = res.Usage
			s.lastSentTools = !spent
			s.LastToolTokens = 0
			if !spent && s.ContextBase != nil && s.ContextBase.Model == s.Model {
				s.LastToolTokens = s.ContextBase.Tools
			}
			s.mu.Unlock()
		}

		// Calls that arrive after the budget is spent were not offered and are
		// not run, so they are dropped rather than left in the history unpaired.
		if len(res.ToolCalls) == 0 || spent {
			s.mu.Lock()
			s.appendEntry(store.Entry{Kind: "assistant", Role: "assistant",
				Content: res.Content, Reasoning: res.Reasoning, InContext: true, Visible: true})
			s.lastUsageEntry = len(s.Entries) - 1
			s.mu.Unlock()
			e.save(s)
			e.probeContextBase(s, a, b)
			e.publishUsage(s)
			// A message that landed while the model was answering makes this
			// the middle of the turn rather than the end of it.
			if e.absorbSteer(s) {
				continue
			}
			e.setState(s, StateFinal)
			counted := cost.fields()
			e.noteCost(s, counted)
			e.finish(s, StateIdle, res.FinishReason, iteration+1, counted)
			// One that landed between the check above and the session going
			// idle has no turn to join, so it starts one of its own.
			e.flushSteer(s)
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

		for i, call := range res.ToolCalls {
			calls++
			e.setState(s, StateDeciding)
			decision, record, stopped := e.decide(ctx, s, cfg, a, call)
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

}

// noteLimit records that the budget ran out, so the transcript says why the
// model stopped reaching for tools.
func (e *Engine) noteLimit(s *Session, calls int) {
	text := fmt.Sprintf("Tool call limit reached after %d calls: the model was asked to finish without tools", calls)
	s.mu.Lock()
	entry := s.appendEntry(store.Entry{Kind: "status", Content: text, InContext: false, Visible: true})
	s.mu.Unlock()
	e.save(s)
	e.publish(s.ID, ev("status_entry", map[string]any{"content": entry.Content}))
}

// endWithError separates a cancellation, which is a stop, from a failure.
func (e *Engine) endWithError(s *Session, err error, iteration int) {
	if llm.Kind(err) == llm.KindCanceled || errors.Is(err, context.Canceled) {
		e.markStopped(s)
		return
	}
	e.fail(s, err)
}

func (e *Engine) markStopped(s *Session) {
	s.mu.Lock()
	entry := s.appendEntry(store.Entry{Kind: "status", Content: "Stopped", InContext: false, Visible: true})
	s.mu.Unlock()
	e.setState(s, StateStopped)
	e.save(s)
	// A stop between two requests draws nothing of its own, so the marker is
	// published rather than left for the next time the page is loaded.
	e.publish(s.ID, ev("status_entry", map[string]any{"content": entry.Content}))
	e.finish(s, StateIdle, "stopped", 0, nil)
	e.flushSteer(s)
}

// stopWith records the refused call so the assistant message keeps its pair
// before the turn ends.
func (e *Engine) stopWith(s *Session, call llm.ToolCall, record *store.PermissionRecord) {
	s.mu.Lock()
	s.appendEntry(store.Entry{Kind: "tool", Role: "tool", ToolCallID: call.ID,
		ToolName: call.Function.Name, Content: "stopped by user",
		Permission: record, InContext: true, Visible: true})
	s.mu.Unlock()
	e.save(s)
	e.markStopped(s)
}

func (e *Engine) forward(s *Session, event llm.Event) {
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

func (e *Engine) publishUsage(s *Session) {
	s.mu.Lock()
	u := s.LastUsage
	fill := s.computeFill()
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
		"prompt_tokens": u.PromptTokens, "completion_tokens": u.CompletionTokens,
		"cached_tokens": u.CachedTokens, "fill": fill, "n_ctx": nctx}
	if parts != nil {
		payload["breakdown"] = parts
	}
	e.publish(s.ID, ev("usage", payload))
}

// decide applies the policy and, when it says prompt, blocks on the browser.
// The third result reports that the session was stopped while waiting.
func (e *Engine) decide(ctx context.Context, s *Session, cfg config.Config, a config.Agent, call llm.ToolCall) (perms.Decision, *store.PermissionRecord, bool) {
	tool := call.Function.Name
	command, path := argSummary(call)
	record := &store.PermissionRecord{Tool: tool, Command: command, Path: path}

	if e.perms.Evaluate(s.Project, tool, command, cfg.Mode) == perms.ActionAllow {
		// A call skips the card two ways, and the record keeps them apart.
		// "auto" is a policy that allowed this call, "full" is no policy.
		record.Decision = "auto"
		if cfg.Mode == config.ModeFull {
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

// execute runs the tool or writes the denial, and appends the tool entry that
// pairs with the assistant message.
func (e *Engine) execute(ctx context.Context, s *Session, runner *tools.Runner,
	call llm.ToolCall, d perms.Decision, record *store.PermissionRecord, index int) store.Entry {
	content := ""
	var exitCode *int
	truncated := false
	var meta map[string]any

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
		case "find":
			res, err = runner.Find(ctx, args)
		case "write_file":
			res, err = runner.WriteFile(ctx, args)
		case "replace_in_file":
			res, err = runner.ReplaceInFile(ctx, args)
		case "run_shell":
			res, err = runner.RunShell(ctx, args)
		case "write_todos":
			res, err = e.writeTodos(s, args)
		case "ask_question":
			res, err = e.askQuestion(ctx, s, call)
		case "run_agent":
			res, err = e.runAgent(s, call)
		case "send_message":
			res, err = e.sendMessage(s, call)
		default:
			err = fmt.Errorf("no tool named %s: use read_file, find, write_file, replace_in_file, run_shell, write_todos, ask_question, run_agent or send_message", call.Function.Name)
		}
		if err != nil {
			content = err.Error()
		} else {
			content, exitCode, truncated, meta = res.Output, res.ExitCode, res.Truncated, res.Meta
		}
	}

	s.mu.Lock()
	entry := s.appendEntry(store.Entry{Kind: "tool", Role: "tool",
		ToolCallID: call.ID, ToolName: call.Function.Name, Content: content,
		ExitCode: exitCode, Truncated: truncated, Permission: record, Meta: meta,
		InContext: true, Visible: true})
	s.mu.Unlock()
	return entry
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
	e.save(s)
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
// permission card, tolerating arguments the model malformed.
func argSummary(call llm.ToolCall) (command, path string) {
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
func (e *Engine) compact(ctx context.Context, s *Session, back Backend, a config.Agent, b brief, caps llm.Caps) error {
	msgs := append(e.messages(s, a, b), llm.Message{Role: "user", Content: prompts.Compact()})
	s.mu.Lock()
	before := s.computeFill()
	s.mu.Unlock()

	res, err := back.Chat(ctx, llm.ChatRequest{
		Model: a.Model, Messages: msgs, Effort: llm.EffortOff,
		PreserveReasoning: a.PreserveReasoning,
		Temperature:       a.Temperature, TopP: a.TopP, TopK: a.TopK,
		RepeatPenalty: a.RepeatPenalty, Seed: a.Seed, Caps: caps}, nil)
	if err != nil {
		return err
	}
	summary := strings.TrimSpace(res.Content)
	if summary == "" {
		return errors.New("The model returned an empty summary: try again or start a new session")
	}

	s.mu.Lock()
	for i := range s.Entries {
		s.Entries[i].InContext = false
	}
	s.Compactions++
	s.appendEntry(store.Entry{Kind: "compaction", Role: "user",
		Content: summary, InContext: true, Visible: true,
		Meta: map[string]any{"before": before, "n": s.Compactions}})
	s.LastUsage = nil
	s.LastToolTokens = 0
	s.lastUsageEntry = len(s.Entries) - 1
	after := s.computeFill()
	s.Entries[len(s.Entries)-1].Meta["after"] = after
	s.mu.Unlock()

	e.save(s)
	e.publish(s.ID, ev("compacted", map[string]any{"before": before, "after": after, "summary": summary}))
	return nil
}
