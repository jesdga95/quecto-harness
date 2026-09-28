package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func responsesFrames(t *testing.T, frames []string) (Result, []Event, error) {
	t.Helper()
	srv := sseServer(t, frames)
	defer srv.Close()
	var events []Event
	res, err := New(srv.URL, "", ProtocolResponses).stream(context.Background(),
		responsesPath, sent, "m", newResponsesState(), func(e Event) error {
			events = append(events, e)
			return nil
		})
	return res, events, err
}

// A call is announced under an item id and summarised under a call id. Both
// name the same call, and the harness has to send its result back under the
// second, so the two must fold into one call rather than into two.
func TestResponsesGathersOneCallUnderTwoIDs(t *testing.T) {
	res, events, err := responsesFrames(t, []string{
		`data: {"type":"response.output_item.added","item_id":"fc_1","item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"read"}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"path\":"}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"/etc/hosts\"}"}`,
		`data: {"type":"response.output_item.done","item_id":"fc_1","item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"read","arguments":"{\"path\":\"/etc/hosts\"}"}}`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"call_abc","name":"read","arguments":"{\"path\":\"/etc/hosts\"}"}],"usage":{"input_tokens":60,"input_tokens_details":{"cached_tokens":48},"output_tokens":21,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":81}}}`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(res.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1: %+v", len(res.ToolCalls), res.ToolCalls)
	}
	tc := res.ToolCalls[0]
	if tc.ID != "call_abc" {
		t.Errorf("id = %q, want the call id, which is what a result is sent back under", tc.ID)
	}
	if tc.Function.Name != "read" || tc.Function.Arguments != `{"path":"/etc/hosts"}` {
		t.Errorf("call = %+v", tc.Function)
	}
	if res.FinishReason != "tool_calls" {
		t.Errorf("finish = %q, want tool_calls", res.FinishReason)
	}
	if res.Usage == nil || res.Usage.PromptTokens != 60 || res.Usage.CompletionTokens != 21 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.Usage != nil && (res.Usage.CachedTokens == nil || *res.Usage.CachedTokens != 48) {
		t.Errorf("cached = %v, want 48 from input_tokens_details", res.Usage.CachedTokens)
	}
	var kinds []EventKind
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if len(kinds) == 0 || kinds[0] != EventToolCall {
		t.Errorf("events = %v, want the fragments as they arrive", kinds)
	}
}

