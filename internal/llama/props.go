package llama

import "context"

// Caps is chat_template_caps. Known is false when the server did not send the
// object, which is how a not-yet-loaded model looks.
type Caps struct {
	Known             bool `json:"known"`
	ToolCalls         bool `json:"tool_calls"`
	ParallelToolCalls bool `json:"parallel_tool_calls"`
	ReasoningEffort   bool `json:"reasoning_effort"`
	SystemRole        bool `json:"system_role"`
	PreserveReasoning bool `json:"preserve_reasoning"`
	// EffortLevels are the levels this model's template accepts. Empty when
	// the template named no set, which means it takes any name.
	EffortLevels []Effort `json:"effort_levels"`
}

// Props is GET /props for one model. NCtx is per slot.
type Props struct {
	Model      string
	NCtx       int
	TotalSlots int
	Build      string
	Sleeping   bool
	Caps       Caps
}

// Props reads one model's properties. The request carries autoload=false, so
// a model that is not loaded answers 4xx rather than being started.
func (c *Client) Props(ctx context.Context, model string) (Props, error) {
	var body propsBody
	if err := c.getJSON(ctx, "props", model, c.modelURL("/props", model, nil), &body); err != nil {
		return Props{}, err
	}
	p := Props{
		Model:      deref(body.ModelAlias),
		TotalSlots: deref(body.TotalSlots),
		Build:      deref(body.BuildInfo),
		Sleeping:   deref(body.IsSleeping),
	}
	if body.DefaultGenerationSettings != nil {
		p.NCtx = deref(body.DefaultGenerationSettings.NCtx)
	}
	if body.ChatTemplateCaps != nil {
		p.Caps = Caps{
			Known:             true,
			ToolCalls:         body.ChatTemplateCaps["supports_tool_calls"],
			ParallelToolCalls: body.ChatTemplateCaps["supports_parallel_tool_calls"],
			ReasoningEffort:   body.ChatTemplateCaps["supports_reasoning_effort"],
			SystemRole:        body.ChatTemplateCaps["supports_system_role"],
			PreserveReasoning: body.ChatTemplateCaps["supports_preserve_reasoning"],
		}
	}
	// The accepted levels are written in the template itself, not in the caps,
	// so they are read whether or not the caps object arrived.
	p.Caps.EffortLevels = templateEffortLevels(deref(body.ChatTemplate))
	return p, nil
}
