package llm

import (
	"encoding/base64"
	"encoding/json"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// Parts carry a message the endpoint takes as an array rather than a
	// string, which is how an image travels. They replace Content when set.
	Parts []Part `json:"-"`
	// ReasoningContent is the thinking that came with an assistant message.
	// Sent back so the prompt matches what the model generated; a template
	// that cannot place it ignores it.
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
}

// Part is one piece of a multimodal message: a run of text, or an image as a
// data URI.
type Part struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

func TextPart(text string) Part { return Part{Type: "text", Text: text} }

// ImagePart carries the bytes inline, which is the only shape the endpoint
// takes: it has no route to a file on this machine.
func ImagePart(mime string, data []byte) Part {
	return Part{Type: "image_url", ImageURL: &ImageURL{
		URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
	}}
}

// MarshalJSON writes content as a string, or as the array of parts when the
// message carries one, which is the shape the endpoint takes for both.
func (m Message) MarshalJSON() ([]byte, error) {
	wire := struct {
		Role             string     `json:"role"`
		Content          any        `json:"content,omitempty"`
		ReasoningContent string     `json:"reasoning_content,omitempty"`
		ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string     `json:"tool_call_id,omitempty"`
		Name             string     `json:"name,omitempty"`
	}{
		Role: m.Role, ReasoningContent: m.ReasoningContent,
		ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, Name: m.Name,
	}
	switch {
	case len(m.Parts) > 0:
		wire.Content = m.Parts
	case m.Content != "":
		wire.Content = m.Content
	}
	return json.Marshal(wire)
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

// Effort is the thinking level. Auto and off are the harness's own; low,
// medium and high are the names chat templates agree on. It becomes request
// fields in applyEffort and has no other meaning.
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

// ChatRequest is the harness-level request. Each backend renders it into its
// own wire body, so a caller never names a protocol field.
type ChatRequest struct {
	Model    string
	Messages []Message
	Tools    []Tool
	Effort   Effort
	// PreserveReasoning asks the template to keep thinking from earlier turns
	// in the prompt rather than dropping it.
	PreserveReasoning bool
	Caps              Caps
	// MaxTokens caps the generation. Zero leaves it to the server, which is
	// what a turn wants; a request sent only for its usage report sets it to 1.
	MaxTokens int
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

// baseBody is the request every server takes: the fields the OpenAI chat API
// defines and nothing else. Each backend adds what its own protocol wants. It
// is a map because the field set is conditional, and a map also marshals with
// sorted keys for the tests.
func baseBody(req ChatRequest) map[string]any {
	body := map[string]any{
		"model":          req.Model,
		"messages":       req.Messages,
		"tool_choice":    "auto",
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	return body
}

// buildError reports a request that could not be rendered, which is a bug
// here rather than anything the server did.
func buildError(err error) *Error {
	return &Error{Kind: KindBadRequest, Op: "chat", err: err,
		Message: "Build the request: " + err.Error()}
}
