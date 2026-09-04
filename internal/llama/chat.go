package llama

import (
	"encoding/json"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// ReasoningContent is the thinking that came with an assistant message.
	// Sent back so the prompt matches what the model generated; a template
	// that cannot place it ignores it.
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
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

// Effort is the thinking level. Auto and off are the harness's own; the rest
// are level names a chat template reads. It becomes request fields in
// applyEffort and has no other meaning.
type Effort string

const (
	// Auto sends no thinking field at all. That is not the same as off: the
	// template decides.
	EffortAuto    Effort = "auto"
	EffortOff     Effort = "off"
	EffortMinimal Effort = "minimal"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

// ValidEffort reports whether s is auto, off, or a level name. Which levels a
// given model takes is narrower and comes from its template.
func ValidEffort(s string) bool {
	e := Effort(s)
	if e == EffortAuto || e == EffortOff {
		return true
	}
	_, ok := levelStrength[e]
	return ok
}

// budget is the fallback for a template that will not take a named level. The
// top three are -1, which llama.cpp reads as unbounded.
var budget = map[Effort]int{
	EffortMinimal: 1024,
	EffortLow:     2048,
	EffortMedium:  8192,
	EffortHigh:    -1,
	EffortXHigh:   -1,
	EffortMax:     -1,
}

// ChatRequest is the harness-level request. Wire fields are derived from it
// by buildBody, so the effort mapping lives in one place.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []Tool
	Effort   Effort
	// PreserveReasoning asks the template to keep thinking from earlier turns
	// in the prompt rather than dropping it.
	PreserveReasoning bool
	Caps              Caps
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
// to a level the template did not ask for are different requests, and a level
// outside the template's set raises there, so it falls back to a budget.
func applyEffort(body, kwargs map[string]any, e Effort, caps Caps) {
	switch {
	case e == EffortAuto:
	case e == EffortOff:
		body["reasoning_budget"] = 0
		kwargs["enable_thinking"] = false
	case caps.Known && caps.ReasoningEffort && caps.accepts(e):
		body["reasoning_effort"] = string(e)
	default:
		if b, ok := budget[e]; ok {
			body["reasoning_budget"] = b
		}
	}
}

// applyPreserve names the kwarg that keeps earlier thinking in the prompt. It
// is sent only where the capability says the template acts on it, which is the
// only case where the value changes anything.
func applyPreserve(kwargs map[string]any, preserve bool, caps Caps) {
	if caps.Known && caps.PreserveReasoning {
		kwargs["preserve_reasoning"] = preserve
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
	kwargs := map[string]any{}
	applyEffort(body, kwargs, req.Effort, req.Caps)
	applyPreserve(kwargs, req.PreserveReasoning, req.Caps)
	if len(kwargs) > 0 {
		body["chat_template_kwargs"] = kwargs
	}
	return json.Marshal(body)
}
