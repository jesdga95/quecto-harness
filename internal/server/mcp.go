package server

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/mcp"
)

// mcpServerJSON is one server as the settings dialog draws it: the record as
// configured, then where it stands. Secrets in env and headers round-trip in
// plaintext, as the agent key does.
type mcpServerJSON struct {
	Name      string            `json:"name"`
	Enabled   bool              `json:"enabled"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	Cwd       string            `json:"cwd"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	TimeoutS  int               `json:"timeout_s"`
	ReadOnly  bool              `json:"read_only"`
	Allow     []string          `json:"allow"`
	Problem   string            `json:"problem"`

	State   string   `json:"state"`
	Error   string   `json:"error"`
	Tools   []string `json:"tools"`
	Running int      `json:"running"`
}

func toMCPServerJSON(srv config.MCPServer, st mcp.ServerStatus) mcpServerJSON {
	out := mcpServerJSON{
		Name: srv.Name, Enabled: srv.Enabled, Transport: string(srv.Transport),
		Command: srv.Command, Args: orEmpty(srv.Args), Env: orEmptyMap(srv.Env), Cwd: srv.Cwd,
		URL: srv.URL, Headers: orEmptyMap(srv.Headers), TimeoutS: int(srv.Timeout / time.Second),
		ReadOnly: srv.ReadOnly, Allow: orEmpty(srv.Allow), Problem: srv.Problem,
		State: string(mcp.StateIdle), Tools: []string{},
	}
	if st.Name != "" {
		out.State, out.Tools, out.Running = string(st.State), st.Tools, st.Running
		if st.Error != "" {
			out.Error = "The last start failed: " + st.Error
		}
	}
	return out
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func (s *Server) writeMCP(w http.ResponseWriter, status int) {
	byName := map[string]mcp.ServerStatus{}
	if m := s.engine.MCP(); m != nil {
		for _, st := range m.Status() {
			byName[st.Name] = st
		}
	}
	servers := s.cfg.MCP()
	out := make([]mcpServerJSON, 0, len(servers))
	for _, srv := range servers {
		out = append(out, toMCPServerJSON(srv, byName[srv.Name]))
	}
	writeJSON(w, status, map[string]any{"servers": out})
}

func (s *Server) getMCP(w http.ResponseWriter, r *http.Request) { s.writeMCP(w, http.StatusOK) }

// mcpServerBody is one server as the dialog sends it.
type mcpServerBody struct {
	Name      string            `json:"name"`
	Enabled   *bool             `json:"enabled"`
	Transport string            `json:"transport"`
	Command   string            `json:"command"`
	Args      []string          `json:"args"`
	Env       map[string]string `json:"env"`
	Cwd       string            `json:"cwd"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	TimeoutS  int               `json:"timeout_s"`
	ReadOnly  bool              `json:"read_only"`
	Allow     []string          `json:"allow"`
}

var mcpName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// record checks what the dialog cannot leave wrong and builds the record. A
// record that is complete but cannot run, such as a stdio server with no
// command, is kept and drawn with its problem.
func (b mcpServerBody) record() (config.MCPServer, error) {
	name := strings.TrimSpace(b.Name)
	if !mcpName.MatchString(name) {
		return config.MCPServer{}, errors.New("Server name \"" + b.Name + "\" may hold only letters, digits, - and _, up to 32")
	}
	if b.TimeoutS < 0 || b.TimeoutS > config.MaxMCPTimeoutS {
		return config.MCPServer{}, errors.New("The timeout for " + name + " is outside 1 to 3600 seconds")
	}
	srv := config.MCPServer{
		Name: name, Enabled: b.Enabled == nil || *b.Enabled,
		Transport: mcp.Transport(strings.TrimSpace(b.Transport)),
		Command:   strings.TrimSpace(b.Command), Args: b.Args, Env: cleanMap(b.Env),
		Cwd: strings.TrimSpace(b.Cwd), URL: strings.TrimSpace(b.URL), Headers: cleanMap(b.Headers),
		Timeout: time.Duration(b.TimeoutS) * time.Second, ReadOnly: b.ReadOnly, Allow: b.Allow,
	}
	if len(srv.Args) == 0 {
		srv.Args = nil
	}
	return srv, nil
}

// cleanMap drops rows with no name, which is what an empty row in the
// dialog's list sends.
func cleanMap(m map[string]string) map[string]string {
	var out map[string]string
	for k, v := range m {
		if k = strings.TrimSpace(k); k == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out
}

// putMCP replaces every server, writes mcp.json and hands the list to the
// running servers, which restart where their launch changed.
func (s *Server) putMCP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Servers []mcpServerBody `json:"servers"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	records := make([]config.MCPServer, 0, len(body.Servers))
	for _, b := range body.Servers {
		srv, err := b.record()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		records = append(records, srv)
	}
	saved, err := s.cfg.SetMCP(records)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if m := s.engine.MCP(); m != nil {
		m.Configure(saved)
	}
	s.writeMCP(w, http.StatusOK)
}

// probeMCP answers the Test button: it starts the server as drawn, which may
// not be saved, lists its tools and stops it. It answers 200 even on failure,
// so the pane draws the error inside the form.
func (s *Server) probeMCP(w http.ResponseWriter, r *http.Request) {
	var body mcpServerBody
	if !readJSON(w, r, &body) {
		return
	}
	srv, err := body.record()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resolved, _ := config.ResolveMCP(config.FileMCPFrom([]config.MCPServer{srv}))
	srv = resolved[0]
	if srv.Problem != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": srv.Problem})
		return
	}
	m := s.engine.MCP()
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "MCP is not running in this harness"})
		return
	}
	project := ""
	if srv.PerProject() {
		if projects := s.cfg.Config().Projects; len(projects) > 0 {
			project = projects[0]
		}
	}
	info, tools, notes, err := m.Probe(r.Context(), srv, project)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "The server did not start: " + err.Error()})
		return
	}
	type toolJSON struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	list := make([]toolJSON, 0, len(tools))
	for _, t := range tools {
		list = append(list, toolJSON{Name: t.Name, Description: t.Description})
	}
	if notes == nil {
		notes = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "server": map[string]string{"name": info.Name, "version": info.Version, "protocol": info.Protocol},
		"tools": list, "notes": notes,
	})
}
