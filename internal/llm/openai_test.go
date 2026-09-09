package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// openAIServer answers like a server that speaks the OpenAI API and nothing
// else: no properties endpoint, a model list, and a chat stream captured from
// a running one. It records the last request body.
func openAIServer(t *testing.T, sent *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			writeFixture(t, w, "openai-models.json")
		case "/v1/chat/completions":
			if sent != nil {
				body := make([]byte, r.ContentLength)
				r.Body.Read(body)
				*sent = body
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write(readFixture(t, "openai-stream.txt"))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestProbeFallsBackToOpenAI(t *testing.T) {
	srv := openAIServer(t, nil)
	defer srv.Close()
	info, err := New(srv.URL, "", ProtocolChat).Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if info.Mode != ModeOpenAI {
		t.Fatalf("mode = %q, a server with no properties endpoint is OpenAI-compatible", info.Mode)
	}
	if info.Model != "Delta3.8-27B" || info.NCtx != 240000 {
		t.Errorf("info = %+v, want the one model the list names", info)
	}
}

func TestProbeKeepsARefusedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"no key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "", ProtocolChat).Detect(context.Background())
	if Kind(err) != KindAuth {
		t.Fatalf("kind = %q, err = %v: a refused token names no protocol", Kind(err), err)
	}
}

func TestOpenAIModelsCarryTheirWindow(t *testing.T) {
	srv := openAIServer(t, nil)
	defer srv.Close()
	models, err := New(srv.URL, "", ProtocolChat).Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	m := models[0]
	if m.Status != StatusLoaded {
		t.Errorf("status = %q, a listed model can answer", m.Status)
	}
	if m.CtxHint != 240000 || m.CtxSource != "models" {
		t.Errorf("ctx = %d from %q, want the published window", m.CtxHint, m.CtxSource)
	}
}

func TestOpenAIPrepareReadsTheList(t *testing.T) {
	srv := openAIServer(t, nil)
	defer srv.Close()
	r, err := New(srv.URL, "", ProtocolChat).Prepare(context.Background(), "Delta3.8-27B")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if r.NCtx != 240000 || r.Source != "models" {
		t.Errorf("ready = %+v, want the window the list published", r)
	}
	if r.Caps.Known {
		t.Error("capabilities were claimed for a server that reports none")
	}
}

func TestOpenAIPrepareUnknownModel(t *testing.T) {
	srv := openAIServer(t, nil)
	defer srv.Close()
	_, err := New(srv.URL, "", ProtocolChat).Prepare(context.Background(), "Nothing-7B")
	if Kind(err) != KindNotFound {
		t.Fatalf("kind = %q, err = %v", Kind(err), err)
	}
	if !strings.Contains(err.Error(), "pick another") {
		t.Errorf("message = %q, it should say what to do", err)
	}
}

func TestOpenAIBodyCarriesNoLlamaFields(t *testing.T) {
	cases := []struct {
		effort Effort
		want   any
	}{
		{EffortAuto, nil},
		{EffortOff, "none"},
		{EffortHigh, "high"},
	}
	for _, tc := range cases {
		t.Run(string(tc.effort), func(t *testing.T) {
			raw, err := buildOpenAIBody(ChatRequest{
				Model: "m", Effort: tc.effort, PreserveReasoning: true,
				Caps: Caps{Known: true, ReasoningEffort: true, PreserveReasoning: true},
			})
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			for _, field := range []string{"reasoning_format", "chat_template_kwargs", "reasoning_budget"} {
				if _, ok := body[field]; ok {
					t.Errorf("%s is a llama.cpp field and was sent anyway", field)
				}
			}
			if got := body["reasoning_effort"]; got != tc.want {
				t.Errorf("reasoning_effort = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOpenAIChatDecodesACapturedStream(t *testing.T) {
	var sent []byte
	srv := openAIServer(t, &sent)
	defer srv.Close()
	res, err := New(srv.URL, "", ProtocolChat).Chat(context.Background(), ChatRequest{
		Model: "Delta3.8-27B", Effort: EffortOff,
		Messages: []Message{{Role: "user", Content: "List the files here"}},
	}, nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Function.Name != "run_shell" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.FinishReason != "tool_calls" {
		t.Errorf("finish = %q", res.FinishReason)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 311 {
		t.Errorf("usage = %+v", res.Usage)
	}
	var body map[string]any
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatalf("the request was not JSON: %v", err)
	}
	if body["reasoning_effort"] != "none" {
		t.Errorf("reasoning_effort = %v, want the field the API defines", body["reasoning_effort"])
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// TestOpenAIStreamFixtureIsAStream guards the capture itself, since a fixture
// that stopped being SSE would pass every test above for the wrong reason.
func TestOpenAIStreamFixtureIsAStream(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "openai-stream.txt"))
	if err != nil {
		t.Fatalf("open the capture: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan()
	if !strings.HasPrefix(sc.Text(), "data: ") {
		t.Fatalf("first line = %q, want an SSE frame", sc.Text())
	}
}

// The protocol is the one the client was given. A chat client never reads
// /props, so an address that would have answered it is still spoken to the
// way it was named, and nothing about the address can change that.
func TestTheProtocolIsTheOneItWasGiven(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/props":
			writeFixture(t, w, "props-router.json")
		case "/v1/models":
			writeFixture(t, w, "openai-models.json")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "", ProtocolChat)
	ctx := context.Background()
	info, err := c.Detect(ctx)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if info.Mode != ModeOpenAI {
		t.Errorf("mode = %q, want %q even though the address answers /props", info.Mode, ModeOpenAI)
	}
	for i := 0; i < 3; i++ {
		if _, err := c.Models(ctx); err != nil {
			t.Fatalf("models: %v", err)
		}
		if _, err := c.Prepare(ctx, "Delta3.8-27B"); err != nil {
			t.Fatalf("prepare: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if p == "/props" {
			t.Fatalf("a chat client read /props; paths = %v", paths)
		}
	}
}
