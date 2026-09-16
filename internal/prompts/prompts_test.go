package prompts

import (
	"strings"
	"testing"
)

func TestEveryFileIsEmbeddedAndNonEmpty(t *testing.T) {
	for name, get := range map[string]func() string{
		"tool_read_file.md":       ToolReadFile,
		"tool_find.md":            ToolFind,
		"tool_web_search.md":      ToolWebSearch,
		"tool_write_file.md":      ToolWriteFile,
		"tool_replace_in_file.md": ToolReplaceInFile,
		"tool_run_shell.md":       ToolRunShell,
		"tool_write_todos.md":     ToolWriteTodos,
		"tool_ask_question.md":    ToolAskQuestion,
		"tool_run_agent.md":       ToolRunAgent,
		"tool_send_message.md":    ToolSendMessage,
		"compact.md":              Compact,
		"title.md":                func() string { return Title("name this") },
	} {
		if strings.TrimSpace(get()) == "" {
			t.Errorf("%s is empty", name)
		}
	}
}

func TestTitleCarriesTheMessage(t *testing.T) {
	got := Title("count the digits of pi")
	if !strings.Contains(got, "count the digits of pi") {
		t.Errorf("title prompt did not substitute: %q", got)
	}
	if strings.Contains(got, "{{") {
		t.Errorf("title prompt left a template action: %q", got)
	}
}

func TestSystemSubstitutesBothFields(t *testing.T) {
	got := System(SystemVars{Project: "/Users/user/src/x", OS: "darwin"})
	if !strings.Contains(got, "/Users/user/src/x") || !strings.Contains(got, "darwin") {
		t.Errorf("system prompt did not substitute: %q", got)
	}
	if strings.Contains(got, "{{") {
		t.Errorf("system prompt left a template action: %q", got)
	}
	for _, tool := range []string{"read_file", "write_file", "run_shell"} {
		if !strings.Contains(got, tool) {
			t.Errorf("system prompt does not name %s", tool)
		}
	}
}

func TestToolDescriptionsNameTheirArguments(t *testing.T) {
	if !strings.Contains(ToolReadFile(), "offset") || !strings.Contains(ToolReadFile(), "limit") {
		t.Errorf("read_file description omits its arguments: %q", ToolReadFile())
	}
	if !strings.Contains(ToolRunShell(), "exit code") {
		t.Errorf("run_shell description omits the exit code: %q", ToolRunShell())
	}
	if !strings.Contains(ToolFind(), "text") || !strings.Contains(ToolFind(), "name") || !strings.Contains(ToolFind(), "path") {
		t.Errorf("find description omits its arguments: %q", ToolFind())
	}
	if !strings.Contains(ToolWebSearch(), "query") || !strings.Contains(ToolWebSearch(), "results") ||
		!strings.Contains(ToolWebSearch(), "full") || !strings.Contains(ToolWebSearch(), "since") {
		t.Errorf("web_search description omits its arguments: %q", ToolWebSearch())
	}
	if !strings.Contains(ToolReplaceInFile(), "old_string") || !strings.Contains(ToolReplaceInFile(), "new_string") {
		t.Errorf("replace_in_file description omits its arguments: %q", ToolReplaceInFile())
	}
	if !strings.Contains(ToolAskQuestion(), "question") || !strings.Contains(ToolAskQuestion(), "options") ||
		!strings.Contains(ToolAskQuestion(), "multi_select") {
		t.Errorf("ask_question description omits its arguments: %q", ToolAskQuestion())
	}
}

func TestSystemAppendsInstructions(t *testing.T) {
	base := System(SystemVars{Project: "/src/x", OS: "darwin"})
	got := System(SystemVars{Project: "/src/x", OS: "darwin", Instructions: "Review Go, do not write it."})

	if !strings.HasPrefix(got, base) {
		t.Fatalf("instructions did not follow the built-in prompt: %q", got)
	}
	if !strings.HasSuffix(got, "Review Go, do not write it.") {
		t.Errorf("instructions are not last: %q", got)
	}
	for _, want := range []string{"/src/x", "darwin", "read_file", "run_shell"} {
		if !strings.Contains(got, want) {
			t.Errorf("appending dropped %q: %q", want, got)
		}
	}
}

func TestSystemWithoutInstructionsIsUnchanged(t *testing.T) {
	base := System(SystemVars{Project: "/src/x", OS: "darwin"})
	for _, in := range []string{"", "   ", "\n\t\n"} {
		if got := System(SystemVars{Project: "/src/x", OS: "darwin", Instructions: in}); got != base {
			t.Errorf("instructions %q changed the prompt:\n%q\nwant\n%q", in, got, base)
		}
	}
}

