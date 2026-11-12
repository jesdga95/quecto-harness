package agent

import (
	"os"
	"path/filepath"
	"testing"

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

func TestBasaltIsSentTheSession(t *testing.T) {
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
