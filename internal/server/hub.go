package server

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"quecto-harness/internal/agent"
)

const (
	ringSize     = 512
	subscriberQ  = 256
	resyncEvType = "resync"
	statusEvType = "status"
)

// The events a model streams a token at a time. They are the only ones joined
// before they go out.
const (
	deltaEvType     = "delta"
	reasoningEvType = "reasoning"
)

// coalesceWindow is how long a run of streamed text is held before it goes out
// as one frame. A frame per token is thousands a turn, each costing the browser
// a redraw of the whole message, so a page that falls behind never catches up.
// It also keeps the replay ring worth having: a ring of joined text covers half
// a minute of streaming, where one of tokens covers under a second.
const coalesceWindow = 50 * time.Millisecond

// frame is one event as it goes on the wire, with the sequence number the hub
// assigned it.
type frame struct {
	Seq  int64
	Type string
	Data []byte
}

// Hub fans one session's events out to every attached browser and keeps a
// short replay buffer so a reconnect does not lose the middle of a turn.
type Hub struct {
	mu       sync.Mutex
	sessions map[string]*channel
	// watchers follow every session's status at once, for the list that
	// shows which are working.
	watchers    map[int64]chan []byte
	nextWatcher int64
}

type channel struct {
	seq    int64
	ring   []frame
	base   int64
	subs   map[int64]*subscriber
	nextID int64
	run    *textRun
}

// textRun is streamed text waiting to go out, with the timer that sends it
// when nothing follows.
type textRun struct {
	kind  string
	text  strings.Builder
	timer *time.Timer
}

type subscriber struct {
	ch      chan frame
	dropped bool
}

func NewHub() *Hub {
	return &Hub{sessions: map[string]*channel{}, watchers: map[int64]chan []byte{}}
}

// Publish sends one event. Streamed text is joined into a run and goes out a
// window later; anything else sends the run waiting before it, so what reaches
// a browser is in the order it was published.
func (h *Hub) Publish(sessionID string, ev agent.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.channelLocked(sessionID)
	if ev.Type == deltaEvType || ev.Type == reasoningEvType {
		h.holdLocked(sessionID, c, ev)
		return
	}
	h.sendLocked(c)
	h.emitLocked(c, ev.Type, ev.Fields)
	if ev.Type == statusEvType {
		h.tellWatchersLocked(sessionID, ev.Fields["state"])
	}
}

// tellWatchersLocked sends one session's status to every watcher. A watcher
// that cannot keep up is closed, and the list it reopens with is current.
func (h *Hub) tellWatchersLocked(sessionID string, state any) {
	data, _ := json.Marshal(map[string]any{"session": sessionID, "state": state})
	for id, ch := range h.watchers {
		select {
		case ch <- data:
		default:
			close(ch)
			delete(h.watchers, id)
		}
	}
}

// Watch returns a channel of every session's status changes and the function
// that detaches it. The channel is closed when the watcher falls behind.
func (h *Hub) Watch() (<-chan []byte, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextWatcher
	h.nextWatcher++
	ch := make(chan []byte, subscriberQ)
	h.watchers[id] = ch
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.watchers[id]; ok {
			close(ch)
			delete(h.watchers, id)
		}
	}
}

// holdLocked adds text to the run a session is building, starting one when
// there is none. Thinking and the reply are separate blocks on the page, so a
// run holds one kind and a change of kind ends it.
func (h *Hub) holdLocked(sessionID string, c *channel, ev agent.Event) {
	if c.run != nil && c.run.kind != ev.Type {
		h.sendLocked(c)
	}
	if c.run == nil {
		run := &textRun{kind: ev.Type}
		c.run = run
		// Timed from the first token rather than the last, so text keeps
		// arriving while the model is still generating.
		run.timer = time.AfterFunc(coalesceWindow, func() { h.expire(sessionID, run) })
	}
	text, _ := ev.Fields["text"].(string)
	c.run.text.WriteString(text)
}

// sendLocked puts the run a session has waiting on the wire.
func (h *Hub) sendLocked(c *channel) {
	run := c.run
	if run == nil {
		return
	}
	run.timer.Stop()
	c.run = nil
	if run.text.Len() > 0 {
		h.emitLocked(c, run.kind, map[string]any{"text": run.text.String()})
	}
}

