package llama

import (
	"encoding/json"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

type FunctionSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Effort is the UI thinking level. It becomes request fields in applyEffort
// and has no other meaning.
type Effort string

const (
	// Auto sends no thinking field at all. That is not the same as off: the
	// template decides.
	EffortAuto   Effort = "auto"
	EffortOff    Effort = "off"
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// ValidEffort reports whether s is one of the five levels.
func ValidEffort(s string) bool {
	switch Effort(s) {
	case EffortAuto, EffortOff, EffortLow, EffortMedium, EffortHigh:
		return true
	}
	return false
}

// budget is the fallback for a template that cannot take a named effort
// level. High is -1, which llama.cpp reads as unbounded.
var budget = map[Effort]int{EffortLow: 2048, EffortMedium: 8192, EffortHigh: -1}

// ChatRequest is the harness-level request. Wire fields are derived from it
// by buildBody, so the effort mapping lives in one place.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []Tool
	Effort   Effort
	Caps     Caps
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens"`
}

type Timings struct {
	PromptN            int
	PromptPerSecond    float64
	PredictedN         int
	PredictedPerSecond float64
	CacheN             int
}

// applyEffort writes the thinking fields. Omitting the field and setting it
// to a level the template did not ask for are different requests.
func applyEffort(body map[string]any, e Effort, caps Caps) {
	switch {
	case e == EffortAuto:
	case e == EffortOff:
		body["reasoning_budget"] = 0
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	case caps.Known && caps.ReasoningEffort:
		body["reasoning_effort"] = string(e)
	default:
		if b, ok := budget[e]; ok {
			body["reasoning_budget"] = b
		}
	}
}

// buildBody renders the wire request. It is a map because the field set is
// conditional, and a map also marshals with sorted keys for the tests.
func buildBody(req ChatRequest) ([]byte, error) {
	body := map[string]any{
		"model":            req.Model,
		"messages":         req.Messages,
		"tool_choice":      "auto",
		"stream":           true,
		"stream_options":   map[string]any{"include_usage": true},
		"reasoning_format": "deepseek",
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	applyEffort(body, req.Effort, req.Caps)
	return json.Marshal(body)
}
