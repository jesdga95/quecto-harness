package tools

import (
	"encoding/json"

	"quecto-harness/internal/llm"
	"quecto-harness/internal/prompts"
)

// schemas is built once, so a turn marshals the definitions rather than
// re-encoding them.
var schemas = buildSchemas()

// Offer says which of the tools a session may not have belong in this turn's
// request. The rest are sent to every session.
type Offer struct {
	Ask     bool
	Agents  bool
	Message bool
}

// Schemas returns the tool definitions in the order the model sees them,
// without the ones this turn does not offer.
func Schemas(o Offer) []llm.Tool {
	out := make([]llm.Tool, 0, len(schemas))
	for _, t := range schemas {
		switch t.Function.Name {
		case "ask_question":
			if !o.Ask {
				continue
			}
		case "run_agent":
			if !o.Agents {
				continue
			}
		case "send_message":
			if !o.Message {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

func buildSchemas() []llm.Tool {
	return []llm.Tool{
		{Type: "function", Function: llm.FunctionSpec{
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
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "find",
			Description: prompts.ToolFind(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":  map[string]any{"type": "string", "description": "Text to look for inside files."},
					"name":  map[string]any{"type": "string", "description": "File name pattern, such as *.go. Matched against the name, not the folders above it."},
					"path":  map[string]any{"type": "string", "description": "Folder or file to search, relative to the project root. Default the whole project."},
					"regex": map[string]any{"type": "boolean", "description": "Treat text as a regular expression. Default false."},
				},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
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
		{Type: "function", Function: llm.FunctionSpec{
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
		{Type: "function", Function: llm.FunctionSpec{
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
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "ask_question",
			Description: prompts.ToolAskQuestion(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{"type": "string", "description": "What is being asked, in one sentence."},
					"options": map[string]any{
						"type":        "array",
						"description": "The answers to choose from, two to four of them.",
						"minItems":    2,
						"maxItems":    4,
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"label":       map[string]any{"type": "string", "description": "The answer, in a few words."},
								"description": map[string]any{"type": "string", "description": "What choosing it means. Optional."},
							},
							"required":             []string{"label"},
							"additionalProperties": false,
						},
					},
					"multi_select": map[string]any{"type": "boolean", "description": "Allow more than one option to be chosen. Default false."},
				},
				"required":             []string{"question", "options"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "run_agent",
			Description: prompts.ToolRunAgent(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{"type": "string", "description": "The name of the agent to run the task."},
					"task":  map[string]any{"type": "string", "description": "The whole brief. That agent sees none of this conversation."},
				},
				"required":             []string{"agent", "task"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "send_message",
			Description: prompts.ToolSendMessage(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to":   map[string]any{"type": "string", "description": "The handle run_agent answered with, or parent."},
					"text": map[string]any{"type": "string", "description": "The message."},
				},
				"required":             []string{"to", "text"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
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
