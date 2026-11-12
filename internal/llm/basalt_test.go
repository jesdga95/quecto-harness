package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	took, err := c.AnswerNow(context.Background(), "qh-1")
	if took || err != nil {
		t.Errorf("llama.cpp answer now = %v %v", took, err)
	}
}

// chatCapture answers one chat request with a short stream and keeps the
// headers and body it was sent.
func chatCapture(t *testing.T, got *http.Header, body *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.Header.Clone()
		*body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\",\"index\":0}]}\n\ndata: [DONE]\n\n")
	}))
}

func TestOnlyBasaltIsSentSessionAndRequestID(t *testing.T) {
	var hdr http.Header
	var body []byte
	srv := chatCapture(t, &hdr, &body)
	defer srv.Close()
	req := ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}},
		Session: "sess_1", RequestID: "qh-abc"}

	if _, err := New(srv.URL, "", ProtocolLlama).Chat(context.Background(), req, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if hdr.Get("X-Session-Id") != "" || hdr.Get("X-Client-Request-Id") != "" {
		t.Errorf("llama.cpp was sent %v", hdr)
	}
	llama := body

	if _, err := New(srv.URL, "", ProtocolBasalt).Chat(context.Background(), req, nil); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if hdr.Get("X-Session-Id") != "sess_1" || hdr.Get("X-Client-Request-Id") != "qh-abc" {
		t.Errorf("headers = %v", hdr)
	}
	if string(body) != string(llama) {
		t.Errorf("the two bodies differ:\n%s\n%s", llama, body)
	}
}

func TestAnswerNow(t *testing.T) {
	cases := []struct {
		status int
		body   string
		took   bool
		fails  bool
	}{
		{200, `{"object":"answer_now","request_id":"qh-1","state":"thinking"}`, true, false},
		{409, `{"error":{"code":"not_thinking","state":"answering","message":"request qh-1 is already answering"}}`, false, false},
		{404, `{"error":{"code":"unknown_request","message":"no running or recent request qh-1"}}`, false, false},
		{400, `{"error":{"code":"no_target","message":"name the request"}}`, false, false},
		{500, `{"error":{"message":"boom"}}`, false, true},
	}
	for _, tc := range cases {
		var sent map[string]string
		var auth, path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path, auth = r.URL.Path, r.Header.Get("Authorization")
			json.NewDecoder(r.Body).Decode(&sent)
			w.WriteHeader(tc.status)
			fmt.Fprint(w, tc.body)
		}))
		took, err := New(srv.URL, "key", ProtocolBasalt).AnswerNow(context.Background(), "qh-1")
		srv.Close()
		if took != tc.took || (err != nil) != tc.fails {
			t.Errorf("%d: took %v err %v", tc.status, took, err)
		}
		if path != "/v1/answer_now" || auth != "Bearer key" || sent["request_id"] != "qh-1" {
			t.Errorf("%d: sent %s %q %v", tc.status, path, auth, sent)
		}
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
