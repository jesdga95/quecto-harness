package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixtureServer answers the three read-only endpoints from the captures and
// refuses a model-scoped GET that forgot autoload=false.
func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			if model := r.URL.Query().Get("model"); model != "" {
				if r.URL.Query().Get("autoload") != "false" {
					http.Error(w, "model-scoped GET without autoload=false", 500)
					return
				}
				if model == "Delta3.8-27B Q5" {
					writeFixture(t, w, "props-reasoning.json")
					return
				}
				writeFixture(t, w, "props-model.json")
				return
			}
			writeFixture(t, w, "props-router.json")
		case "/v1/models":
			writeFixture(t, w, "v1-models.json")
		default:
			http.NotFound(w, r)
		}
	}))
}

func writeFixture(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.Write(readFixture(t, name))
}

func TestDetectRouter(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()
	info, err := New(srv.URL, "").Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if info.Mode != ModeRouter {
		t.Errorf("mode = %q, want router", info.Mode)
	}
	if info.Build != "b10812-8b4b3558f" {
		t.Errorf("build = %q", info.Build)
	}
	if !info.Autoload {
		t.Error("autoload = false, fixture says true")
	}
	if info.NCtx != 0 || info.Model != "" {
		t.Errorf("router leaked single-mode fields: %+v", info)
	}
}

func TestDetectSingle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model_alias":"local","build_info":"b1","default_generation_settings":{"n_ctx":4096}}`)
	}))
	defer srv.Close()
	info, err := New(srv.URL, "").Detect(context.Background())
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if info.Mode != ModeSingle || info.Model != "local" || info.NCtx != 4096 {
		t.Errorf("got %+v", info)
	}
}

func TestModelsCtxHint(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()
	models, err := New(srv.URL, "").Models(context.Background())
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	if len(models) != 6 {
		t.Fatalf("got %d models, want 6", len(models))
	}
	want := map[string]struct {
		status ModelStatus
		hint   int
		source string
	}{
		"Alpha-12B":              {StatusLoaded, 131072, "meta"},
		"Beta 30B Q5":            {StatusUnloaded, 131072, "args"},
		"Gamma3.6-35B-A3B Q5":    {StatusUnloaded, 262144, "args"},
		"Delta3.8-27B Q5":        {StatusUnloaded, 262144, "args"},
		"Delta3.8-27B Q8":        {StatusUnloaded, 262144, "args"},
		"Epsilon-Flash-Next IQ4": {StatusUnloaded, 131072, "args"},
	}
	for _, m := range models {
		w, ok := want[m.ID]
		if !ok {
			t.Errorf("unexpected model %q", m.ID)
			continue
		}
		if m.Status != w.status || m.CtxHint != w.hint || m.CtxSource != w.source {
			t.Errorf("%s: got %v/%d/%s, want %v/%d/%s", m.ID, m.Status, m.CtxHint, m.CtxSource, w.status, w.hint, w.source)
		}
	}
	if got := models[0].Modalities; len(got) != 3 || got[0] != "text" {
		t.Errorf("modalities = %v", got)
	}
}

func TestPropsCaps(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()
	p, err := New(srv.URL, "").Props(context.Background(), "Alpha-12B")
	if err != nil {
		t.Fatalf("props: %v", err)
	}
	if p.Model != "Alpha-12B" || p.NCtx != 131072 || p.TotalSlots != 2 || p.Sleeping {
		t.Errorf("got %+v", p)
	}
	if !p.Caps.Known || !p.Caps.ToolCalls || !p.Caps.ParallelToolCalls || !p.Caps.SystemRole {
		t.Errorf("caps = %+v", p.Caps)
	}
	if p.Caps.ReasoningEffort || p.Caps.PreserveReasoning {
		t.Errorf("caps overread: %+v", p.Caps)
	}
}

// The capture for this model reports a template that can hold thinking.
func TestPropsReadsPreserveReasoning(t *testing.T) {
	srv := fixtureServer(t)
	defer srv.Close()
	p, err := New(srv.URL, "").Props(context.Background(), "Delta3.8-27B Q5")
	if err != nil {
		t.Fatalf("props: %v", err)
	}
	if !p.Caps.ReasoningEffort || !p.Caps.PreserveReasoning {
		t.Errorf("caps = %+v", p.Caps)
	}
}

