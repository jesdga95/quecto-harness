// Package mcp is a Model Context Protocol client for tools alone: it starts a
// server, lists its tools and calls them, over stdio or Streamable HTTP. It
// declares no client capabilities and answers any request a server sends it,
// other than ping, as a method it does not have.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// JSON-RPC error codes the client sends or reads.
const (
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// message is any JSON-RPC 2.0 frame. A response has an id and a result or an
// error, a request has an id and a method, and a notification has a method
// alone. The id stays raw, since a server may number its own requests with
// strings and the answer has to echo the same token.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (m message) isResponse() bool { return len(m.ID) > 0 && m.Method == "" }
func (m message) isRequest() bool  { return len(m.ID) > 0 && m.Method != "" }

// intID reads an id the client minted. Any other id answers nothing pending.
func (m message) intID() (int64, bool) {
	n, err := strconv.ParseInt(string(m.ID), 10, 64)
	return n, err == nil
}

// RPCError is an error a server answered with.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("server error %d", e.Code)
	}
	return fmt.Sprintf("server error %d: %s", e.Code, e.Message)
}

// ErrClosed is what a call on a client whose connection has ended returns,
// wrapped with the reason the connection ended where one is known.
var ErrClosed = errors.New("the server connection is closed")

// decodeFrames reads one body that holds a message or a batch of them. A
// frame that is not JSON-RPC is dropped, never fatal.
func decodeFrames(raw []byte) []message {
	var batch []json.RawMessage
	if err := json.Unmarshal(raw, &batch); err != nil {
		batch = []json.RawMessage{raw}
	}
	out := make([]message, 0, len(batch))
	for _, b := range batch {
		var m message
		if err := json.Unmarshal(b, &m); err != nil {
			continue
		}
		if m.Method == "" && len(m.ID) == 0 {
			continue
		}
		out = append(out, m)
	}
	return out
}
