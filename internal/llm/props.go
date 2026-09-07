package llm

// Caps is what a chat template accepts. Known is false when the server did
// not say, which is how a not-yet-loaded model and a server with no such
// endpoint both look.
type Caps struct {
	Known             bool `json:"known"`
	ToolCalls         bool `json:"tool_calls"`
	ParallelToolCalls bool `json:"parallel_tool_calls"`
	ReasoningEffort   bool `json:"reasoning_effort"`
	SystemRole        bool `json:"system_role"`
	PreserveReasoning bool `json:"preserve_reasoning"`
}

// Props is what one model reports about itself. NCtx is per slot.
type Props struct {
	Model      string
	NCtx       int
	TotalSlots int
	Build      string
	Sleeping   bool
	Caps       Caps
}

// Ready is what a session needs to know about a model before it can talk to
// it: the window one conversation gets, where that number came from, and what
// the chat template accepts.
type Ready struct {
	NCtx   int
	Source string
	Caps   Caps
}

// ready reduces properties to the three things a session takes from them.
// Source names the endpoint the number came from, and a server that reports
// no context size leaves the caller on its own hint.
func ready(p Props, source string) Ready {
	r := Ready{Caps: p.Caps}
	if p.NCtx > 0 {
		r.NCtx, r.Source = p.NCtx, source
	}
	return r
}
