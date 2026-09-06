package agent

import (
	"context"

	"quecto-harness/internal/llama"
)

// Backend is the model server, as much of it as a session needs. The engine
// holds one of these rather than a concrete client, so a server that is not
// llama.cpp is a new implementation of three methods rather than a change to
// the turn loop.
//
// Prepare carries the whole of "this model has to be ready before we can talk
// to it". A server that keeps everything loaded returns what it knows and does
// nothing else, and no caller can tell the difference.
type Backend interface {
	Chat(ctx context.Context, req llama.ChatRequest, on func(llama.Event) error) (llama.Result, error)
	Models(ctx context.Context) ([]llama.Model, error)
	Prepare(ctx context.Context, model string) (llama.Ready, error)
}
