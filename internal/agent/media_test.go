package agent

import (
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/store"
)

// userParts is the content of the newest user message of a recorded request.
func userParts(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m["role"] != "user" {
			continue
		}
		raw, ok := m["content"].([]any)
		if !ok {
			return []map[string]any{{"type": "text", "text": m["content"]}}
		}
		var out []map[string]any
		for _, p := range raw {
			part, _ := p.(map[string]any)
			out = append(out, part)
		}
		return out
	}
	t.Fatal("no user message in the request")
	return nil
}

func TestDocumentGoesAsAPart(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	h.basalt(t, basaltDocs)
	s := h.session(t)
	pdf := writeFile(t, "paper.pdf", []byte("%PDF-1.7\nbody"))
	if _, err := h.engine.Send(s.ID, "what does it say", []string{pdf}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitIdle(t, s)

	h.replay.mu.Lock()
	body := h.replay.Requests[0]
	h.replay.mu.Unlock()
	parts := userParts(t, body)
	if len(parts) != 2 || parts[1]["type"] != "file" {
		t.Fatalf("parts = %v", parts)
	}
	file, _ := parts[1]["file"].(map[string]any)
	if file["filename"] != "paper.pdf" || !strings.HasPrefix(file["file_data"].(string), "data:application/pdf;base64,") {
		t.Errorf("file part = %v", file)
	}

	doc, _, _, _ := s.Snapshot()
	a := doc.Entries[0].Attachments
	if len(a) != 1 || a[0].Kind != "document" || a[0].File == "" {
		t.Errorf("attachment = %+v", a)
	}
}

// Against llama.cpp a PDF is refused at send as any binary file was, even
// where the server behind it is Basalt.
func TestLlamaCppTakesNoDocuments(t *testing.T) {
	h := newHarness(t)
	h.replay.Basalt = basaltVideo
	s := h.session(t)
	pdf := writeFile(t, "paper.pdf", []byte("%PDF-1.7\nbody"))
	if _, err := h.engine.Send(s.ID, "read", []string{pdf}); err == nil || !strings.Contains(err.Error(), "does not read documents") {
		t.Fatalf("send = %v, want a refusal", err)
	}
}

func TestVideoNeedsTheVideoFeature(t *testing.T) {
	mp4 := append([]byte{0, 0, 0, 0x20}, []byte("ftypisom\x00\x00\x02\x00")...)

	h := newHarness(t, "stream_plain.txt")
	h.basalt(t, basaltDocs)
	s := h.session(t)
	clip := writeFile(t, "clip.mp4", mp4)
	if _, err := h.engine.Send(s.ID, "watch", []string{clip}); err == nil || !strings.Contains(err.Error(), "does not read video") {
		t.Fatalf("send = %v, want a refusal", err)
	}

	h = newHarness(t, "stream_plain.txt")
	h.basalt(t, basaltVideo)
	s = h.session(t)
	if _, err := h.engine.Send(s.ID, "watch", []string{clip}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitIdle(t, s)
	h.replay.mu.Lock()
	body := h.replay.Requests[0]
	h.replay.mu.Unlock()
	parts := userParts(t, body)
	video, _ := parts[len(parts)-1]["video_url"].(map[string]any)
	if url, _ := video["url"].(string); !strings.HasPrefix(url, "data:video/mp4;base64,") {
		t.Errorf("parts = %v", parts)
	}
}

// A document the server refuses is marked, and the next request names it
// rather than sending it again.
func TestRefusedDocumentIsNotSentAgain(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	h.basalt(t, basaltDocs)
	h.replay.mu.Lock()
	h.replay.RefuseFiles = "document paper.pdf: password-protected"
	h.replay.mu.Unlock()
	s := h.session(t)
	pdf := writeFile(t, "paper.pdf", []byte("%PDF-1.7\nbody"))
	if _, err := h.engine.Send(s.ID, "read", []string{pdf}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if got := doc.Entries[0].Attachments[0].Refused; got != "document paper.pdf: password-protected" {
		t.Fatalf("refused = %q", got)
	}

	if _, err := h.engine.Send(s.ID, "then just say hi", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	h.replay.mu.Lock()
	body := h.replay.Requests[len(h.replay.Requests)-1]
	h.replay.mu.Unlock()
	if carriesPart(body, "file") {
		t.Error("the refused document was sent again")
	}
	msgs, _ := body["messages"].([]any)
	first, _ := msgs[1].(map[string]any)
	if text, _ := first["content"].(string); !strings.Contains(text, "paper.pdf was not sent: document paper.pdf: password-protected") {
		t.Errorf("first user message = %v", first["content"])
	}
}

// A session moved to a server that reads no documents names them instead.
func TestDocumentNamedWhereTheServerReadsNone(t *testing.T) {
	e := store.Entry{Kind: "user", Content: "look", Attachments: []store.Attachment{
		{Path: "/x/paper.pdf", Kind: "document", Mime: "application/pdf", File: "file_aaaaaaaaaaaaaaaa"},
	}}
	m := userMessage(e, nil, "", llm.Features{})
	if len(m.Parts) != 0 || !strings.Contains(m.Content, "paper.pdf was not sent: the model server does not read documents") {
		t.Errorf("message = %+v", m)
	}
}
