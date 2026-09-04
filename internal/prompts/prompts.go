// Package prompts holds every string sent to a model as markdown, embedded by
// name so a missing file fails the build rather than a turn. JSON schemas stay
// in Go, since they are structure rather than prose.
package prompts

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed system.md tool_read_file.md tool_write_file.md tool_run_shell.md compact.md
var files embed.FS

var systemTmpl = template.Must(template.New("system").Parse(mustRead("system.md")))

func mustRead(name string) string {
	b, err := files.ReadFile(name)
	if err != nil {
		panic("prompts: " + name + " is not embedded: add it to the go:embed list")
	}
	return strings.TrimRight(string(b), " \t\r\n")
}

// SystemVars are the values system.md takes.
type SystemVars struct {
	Project string
	OS      string
}

func System(v SystemVars) string {
	var b strings.Builder
	if err := systemTmpl.Execute(&b, v); err != nil {
		panic("prompts: render system.md: " + err.Error())
	}
	return b.String()
}

func ToolReadFile() string  { return mustRead("tool_read_file.md") }
func ToolWriteFile() string { return mustRead("tool_write_file.md") }
func ToolRunShell() string  { return mustRead("tool_run_shell.md") }
func Compact() string       { return mustRead("compact.md") }
