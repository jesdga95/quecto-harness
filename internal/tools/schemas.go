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

// Offer says which tools belong in this turn's request, which of them the
// harness toolset offers at all, and which of those the session's mode lets run.
type Offer struct {
	Set     config.Toolset
	Mode    config.WorkMode
	Ask     bool
	Agents  bool
	Message bool
	Search  bool
}

// modeTools is the fixed list each mode may run. Plan holds no tool that
// writes a file, and write_plan and exit_plan_mode run in plan mode alone.
// Every mode sends the same tools, so a mode switch leaves the head of the
// prompt as it was; a call outside the mode's list is answered, not run.
var modeTools = map[config.WorkMode][]string{
	config.WorkPlan: {"read", "read_image", "grep", "web_search", "ask_user_question",
		"subagent", "send_message", "write_plan", "exit_plan_mode", "bash", "processes"},
	config.WorkExecute: {"read", "read_image", "grep", "web_search", "write", "edit",
		"todo_write", "ask_user_question", "subagent", "send_message", "bash", "processes"},
}

// setTools is the fixed list each harness toolset offers. A set outside it is
// a call that is answered rather than run, whatever the mode.
var setTools = map[config.Toolset][]string{
	config.ToolsetFull: {"read", "read_image", "grep", "web_search", "write", "edit", "todo_write",
		"ask_user_question", "write_plan", "exit_plan_mode", "subagent", "send_message", "bash", "processes"},
	config.ToolsetMinimal: {"read", "write", "edit", "bash", "processes"},
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

// AllCount is the number of tools the harness knows, which is the ceiling a
// turn's list can reach. A list short of it means the rules or the history
// may name a tool the model cannot reach.
func AllCount() int { return len(schemas) }

// Known reports whether a tool of that name exists in any mode.
func Known(name string) bool {
	for _, t := range schemas {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

// InMode reports whether the offer's mode runs a tool, whatever the session is
// offered. A call to a tool outside it is answered and not run.
func (o Offer) InMode(name string) bool {
	for _, n := range modeTools[o.Mode] {
		if n == name {
			return true
		}
	}
	return false
}

// InSet reports whether a toolset offers a tool at all, whatever the mode. A
// set that withholds a tool keeps it out of the request. An empty set stands
// at full, since a caller that leaves it unset gets the harness's default
// rather than a turn that quietly drops every tool.
func InSet(set config.Toolset, name string) bool {
	if set == "" {
		set = config.ToolsetFull
	}
	for _, n := range setTools[set] {
		if n == name {
			return true
		}
	}
	return false
}

func (o Offer) InSet(name string) bool { return InSet(o.Set, name) }

// Sends reports whether a tool is in this turn's request, which depends on
// what the harness offers and what the session is offered, and not on its mode.
func (o Offer) Sends(name string) bool {
	if !o.InSet(name) {
		return false
	}
	switch name {
	case "ask_user_question", "write_plan", "exit_plan_mode":
		return o.Ask
	case "subagent":
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
			Name:        "read",
			Description: prompts.ToolRead(),
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
			Name:        "read_image",
			Description: prompts.ToolReadImage(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Path relative to the project root."},
				},
				"required":             []string{"path"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "grep",
			Description: prompts.ToolGrep(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text": map[string]any{"type": "string", "description": "Text or regular expression to look for inside files."},
					"name": map[string]any{"type": "string", "description": "File name pattern, such as *.go. Matched against the name, not the folders above it."},
					"path": map[string]any{"type": "string", "description": "Folder or file to search, relative to the project root. Default the whole project."},
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
			Name:        "write",
			Description: prompts.ToolWrite(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": ordered{
					{"path", map[string]any{"type": "string", "description": "Path relative to the project root."}},
					{"content", map[string]any{"type": "string", "description": "The full file contents."}},
				},
				"required":             []string{"path", "content"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "edit",
			Description: prompts.ToolEdit(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": ordered{
					{"path", map[string]any{"type": "string", "description": "Path relative to the project root."}},
					{"old_string", map[string]any{"type": "string", "description": "The exact text to replace. It must appear once unless replace_all is set."}},
					{"new_string", map[string]any{"type": "string", "description": "What to put in its place. Empty deletes the text."}},
					{"replace_all", map[string]any{"type": "boolean", "description": "Replace every occurrence rather than exactly one."}},
				},
				"required":             []string{"path", "old_string", "new_string"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "todo_write",
			Description: prompts.ToolTodoWrite(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"todos": map[string]any{
						"type":        "array",
						"description": "The whole plan, in order. It replaces the plan that was there.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"content": map[string]any{"type": "string", "description": "What the step is, in a few words."},
								"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}, "description": "Where the step stands."},
							},
							"required":             []string{"content", "status"},
							"additionalProperties": false,
						},
					},
				},
				"required":             []string{"todos"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "ask_user_question",
			Description: prompts.ToolAskUserQuestion(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"questions": map[string]any{
						"type":        "array",
						"description": "One question, or several independent ones together so they are answered as a set.",
						"minItems":    1,
						"maxItems":    5,
						"items": map[string]any{
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
						},
					},
				},
				"required":             []string{"questions"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "write_plan",
			Description: prompts.ToolWritePlan(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":      map[string]any{"type": "string", "description": "The change, in a few words."},
					"goal":       map[string]any{"type": "string", "description": "What the change is for, the approach taken and the one not taken."},
					"code":       map[string]any{"type": "array", "description": "Each file to change, with the functions or sections in it and what changes there.", "items": map[string]any{"type": "string"}},
					"tests":      map[string]any{"type": "array", "description": "Each unit test to add or change, and what it asserts.", "items": map[string]any{"type": "string"}},
					"validation": map[string]any{"type": "array", "description": "The commands to run and what to check by hand or against a running instance.", "items": map[string]any{"type": "string"}},
					"migration":  map[string]any{"type": "string", "description": "Stored data, config or API shapes that change and how, or None."},
					"risks":      map[string]any{"type": "array", "description": "What could break: callers, behaviour a user sees, performance.", "items": map[string]any{"type": "string"}},
					"steps":      map[string]any{"type": "array", "description": "The work in order, one line each, naming its files.", "items": map[string]any{"type": "string"}},
					"open":       map[string]any{"type": "array", "description": "What is still undecided. Empty when nothing is.", "items": map[string]any{"type": "string"}},
				},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "exit_plan_mode",
			Description: prompts.ToolExitPlanMode(),
			Parameters: mustSchema(map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "subagent",
			Description: prompts.ToolSubagent(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"agent": map[string]any{"type": "string", "description": "The name of the agent to run the task."},
					"title": map[string]any{"type": "string", "description": "The task in a few words, in sentence case. The run is listed under it."},
					"task":  map[string]any{"type": "string", "description": "The whole brief. That agent sees none of this conversation."},
					"wait":  map[string]any{"type": "boolean", "description": "False starts the agent and carries on without its result, which arrives later as a message. Waits when left out."},
				},
				"required":             []string{"agent", "title", "task"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "send_message",
			Description: prompts.ToolSendMessage(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"to":   map[string]any{"type": "string", "description": "The handle subagent answered with, or parent."},
					"text": map[string]any{"type": "string", "description": "The message."},
				},
				"required":             []string{"to", "text"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "bash",
			Description: prompts.ToolBash(),
			Parameters: mustSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":    map[string]any{"type": "string", "description": "The command line to run."},
					"timeout_s":  map[string]any{"type": "integer", "description": "Seconds before the command is killed. Default 120.", "minimum": 1},
					"background": map[string]any{"type": "boolean", "description": "Start the command and return at once, with no deadline. Default false."},
				},
				"required":             []string{"command"},
				"additionalProperties": false,
			}),
		}},
		{Type: "function", Function: llm.FunctionSpec{
			Name:        "processes",
			Description: prompts.ToolProcesses(),
			Parameters: mustSchema(map[string]any{
				"type":                 "object",
				"properties":           map[string]any{},
				"additionalProperties": false,
			}),
		}},
	}
}

// ordered is a JSON object that keeps its keys in the order written, where a
// map would sort them. A model writes arguments in the order the schema lists
// them, and a path listed before the content is what lets the reader place a
// file while it is still being written.
type ordered []struct {
	key   string
	value any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	buf := []byte{'{'}
	for i, f := range o {
		if i > 0 {
			buf = append(buf, ',')
		}
		k, err := json.Marshal(f.key)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(f.value)
		if err != nil {
			return nil, err
		}
		buf = append(append(append(buf, k...), ':'), v...)
	}
	return append(buf, '}'), nil
}

func mustSchema(v map[string]any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("tools: build schema: " + err.Error())
	}
	return b
}
