package llama

import "context"

// Ready is what a session needs to know about a model before it can talk to
// it: the window one conversation gets, where that number came from, and what
// the chat template accepts.
type Ready struct {
	NCtx   int
	Source string
	Caps   Caps
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
func (c *Client) Prepare(ctx context.Context, model string) (Ready, error) {
	var body propsBody
	if err := c.getJSON(ctx, "prepare", "", c.BaseURL+"/props", &body); err != nil {
		return Ready{}, err
	}
	if deref(body.Role) != "router" {
		// The server holds one model and it is already up, so the properties
		// it just sent are that model's.
		return ready(fromPropsBody(body)), nil
	}
	if model == "" {
		return Ready{}, &Error{Kind: KindBadRequest, Op: "prepare",
			Message: "No model was named: pick one from the model list"}
	}

	autoload := true
	models, err := c.Models(ctx)
	if err == nil {
		for _, m := range models {
			if m.ID == model && m.Status == StatusLoaded {
				autoload = false
			}
		}
	}
	var scoped propsBody
	if err := c.getJSON(ctx, "prepare", model, c.modelURL("/props", model, autoload, nil), &scoped); err != nil {
		return Ready{}, err
	}
	return ready(fromPropsBody(scoped)), nil
}

// ready reduces properties to the three things a session takes from them. A
// server that reports no context size leaves the caller on its own hint.
func ready(p Props) Ready {
	r := Ready{Caps: p.Caps}
	if p.NCtx > 0 {
		r.NCtx, r.Source = p.NCtx, "props"
	}
	return r
}
