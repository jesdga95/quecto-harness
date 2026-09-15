package llm

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// propsPath is the properties endpoint. Without query parameters it reports
// the server; with a model named it reports that model.
const propsPath = "/props"

// llamaBackend is a llama.cpp server, holding one model or routing to many.
type llamaBackend struct{ c *Client }

// propsBody is one /props response, server-wide or model-scoped. Every scalar
// is a pointer, since a field the build does not send must stay distinct from
// the same field sent as zero.
type propsBody struct {
	Role                      *string         `json:"role"`
	ModelsAutoload            *bool           `json:"models_autoload"`
	ModelAlias                *string         `json:"model_alias"`
	BuildInfo                 *string         `json:"build_info"`
	TotalSlots                *int            `json:"total_slots"`
	IsSleeping                *bool           `json:"is_sleeping"`
	ChatTemplateCaps          map[string]bool `json:"chat_template_caps"`
	DefaultGenerationSettings *struct {
		NCtx *int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// Detect reads /props with no query parameters, so it can never load a model.
// Anything but role "router" is a single-model server.
func (b *llamaBackend) Detect(ctx context.Context) (Info, error) {
	var body propsBody
	if err := b.c.getJSON(ctx, "detect", "", b.c.BaseURL+propsPath, &body); err != nil {
		return Info{Mode: ModeUnknown}, err
	}
	info := Info{Mode: ModeSingle, Build: deref(body.BuildInfo)}
	if deref(body.Role) == "router" {
		info.Mode = ModeRouter
		info.Autoload = deref(body.ModelsAutoload)
		return info, nil
	}
	info.Model = deref(body.ModelAlias)
	if body.DefaultGenerationSettings != nil {
		info.NCtx = deref(body.DefaultGenerationSettings.NCtx)
	}
	return info, nil
}

// Props reads one model's properties. The request carries autoload=false, so
// a model that is not loaded answers 4xx rather than being started.
func (b *llamaBackend) Props(ctx context.Context, model string) (Props, error) {
	var body propsBody
	u := modelURL(b.c.BaseURL, propsPath, model, false)
	if err := b.c.getJSON(ctx, "props", model, u, &body); err != nil {
		return Props{}, err
	}
	return fromPropsBody(body), nil
}

// Prepare makes a model answerable and reports what it answers.
//
// A router holds models unloaded, and nothing about a model can be read until
// it is loaded: the chat template is inside the model file. So this is the one
// request allowed to name a model with autoload on. A single-model server has
// nothing to load and this is a read.
//
// The model is ready when it returns, so a session never has to shape its
// first request around an answer it does not have yet.
func (b *llamaBackend) Prepare(ctx context.Context, model string) (Ready, error) {
	var body propsBody
	if err := b.c.getJSON(ctx, "prepare", "", b.c.BaseURL+propsPath, &body); err != nil {
		return Ready{}, err
	}
	if deref(body.Role) != "router" {
		// The server holds one model and it is already up, so the properties
		// it just sent are that model's.
		return ready(fromPropsBody(body), "props"), nil
	}
	if model == "" {
		return Ready{}, &Error{Kind: KindBadRequest, Op: "prepare",
			Message: "No model was named: pick one from the model list"}
	}

	autoload := true
	models, err := b.Models(ctx)
	if err == nil {
		for _, m := range models {
			if m.ID == model && m.Status == StatusLoaded {
				autoload = false
			}
		}
	}
	var scoped propsBody
	if err := b.c.getJSON(ctx, "prepare", model, modelURL(b.c.BaseURL, propsPath, model, autoload), &scoped); err != nil {
		return Ready{}, err
	}
	return ready(fromPropsBody(scoped), "props"), nil
}

type llamaModelsBody struct {
	Data []llamaModelEntry `json:"data"`
}

type llamaModelEntry struct {
	ID     *string `json:"id"`
	Status *struct {
		Value    *string  `json:"value"`
		Failed   *bool    `json:"failed"`
		ExitCode *int     `json:"exit_code"`
		Args     []string `json:"args"`
	} `json:"status"`
	Architecture *struct {
		InputModalities []string `json:"input_modalities"`
	} `json:"architecture"`
	Meta *struct {
		NCtx *int `json:"n_ctx"`
	} `json:"meta"`
}

// Models lists what the server has. It sends no query parameters, so it
// cannot load anything.
func (b *llamaBackend) Models(ctx context.Context) ([]Model, error) {
	var body llamaModelsBody
	if err := b.c.getJSON(ctx, "models", "", b.c.BaseURL+modelsPath, &body); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(body.Data))
	for _, e := range body.Data {
		id := deref(e.ID)
		if id == "" {
			continue
		}
		m := Model{ID: id, Status: StatusUnloaded}
		if e.Status != nil {
			if v := deref(e.Status.Value); v != "" {
				m.Status = ModelStatus(v)
			}
			m.Failed = deref(e.Status.Failed)
			m.ExitCode = deref(e.Status.ExitCode)
		}
		if e.Architecture != nil {
			m.Modalities = e.Architecture.InputModalities
		}
		if e.Meta != nil && deref(e.Meta.NCtx) > 0 {
			m.CtxHint, m.CtxSource = deref(e.Meta.NCtx), "meta"
		} else if e.Status != nil {
			if hint := ctxFromArgs(e.Status.Args); hint > 0 {
				m.CtxHint, m.CtxSource = hint, "args"
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// Chat streams one completion, with the fields llama.cpp adds to the request:
// a reasoning format it can parse back out, and the template arguments that
// carry thinking settings.
func (b *llamaBackend) Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error) {
	body, err := buildLlamaBody(req)
	if err != nil {
		return Result{}, buildError(err)
	}
	return b.c.stream(ctx, chatPath, body, req.Model, newStreamState(), on)
}

// modelURL builds a model-scoped GET. autoload says whether the server may
// start the model to answer, so every caller states which it is asking for.
// Only Prepare passes true; naming a model with autoload on is what turns a
// read into a load.
func modelURL(base, path, model string, autoload bool) string {
	if model == "" {
		return base + path
	}
	q := url.Values{}
	q.Set("model", model)
	q.Set("autoload", strconv.FormatBool(autoload))
	return base + path + "?" + q.Encode()
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

// ctxFromArgs divides the launch context size by the slot count, which is
// what one conversation actually gets. Zero when the args did not say.
func ctxFromArgs(args []string) int {
	ctxSize, parallel := 0, 1
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--ctx-size", "-c":
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				ctxSize = n
			}
		case "--parallel", "-np":
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				parallel = n
			}
		}
	}
	if ctxSize == 0 {
		return 0
	}
	return ctxSize / parallel
}

// budget is the fallback for a template that cannot take a named effort
// level. High and xhigh are both -1, which llama.cpp reads as unbounded:
// there is nothing above unbounded left to ask such a template for.
var budget = map[Effort]int{
	EffortLow: 2048, EffortMedium: 8192, EffortHigh: -1, EffortXHigh: -1,
}

// applyEffort writes the thinking fields. Omitting the field and setting it
// to a level the template did not ask for are different requests. A template
// that knows none of the three names refuses the request rather than guessing,
// which is the model server's answer to give.
func applyEffort(body, kwargs map[string]any, e Effort, caps Caps) {
	switch {
	case e == EffortAuto:
	case e == EffortOff:
		body["reasoning_budget"] = 0
		kwargs["enable_thinking"] = false
	case caps.Known && caps.ReasoningEffort:
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

// buildLlamaBody renders the wire request. It is a map because the field set
// is conditional, and a map also marshals with sorted keys for the tests.
func buildLlamaBody(req ChatRequest) ([]byte, error) {
	body := baseBody(req)
	body["reasoning_format"] = "deepseek"
	// top_k and repeat_penalty are this server's, and the OpenAI API defines
	// neither, so they are built here rather than in the shared body.
	if req.TopK != nil {
		body["top_k"] = *req.TopK
	}
	if req.RepeatPenalty != nil {
		body["repeat_penalty"] = *req.RepeatPenalty
	}
	kwargs := map[string]any{}
	applyEffort(body, kwargs, req.Effort, req.Caps)
	applyPreserve(kwargs, req.PreserveReasoning, req.Caps)
	if len(kwargs) > 0 {
		body["chat_template_kwargs"] = kwargs
	}
	return json.Marshal(body)
}
