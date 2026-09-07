package llm

import (
	"context"
	"time"
)

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

// resolveTTL bounds how long the protocol is taken on trust. What answers at
// an address can change while the harness runs, and one probe is not a fact
// about that address for ever.
const resolveTTL = time.Minute

// remember stores the protocol the last detection settled on.
func (c *Client) remember(b Backend) {
	c.mu.Lock()
	c.resolved, c.resolvedAt = b, time.Now()
	c.mu.Unlock()
}

// backend is the protocol for a call that is not itself a detection. A run of
// requests costs one probe; after that the answer is old enough to be worth
// asking again.
func (c *Client) backend(ctx context.Context) (Backend, error) {
	c.mu.Lock()
	b, at := c.resolved, c.resolvedAt
	c.mu.Unlock()
	if b != nil && time.Since(at) < resolveTTL {
		return b, nil
	}
	if _, err := c.Detect(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolved, nil
}

// Detect asks the address what it is, and asks every time rather than
// answering from what it found before.
//
// The llama.cpp read comes first because it is also that protocol's
// detection: GET /props with no query parameters is a read on any llama.cpp
// server and cannot load a model, and a reply carrying a build says which
// server this is. Anything else that answers a model list is treated as
// OpenAI-compatible. A refused token and silence name no protocol, so they
// come back as the failure they are and nothing is remembered.
func (c *Client) Detect(ctx context.Context) (Info, error) {
	llama := &llamaBackend{c: c}
	info, err := llama.Detect(ctx)
	if err == nil && info.Build != "" {
		c.remember(llama)
		return info, nil
	}
	switch Kind(err) {
	case KindAuth, KindUnreachable, KindCanceled:
		return Info{Mode: ModeUnknown}, err
	}
	openai := &openAIBackend{c: c}
	info, err = openai.Detect(ctx)
	if err != nil {
		return Info{Mode: ModeUnknown}, err
	}
	c.remember(openai)
	return info, nil
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	b, err := c.backend(ctx)
	if err != nil {
		return nil, err
	}
	return b.Models(ctx)
}

func (c *Client) Prepare(ctx context.Context, model string) (Ready, error) {
	b, err := c.backend(ctx)
	if err != nil {
		return Ready{}, err
	}
	return b.Prepare(ctx, model)
}

func (c *Client) Props(ctx context.Context, model string) (Props, error) {
	b, err := c.backend(ctx)
	if err != nil {
		return Props{}, err
	}
	return b.Props(ctx, model)
}

func (c *Client) Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error) {
	b, err := c.backend(ctx)
	if err != nil {
		return Result{}, err
	}
	return b.Chat(ctx, req, on)
}
