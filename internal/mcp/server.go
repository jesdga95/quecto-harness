package mcp

import (
	"slices"
	"strings"
	"time"
)

// Transport names how a server is reached. It is stated in the config, never
// worked out from the server.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// Server is one configured server, resolved. Problem is set when the record
// cannot run as written; such a record is kept, so a save does not lose it,
// and is offered to no one.
type Server struct {
	Name      string
	Enabled   bool
	Transport Transport
	Command   string
	Args      []string
	Env       map[string]string
	Cwd       string
	URL       string
	Headers   map[string]string
	// Timeout bounds one tool call. Zero takes the command timeout.
	Timeout time.Duration
	// ReadOnly is the user stating that no tool on this server changes
	// anything. It is never taken from the server's own annotations.
	ReadOnly bool
	// Allow names the tools that run without a card in allowlist mode.
	Allow []string
	// EnabledTools, when not nil, names the only tools the server offers a
	// turn; DisabledTools names tools it never offers. A tool that is not
	// offered costs nothing in the prompt.
	EnabledTools  []string
	DisabledTools []string
	Problem       string
}

// Usable reports whether the server is offered to a turn.
func (s Server) Usable() bool { return s.Enabled && s.Problem == "" }

// Allows reports whether a tool runs without a card in allowlist mode.
func (s Server) Allows(tool string) bool { return slices.Contains(s.Allow, tool) }

// Offers reports whether a turn is offered the tool at all.
func (s Server) Offers(tool string) bool {
	if s.EnabledTools != nil && !slices.Contains(s.EnabledTools, tool) {
		return false
	}
	return !slices.Contains(s.DisabledTools, tool)
}

// projectToken is replaced by the session's project root. A server whose
// command line or environment names it runs once per project.
const projectToken = "{project}"

// PerProject reports whether the server runs once per project.
func (s Server) PerProject() bool {
	if s.Transport != TransportStdio {
		return false
	}
	if strings.Contains(s.Cwd, projectToken) || slices.ContainsFunc(s.Args, hasToken) {
		return true
	}
	for _, v := range s.Env {
		if hasToken(v) {
			return true
		}
	}
	return false
}

func hasToken(s string) bool { return strings.Contains(s, projectToken) }

// Clone copies the slices and maps, so a copy handed out does not change with
// the original.
func (s Server) Clone() Server {
	s.Args = slices.Clone(s.Args)
	s.Allow = slices.Clone(s.Allow)
	s.EnabledTools = slices.Clone(s.EnabledTools)
	s.DisabledTools = slices.Clone(s.DisabledTools)
	s.Env = cloneMap(s.Env)
	s.Headers = cloneMap(s.Headers)
	return s
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Spec is how to reach the server for one project, with the project token
// replaced. An empty project leaves the token as written.
func (s Server) Spec(project string) Spec {
	sub := func(v string) string {
		if project == "" {
			return v
		}
		return strings.ReplaceAll(v, projectToken, project)
	}
	if s.Transport == TransportHTTP {
		return Spec{HTTP: &HTTPSpec{URL: s.URL, Headers: cloneMap(s.Headers)}}
	}
	args := make([]string, len(s.Args))
	for i, a := range s.Args {
		args[i] = sub(a)
	}
	var env []string
	for k, v := range s.Env {
		env = append(env, k+"="+sub(v))
	}
	slices.Sort(env)
	return Spec{Stdio: &StdioSpec{Command: s.Command, Args: args, Env: env, Dir: sub(s.Cwd)}}
}
