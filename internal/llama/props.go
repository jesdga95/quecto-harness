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
	if err := c.getJSON(ctx, "props", model, c.modelURL("/props", model, false, nil), &body); err != nil {
		return Props{}, err
	}
	return fromPropsBody(body), nil
}

// fromPropsBody reduces one /props response, scoped or not, to the fields the
// harness reads.
func fromPropsBody(body propsBody) Props {
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
	return p
}
