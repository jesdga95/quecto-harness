package agent

import "quecto-harness/internal/perms"

// Event is one SSE payload. Type names the event; Fields carries the rest,
// which the transport merges with the sequence number it assigns.
type Event struct {
	Type   string
	Fields map[string]any
}

func ev(kind string, fields map[string]any) Event {
	if fields == nil {
		fields = map[string]any{}
	}
	return Event{Type: kind, Fields: fields}
}

// Publisher is the transport that fans events out to attached browsers.
type Publisher interface {
	Publish(sessionID string, ev Event)
}

// PermissionEvent renders a pending request for the browser. The broker calls
// it from whichever goroutine is blocked on the decision.
func PermissionEvent(r perms.Request) Event {
	return ev("permission", map[string]any{
		"id":      r.ID,
		"tool":    r.Tool,
		"command": r.Command,
		"path":    r.Path,
		"prefix":  r.Prefix,
		"args":    r.Args,
	})
}
