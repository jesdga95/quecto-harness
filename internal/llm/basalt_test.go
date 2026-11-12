package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBasaltFeatures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, "props-basalt.json")
	}))
	defer srv.Close()
	c := New(srv.URL, "", ProtocolBasalt)
	info, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if info.Mode != ModeSingle || info.Model != "Qwen 3.8 FN" || info.NCtx != 262144 {
		t.Errorf("detect = %+v", info)
	}
	want := Features{AnswerNow: true, Documents: true, Video: true}
	p, err := c.Props(context.Background(), "Qwen 3.8 FN")
	if err != nil {
		t.Fatalf("props: %v", err)
	}
	if p.Features != want || !p.Caps.Known || p.TotalSlots != 4 {
		t.Errorf("props = %+v", p)
	}
	r, err := c.Prepare(context.Background(), "Qwen 3.8 FN")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if r.Features != want || r.NCtx != 262144 {
		t.Errorf("ready = %+v", r)
	}
}

// DeepSeek does not read video, so Basalt leaves the name off its list.
func TestBasaltWithoutVideo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, "props-basalt-novideo.json")
	}))
	defer srv.Close()
	p, err := New(srv.URL, "", ProtocolBasalt).Props(context.Background(), "DeepSeek V4.1 Flash")
	if err != nil {
		t.Fatalf("props: %v", err)
	}
	if want := (Features{AnswerNow: true, Documents: true}); p.Features != want {
		t.Errorf("features = %+v, want %+v", p.Features, want)
	}
}

// The llama.cpp backend reads the same response as it always did, and finds
// no features in it.
func TestLlamaCppIgnoresTheBasaltKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, "props-basalt.json")
	}))
	defer srv.Close()
	c := New(srv.URL, "", ProtocolLlama)
	p, err := c.Props(context.Background(), "Qwen 3.8 FN")
	if err != nil {
		t.Fatalf("props: %v", err)
	}
	if p.Features != (Features{}) || p.Model != "Qwen 3.8 FN" {
		t.Errorf("props = %+v", p)
	}
}

// Basalt adds logprobs to the chunks of every API, thinking and calls
// included. They are not read, and must not cost the reply anything.
func TestLogprobsAreIgnored(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"reasoning_content":"hm"},"logprobs":{"content":null,"reasoning_content":[{"token":"hm","logprob":-0.1,"top_logprobs":[]}]}}]}`,
		`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"ok"},"logprobs":{"content":[{"token":"ok","logprob":-0.01,"bytes":[111,107],"top_logprobs":[{"token":"ok","logprob":-0.01}]}],"tool_calls":[]},"finish_reason":"stop"}]}`,
		`data: [DONE]`, ``,
	}, "\n\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, stream)
	}))
	defer srv.Close()
	res, err := New(srv.URL, "", ProtocolBasalt).Chat(context.Background(),
		ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "ok" || res.Reasoning != "hm" || res.Dropped != 0 {
		t.Errorf("got %+v", res)
	}
}
