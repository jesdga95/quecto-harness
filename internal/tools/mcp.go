package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jesdga95/quecto-harness/internal/mcp"
)

// MCPCaller runs one tool on one MCP server for a project.
type MCPCaller interface {
	Call(ctx context.Context, project, server, tool string, args json.RawMessage, fallback time.Duration) (mcp.Rendered, error)
}

// CallMCP runs a tool an MCP server offered this turn. The output is cut to
// the output limit like every other tool's, and a result the tool marked as
// failed carries the error flag.
func (r *Runner) CallMCP(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	o, ok := r.MCPTools[name]
	if !ok || r.MCP == nil {
		return Result{}, fmt.Errorf("no tool named %s: no MCP server offers it this turn", name)
	}
	project := r.Root
	if r.NoProject {
		project = ""
	}
	res, err := r.MCP.Call(ctx, project, o.Server, o.Tool, args, r.Timeout)
	if err != nil {
		return Result{}, err
	}
	out, cut := Truncate(res.Text, r.OutputLimit)
	meta := map[string]any{"server": o.Server, "tool": o.Tool}
	if res.IsError {
		meta["error"] = true
	}
	result := Result{Output: out, Truncated: cut, Bytes: len(res.Text), Meta: meta}
	for _, img := range res.Images {
		result.Images = append(result.Images, Image{Path: o.Server + " · " + o.Tool, Mime: img.Mime, Bytes: len(img.Data), Data: img.Data})
	}
	return result, nil
}