func TestSystemDoesNotParseInstructions(t *testing.T) {
	got := System(SystemVars{Project: "/src/x", OS: "darwin", Instructions: "Work in {{.Project}} and {{if"})
	if !strings.Contains(got, "{{.Project}} and {{if") {
		t.Errorf("instructions were not passed through verbatim: %q", got)
	}
}

// The prompt names the tools that were sent and no others, or the model
// reaches for one it does not have.
func TestSystemNamesOnlyTheToolsInTheRequest(t *testing.T) {
	full := System(SystemVars{Project: "/src/x", OS: "darwin", CanAsk: true, CanMessage: true, CanSearch: true,
		Agents: []AgentBrief{{Name: "Reviewer"}}})
	for _, name := range []string{"ask_question", "run_agent", "send_message", "web_search"} {
		if !strings.Contains(full, name) {
			t.Errorf("prompt does not name %s: %q", name, full)
		}
	}

	child := System(SystemVars{Project: "/src/x", OS: "darwin", CanMessage: true,
		Launcher: "sess_aaaaaaaaaaaaaaaa"})
	for _, name := range []string{"ask_question", "run_agent", "web_search"} {
		if strings.Contains(child, name) {
			t.Errorf("a subagent's prompt names %s, which it was not sent: %q", name, child)
		}
	}
	if !strings.Contains(child, "send_message") || !strings.Contains(child, "sess_aaaaaaaaaaaaaaaa") {
		t.Errorf("a subagent's prompt does not give its launcher's handle or the tool that reaches it: %q", child)
	}
}

func TestSystemListsTheAgentsWithTheirRoles(t *testing.T) {
	got := System(SystemVars{Project: "/src/x", OS: "darwin", CanAsk: true, Agents: []AgentBrief{
		{Name: "Reviewer", Role: "Reviews Go and does not write it."},
		{Name: "Searcher"},
	}})
	for _, want := range []string{"- Reviewer: Reviews Go and does not write it.", "- Searcher"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt omits %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Searcher:") {
		t.Errorf("an agent with no role got an empty one: %q", got)
	}
	if strings.Contains(got, "{{") {
		t.Errorf("prompt left a template action: %q", got)
	}
}

// An agent is chosen on what it is for, so what it runs is not in the prompt.
// The list carries a name and a description and nothing else.
func TestSystemDoesNotNameTheModelBehindAnAgent(t *testing.T) {
	got := System(SystemVars{Project: "/src/x", OS: "darwin", Agents: []AgentBrief{
		{Name: "Reviewer", Role: "Reviews Go and does not write it."},
	}})
	if !strings.Contains(got, "- Reviewer: Reviews Go and does not write it.\n") {
		t.Errorf("the line is not the name and the description alone: %q", got)
	}
}

// A name or a role is a value like the instructions are, so a template action
// inside one is text.
func TestSystemDoesNotParseAgentNames(t *testing.T) {
	got := System(SystemVars{Project: "/src/x", OS: "darwin",
		Agents:   []AgentBrief{{Name: "{{.Project}}", Role: "{{if"}},
		Launcher: "{{range}}"})
	for _, want := range []string{"{{.Project}}", "{{if", "{{range}}"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was not passed through verbatim: %q", want, got)
		}
	}
}

// The renderer for figures and pages is in the UI that shows the person's
// conversation, so only a session with no launcher is told about it.
func TestSystemNamesDrawingsOnlyForTheMainAgent(t *testing.T) {
	main := System(SystemVars{Project: "/src/x", OS: "darwin"})
	if !strings.Contains(main, "```svg") || !strings.Contains(main, "```html") {
		t.Errorf("prompt does not name the figure and page fences: %q", main)
	}
	child := System(SystemVars{Project: "/src/x", OS: "darwin", Launcher: "sess_aaaaaaaaaaaaaaaa"})
	if strings.Contains(child, "```svg") || strings.Contains(child, "```html") {
		t.Errorf("a subagent's prompt names the fences, but its output is not drawn: %q", child)
	}
}

// A session that hands work to nobody and was launched by nobody reads the
// prompt the harness sent before either was possible.
func TestSystemWithoutAgentsIsUnchanged(t *testing.T) {
	base := System(SystemVars{Project: "/src/x", OS: "darwin", CanAsk: true})
	if strings.Contains(base, "run_agent") || strings.Contains(base, "send_message") {
		t.Errorf("prompt names a tool that was not sent: %q", base)
	}
	got := System(SystemVars{Project: "/src/x", OS: "darwin", CanAsk: true, Agents: nil, Launcher: ""})
	if got != base {
		t.Errorf("empty agents changed the prompt:\n%q\nwant\n%q", got, base)
	}
}
