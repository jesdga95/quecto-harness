package mcp

import "strings"

// WirePrefix starts the name of every tool that comes from a server, which is
// what keeps one from colliding with a built-in tool.
const WirePrefix = "mcp__"

// maxWireName is the longest tool name the OpenAI protocols accept.
const maxWireName = 64

// WireName is the name a server's tool is sent under. Characters a tool name
// may not carry become underscores. The second result is false when the name
// does not fit.
func WireName(server, tool string) (string, bool) {
	var b strings.Builder
	for _, r := range tool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name := WirePrefix + server + "__" + b.String()
	if server == "" || b.Len() == 0 || len(name) > maxWireName {
		return "", false
	}
	return name, true
}

// IsWire reports whether a tool name is one a server's tool is sent under.
func IsWire(name string) bool { return strings.HasPrefix(name, WirePrefix) }
