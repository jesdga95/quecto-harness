package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// latestVersion is the protocol version the client asks for. A server that
// answers with another one is accepted only when it is on supportedVersions,
// every one of which defines tools/list and tools/call the same way.
const latestVersion = "2025-06-18"

var supportedVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// Limits on what one server may hand the client.
const (
	maxTools = 500
	maxPages = 50
)

// Spec says how to reach one server. Exactly one of the two is set.
type Spec struct {
	Stdio *StdioSpec
	HTTP  *HTTPSpec
}

// Options are what the client tells the server about itself, and where the
// server's notifications go. OnNotify runs on the read loop and must not
// block.
type Options struct {
	Version  string
	OnNotify func(method string, params json.RawMessage)
}

// ServerInfo is what a server said about itself when it started. Every field
// may be empty.
type ServerInfo struct {
	Name         string
	Version      string
	Protocol     string
	Instructions string
}

// Client is one live connection to one server.
type Client struct {
	t      transport
	notify func(string, json.RawMessage)
	next   atomic.Int64
	done   chan struct{}
	// expired is set when the server answered that it no longer knows the
	// session, which ends this client without the server having failed.
	expired atomic.Bool

	mu      sync.Mutex
	pending map[int64]chan message

	Info ServerInfo
}

// Start connects to a server and runs the initialize exchange. A server that
// does not finish it before ctx ends is stopped. One that ends the session
// before the exchange is over gets one more try with a new session.
func Start(ctx context.Context, spec Spec, o Options) (*Client, error) {
	c, err := connect(ctx, spec, o)
	if errors.Is(err, errSessionExpired) {
		c, err = connect(ctx, spec, o)
	}
	return c, err
}

func connect(ctx context.Context, spec Spec, o Options) (*Client, error) {
	var t transport
	switch {
	case spec.Stdio != nil:
		s, err := startStdio(*spec.Stdio)
		if err != nil {
			return nil, err
		}
		t = s
	case spec.HTTP != nil:
		h, err := newHTTP(*spec.HTTP)
		if err != nil {
			return nil, err
		}
		t = h
	default:
		return nil, errors.New("no transport is set")
	}
	c := &Client{t: t, notify: o.OnNotify, done: make(chan struct{}), pending: map[int64]chan message{}}
	go c.loop()
	if err := c.initialize(ctx, o.Version); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) initialize(ctx context.Context, version string) error {
	if version == "" {
		version = "dev"
	}
	params := map[string]any{
		"protocolVersion": latestVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "quecto-harness", "title": "Quecto Harness", "version": version},
	}
	var res struct {
		ProtocolVersion *string `json:"protocolVersion"`
		ServerInfo      *struct {
			Name    *string `json:"name"`
			Version *string `json:"version"`
		} `json:"serverInfo"`
		Instructions *string `json:"instructions"`
	}
	if err := c.call(ctx, "initialize", params, &res); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if res.ProtocolVersion == nil {
		return errors.New("initialize: the server named no protocol version")
	}
	if !slices.Contains(supportedVersions, *res.ProtocolVersion) {
		return fmt.Errorf("initialize: the server speaks protocol %s and the harness speaks %s", *res.ProtocolVersion, latestVersion)
	}
	c.Info.Protocol = *res.ProtocolVersion
	if res.ServerInfo != nil {
		c.Info.Name = deref(res.ServerInfo.Name)
		c.Info.Version = deref(res.ServerInfo.Version)
	}
	c.Info.Instructions = deref(res.Instructions)
	if v, ok := c.t.(interface{ setVersion(string) }); ok {
		v.setVersion(c.Info.Protocol)
	}
	if err := c.send(ctx, message{Method: "notifications/initialized"}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return nil
}

// loop reads every frame until the connection ends. Responses go to the call
// waiting on them, requests are answered, notifications are handed on.
func (c *Client) loop() {
	defer close(c.done)
	for m := range c.t.Recv() {
		switch {
		case m.isResponse():
			id, ok := m.intID()
			if !ok {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case m.isRequest():
			go c.answer(m)
		default:
			if c.notify != nil {
				c.notify(m.Method, m.Params)
			}
		}
	}
}

// answer replies to a request from the server. The client offers nothing a
// server may ask for, so ping alone has an answer.
func (c *Client) answer(m message) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reply := message{ID: m.ID}
	if m.Method == "ping" {
		reply.Result = json.RawMessage(`{}`)
	} else {
		reply.Error = &RPCError{Code: codeMethodNotFound, Message: "method not found: " + m.Method}
	}
	_ = c.send(ctx, reply)
}

func (c *Client) send(ctx context.Context, m message) error {
	m.JSONRPC = "2.0"
	frame, err := json.Marshal(m)
	if err != nil {
		return err
	}
	err = c.t.Send(ctx, frame)
	if errors.Is(err, errSessionExpired) && !c.expired.Swap(true) {
		// A session the server forgot is gone for good, whatever was being
		// sent: the client ends, and a new one starts with a new initialize.
		go c.t.Close()
	}
	return err
}

// call sends a request and waits for its answer. When ctx ends first the
// server is told the request is cancelled, and the call returns at once.
func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := c.next.Add(1)
	ch := make(chan message, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(ctx, message{ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: method, Params: raw}); err != nil {
		if errors.Is(err, errSessionExpired) {
			return fmt.Errorf("%w: %w", ErrClosed, err)
		}
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if out != nil {
			if len(m.Result) == 0 || json.Unmarshal(m.Result, out) != nil {
				return fmt.Errorf("the server's answer to %s is not valid", method)
			}
		}
		return nil
	case <-ctx.Done():
		go c.cancelRequest(id, ctx.Err())
		return ctx.Err()
	case <-c.done:
		return c.closedErr()
	}
}

