package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
)

const (
	basaltDocs  = `{"version":"0.3.3","capabilities":["answer_now","logprobs","documents"]}`
	basaltVideo = `{"version":"0.3.3","capabilities":["answer_now","logprobs","documents","video"]}`
)

// basalt puts the harness's agent on the Basalt protocol, with /props
// carrying the given key.
func (h *harness) basalt(t *testing.T, key string) {
	t.Helper()
	h.replay.mu.Lock()
	h.replay.Basalt = key
	h.replay.mu.Unlock()
	h.setAgent(t, func(a *config.Agent) { a.Protocol = config.ProtocolBasalt })
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBasaltIsSentTheSessionAndRequest(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	h.basalt(t, basaltDocs)
	s := h.session(t)
	if _, err := h.engine.Send(s.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	h.replay.mu.Lock()
	hdr := h.replay.Headers[0]
	h.replay.mu.Unlock()
	if hdr.Get("X-Session-Id") != s.ID {
		t.Errorf("session header = %q, want %q", hdr.Get("X-Session-Id"), s.ID)
	}
	if !strings.HasPrefix(hdr.Get("X-Client-Request-Id"), "qh-") {
		t.Errorf("request id header = %q", hdr.Get("X-Client-Request-Id"))
	}
}

// Against llama.cpp nothing changes, even where the server is Basalt.
func TestLlamaCppIsSentNoHeaders(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	h.replay.Basalt = basaltVideo
	s := h.session(t)
	if _, err := h.engine.Send(s.ID, "hello", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	h.replay.mu.Lock()
	hdr := h.replay.Headers[0]
	h.replay.mu.Unlock()
	if hdr.Get("X-Session-Id") != "" || hdr.Get("X-Client-Request-Id") != "" {
		t.Errorf("llama.cpp was sent %v", hdr)
	}
}

func TestAnswerNowNamesTheRequestUnderWay(t *testing.T) {
	h := newHarness(t, "stream_thinking_hold.txt")
	h.basalt(t, basaltDocs)
	s := h.session(t)
	if _, err := h.engine.Send(s.ID, "think", nil); err != nil {
		t.Fatal(err)
	}
	waitPaused(t, h)
	if waitForEvents(t, h, "answer_now", 1) < 1 {
		t.Fatal("no answer_now event")
	}
	took, err := h.engine.AnswerNow(context.Background(), s.ID)
	if err != nil || !took {
		t.Fatalf("answer now = %v %v", took, err)
	}
	h.replay.mu.Lock()
	sent, answered := h.replay.Headers[0].Get("X-Client-Request-Id"), append([]string(nil), h.replay.Answered...)
	h.replay.mu.Unlock()
	if len(answered) != 1 || answered[0] != sent {
		t.Errorf("answered %v, request sent as %q", answered, sent)
	}
	h.engine.Stop(s.ID)
	waitIdle(t, s)

	// With the request over there is nothing to end, and nothing is sent.
	took, err = h.engine.AnswerNow(context.Background(), s.ID)
	if err != nil || took {
		t.Errorf("after the turn: %v %v", took, err)
	}
	time.Sleep(10 * time.Millisecond)
	h.replay.mu.Lock()
	defer h.replay.mu.Unlock()
	if len(h.replay.Answered) != 1 {
		t.Errorf("answered %v", h.replay.Answered)
	}
}
