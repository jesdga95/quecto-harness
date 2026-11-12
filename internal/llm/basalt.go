package llm

import (
	"context"
	"net/http"
	"slices"
)

// Headers a Basalt server reads on a chat request. The session header keeps
// a conversation on the slot holding its cache; the request id names the
// request before any reply has arrived.
const (
	headerSession   = "X-Session-Id"
	headerRequestID = "X-Client-Request-Id"
)

// basaltBackend is a Basalt server. It speaks llama.cpp's protocol and keeps
// llama.cpp's /props fields, so it is that backend with two differences: it
// reads the features Basalt adds to /props, and it names the session and the
// request on every chat request.
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

// Chat streams one completion with llama.cpp's body and Basalt's headers.
// Basalt names a prompt too long for the slot either way the two APIs do.
func (b *basaltBackend) Chat(ctx context.Context, req ChatRequest, on func(Event) error) (Result, error) {
	body, err := buildLlamaBody(req)
	if err != nil {
		return Result{}, buildError(err)
	}
	h := http.Header{}
	if req.Session != "" {
		h.Set(headerSession, req.Session)
	}
	if req.RequestID != "" {
		h.Set(headerRequestID, req.RequestID)
	}
	res, err := b.c.stream(ctx, chatPath, body, h, req.Model, newStreamState(), on)
	return res, markOverflow(err, func(body string) bool { return llamaOverflow(body) || openAIOverflow(body) })
}