// expire sends a run the window ran out on, which is what ends the last line
// of a turn when no event follows it. A run already sent is left alone, since
// the timer can fire while another goroutine holds the lock.
func (h *Hub) expire(sessionID string, run *textRun) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.sessions[sessionID]
	if !ok || c.run != run {
		return
	}
	h.sendLocked(c)
}

// emitLocked assigns a sequence number, appends to the ring, and sends without
// blocking. A subscriber that cannot keep up is told to resync rather than
// disconnected.
func (h *Hub) emitLocked(c *channel, kind string, fields map[string]any) {
	c.seq++
	f := frame{Seq: c.seq, Type: kind, Data: encode(kind, c.seq, fields)}
	c.append(f)
	for _, s := range c.subs {
		if s.dropped {
			continue
		}
		select {
		case s.ch <- f:
		default:
			// A tab that fell behind is told to refetch rather than being
			// disconnected, so the queue is cleared to make room for that.
			s.dropped = true
			drain(s.ch)
			s.ch <- frame{Seq: c.seq, Type: resyncEvType, Data: encode(resyncEvType, c.seq, nil)}
		}
	}
}

// Subscribe returns the replay for (from, seq], a live channel, and the
// function that detaches it. A from the ring no longer covers yields one
// resync frame instead.
func (h *Hub) Subscribe(sessionID string, from int64) (<-chan frame, func(), []frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.channelLocked(sessionID)
	s := &subscriber{ch: make(chan frame, subscriberQ)}
	id := c.nextID
	c.nextID++
	c.subs[id] = s

	var replay []frame
	switch {
	case from < 0:
		from = 0
	}
	switch {
	case from >= c.seq:
	case from < c.base-1:
		replay = []frame{{Seq: c.seq, Type: resyncEvType, Data: encode(resyncEvType, c.seq, nil)}}
	default:
		for _, f := range c.ring {
			if f.Seq > from {
				replay = append(replay, f)
			}
		}
	}

	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if cc, ok := h.sessions[sessionID]; ok {
			delete(cc.subs, id)
		}
	}
	return s.ch, cancel, replay
}

// Seq reports the last sequence number for a session, which is what a
// snapshot hands the client so nothing is missed between fetch and subscribe.
func (h *Hub) Seq(sessionID string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.sessions[sessionID]; ok {
		return c.seq
	}
	return 0
}

// Mark sends any streamed text still held and reports the last sequence
// number, so everything up to the mark is on the wire.
func (h *Hub) Mark(sessionID string) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.sessions[sessionID]
	if !ok {
		return 0
	}
	h.sendLocked(c)
	return c.seq
}

// Drop discards a session's channel, which ends every attached event source.
func (h *Hub) Drop(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.sessions[sessionID]
	if !ok {
		return
	}
	// Text still waiting has nowhere to go, and its timer would rebuild the
	// channel this is removing.
	if c.run != nil {
		c.run.timer.Stop()
		c.run = nil
	}
	for _, s := range c.subs {
		close(s.ch)
	}
	delete(h.sessions, sessionID)
}

func (h *Hub) channelLocked(sessionID string) *channel {
	c, ok := h.sessions[sessionID]
	if !ok {
		c = &channel{subs: map[int64]*subscriber{}, base: 1}
		h.sessions[sessionID] = c
	}
	return c
}

// drain empties a subscriber's queue without blocking.
func drain(ch chan frame) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func (c *channel) append(f frame) {
	c.ring = append(c.ring, f)
	if len(c.ring) > ringSize {
		c.ring = c.ring[len(c.ring)-ringSize:]
	}
	c.base = c.ring[0].Seq
}

// encode merges the event fields with its type and sequence number, which is
// the payload shape the browser reads.
func encode(kind string, seq int64, fields map[string]any) []byte {
	out := map[string]any{}
	for k, v := range fields {
		out[k] = v
	}
	// Written last: the browser reopens the stream from the sequence number it
	// last saw, so an event carrying a field of its own by that name would
	// rewind it.
	out["type"], out["seq"] = kind, seq
	b, err := json.Marshal(out)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"type": "error", "seq": seq,
			"kind": "internal", "message": "An event could not be encoded"})
	}
	return b
}