// The summary alone is enough: a server that sends no fragments still yields
// the call, which is what a reply that ignored stream:true looks like.
func TestResponsesTakesACallFromTheSummaryAlone(t *testing.T) {
	res, _, err := responsesFrames(t, []string{
		`data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","call_id":"call_z","name":"list_dir","arguments":"{}"}]}}`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "call_z" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
}

func TestResponsesStreamsTextAndThinking(t *testing.T) {
	res, events, err := responsesFrames(t, []string{
		`data: {"type":"response.reasoning_summary_text.delta","delta":"Weighing "}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"the two."}`,
		`data: {"type":"response.output_text.delta","delta":"Hel"}`,
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.Content != "Hello" {
		t.Errorf("content = %q", res.Content)
	}
	if res.Reasoning != "Weighing the two." {
		t.Errorf("reasoning = %q", res.Reasoning)
	}
	if res.FinishReason != "stop" {
		t.Errorf("finish = %q, want stop when nothing was called", res.FinishReason)
	}
	var reasoning, content int
	for _, e := range events {
		switch e.Kind {
		case EventReasoning:
			reasoning++
		case EventContent:
			content++
		}
	}
	if reasoning != 2 || content != 2 {
		t.Errorf("events: %d reasoning, %d content, want 2 and 2", reasoning, content)
	}
}

// A turn cut short says so on the response rather than on a choice, and the
// reason has to survive as the finish reason so the caller can see it.
func TestResponsesCarriesAnIncompleteReason(t *testing.T) {
	res, _, err := responsesFrames(t, []string{
		`data: {"type":"response.output_text.delta","delta":"half"}`,
		`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if res.FinishReason != "max_output_tokens" {
		t.Errorf("finish = %q, want max_output_tokens", res.FinishReason)
	}
}

func TestResponsesReportsAFailedTurn(t *testing.T) {
	_, _, err := responsesFrames(t, []string{
		`data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"the model gave out"}}}`,
	})
	if Kind(err) != KindUpstream {
		t.Fatalf("kind = %v, want upstream: %v", Kind(err), err)
	}
	if !strings.Contains(err.Error(), "the model gave out") {
		t.Errorf("message = %q, want the server's own sentence", err.Error())
	}
}

func body(t *testing.T, req ChatRequest) map[string]any {
	t.Helper()
	raw, err := buildResponsesBody(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// The system prompt is a field of its own here, not a message, and the two
// halves of a tool exchange are items rather than parts of two messages.
func TestResponsesInputIsFlattened(t *testing.T) {
	got := body(t, ChatRequest{
		Model: "m",
		Messages: []Message{
			{Role: "system", Content: "Be terse."},
			{Role: "user", Content: "Read it."},
			{Role: "assistant", Content: "On it.", ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: FunctionCall{Name: "read", Arguments: `{"path":"/x"}`}},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "the bytes"},
		},
	})
	if got["instructions"] != "Be terse." {
		t.Errorf("instructions = %v, want the system message lifted out", got["instructions"])
	}
	if got["store"] != false {
		t.Errorf("store = %v, want false: the harness holds the conversation", got["store"])
	}
	items, _ := got["input"].([]any)
	if len(items) != 4 {
		t.Fatalf("input = %d items, want user, assistant text, the call and its result: %v", len(items), items)
	}
	want := []string{"", "", "function_call", "function_call_output"}
	for i, w := range want {
		m, _ := items[i].(map[string]any)
		if got, _ := m["type"].(string); got != w {
			t.Errorf("item %d type = %q, want %q", i, got, w)
		}
	}
	call, _ := items[2].(map[string]any)
	if call["call_id"] != "call_1" || call["name"] != "read" {
		t.Errorf("call item = %v", call)
	}
	out, _ := items[3].(map[string]any)
	if out["call_id"] != "call_1" || out["output"] != "the bytes" {
		t.Errorf("result item = %v", out)
	}
}

// Auto sends no reasoning object at all, which for a model that reasons by
// default is not the same as asking it not to. Asking for thinking also asks
// for the summary of it, which is the only readable form this endpoint has.
func TestResponsesEffort(t *testing.T) {
	for _, tc := range []struct {
		effort  Effort
		want    any
		summary bool
	}{
		{EffortAuto, nil, false},
		{EffortOff, "none", false},
		{EffortLow, "low", true},
		{EffortHigh, "high", true},
		{EffortXHigh, "xhigh", true},
	} {
		got := body(t, ChatRequest{Model: "m", Effort: tc.effort})
		r, ok := got["reasoning"].(map[string]any)
		if tc.want == nil {
			if ok {
				t.Errorf("effort %q sent reasoning %v, want the field left out", tc.effort, r)
			}
			continue
		}
		if !ok || r["effort"] != tc.want {
			t.Errorf("effort %q sent %v, want effort %v", tc.effort, got["reasoning"], tc.want)
		}
		if _, asked := r["summary"]; asked != tc.summary {
			t.Errorf("effort %q summary asked = %v, want %v", tc.effort, asked, tc.summary)
		}
	}
}

// The cap is named for the output rather than the completion, and the tool
// carries its name and schema itself rather than inside a function object.
func TestResponsesToolsAndCapAreFlat(t *testing.T) {
	got := body(t, ChatRequest{
		Model: "m", MaxTokens: 512,
		Tools: []Tool{{Type: "function", Function: FunctionSpec{
			Name: "read", Description: "Read a file",
			Parameters: json.RawMessage(`{"type":"object"}`),
		}}},
	})
	if got["max_output_tokens"] != float64(512) {
		t.Errorf("max_output_tokens = %v", got["max_output_tokens"])
	}
	if _, bad := got["max_tokens"]; bad {
		t.Error("max_tokens was sent, and this endpoint refuses it")
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", tools)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["name"] != "read" || tool["type"] != "function" {
		t.Errorf("tool = %v, want the name on the tool itself", tool)
	}
	if _, nested := tool["function"]; nested {
		t.Error("the tool nested a function object, which this endpoint does not take")
	}
}

// An image is the same bytes under different part names.
func TestResponsesImagePartsAreRenamed(t *testing.T) {
	got := body(t, ChatRequest{Model: "m", Messages: []Message{{Role: "user", Parts: []Part{
		TextPart("look"), ImagePart("image/png", []byte{1, 2, 3}),
	}}}})
	items, _ := got["input"].([]any)
	first, _ := items[0].(map[string]any)
	parts, _ := first["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("parts = %v", parts)
	}
	a, _ := parts[0].(map[string]any)
	b, _ := parts[1].(map[string]any)
	if a["type"] != "input_text" || b["type"] != "input_image" {
		t.Errorf("parts = %v, %v, want input_text and input_image", a, b)
	}
	if s, _ := b["image_url"].(string); !strings.HasPrefix(s, "data:image/png;base64,") {
		t.Errorf("image_url = %v, want the data URI as a plain string", b["image_url"])
	}
}
