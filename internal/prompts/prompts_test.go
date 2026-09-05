package prompts

import (
	"strings"
	"testing"
)

func TestEveryFileIsEmbeddedAndNonEmpty(t *testing.T) {
	for name, get := range map[string]func() string{
		"tool_read_file.md":       ToolReadFile,
		"tool_find.md":            ToolFind,
		"tool_write_file.md":      ToolWriteFile,
		"tool_replace_in_file.md": ToolReplaceInFile,
		"tool_run_shell.md":       ToolRunShell,
		"tool_write_todos.md":     ToolWriteTodos,
		"compact.md":              Compact,
		"title.md":                Title,
	} {
		if strings.TrimSpace(get()) == "" {
			t.Errorf("%s is empty", name)
		}
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
	if !strings.Contains(ToolReplaceInFile(), "old_string") || !strings.Contains(ToolReplaceInFile(), "new_string") {
		t.Errorf("replace_in_file description omits its arguments: %q", ToolReplaceInFile())
	}
}
