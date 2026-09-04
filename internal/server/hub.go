package server

import (
	"encoding/json"
	"sync"

	"quecto-harness/internal/agent"
)

const (
	ringSize     = 512
	subscriberQ  = 256
	resyncEvType = "resync"
)

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
}

type channel struct {
	seq    int64
	ring   []frame
	base   int64
	subs   map[int64]*subscriber
	nextID int64
}

type subscriber struct {
	ch      chan frame
	dropped bool
}

func NewHub() *Hub {
	return &Hub{sessions: map[string]*channel{}}
}

// Publish assigns a sequence number, appends to the ring, and sends without
// blocking. A subscriber that cannot keep up is told to resync rather than
// disconnected.
func (h *Hub) Publish(sessionID string, ev agent.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.channelLocked(sessionID)
	c.seq++
	f := frame{Seq: c.seq, Type: ev.Type, Data: encode(ev.Type, c.seq, ev.Fields)}
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

// Drop discards a session's channel, which ends every attached event source.
func (h *Hub) Drop(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.sessions[sessionID]
	if !ok {
		return
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
	out := map[string]any{"type": kind, "seq": seq}
	for k, v := range fields {
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"type": "error", "seq": seq,
			"kind": "internal", "message": "an event could not be encoded"})
	}
	return b
}
