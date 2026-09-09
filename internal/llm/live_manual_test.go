package llm

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestLiveResponses runs the responses backend against a real endpoint. It is
// skipped unless QH_LIVE_KEY is set, so it never runs in an ordinary build.
func TestLiveResponses(t *testing.T) {
	key := os.Getenv("QH_LIVE_KEY")
	if key == "" {
		t.Skip("QH_LIVE_KEY is not set")
	}
	model := os.Getenv("QH_LIVE_MODEL")
	c := New("https://api.openai.com", key, ProtocolResponses)
	ctx := context.Background()

	models, err := c.Models(ctx)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	t.Logf("models: %d listed", len(models))

	tools := []Tool{{Type: "function", Function: FunctionSpec{
		Name: "read_file", Description: "Read a file",
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
	}}}
	msgs := []Message{
		{Role: "system", Content: "You are a terse coding agent."},
		{Role: "user", Content: "Read /etc/hosts with your tool."},
	}

	var kinds []EventKind
	res, err := c.Chat(ctx, ChatRequest{Model: model, Messages: msgs, Tools: tools,
		Effort: EffortMedium, MaxTokens: 2000}, func(e Event) error {
		kinds = append(kinds, e.Kind)
		return nil
	})
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	t.Logf("turn 1: finish=%q calls=%d usage=%+v events=%d", res.FinishReason, len(res.ToolCalls), res.Usage, len(kinds))
	if len(res.ToolCalls) != 1 {
		t.Fatalf("turn 1 tool calls = %d, want 1: %+v", len(res.ToolCalls), res)
	}
	tc := res.ToolCalls[0]
	t.Logf("turn 1 call: id=%q name=%q args=%s", tc.ID, tc.Function.Name, tc.Function.Arguments)
	if tc.Function.Name != "read_file" || tc.Function.Arguments == "" {
		t.Fatalf("call = %+v", tc)
	}

	// Feed the call and its result back, which is the shape a turn two takes.
	msgs = append(msgs,
		Message{Role: "assistant", ToolCalls: res.ToolCalls},
		Message{Role: "tool", ToolCallID: tc.ID, Content: "127.0.0.1 localhost\n::1 localhost"})
	res2, err := c.Chat(ctx, ChatRequest{Model: model, Messages: msgs, Tools: tools,
		Effort: EffortMedium, MaxTokens: 2000}, nil)
	if err != nil {
		t.Fatalf("turn 2: %v", err)
	}
	t.Logf("turn 2: finish=%q content=%q usage=%+v", res2.FinishReason, res2.Content, res2.Usage)
	if res2.Content == "" {
		t.Fatal("turn 2 said nothing, so the call and its result did not reach the model")
	}
}

// TestLiveResponsesEffort checks that each level the harness has reaches the
// endpoint as something it accepts.
func TestLiveResponsesEffort(t *testing.T) {
	key := os.Getenv("QH_LIVE_KEY")
	if key == "" {
		t.Skip("QH_LIVE_KEY is not set")
	}
	model := os.Getenv("QH_LIVE_MODEL")
	c := New("https://api.openai.com", key, ProtocolResponses)
	tools := []Tool{{Type: "function", Function: FunctionSpec{
		Name: "read_file", Description: "Read a file",
		Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`),
	}}}
	for _, e := range []Effort{EffortAuto, EffortOff, EffortLow, EffortMedium, EffortHigh} {
		res, err := c.Chat(context.Background(), ChatRequest{
			Model: model, Effort: e, Tools: tools, MaxTokens: 2000,
			Messages: []Message{{Role: "user", Content: "Read /etc/hosts with your tool."}},
		}, nil)
		if err != nil {
			t.Errorf("effort %q: %v", e, err)
			continue
		}
		t.Logf("effort %-6q ok  finish=%-10q calls=%d out=%d", e, res.FinishReason,
			len(res.ToolCalls), res.Usage.CompletionTokens)
	}
}
