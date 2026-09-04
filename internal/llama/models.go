package llama

import (
	"context"
	"strconv"
)

type ModelStatus string

const (
	StatusUnloaded    ModelStatus = "unloaded"
	StatusLoading     ModelStatus = "loading"
	StatusLoaded      ModelStatus = "loaded"
	StatusSleeping    ModelStatus = "sleeping"
	StatusDownloading ModelStatus = "downloading"
)

// Model is one entry of GET /v1/models, reduced to what the picker needs.
// CtxHint is a per-slot context size and CtxSource says how it was obtained.
type Model struct {
	ID         string
	Status     ModelStatus
	Failed     bool
	ExitCode   int
	Modalities []string
	CtxHint    int
	CtxSource  string
}

type modelsBody struct {
	Data []modelEntry `json:"data"`
}

type modelEntry struct {
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
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var body modelsBody
	if err := c.getJSON(ctx, "models", "", c.BaseURL+"/v1/models", &body); err != nil {
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
