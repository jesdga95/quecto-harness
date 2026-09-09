package agent

import (
	"context"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
)

// Backend is the model server, as much of it as a session needs. The engine
// reaches one through Backends rather than holding it, so the turn loop never
// learns which protocol answered and a fake needs three methods.
//
// Prepare carries the whole of "this model has to be ready before we can talk
// to it". A server that keeps everything loaded returns what it knows and does
// nothing else, and no caller can tell the difference.
type Backend interface {
	Chat(ctx context.Context, req llm.ChatRequest, on func(llm.Event) error) (llm.Result, error)
	Models(ctx context.Context) ([]llm.Model, error)
	Prepare(ctx context.Context, model string) (llm.Ready, error)
}

// Backends is the model server for one agent. A turn resolves the agent it
// runs against, so an edit to another agent reaches nothing in flight and two
// agents can be answering at once.
type Backends interface {
	For(a config.Agent) Backend
}

// Clients resolves an agent to the pooled client for its address and key, so
// an edit that moves neither keeps the client and the protocol it settled on.
type Clients struct{ Pool *llm.Pool }

func (c Clients) For(a config.Agent) Backend { return c.Pool.Get(a.URL, a.Key, a.Protocol) }