func (c *Client) cancelRequest(id int64, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reason := "cancelled"
	if errors.Is(cause, context.DeadlineExceeded) {
		reason = "timed out"
	}
	params, _ := json.Marshal(map[string]any{"requestId": id, "reason": reason})
	_ = c.send(ctx, message{Method: "notifications/cancelled", Params: params})
}

func (c *Client) closedErr() error {
	if err := c.t.Err(); err != nil {
		return err
	}
	return ErrClosed
}

// Expired reports that the server ended the session, which a new client
// recovers from at once.
func (c *Client) Expired() bool { return c.expired.Load() }

// Done closes when the connection ends, for whatever reason.
func (c *Client) Done() <-chan struct{} { return c.done }

// Err is why the connection ended, nil while it is open.
func (c *Client) Err() error {
	select {
	case <-c.done:
		return c.closedErr()
	default:
		return nil
	}
}

// Close ends the connection and waits for the read loop to finish.
func (c *Client) Close() error {
	err := c.t.Close()
	<-c.done
	return err
}

// Ping checks that the server still answers.
func (c *Client) Ping(ctx context.Context) error {
	return c.call(ctx, "ping", map[string]any{}, nil)
}

// Tool is one tool as the server listed it. InputSchema is the JSON Schema
// exactly as received, so the same list marshals to the same bytes.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// emptySchema stands in for a tool that named no input schema.
var emptySchema = json.RawMessage(`{"type":"object","properties":{}}`)

// ListTools reads every page of the server's tools. A malformed entry is
// dropped and named in the notes rather than failing the list.
func (c *Client) ListTools(ctx context.Context) ([]Tool, []string, error) {
	var tools []Tool
	var notes []string
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < maxPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Tools      []json.RawMessage `json:"tools"`
			NextCursor *string           `json:"nextCursor"`
		}
		if err := c.call(ctx, "tools/list", params, &res); err != nil {
			return nil, nil, fmt.Errorf("list tools: %w", err)
		}
		if res.Tools == nil {
			return nil, nil, errors.New("list tools: the server did not return a tools array")
		}
		for _, raw := range res.Tools {
			t, note := decodeTool(raw)
			if note != "" {
				notes = append(notes, note)
				continue
			}
			if seen[t.Name] {
				notes = append(notes, fmt.Sprintf("tool %q was listed twice; the second was dropped", t.Name))
				continue
			}
			if len(tools) == maxTools {
				notes = append(notes, fmt.Sprintf("the server lists more than %d tools; the rest were dropped", maxTools))
				return tools, notes, nil
			}
			seen[t.Name] = true
			tools = append(tools, t)
		}
		if res.NextCursor == nil || *res.NextCursor == "" || *res.NextCursor == cursor {
			return tools, notes, nil
		}
		cursor = *res.NextCursor
	}
	notes = append(notes, fmt.Sprintf("the tool list ran past %d pages; the rest were dropped", maxPages))
	return tools, notes, nil
}

func decodeTool(raw json.RawMessage) (Tool, string) {
	var t struct {
		Name        *string         `json:"name"`
		Title       *string         `json:"title"`
		Description *string         `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	if json.Unmarshal(raw, &t) != nil {
		return Tool{}, "a tool entry is not valid JSON and was dropped"
	}
	if t.Name == nil || *t.Name == "" {
		return Tool{}, "a tool with no name was dropped"
	}
	schema := bytes.TrimSpace(t.InputSchema)
	switch {
	case len(schema) == 0 || string(schema) == "null":
		schema = emptySchema
	case schema[0] != '{':
		return Tool{}, fmt.Sprintf("tool %q has an input schema that is not an object and was dropped", *t.Name)
	}
	return Tool{Name: *t.Name, Title: deref(t.Title), Description: deref(t.Description), InputSchema: json.RawMessage(schema)}, ""
}

// Content is one block of a tool result. Which fields are set depends on
// Type: text, image, audio, resource_link or resource.
type Content struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	Data     string            `json:"data,omitempty"`
	MimeType string            `json:"mimeType,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Name     string            `json:"name,omitempty"`
	Resource *ResourceContents `json:"resource,omitempty"`
}

// ResourceContents is a resource embedded in a result, as text or as base64.
type ResourceContents struct {
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// CallResult is what a tool call returned. IsError is the tool failing, which
// is a result and not a protocol error.
type CallResult struct {
	Content    []Content
	Structured json.RawMessage
	IsError    bool
}

// CallTool runs one tool. args must be a JSON object; empty is none.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	args = bytes.TrimSpace(args)
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) || args[0] != '{' {
		return CallResult{}, errors.New("the arguments are not a JSON object")
	}
	var res struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
		IsError           *bool             `json:"isError"`
	}
	if err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return CallResult{}, err
	}
	out := CallResult{IsError: res.IsError != nil && *res.IsError}
	if s := bytes.TrimSpace(res.StructuredContent); len(s) > 0 && string(s) != "null" {
		out.Structured = json.RawMessage(s)
	}
	for _, raw := range res.Content {
		var b Content
		if json.Unmarshal(raw, &b) == nil && b.Type != "" {
			out.Content = append(out.Content, b)
		}
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
