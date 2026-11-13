package mcp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// fake is a scripted server shared by the stdio helper process and the HTTP
// test server. emit sends a frame to the client.
type fake struct {
	version string
	emit    func(message)

	mu        sync.Mutex
	cancelled []string
	rootsErr  string
	slow      map[string]chan struct{}
	// extra are tools only the helper process has, such as one that exits.
	extra map[string]func() string
}

func newFake(version string, emit func(message)) *fake {
	if version == "" {
		version = latestVersion
	}
	return &fake{version: version, emit: emit, slow: map[string]chan struct{}{}}
}

var tinyPNG = base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))

func (f *fake) reply(m message, result any) {
	raw, _ := json.Marshal(result)
	f.emit(message{JSONRPC: "2.0", ID: m.ID, Result: raw})
}

func (f *fake) fail(m message, code int, msg string) {
	f.emit(message{JSONRPC: "2.0", ID: m.ID, Error: &RPCError{Code: code, Message: msg}})
}

// handle answers one frame from the client.
func (f *fake) handle(m message) {
	switch {
	case m.isResponse():
		if string(m.ID) == `"srv-1"` && m.Error != nil {
			f.mu.Lock()
			f.rootsErr = fmt.Sprint(m.Error.Code)
			f.mu.Unlock()
		}
		return
	case m.Method == "initialize":
		f.reply(m, map[string]any{
			"protocolVersion": f.version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
			"serverInfo":      map[string]any{"name": "fake", "version": "1.0"},
			"instructions":    "Use the fake.",
		})
	case m.Method == "notifications/initialized":
		// A server may ask the client something it never offered.
		f.emit(message{JSONRPC: "2.0", ID: json.RawMessage(`"srv-1"`), Method: "roots/list"})
	case m.Method == "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason"`
		}
		_ = json.Unmarshal(m.Params, &p)
		f.mu.Lock()
		f.cancelled = append(f.cancelled, string(p.RequestID)+" "+p.Reason)
		if ch := f.slow[string(p.RequestID)]; ch != nil {
			close(ch)
			delete(f.slow, string(p.RequestID))
		}
		f.mu.Unlock()
	case m.Method == "ping":
		f.reply(m, map[string]any{})
	case m.Method == "tools/list":
		var p struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(m.Params, &p)
		if p.Cursor == "" {
			f.reply(m, map[string]any{
				"tools": []any{
					map[string]any{"name": "echo", "description": "Echoes.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
					map[string]any{"description": "No name."},
					map[string]any{"name": "bad_schema", "inputSchema": "string"},
					map[string]any{"name": "noschema"},
				},
				"nextCursor": "page2",
			})
			return
		}
		f.reply(m, map[string]any{"tools": []any{
			map[string]any{"name": "echo", "inputSchema": map[string]any{"type": "object"}},
			map[string]any{"name": "image", "inputSchema": map[string]any{"type": "object"}},
			map[string]any{"name": "slow", "inputSchema": map[string]any{"type": "object"}},
		}})
	case m.Method == "tools/call":
		f.call(m)
	default:
		if len(m.ID) > 0 {
			f.fail(m, codeMethodNotFound, "no "+m.Method)
		}
	}
}

func (f *fake) call(m message) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(m.Params, &p)
	text := func(s string) map[string]any { return map[string]any{"type": "text", "text": s} }
	if fn := f.extra[p.Name]; fn != nil {
		f.reply(m, map[string]any{"content": []any{text(fn())}})
		return
	}
	switch p.Name {
	case "echo":
		f.reply(m, map[string]any{"content": []any{text(string(p.Arguments))}})
	case "image":
		f.reply(m, map[string]any{"content": []any{
			text("A picture."),
			map[string]any{"type": "image", "mimeType": "image/png", "data": tinyPNG},
			map[string]any{"type": "image", "mimeType": "image/tiff", "data": tinyPNG},
			map[string]any{"type": "resource_link", "uri": "file:///a.txt", "name": "a.txt", "mimeType": "text/plain"},
		}})
	case "structured":
		f.reply(m, map[string]any{"content": []any{}, "structuredContent": map[string]any{"temp": 21}})
	case "fails":
		f.reply(m, map[string]any{"content": []any{text("it broke")}, "isError": true})
	case "slow":
		ch := make(chan struct{})
		f.mu.Lock()
		f.slow[string(m.ID)] = ch
		f.mu.Unlock()
		go func() {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				f.reply(m, map[string]any{"content": []any{text("late")}})
			}
		}()
	case "report":
		f.mu.Lock()
		out := map[string]any{"cancelled": f.cancelled, "roots": f.rootsErr}
		f.mu.Unlock()
		raw, _ := json.Marshal(out)
		f.reply(m, map[string]any{"content": []any{text(string(raw))}})
	case "notify":
		f.reply(m, map[string]any{"content": []any{text("ok")}})
		f.emit(message{JSONRPC: "2.0", Method: "notifications/tools/list_changed"})
	default:
		f.fail(m, codeInvalidParams, "unknown tool "+p.Name)
	}
}