func TestLlamaBodyPreserveReasoning(t *testing.T) {
	able := Caps{Known: true, PreserveReasoning: true}
	cases := []struct {
		name     string
		req      ChatRequest
		want     any
		absent   bool
		thinking bool
	}{
		{"on where the template acts on it",
			ChatRequest{Effort: EffortAuto, PreserveReasoning: true, Caps: able}, true, false, false},
		{"off where the template acts on it",
			ChatRequest{Effort: EffortAuto, PreserveReasoning: false, Caps: able}, false, false, false},
		{"not sent where the template ignores it",
			ChatRequest{Effort: EffortAuto, PreserveReasoning: true, Caps: Caps{Known: true}}, nil, true, false},
		{"not sent before the caps are known",
			ChatRequest{Effort: EffortAuto, PreserveReasoning: true}, nil, true, false},
		{"shares the kwargs with off",
			ChatRequest{Effort: EffortOff, PreserveReasoning: true, Caps: able}, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := buildLlamaBody(tc.req)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			kw, _ := body["chat_template_kwargs"].(map[string]any)
			got, present := kw["preserve_reasoning"]
			if tc.absent {
				if present {
					t.Errorf("preserve_reasoning = %v, want absent", got)
				}
				return
			}
			if got != tc.want {
				t.Errorf("preserve_reasoning = %v, want %v", got, tc.want)
			}
			if tc.thinking && kw["enable_thinking"] != false {
				t.Errorf("enable_thinking = %v, want false beside it", kw["enable_thinking"])
			}
		})
	}
}

func TestReasoningContentRidesTheAssistantMessage(t *testing.T) {
	raw, err := buildLlamaBody(ChatRequest{Model: "m", Messages: []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi", ReasoningContent: "they said hello"},
		{Role: "assistant", Content: "no thinking here"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Messages[1]["reasoning_content"] != "they said hello" {
		t.Errorf("assistant message = %v", body.Messages[1])
	}
	if _, ok := body.Messages[2]["reasoning_content"]; ok {
		t.Error("an empty reasoning_content was sent")
	}
	if _, ok := body.Messages[0]["reasoning_content"]; ok {
		t.Error("reasoning_content was sent on a user message")
	}
}

func TestModelURLStatesAutoload(t *testing.T) {
	const base = "http://example"
	u := modelURL(base, "/props", "Beta 30B Q5", false)
	if !strings.Contains(u, "autoload=false") {
		t.Fatalf("modelURL = %q, a read must say autoload=false", u)
	}
	if !strings.Contains(u, "model=Beta+30B+Q5") {
		t.Errorf("modelURL = %q, model id not escaped", u)
	}
	if u := modelURL(base, "/props", "Beta 30B Q5", true); !strings.Contains(u, "autoload=true") {
		t.Errorf("modelURL = %q, a load must say autoload=true", u)
	}
	if got := modelURL(base, "/props", "", false); got != "http://example/props" {
		t.Errorf("unscoped modelURL = %q", got)
	}
}

func TestLlamaBodyEffort(t *testing.T) {
	known := Caps{Known: true, ReasoningEffort: true}
	unknown := Caps{}
	cases := []struct {
		effort Effort
		caps   Caps
		want   map[string]any
		absent []string
	}{
		{EffortAuto, known, nil, []string{"reasoning_effort", "reasoning_budget", "chat_template_kwargs"}},
		{EffortAuto, unknown, nil, []string{"reasoning_effort", "reasoning_budget", "chat_template_kwargs"}},
		{EffortOff, known, map[string]any{"reasoning_budget": 0.0}, []string{"reasoning_effort"}},
		{EffortOff, unknown, map[string]any{"reasoning_budget": 0.0}, []string{"reasoning_effort"}},
		{EffortLow, known, map[string]any{"reasoning_effort": "low"}, []string{"reasoning_budget"}},
		{EffortLow, unknown, map[string]any{"reasoning_budget": 2048.0}, []string{"reasoning_effort"}},
		{EffortMedium, known, map[string]any{"reasoning_effort": "medium"}, []string{"reasoning_budget"}},
		{EffortMedium, unknown, map[string]any{"reasoning_budget": 8192.0}, []string{"reasoning_effort"}},
		{EffortHigh, known, map[string]any{"reasoning_effort": "high"}, []string{"reasoning_budget"}},
		{EffortHigh, unknown, map[string]any{"reasoning_budget": -1.0}, []string{"reasoning_effort"}},
	}
	for _, tc := range cases {
		name := fmt.Sprintf("%s/known=%v", tc.effort, tc.caps.Known && tc.caps.ReasoningEffort)
		t.Run(name, func(t *testing.T) {
			raw, err := buildLlamaBody(ChatRequest{Model: "m", Effort: tc.effort, Caps: tc.caps})
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.want {
				if body[k] != v {
					t.Errorf("%s = %v, want %v", k, body[k], v)
				}
			}
			for _, k := range tc.absent {
				if _, ok := body[k]; ok {
					t.Errorf("%s present, want absent", k)
				}
			}
			if body["reasoning_format"] != "deepseek" {
				t.Errorf("reasoning_format = %v", body["reasoning_format"])
			}
			if body["tool_choice"] != "auto" || body["stream"] != true {
				t.Errorf("missing constant fields: %v", body)
			}
			opts, _ := body["stream_options"].(map[string]any)
			if opts == nil || opts["include_usage"] != true {
				t.Errorf("stream_options = %v", body["stream_options"])
			}
			if _, ok := body["tools"]; ok {
				t.Error("tools present for an empty tool list")
			}
			if tc.effort == EffortOff {
				kw, _ := body["chat_template_kwargs"].(map[string]any)
				if kw == nil || kw["enable_thinking"] != false {
					t.Errorf("chat_template_kwargs = %v", body["chat_template_kwargs"])
				}
			}
		})
	}
}

// sseServer writes frames verbatim, flushing after each so the incremental
// path is exercised.
func sseServer(t *testing.T, frames []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		for _, fr := range frames {
			fmt.Fprint(w, fr+"\n\n")
			if f != nil {
				f.Flush()
			}
		}
	}))
}

