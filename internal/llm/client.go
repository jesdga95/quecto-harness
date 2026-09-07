// Package llm talks to one model server. Two protocols are spoken: a
// llama.cpp server, in router or single-model mode, and an OpenAI-compatible
// endpoint. Which one is behind an address is settled on the first request.
// The package converts HTTP and SSE into Go values and knows nothing about
// sessions, tools or permissions.
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxErrorBody is how much of a failing response is kept for the message.
const maxErrorBody = 8 << 10

// Both protocols serve these two routes, and they are the only ones an
// OpenAI-compatible server is expected to have.
const (
	modelsPath = "/v1/models"
	chatPath   = "/v1/chat/completions"
)

// Client is the model server at one address: the HTTP half every backend
// shares, and the backend that address turned out to speak. It has no global
// timeout because a chat stream can legitimately run for minutes; deadlines
// come from ctx.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client

	mu         sync.Mutex
	resolved   Backend
	resolvedAt time.Time
}

func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				ExpectContinueTimeout: time.Second,
				DisableCompression:    true,
			},
		},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// authorize adds the bearer token, if there is one, to any request.
func (c *Client) authorize(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// getJSON performs a GET and decodes the body into v. A non-2xx status or an
// undecodable body becomes an *Error carrying op.
func (c *Client) getJSON(ctx context.Context, op, model, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return classify(op, c.BaseURL, model, 0, "", err)
	}
	req.Header.Set("Accept", "application/json")
	c.authorize(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return classify(op, c.BaseURL, model, 0, "", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return classify(op, c.BaseURL, model, resp.StatusCode, string(body), nil)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return classify(op, c.BaseURL, model, 0, "", err)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &Error{Kind: KindMalformed, Op: op, err: err,
			Message: "The model server sent a reply that could not be read: check that " + c.BaseURL + " is a model server"}
	}
	return nil
}
