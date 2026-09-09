package llm

import (
	"context"
	"encoding/json"
	"strconv"
)

// openAIBackend is any server that speaks the OpenAI chat API and nothing
// more: one model list, one chat endpoint, no way to ask what a model is
// doing. Everything the harness would read from /props is either taken from
// the model list or left unknown.
type openAIBackend struct{ c *Client }

type openAIModelsBody struct {
	Data []openAIModelEntry `json:"data"`
}

// openAIModelEntry is one entry of GET /v1/models. Only id is standard; the
// two context fields are what self-hosted servers put there, and a server
// that sends neither leaves the window unknown.
type openAIModelEntry struct {
	ID            *string `json:"id"`
	MaxModelLen   *int    `json:"max_model_len"`
	ContextLength *int    `json:"context_length"`
}

// Detect asks for the model list, which is the only thing every such server
// has. A list of one names the model the server holds.
func (b *openAIBackend) Detect(ctx context.Context) (Info, error) {
	models, err := b.Models(ctx)
	if err != nil {
		return Info{Mode: ModeUnknown}, err
	}
	info := Info{Mode: ModeOpenAI}
	if len(models) == 1 {
		info.Model, info.NCtx = models[0].ID, models[0].CtxHint
	}
	return info, nil
}

// Models lists what the server offers. Every model it names can answer, so
// none of them is reported as waiting to be loaded.
func (b *openAIBackend) Models(ctx context.Context) ([]Model, error) {
	return listOpenAIModels(ctx, b.c)
}

// listOpenAIModels reads GET /v1/models, which is the one route every server
// speaking either OpenAI protocol has, and is shared by both backends.
func listOpenAIModels(ctx context.Context, c *Client) ([]Model, error) {
	var body openAIModelsBody
	if err := c.getJSON(ctx, "models", "", c.BaseURL+modelsPath, &body); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(body.Data))
	for _, e := range body.Data {
		id := deref(e.ID)
		if id == "" {
			continue
		}
		m := Model{ID: id, Status: StatusLoaded, Modalities: []string{}}
		if n := derefOr(e.MaxModelLen, deref(e.ContextLength)); n > 0 {
			m.CtxHint, m.CtxSource = n, "models"
		}
		out = append(out, m)
	}
	return out, nil
}

// Prepare has nothing to start, so it reports what the model list says and
// returns. The chat template is the server's business, so no capability is
// claimed and the request goes out with the fields the API defines.
func (b *openAIBackend) Prepare(ctx context.Context, model string) (Ready, error) {
	p, err := b.Props(ctx, model)
	if err != nil {
		return Ready{}, err
	}
	return ready(p, "models"), nil
}

// Props reports the one model property such a server publishes, the context
// window, and leaves the rest unknown.
func (b *openAIBackend) Props(ctx context.Context, model string) (Props, error) {
	return openAIProps(ctx, b.c, model)
}

func openAIProps(ctx context.Context, c *Client, model string) (Props, error) {
	models, err := listOpenAIModels(ctx, c)
	if err != nil {
		return Props{}, err
	}
	if model == "" {
		return Props{}, &Error{Kind: KindBadRequest, Op: "props",
			Message: "No model was named: pick one from the model list"}
	}
	for _, m := range models {
		if m.ID == model {
			return Props{Model: m.ID, NCtx: m.CtxHint}, nil
		}
	}
	return Props{}, &Error{Kind: KindNotFound, Op: "props",
		Message: "Model " + strconv.Quote(model) + " is not on " + c.BaseURL + ": pick another from the model list"}
}

// Chat streams one completion using only fields the OpenAI API defines.
func (b *openAIBackend) Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error) {
	body, err := buildOpenAIBody(req)
	if err != nil {
		return Result{}, buildError(err)
	}
	return b.c.stream(ctx, chatPath, body, req.Model, newStreamState(), on)
}

// buildOpenAIBody renders the wire request. Thinking travels as
// reasoning_effort, the field the API has for it, with "none" for off. A
// server that does not take the field, or does not take the level, refuses
// the request and says so.
func buildOpenAIBody(req ChatRequest) ([]byte, error) {
	body := baseBody(req)
	switch req.Effort {
	case EffortAuto:
	case EffortOff:
		body["reasoning_effort"] = "none"
	default:
		body["reasoning_effort"] = string(req.Effort)
	}
	return json.Marshal(body)
}