// sent is a rendered request. Which backend rendered it changes nothing
// downstream, so the decoding tests post it straight at the shared reader.
var sent = []byte(`{"model":"m","stream":true}`)

func chatFrames(t *testing.T, frames []string) (Result, []Event, error) {
	t.Helper()
	srv := sseServer(t, frames)
	defer srv.Close()
	var events []Event
	res, err := New(srv.URL, "").stream(context.Background(), sent, "m", func(e Event) error {
		events = append(events, e)
		return nil
	})
	return res, events, err
}

func TestChatPlain(t *testing.T) {
	res, events, err := chatFrames(t, []string{
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":44,"completion_tokens":48,"prompt_tokens_details":{"cached_tokens":12}},"timings":{"predicted_per_second":52.94,"cache_n":236}}`,
		`data: [DONE]`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "Hello" || res.FinishReason != "stop" {
		t.Errorf("got %+v", res)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 92 || res.Usage.CachedTokens != 12 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.Timings == nil || res.Timings.CacheN != 236 {
		t.Errorf("timings = %+v", res.Timings)
	}
	if res.Chunks != 3 || res.Dropped != 0 {
		t.Errorf("chunks=%d dropped=%d", res.Chunks, res.Dropped)
	}
	var kinds []EventKind
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []EventKind{EventContent, EventContent, EventFinish, EventUsage, EventTimings}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Errorf("events = %v, want %v", kinds, want)
	}
}

func TestChatToolCallFragments(t *testing.T) {
	res, events, err := chatFrames(t, []string{
		`data: {"choices":[{"delta":{"reasoning_content":"The user wants"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"run_shell","arguments":"{\"comm"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_0" || tc.Type != "function" || tc.Function.Name != "run_shell" {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.Function.Arguments != `{"command":"ls"}` {
		t.Errorf("arguments = %q", tc.Function.Arguments)
	}
	if res.Reasoning != "The user wants" {
		t.Errorf("reasoning = %q", res.Reasoning)
	}
	frags := 0
	for _, e := range events {
		if e.Kind == EventToolCall {
			frags++
			if e.Index != 0 {
				t.Errorf("fragment index = %d", e.Index)
			}
		}
	}
	if frags != 3 {
		t.Errorf("tool call events = %d, want 3", frags)
	}
}

func TestChatToolCallWithoutIndexOrID(t *testing.T) {
	res, _, err := chatFrames(t, []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"a.txt\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].ID != "call_0" {
		t.Errorf("synthesised id = %q, want call_0", res.ToolCalls[0].ID)
	}
	if res.ToolCalls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("arguments = %q", res.ToolCalls[0].Function.Arguments)
	}
}

func TestChatTwoToolCalls(t *testing.T) {
	res, _, err := chatFrames(t, []string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"read_file","arguments":"{\"path\""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"run_shell","arguments":"{\"comm"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"a\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"and\":\"ls\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(res.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("first arguments = %q", res.ToolCalls[0].Function.Arguments)
	}
	if res.ToolCalls[1].Function.Arguments != `{"command":"ls"}` {
		t.Errorf("second arguments = %q", res.ToolCalls[1].Function.Arguments)
	}
}

func TestChatMalformedFramesAreSkipped(t *testing.T) {
	res, _, err := chatFrames(t, []string{
		`data: {"choices":[{"delta":{"content":"a"}}]}`,
		`data: {"choices":[{"delta":`,
		`data: not json at all`,
		`data: {"choices":[{"delta":{"content":"b"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "ab" {
		t.Errorf("content = %q", res.Content)
	}
	if res.Dropped != 2 {
		t.Errorf("dropped = %d, want 2", res.Dropped)
	}
}

func TestChatEarlyCloseKeepsPartial(t *testing.T) {
	res, _, err := chatFrames(t, []string{
		`data: {"choices":[{"delta":{"content":"partial"}}]}`,
	})
	if Kind(err) != KindUpstream {
		t.Fatalf("kind = %q, err = %v", Kind(err), err)
	}
	if res.Content != "partial" {
		t.Errorf("content = %q, the partial reply must survive", res.Content)
	}
}

func TestChatNothingDecodable(t *testing.T) {
	res, _, err := chatFrames(t, []string{`data: garbage`, `data: more garbage`})
	if Kind(err) != KindMalformed {
		t.Fatalf("kind = %q, err = %v", Kind(err), err)
	}
	if res.Dropped != 2 {
		t.Errorf("dropped = %d", res.Dropped)
	}
	if !strings.Contains(err.Error(), "2 frames") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestChatMidStreamErrorObject(t *testing.T) {
	res, _, err := chatFrames(t, []string{
		`data: {"choices":[{"delta":{"content":"a"}}]}`,
		`data: {"choices":[{"delta":{"content":"b"}}]}`,
		`data: {"error":{"code":500,"message":"context shift is disabled"}}`,
	})
	if Kind(err) != KindUpstream {
		t.Fatalf("kind = %q, err = %v", Kind(err), err)
	}
	if !strings.Contains(err.Error(), "context shift is disabled") {
		t.Errorf("message = %q", err.Error())
	}
	if res.Content != "ab" {
		t.Errorf("content = %q", res.Content)
	}
}

func TestChatCommentsAndUnknownFields(t *testing.T) {
	res, _, err := chatFrames(t, []string{
		`:ping`,
		"event: message\nid: 7\nretry: 3000\ndata: {\"choices\":[{\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}",
		`data: [DONE]`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "x" || res.Dropped != 0 {
		t.Errorf("got %+v", res)
	}
}

func TestChatMultiLineData(t *testing.T) {
	res, _, err := chatFrames(t, []string{
		"data: {\"choices\":[{\"delta\":\ndata: {\"content\":\"joined\"},\"finish_reason\":\"stop\"}]}",
		`data: [DONE]`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "joined" {
		t.Errorf("content = %q, multi-line data must be joined with a newline", res.Content)
	}
}

func TestChatNonStreamingReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"whole"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	res, err := New(srv.URL, "").stream(context.Background(), sent, "m", nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "whole" || res.FinishReason != "stop" || res.Usage.TotalTokens != 3 {
		t.Errorf("got %+v", res)
	}
}

func TestChatCanceled(t *testing.T) {
	srv := sseServer(t, []string{`data: {"choices":[{"delta":{"content":"a"}}]}`})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	_, err := New(srv.URL, "").stream(ctx, sent, "m", func(e Event) error {
		cancel()
		return nil
	})
	if Kind(err) != KindCanceled {
		t.Fatalf("kind = %q, err = %v", Kind(err), err)
	}
}

func TestChatCallbackErrorAborts(t *testing.T) {
	srv := sseServer(t, []string{
		`data: {"choices":[{"delta":{"content":"a"}}]}`,
		`data: {"choices":[{"delta":{"content":"b"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	})
	defer srv.Close()
	sentinel := errors.New("stop reading")
	_, err := New(srv.URL, "").stream(context.Background(), sent, "m", func(e Event) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want the callback error", err)
	}
}

func TestErrorTaxonomy(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   ErrKind
		says   string
	}{
		{401, `{"error":{"message":"bad key"}}`, KindAuth, "server.token"},
		{403, ``, KindAuth, "server.token"},
		{404, `{"error":{"message":"model not found"}}`, KindNotFound, "pick another"},
		{400, `{"error":{"message":"the model foo was not found"}}`, KindNotFound, "pick another"},
		{400, `{"error":{"message":"bad temperature"}}`, KindBadRequest, "bad temperature"},
		{503, `{"error":{"message":"model is loading"}}`, KindModelLoading, "send the message again"},
		{500, `{"error":{"message":"out of memory"}}`, KindUpstream, "out of memory"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d/%s", tc.status, tc.want), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			_, err := New(srv.URL, "").Chat(context.Background(), ChatRequest{Model: "m"}, nil)
			if Kind(err) != tc.want {
				t.Fatalf("kind = %q, want %q (err %v)", Kind(err), tc.want, err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("message = %q, want it to mention %q", err.Error(), tc.says)
			}
		})
	}
}

func TestErrorUnreachable(t *testing.T) {
	_, err := New("http://127.0.0.1:1", "").Detect(context.Background())
	if Kind(err) != KindUnreachable {
		t.Fatalf("kind = %q, err = %v", Kind(err), err)
	}
	if !strings.Contains(err.Error(), "check it is running") {
		t.Errorf("message = %q", err.Error())
	}
}
