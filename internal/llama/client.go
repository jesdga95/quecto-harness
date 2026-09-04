// Package llama talks to one llama.cpp server, in router mode or in
// single-model mode. It converts HTTP and SSE into Go values and knows
// nothing about sessions, tools or permissions.
package llama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxErrorBody is how much of a failing response is kept for the message.
const maxErrorBody = 8 << 10

// Client talks to one llama.cpp server. It has no global timeout because a
// chat stream can legitimately run for minutes; deadlines come from ctx.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
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

// modelURL builds a model-scoped GET. autoload=false is not optional: a
// model-scoped GET without it loads the model on a router.
func (c *Client) modelURL(path, model string, extra url.Values) string {
	q := url.Values{}
	for k, v := range extra {
		q[k] = v
	}
	if model != "" {
		q.Set("model", model)
		q.Set("autoload", "false")
	}
	if len(q) == 0 {
		return c.BaseURL + path
	}
	return c.BaseURL + path + "?" + q.Encode()
}

// getJSON performs a GET and decodes the body into v. A non-2xx status or an
// undecodable body becomes an *Error carrying op.
func (c *Client) getJSON(ctx context.Context, op, model, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return classify(op, c.BaseURL, model, 0, "", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
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
			Message: "the model server sent a reply that could not be read: check that " + c.BaseURL + " is a llama.cpp server"}
	}
	return nil
}
