package tools

import (
	"encoding/json"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/prompts"
)

// schemas is built once, so a turn marshals the definitions rather than
// re-encoding them.
var schemas = buildSchemas()

// Offer says which tools belong in this turn's request: the mode's own list,
// less the ones this session is not offered.
type Offer struct {
	Mode    config.WorkMode
	Ask     bool
	Agents  bool
	Message bool
	Search  bool
}

// modeTools is the fixed list each mode may send. Plan holds no tool that
// writes a file, and submit_plan is sent in plan mode alone.
var modeTools = map[config.WorkMode][]string{
	config.WorkPlan: {"read_file", "find", "web_search", "ask_question",
		"run_agent", "send_message", "submit_plan", "run_shell"},
	config.WorkExecute: {"read_file", "find", "web_search", "write_file", "replace_in_file",
		"write_todos", "ask_question", "run_agent", "send_message", "run_shell"},
}

// Schemas returns the tool definitions in the order the model sees them,
// without the ones this turn does not offer.
func Schemas(o Offer) []llm.Tool {
	out := make([]llm.Tool, 0, len(schemas))
	for _, t := range schemas {
		if o.Sends(t.Function.Name) {
			out = append(out, t)
		}
	}
	return out
}

// Names lists the tools Schemas returns, in the same order.
func Names(o Offer) []string {
	var out []string
	for _, t := range Schemas(o) {
		out = append(out, t.Function.Name)
	}
	return out
}

// Known reports whether a tool of that name exists in any mode.
func Known(name string) bool {
	for _, t := range schemas {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// InMode reports whether the offer's mode sends a tool at all, whatever the
// session is offered. A call to a tool outside it is not run.
func (o Offer) InMode(name string) bool {
	for _, n := range modeTools[o.Mode] {
		if n == name {
			return true
		}
	}
	return false
}

// Sends reports whether a tool is in this turn's request.
func (o Offer) Sends(name string) bool {
	if !o.InMode(name) {
		return false
	}
	switch name {
	case "ask_question", "submit_plan":
		return o.Ask
	case "run_agent":
		return o.Agents
	case "send_message":
		return o.Message
	case "web_search":
		return o.Search
	}
	return true
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
			Name:        "web_search",
			Description: prompts.ToolWebSearch(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":   map[string]any{"type": "string", "description": "What to search for, a sentence or a set of terms."},
					"results": map[string]any{"type": "integer", "description": "How many results to return. Fewer carry more of each page.", "minimum": 1},
					"full":    map[string]any{"type": "boolean", "description": "The page from the top instead of the parts that answer the query. Default false."},
					"since":   map[string]any{"type": "string", "description": "Only pages published on or after this date, YYYY-MM-DD."},
				},
				"required":             []string{"query"},
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
			Name:        "submit_plan",
			Description: prompts.ToolSubmitPlan(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string", "description": "The change, in a few words."},
					"goal":  map[string]any{"type": "string", "description": "What the change is for and the approach taken."},
					"files": map[string]any{
						"type":        "array",
						"description": "Each file to change, with the functions or sections in it and what changes there.",
						"items":       map[string]any{"type": "string"},
					},
					"steps": map[string]any{
						"type":        "array",
						"description": "The work in order, one line each, naming its files.",
						"items":       map[string]any{"type": "string"},
						"minItems":    1,
					},
					"check": map[string]any{"type": "string", "description": "The tests to add or change, the commands to run, and what to look at by hand."},
					"open": map[string]any{
						"type":        "array",
						"description": "What is still undecided. Empty when nothing is.",
						"items":       map[string]any{"type": "string"},
					},
				},
				"required":             []string{"title", "steps"},
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
