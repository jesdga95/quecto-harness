package llm

import (
	"context"
	"slices"
)

// basaltBackend is a Basalt server. It speaks llama.cpp's protocol and keeps
// llama.cpp's /props fields, so it is that backend reading the features
// Basalt adds to /props.
type basaltBackend struct {
	*llamaBackend
}

func newBasaltBackend(c *Client) *basaltBackend {
	return &basaltBackend{&llamaBackend{c: c, read: readBasaltProps}}
}

// basaltProps is a /props response: llama.cpp's fields and the "basalt" key,
// whose capabilities are those of the model the response is about. Version
// may be null. Names not read here are ignored.
type basaltProps struct {
	propsBody
	Basalt *struct {
		Version      *string  `json:"version"`
		Capabilities []string `json:"capabilities"`
	} `json:"basalt"`
}

func readBasaltProps(c *Client, ctx context.Context, op, model, u string) (propsBody, Features, error) {
	var body basaltProps
	if err := c.getJSON(ctx, op, model, u, &body); err != nil {
		return propsBody{}, Features{}, err
	}
	var f Features
	if body.Basalt != nil {
		caps := body.Basalt.Capabilities
		f = Features{
			AnswerNow: slices.Contains(caps, "answer_now"),
			Documents: slices.Contains(caps, "documents"),
			Video:     slices.Contains(caps, "video"),
		}
	}
	return body.propsBody, f, nil
}
