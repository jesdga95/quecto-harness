package llm

import "context"

// Backend is one protocol a model server speaks. Everything above this
// package works in these five calls and never learns which kind of server
// answered.
//
// Prepare carries the whole of "this model has to be ready before we can talk
// to it". A server that keeps everything loaded returns what it knows and
// does nothing else, and no caller can tell the difference.
type Backend interface {
	Detect(ctx context.Context) (Info, error)
	Models(ctx context.Context) ([]Model, error)
	Prepare(ctx context.Context, model string) (Ready, error)
	Props(ctx context.Context, model string) (Props, error)
	Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error)
}

type Mode string

const (
	ModeUnknown Mode = "unknown"
	ModeRouter  Mode = "router"
	ModeSingle  Mode = "single"
	ModeOpenAI  Mode = "openai"
)

// Info is what one probe tells us about the server as a whole. Autoload is
// router-only; Build is llama.cpp-only; Model and NCtx are set by a server
// that holds exactly one model.
type Info struct {
	Mode     Mode
	Build    string
	Autoload bool
	Model    string
	NCtx     int
}

// resolve returns the backend for this address, probing once and reusing the
// answer. A probe that failed is not remembered, so a server that comes up
// later is found on the next call.
func (c *Client) resolve(ctx context.Context) (Backend, error) {
	c.mu.Lock()
	b := c.backend
	c.mu.Unlock()
	if b != nil {
		return b, nil
	}
	b, err := c.probe(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.backend = b
	c.mu.Unlock()
	return b, nil
}

// probe reads the llama.cpp properties endpoint, which takes no query
// parameters and so can never load a model. A server that answers it with a
// build is llama.cpp; anything else, a 404 included, is treated as an
// OpenAI-compatible endpoint. Silence and a refused token name no protocol at
// all, so they come back as the failure they are.
func (c *Client) probe(ctx context.Context) (Backend, error) {
	var body propsBody
	err := c.getJSON(ctx, "detect", "", c.BaseURL+propsPath, &body)
	switch Kind(err) {
	case KindAuth, KindUnreachable, KindCanceled:
		return nil, err
	}
	if err == nil && body.BuildInfo != nil {
		return &llamaBackend{c: c}, nil
	}
	return &openAIBackend{c: c}, nil
}

func (c *Client) Detect(ctx context.Context) (Info, error) {
	b, err := c.resolve(ctx)
	if err != nil {
		return Info{Mode: ModeUnknown}, err
	}
	return b.Detect(ctx)
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	b, err := c.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return b.Models(ctx)
}

func (c *Client) Prepare(ctx context.Context, model string) (Ready, error) {
	b, err := c.resolve(ctx)
	if err != nil {
		return Ready{}, err
	}
	return b.Prepare(ctx, model)
}

func (c *Client) Props(ctx context.Context, model string) (Props, error) {
	b, err := c.resolve(ctx)
	if err != nil {
		return Props{}, err
	}
	return b.Props(ctx, model)
}

func (c *Client) Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error) {
	b, err := c.resolve(ctx)
	if err != nil {
		return Result{}, err
	}
	return b.Chat(ctx, req, on)
}
