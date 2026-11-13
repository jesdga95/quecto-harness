package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/mcp"
)

func decodeServers(t *testing.T, body []byte) []mcpServerJSON {
	t.Helper()
	var out struct {
		Servers []mcpServerJSON `json:"servers"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out.Servers
}

func TestMCPRoutes(t *testing.T) {
	f := newFixture(t)
	m := mcp.NewManager(filepath.Join(f.cfg.Paths().Home, "mcp"), "", "test")
	f.engine.SetMCP(m)
	t.Cleanup(m.Close)

	w := f.do(t, "GET", "/api/mcp", nil)
	if w.Code != 200 || len(decodeServers(t, w.Body.Bytes())) != 0 {
		t.Fatalf("empty list = %d %s", w.Code, w.Body.String())
	}

	put := map[string]any{"servers": []map[string]any{
		{"name": "blender", "transport": "stdio", "command": "uvx", "args": []string{"blender-mcp"},
			"env": map[string]string{"A": "1", " ": "dropped"}, "allow": []string{"get_objects_summary"}},
		{"name": "figma", "transport": "http", "url": "not-a-url"},
	}}
	w = f.do(t, "PUT", "/api/mcp", put)
	if w.Code != 200 {
		t.Fatalf("put = %d %s", w.Code, w.Body.String())
	}
	servers := decodeServers(t, w.Body.Bytes())
	if len(servers) != 2 || servers[0].Name != "blender" || servers[0].State != "idle" || len(servers[0].Env) != 1 {
		t.Fatalf("saved = %+v", servers)
	}
	if !strings.HasPrefix(servers[1].Problem, "An http server needs a url") {
		t.Fatalf("figma problem = %q", servers[1].Problem)
	}
	if srv, ok := m.Server("blender"); !ok || !srv.Allows("get_objects_summary") {
		t.Fatal("the running servers were not told")
	}

	for _, bad := range []map[string]any{
		{"servers": []map[string]any{{"name": "bad name"}}},
		{"servers": []map[string]any{{"name": "a", "command": "x"}, {"name": "a", "command": "y"}}},
		{"servers": []map[string]any{{"name": "a", "command": "x", "timeout_s": 99999}}},
	} {
		if w := f.do(t, "PUT", "/api/mcp", bad); w.Code != 400 {
			t.Errorf("%v = %d %s", bad, w.Code, w.Body.String())
		}
	}

	w = f.do(t, "POST", "/api/mcp/probe", map[string]any{"name": "x", "transport": "stdio"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":false`) || !strings.Contains(w.Body.String(), "needs a command") {
		t.Fatalf("probe without a command = %d %s", w.Code, w.Body.String())
	}
	w = f.do(t, "POST", "/api/mcp/probe", map[string]any{"name": "x", "command": "/no/such/server"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "The server did not start") {
		t.Fatalf("probe of a missing command = %d %s", w.Code, w.Body.String())
	}
}
