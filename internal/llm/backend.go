package llm

import (
	"context"
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
	ModeUnknown   Mode = "unknown"
	ModeRouter    Mode = "router"
	ModeSingle    Mode = "single"
	ModeOpenAI    Mode = "openai"
	ModeResponses Mode = "responses"
)

// Protocol is the wire protocol an address speaks. It is named rather than
// found: two of the three answer the same model list at the same address, so
// no probe can tell them apart, and a probe that can be wrong about the
// others buys nothing over being told.
type Protocol string

const (
	ProtocolLlama     Protocol = "llamacpp"
	ProtocolChat      Protocol = "chat"
	ProtocolResponses Protocol = "responses"
)

// ValidProtocol reports whether s is one of the three.
func ValidProtocol(s string) bool {
	switch Protocol(s) {
	case ProtocolLlama, ProtocolChat, ProtocolResponses:
		return true
	}
	return false
}

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

// backend is the protocol this address speaks. It costs nothing to build and
// holds nothing, so there is no memo to go stale and no probe to pay for.
func (c *Client) backend() Backend {
	switch c.Protocol {
	case ProtocolChat:
		return &openAIBackend{c: c}
	case ProtocolResponses:
		return &responsesBackend{c: c}
	default:
		return &llamaBackend{c: c}
	}
}

// Detect asks the address about itself. It does not settle which protocol is
// spoken, only what the server running it says: a build and an autoload
// setting from llama.cpp, and the model it holds when it holds exactly one.
func (c *Client) Detect(ctx context.Context) (Info, error) {
	return c.backend().Detect(ctx)
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	return c.backend().Models(ctx)
}

func (c *Client) Prepare(ctx context.Context, model string) (Ready, error) {
	return c.backend().Prepare(ctx, model)
}

func (c *Client) Props(ctx context.Context, model string) (Props, error) {
	return c.backend().Props(ctx, model)
}

func (c *Client) Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error) {
	return c.backend().Chat(ctx, req, on)
}
