package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/store"
)

// mediaReadTimeout bounds the read that asks the model server what it takes,
// which runs inside the request that sends the message.
const mediaReadTimeout = 5 * time.Second

// readable is what the model server can be sent beyond text and images.
// Unsure is set when the model's own features could not be read, which
// happens on a server whose model is not loaded yet, and the server read for
// another model takes documents or video. A file is then sent and the
// server's refusal, if any, is the answer.
type readable struct {
	features llm.Features
	unsure   bool
}

// readableBy asks what the session's model takes. A prepared session already
// knows. Otherwise the model is read without loading it, and failing that the
// server is read without naming one.
func (e *Engine) readableBy(s *Session) *readable {
	s.mu.Lock()
	prepared, features, child, agentID := s.prepared, s.features, s.Parent != "", s.AgentID
	s.mu.Unlock()
	if prepared {
		return &readable{features: features}
	}
	a, err := e.active()
	if child {
		var ok bool
		if a, ok = e.cfg.Config().AgentByID(agentID); !ok {
			err = ErrNoAgent
		}
	}
	if err != nil {
		return &readable{}
	}
	ctx, cancel := context.WithTimeout(e.ctx, mediaReadTimeout)
	defer cancel()
	back := e.backends.For(a)
	if p, err := back.Props(ctx, a.Model); err == nil {
		return &readable{features: p.Features}
	}
	if p, err := back.Props(ctx, ""); err == nil {
		other := p.Model != a.Model && (p.Features.Documents || p.Features.Video)
		return &readable{features: p.Features, unsure: other}
	}
	return &readable{}
}

// takes refuses a file of a kind the server does not read.
func (r *readable) takes(path, kind string) error {
	switch {
	case r.unsure:
	case kind == "document" && !r.features.Documents:
		return fmt.Errorf("Attach %s: the model server does not read documents", path)
	case kind == "video" && !r.features.Video:
		return fmt.Errorf("Attach %s: the model does not read video on this server", path)
	}
	return nil
}

// mediaPart is the part an attachment travels as, or false when it is not
// sent: a kind this server does not read, or a file the server refused.
func mediaPart(a store.Attachment, raw []byte, f llm.Features) (llm.Part, bool) {
	if a.Refused != "" {
		return llm.Part{}, false
	}
	switch a.Kind {
	case "image":
		return llm.ImagePart(mimeOf(raw), raw), true
	case "document":
		if f.Documents {
			return llm.DocumentPart(filepath.Base(a.Path), a.Mime, raw), true
		}
	case "video":
		if f.Video {
			return llm.VideoPart(a.Mime, raw), true
		}
	}
	return llm.Part{}, false
}

// notSentReason is why an attachment that has no part was left out.
func notSentReason(a store.Attachment, f llm.Features) string {
	switch {
	case a.Refused != "":
		return a.Refused
	case a.Kind == "document" && !f.Documents:
		return "the model server does not read documents"
	case a.Kind == "video" && !f.Video:
		return "the model does not read video on this server"
	}
	return "the file is no longer stored"
}

// refuseMedia marks the documents and videos of the newest message that a
// refusal names, so the next request goes without them. A document is named
// by its file name, a video by the word video. A refusal of the first request
// to carry the message, naming none of them, marks them all: a body too large
// for a proxy in front of the server names no file. It reports whether it
// marked any.
func (e *Engine) refuseMedia(s *Session, err error) bool {
	msg := llm.Reason(err)
	if msg == "" {
		return false
	}
	s.mu.Lock()
	i := s.lastUserEntry()
	if i < 0 {
		s.mu.Unlock()
		return false
	}
	var pending []*store.Attachment
	var named []*store.Attachment
	for j := range s.Entries[i].Attachments {
		a := &s.Entries[i].Attachments[j]
		if a.Refused != "" || (a.Kind != "document" && a.Kind != "video") {
			continue
		}
		pending = append(pending, a)
		if strings.Contains(msg, filepath.Base(a.Path)) ||
			a.Kind == "video" && strings.Contains(strings.ToLower(msg), "video") {
			named = append(named, a)
		}
	}
	if len(named) == 0 && !answeredSince(s.Entries, i) {
		named = pending
	}
	for _, a := range named {
		a.Refused = msg
	}
	s.mu.Unlock()
	if len(named) == 0 {
		return false
	}
	e.save(s)
	e.publish(s.ID, ev("resync", nil))
	return true
}

// answeredSince reports whether a reply has landed after entry i, which means
// a request carrying it was taken. The caller holds the lock.
func answeredSince(entries []store.Entry, i int) bool {
	for _, e := range entries[i+1:] {
		if e.Kind == "assistant" {
			return true
		}
	}
	return false
}
