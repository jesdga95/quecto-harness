package agent

import (
	"context"

	"quecto-harness/internal/llm"
)

// Backend is the model server, as much of it as a session needs. The engine
// holds one of these rather than a concrete client, so the turn loop never
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
