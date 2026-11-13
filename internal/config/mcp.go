package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jesdga95/quecto-harness/internal/mcp"
)

// MCPServer is one configured MCP server.
type MCPServer = mcp.Server

// MaxMCPTimeoutS is the longest a tool call may be given.
const MaxMCPTimeoutS = 3600

// serverName is what a server's key in mcp.json may be. It is strict so that
// the server part of a tool's wire name is the name itself.
var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// FileMCP is mcp.json on disk. The shape is the mcpServers object that MCP
// servers publish in their READMEs, so a snippet pastes in as it is; the
// harness's own fields sit beside the published ones.
type FileMCP struct {
	Servers map[string]FileMCPServer `json:"mcpServers"`
}

type FileMCPServer struct {
	Type     *string           `json:"type,omitempty"`
	Command  *string           `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Cwd      *string           `json:"cwd,omitempty"`
	URL      *string           `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Enabled  *bool             `json:"enabled,omitempty"`
	TimeoutS *int              `json:"timeout_s,omitempty"`
	ReadOnly *bool             `json:"read_only,omitempty"`
	Allow    []string          `json:"allow,omitempty"`
}

// transportNames maps what the type field may say to a transport. The
// aliases are the spellings other clients write in the same file shape.
var transportNames = map[string]mcp.Transport{
	"stdio":           mcp.TransportStdio,
	"http":            mcp.TransportHTTP,
	"streamable-http": mcp.TransportHTTP,
	"streamableHttp":  mcp.TransportHTTP,
}

// ResolveMCP turns the file into servers sorted by name, so the same file
// always yields the same tool order. A record that cannot run is kept with
// its problem set, and each problem is also a note.
func ResolveMCP(f FileMCP) ([]MCPServer, []string) {
	var notes []string
	names := make([]string, 0, len(f.Servers))
	for n := range f.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]MCPServer, 0, len(names))
	for _, n := range names {
		s := resolveServer(n, f.Servers[n])
		if s.Problem != "" {
			notes = append(notes, "MCP server "+strconv.Quote(n)+" cannot run. "+s.Problem)
		}
		out = append(out, s)
	}
	return out, notes
}

func resolveServer(name string, f FileMCPServer) MCPServer {
	s := MCPServer{
		Name: name, Enabled: true,
		Command: deref(f.Command), Args: slices.Clone(f.Args), Env: f.Env, Cwd: deref(f.Cwd),
		URL: deref(f.URL), Headers: f.Headers,
	}
	if f.Enabled != nil {
		s.Enabled = *f.Enabled
	}
	if f.ReadOnly != nil {
		s.ReadOnly = *f.ReadOnly
	}
	s.Allow = normaliseTools(f.Allow)
	problem := func(p string) {
		if s.Problem == "" {
			s.Problem = p
		}
	}
	if !serverName.MatchString(name) {
		problem("The name may hold only letters, digits, - and _, up to 32.")
	}
	switch {
	case f.Type != nil:
		t, ok := transportNames[*f.Type]
		if !ok {
			// Kept as written, so a save does not turn it into another type.
			problem("Type " + strconv.Quote(*f.Type) + " is not stdio or http.")
			t = mcp.Transport(*f.Type)
		}
		s.Transport = t
	case s.Command != "":
		s.Transport = mcp.TransportStdio
	case s.URL != "":
		// A legacy SSE server is not supported, so an address alone can
		// only mean Streamable HTTP.
		s.Transport = mcp.TransportHTTP
	default:
		problem("Set a command or a url.")
	}
	switch s.Transport {
	case mcp.TransportStdio:
		if s.Command == "" {
			problem("A stdio server needs a command.")
		}
	case mcp.TransportHTTP:
		if !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://") {
			problem("An http server needs a url starting http:// or https://.")
		}
	}
	if f.TimeoutS != nil {
		if *f.TimeoutS >= 1 && *f.TimeoutS <= MaxMCPTimeoutS {
			s.Timeout = time.Duration(*f.TimeoutS) * time.Second
		} else {
			problem("Timeout " + strconv.Itoa(*f.TimeoutS) + " is outside 1 to " + strconv.Itoa(MaxMCPTimeoutS) + " seconds.")
		}
	}
	return s
}

