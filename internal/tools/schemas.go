package tools

import (
	"encoding/json"

	"quecto-harness/internal/llama"
	"quecto-harness/internal/prompts"
)

// schemas is built once, so a turn marshals the definitions rather than
// re-encoding them.
var schemas = buildSchemas()

// Schemas returns the three tool definitions in the order the model sees them.
func Schemas() []llama.Tool {
	out := make([]llama.Tool, len(schemas))
	copy(out, schemas)
	return out
}

func buildSchemas() []llama.Tool {
	return []llama.Tool{
		{Type: "function", Function: llama.FunctionSpec{
			Name:        "read_file",
			Description: prompts.ToolReadFile(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "Path relative to the project root."},
					"offset": map[string]any{"type": "integer", "description": "First line to return, 1-based.", "minimum": 1},
					"limit":  map[string]any{"type": "integer", "description": "Number of lines to return. Default 400.", "minimum": 1},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llama.FunctionSpec{
			Name:        "write_file",
			Description: prompts.ToolWriteFile(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string", "description": "Path relative to the project root."},
					"content": map[string]any{"type": "string", "description": "The full file contents."},
				},
				"required":             []string{"path", "content"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llama.FunctionSpec{
			Name:        "run_shell",
			Description: prompts.ToolRunShell(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":   map[string]any{"type": "string", "description": "The command line to run."},
					"timeout_s": map[string]any{"type": "integer", "description": "Seconds before the command is killed. Default 120.", "minimum": 1},
				},
				"required":             []string{"command"},
				"additionalProperties": false,
			}),
		}},
	}
}

func mustSchema(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("tools: build schema: " + err.Error())
	}
	return b
}
