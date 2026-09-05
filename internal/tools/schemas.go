package tools

import (
	"encoding/json"

	"quecto-harness/internal/llama"
	"quecto-harness/internal/prompts"
)

// schemas is built once, so a turn marshals the definitions rather than
// re-encoding them.
var schemas = buildSchemas()

// Schemas returns the tool definitions in the order the model sees them.
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
			Name:        "replace_in_file",
			Description: prompts.ToolReplaceInFile(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":       map[string]any{"type": "string", "description": "Path relative to the project root."},
					"old_string": map[string]any{"type": "string", "description": "The exact text to replace. It must appear once."},
					"new_string": map[string]any{"type": "string", "description": "What to put in its place. Empty deletes the text."},
				},
				"required":             []string{"path", "old_string", "new_string"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llama.FunctionSpec{
			Name:        "write_todos",
			Description: prompts.ToolWriteTodos(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"todos": map[string]any{
						"type":        "array",
						"description": "The whole plan, in order. It replaces the plan that was there.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"text":   map[string]any{"type": "string", "description": "What the step is, in a few words."},
								"status": map[string]any{"type": "string", "enum": []string{"todo", "doing", "done"}, "description": "Where the step stands."},
							},
							"required":             []string{"text", "status"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"todos"},
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
