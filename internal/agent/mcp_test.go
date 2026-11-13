package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/mcp"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// TestMCPHelperProcess is a one-tool MCP server run as a child process. It
// does nothing unless the test binary was started as one.
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("AGENT_MCP_FAKE") == "" {
		return
	}
	defer os.Exit(0)
	out := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue
		}
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "Echoes its arguments.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + string(m.Params.Arguments)}}}
		default:
			result = map[string]any{}
		}
		out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}
}

func mcpFake(name string) config.MCPServer {
	return config.MCPServer{
		Name: name, Enabled: true, Transport: mcp.TransportStdio,
		Command: os.Args[0], Args: []string{"-test.run=^TestMCPHelperProcess$"},
		Env: map[string]string{"AGENT_MCP_FAKE": "1"},
	}
}

func withMCP(t *testing.T, h *harness, servers ...config.MCPServer) *mcp.Manager {
	t.Helper()
	if _, err := h.cfg.SetMCP(servers); err != nil {
		t.Fatal(err)
	}
	m := mcp.NewManager(filepath.Join(h.paths.Home, "mcp"), h.paths.Log, "test")
	m.Configure(h.cfg.MCP())
	h.engine.SetMCP(m)
	t.Cleanup(m.Close)
	return m
}

func TestMCPToolCall(t *testing.T) {
	h := newHarness(t, "stream_mcp.txt", "stream_plain.txt")
	withMCP(t, h, mcpFake("fake"))
	s := h.session(t)
	h.engine.Send(s.ID, "echo hi", nil)

	id := waitForCard(t, h, s.ID)
	req, ok := h.broker.Lookup(id)
	if !ok || req.Tool != "mcp__fake__echo" || req.Server != "fake" || req.MCPTool != "echo" {
		t.Fatalf("card = %+v", req)
	}
	if err := h.broker.Resolve(id, perms.Decision{Kind: perms.DecideAlways}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	if srv, _ := h.cfg.MCPServer("fake"); !srv.Allows("echo") {
		t.Fatalf("allow always did not reach mcp.json: %+v", srv.Allow)
	}
	doc, _, _, _ := s.Snapshot()
	var found bool
	for _, e := range doc.Entries {
		if e.Kind == "tool" && e.ToolName == "mcp__fake__echo" {
			found = true
			if e.Content != `echo: {"text":"hi"}` || e.Meta["server"] != "fake" || e.Meta["tool"] != "echo" {
				t.Fatalf("tool entry = %q %v", e.Content, e.Meta)
			}
		}
	}
	if !found {
		t.Fatal("no tool entry for the MCP call")
	}

	h.replay.mu.Lock()
	first := h.replay.Requests[0]
	h.replay.mu.Unlock()
	toolList, _ := first["tools"].([]any)
	last, _ := toolList[len(toolList)-1].(map[string]any)
	fn, _ := last["function"].(map[string]any)
	if fn["name"] != "mcp__fake__echo" || fn["description"] != "Echoes its arguments." {
		t.Fatalf("last tool sent = %v", fn)
	}
	msgs, _ := first["messages"].([]any)
	sys, _ := msgs[0].(map[string]any)
	if content, _ := sys["content"].(string); !strings.Contains(content, "MCP servers: fake.") {
		t.Fatalf("system prompt does not name the server:\n%s", content)
	}
}

func TestMCPNothingConfiguredChangesNothing(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	cfg := h.cfg.Config()
	before := h.engine.briefFor(context.Background(), s, cfg)
	withMCP(t, h)
	after := h.engine.briefFor(context.Background(), s, cfg)
	a, _ := json.Marshal(tools.Schemas(before.offer))
	b, _ := json.Marshal(tools.Schemas(after.offer))
	if string(a) != string(b) || after.offer.MCP != nil {
		t.Fatal("an empty server list changed the tools sent")
	}
	if _, err := os.Stat(filepath.Join(h.paths.Home, "mcp")); !os.IsNotExist(err) {
		t.Fatal("an empty server list wrote a cache")
	}
}

func TestMCPPlanModeRunsReadOnlyServersOnly(t *testing.T) {
	ro := tools.Offer{Mode: config.WorkPlan, MCP: []mcp.Offered{
		{Wire: "mcp__a__x", Server: "a", Tool: "x", ReadOnly: true},
		{Wire: "mcp__b__y", Server: "b", Tool: "y"},
	}}
	if !ro.InMode("mcp__a__x") || ro.InMode("mcp__b__y") || ro.InMode("mcp__c__z") {
		t.Fatal("plan mode should run only read-only servers' tools")
	}
	ro.Mode = config.WorkExecute
	if !ro.InMode("mcp__b__y") {
		t.Fatal("execute mode should run every MCP tool")
	}
	if tools.InSet(config.ToolsetMinimal, "mcp__a__x") || len(tools.Schemas(tools.Offer{Set: config.ToolsetMinimal, MCP: ro.MCP})) != 5 {
		t.Fatal("the minimal toolset should carry no MCP tool")
	}
	if !readsOnly(ro, "mcp__a__x") || readsOnly(ro, "mcp__b__y") {
		t.Fatal("only a read-only server's tools may run together")
	}
	if got := strings.Join(ro.MCPServers(), ","); got != "a,b" {
		t.Fatalf("servers = %s", got)
	}
}
