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

// Features is what a server does beyond the protocol it speaks, for one
// model. The zero value is a server that does none of it.
type Features struct {
	// AnswerNow: a reply's thinking can be ended while it runs.
	AnswerNow bool `json:"answer_now"`
	// Documents and Video: a message may carry a PDF or Office document, or a
	// video, as a part of its own.
	Documents bool `json:"documents"`
	Video     bool `json:"video"`
}

// Props is what one model reports about itself. NCtx is per slot.
type Props struct {
	Model      string
	NCtx       int
	TotalSlots int
	Build      string
	Sleeping   bool
	Caps       Caps
	Features   Features
}

// Ready is what a session needs to know about a model before it can talk to
// it: the window one conversation gets, where that number came from, and what
// the chat template accepts.
type Ready struct {
	NCtx     int
	Source   string
	Caps     Caps
	Features Features
}

// ready reduces properties to the things a session takes from them.
// Source names the endpoint the number came from, and a server that reports
// no context size leaves the caller on its own hint.
func ready(p Props, source string) Ready {
	r := Ready{Caps: p.Caps, Features: p.Features}
	if p.NCtx > 0 {
		r.NCtx, r.Source = p.NCtx, source
	}
	return r
}
