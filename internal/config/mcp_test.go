package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/mcp"
)

func TestResolveMCP(t *testing.T) {
	var f FileMCP
	raw := `{"mcpServers": {
		"zeta":  {"command": "z", "args": ["--root", "{project}"], "env": {"K": "v"}},
		"figma": {"type": "http", "url": "http://127.0.0.1:3845/mcp", "read_only": true, "allow": ["b", "a", "a", " "]},
		"cline": {"type": "streamableHttp", "url": "https://x/mcp"},
		"bare":  {"url": "https://y/mcp", "timeout_s": 30, "enabled": false},
		"old":   {"type": "sse", "url": "https://z/sse"},
		"empty": {},
		"bad name!": {"command": "x"},
		"slow":  {"command": "s", "timeout_s": 0}
	}}`
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatal(err)
	}
	servers, notes := ResolveMCP(f)
	var names []string
	by := map[string]MCPServer{}
	for _, s := range servers {
		names = append(names, s.Name)
		by[s.Name] = s
	}
	if got := strings.Join(names, ","); got != "bad name!,bare,cline,empty,figma,old,slow,zeta" {
		t.Fatalf("order = %s", got)
	}
	if s := by["figma"]; s.Transport != mcp.TransportHTTP || !s.ReadOnly || strings.Join(s.Allow, ",") != "a,b" || !s.Usable() {
		t.Fatalf("figma = %+v", s)
	}
	if by["cline"].Transport != mcp.TransportHTTP || by["bare"].Transport != mcp.TransportHTTP {
		t.Fatal("aliases and a bare url should be http")
	}
	if s := by["bare"]; s.Usable() || s.Problem != "" || s.Timeout != 30*time.Second {
		t.Fatalf("bare = %+v", s)
	}
	if s := by["zeta"]; s.Transport != mcp.TransportStdio || !s.PerProject() || !s.Usable() {
		t.Fatalf("zeta = %+v", s)
	}
	for _, n := range []string{"old", "empty", "bad name!", "slow"} {
		if by[n].Problem == "" || by[n].Usable() {
			t.Errorf("%s should have a problem: %+v", n, by[n])
		}
	}
	if len(notes) != 4 {
		t.Fatalf("notes = %q", notes)
	}
}

func TestMCPSpec(t *testing.T) {
	s := MCPServer{Transport: mcp.TransportStdio, Command: "srv", Args: []string{"{project}/x"}, Env: map[string]string{"B": "2", "A": "{project}"}, Cwd: "{project}"}
	spec := s.Spec("/p")
	if spec.Stdio == nil || spec.Stdio.Args[0] != "/p/x" || spec.Stdio.Dir != "/p" || strings.Join(spec.Stdio.Env, ",") != "A=/p,B=2" {
		t.Fatalf("spec = %+v", spec.Stdio)
	}
}

func TestStoreMCP(t *testing.T) {
	p := DiscoverPathsIn(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.MCP()) != 0 {
		t.Fatal("servers on a first run")
	}
	if _, err := os.Stat(p.MCP); !os.IsNotExist(err) {
		t.Fatal("a first run created mcp.json")
	}

	if _, err := s.SetMCP([]MCPServer{{Name: "a", Command: "x"}, {Name: "a", Command: "y"}}); err == nil {
		t.Fatal("two servers with one name were saved")
	}
	got, err := s.SetMCP([]MCPServer{{Name: "gh", Enabled: true, Transport: mcp.TransportStdio, Command: "gh-mcp", Env: map[string]string{"T": "1"}}})
	if err != nil || len(got) != 1 || !got[0].Usable() {
		t.Fatalf("set = %+v, %v", got, err)
	}
	if err := s.AllowMCP("gh", "get_issue"); err != nil {
		t.Fatal(err)
	}
	if err := s.AllowMCP("nope", "x"); err == nil {
		t.Fatal("allowed a tool on a missing server")
	}
	info, err := os.Stat(p.MCP)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mcp.json mode = %v, %v", info, err)
	}

	again, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	srv, ok := again.MCPServer("gh")
	if !ok || !srv.Allows("get_issue") || srv.Env["T"] != "1" || srv.Transport != mcp.TransportStdio {
		t.Fatalf("reread = %+v", srv)
	}
	raw, _ := os.ReadFile(filepath.Join(p.Home, "mcp.json"))
	if strings.Contains(string(raw), "enabled") || strings.Contains(string(raw), "read_only") {
		t.Fatalf("defaults were written: %s", raw)
	}
}

func TestStoreMCPBadJSON(t *testing.T) {
	p := DiscoverPathsIn(t.TempDir())
	_ = p.EnsureDirs()
	_ = os.WriteFile(p.MCP, []byte("{"), 0o600)
	if _, err := Open(p); err == nil || !strings.Contains(err.Error(), "mcp.json") {
		t.Fatalf("err = %v", err)
	}
}
