package llama

// deref reads a pointer field that may be absent. Absent is the zero value,
// which callers must not confuse with a value the server actually sent.
func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func derefOr[T any](p *T, d T) T {
	if p == nil {
		return d
	}
	return *p
}

// chunk is one decoded SSE payload. Every scalar with a meaningful zero is a
// pointer so that absent and zero stay distinct.
type chunk struct {
	ID      *string  `json:"id"`
	Object  *string  `json:"object"`
	Model   *string  `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage"`
	Timings *timings `json:"timings"`
	Error   *apiErr  `json:"error"`
}

type choice struct {
	Index        *int    `json:"index"`
	Delta        *delta  `json:"delta"`
	Message      *delta  `json:"message"`
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role             *string         `json:"role"`
	Content          *string         `json:"content"`
	ReasoningContent *string         `json:"reasoning_content"`
	Reasoning        *string         `json:"reasoning"`
	ToolCalls        []deltaToolCall `json:"tool_calls"`
}

type deltaToolCall struct {
	Index    *int    `json:"index"`
	ID       *string `json:"id"`
	Type     *string `json:"type"`
	Function *struct {
		Name      *string `json:"name"`
		Arguments *string `json:"arguments"`
	} `json:"function"`
}

type usage struct {
	PromptTokens        *int `json:"prompt_tokens"`
	CompletionTokens    *int `json:"completion_tokens"`
	TotalTokens         *int `json:"total_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type timings struct {
	CacheN             *int     `json:"cache_n"`
	PromptN            *int     `json:"prompt_n"`
	PromptPerSecond    *float64 `json:"prompt_per_second"`
	PredictedN         *int     `json:"predicted_n"`
	PredictedPerSecond *float64 `json:"predicted_per_second"`
}

type apiErr struct {
	Code    *int    `json:"code"`
	Message *string `json:"message"`
	Type    *string `json:"type"`
}

func (u *usage) toUsage() *Usage {
	if u == nil {
		return nil
	}
	out := &Usage{
		PromptTokens:     deref(u.PromptTokens),
		CompletionTokens: deref(u.CompletionTokens),
		TotalTokens:      deref(u.TotalTokens),
	}
	if u.PromptTokensDetails != nil {
		out.CachedTokens = deref(u.PromptTokensDetails.CachedTokens)
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.PromptTokens + out.CompletionTokens
	}
	return out
}

func (t *timings) toTimings() *Timings {
	if t == nil {
		return nil
	}
	return &Timings{
		PromptN:            deref(t.PromptN),
		PromptPerSecond:    deref(t.PromptPerSecond),
		PredictedN:         deref(t.PredictedN),
		PredictedPerSecond: deref(t.PredictedPerSecond),
		CacheN:             deref(t.CacheN),
	}
}