// FileMCPFrom renders servers back into the file shape. Fields at their
// default are left out, so a written file stays close to a pasted one.
func FileMCPFrom(servers []MCPServer) FileMCP {
	out := FileMCP{Servers: map[string]FileMCPServer{}}
	for _, s := range servers {
		f := FileMCPServer{Args: s.Args, Env: s.Env, Headers: s.Headers, Allow: s.Allow}
		if s.Transport != "" {
			t := string(s.Transport)
			f.Type = &t
		}
		if s.Command != "" {
			f.Command = &s.Command
		}
		if s.Cwd != "" {
			f.Cwd = &s.Cwd
		}
		if s.URL != "" {
			f.URL = &s.URL
		}
		if !s.Enabled {
			f.Enabled = new(bool)
		}
		if s.Timeout > 0 {
			n := int(s.Timeout / time.Second)
			f.TimeoutS = &n
		}
		if s.ReadOnly {
			t := true
			f.ReadOnly = &t
		}
		out.Servers[s.Name] = f
	}
	return out
}

func normaliseTools(in []string) []string {
	var out []string
	for _, t := range in {
		if t = strings.TrimSpace(t); t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// readMCP reads mcp.json. A missing file is no servers, which is MCP off.
func readMCP(path string) (FileMCP, error) {
	var f FileMCP
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &f); err != nil {
			return f, fmt.Errorf("Read %s: %w: fix the JSON or delete the file to turn MCP off", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return f, fmt.Errorf("Read %s: %w", path, err)
	}
	return f, nil
}

// MCP returns the configured servers, sorted by name.
func (s *Store) MCP() []MCPServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneServers(s.mcp)
}

// MCPServer finds one server by name.
func (s *Store) MCPServer(name string) (MCPServer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, srv := range s.mcp {
		if srv.Name == name {
			return srv.Clone(), true
		}
	}
	return MCPServer{}, false
}

// SetMCP replaces every server and writes mcp.json. Names must be unique.
// It answers the servers as resolved, problems included.
func (s *Store) SetMCP(servers []MCPServer) ([]MCPServer, error) {
	seen := map[string]bool{}
	for _, srv := range servers {
		if seen[srv.Name] {
			return nil, fmt.Errorf("Two servers are named %s: give each its own name", srv.Name)
		}
		seen[srv.Name] = true
	}
	resolved, notes := ResolveMCP(FileMCPFrom(servers))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mcp, s.mcpNotes = resolved, notes
	if err := s.persistMCP(); err != nil {
		return cloneServers(s.mcp), err
	}
	return cloneServers(s.mcp), nil
}

// MCPAllows reports whether the tool sent under a wire name is on its
// server's allow list. The name is matched by rebuilding it, since the wire
// form of a tool name cannot be read back.
func (s *Store) MCPAllows(wire string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, srv := range s.mcp {
		if !strings.HasPrefix(wire, mcp.WirePrefix+srv.Name+"__") {
			continue
		}
		for _, t := range srv.Allow {
			if w, ok := mcp.WireName(srv.Name, t); ok && w == wire {
				return true
			}
		}
	}
	return false
}

// AllowMCP adds a tool to a server's allow list, which is what "allow always"
// on an MCP card does.
func (s *Store) AllowMCP(server, tool string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, srv := range s.mcp {
		if srv.Name != server {
			continue
		}
		if srv.Allows(tool) {
			return nil
		}
		s.mcp[i].Allow = normaliseTools(append(slices.Clone(srv.Allow), tool))
		return s.persistMCP()
	}
	return fmt.Errorf("No MCP server is named %s: it may have been removed", server)
}

// persistMCP writes mcp.json. The caller holds the lock. No servers removes
// nothing and writes an empty object, so the file the user made stays put.
func (s *Store) persistMCP() error {
	data, err := json.MarshalIndent(FileMCPFrom(s.mcp), "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(s.paths.MCP, append(data, '\n'), 0o600)
}

func cloneServers(in []MCPServer) []MCPServer {
	if in == nil {
		return nil
	}
	out := make([]MCPServer, len(in))
	for i, s := range in {
		out[i] = s.Clone()
	}
	return out
}
