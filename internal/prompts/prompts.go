// Package prompts holds every string sent to a model as markdown, embedded by
// name so a missing file fails the build rather than a turn. JSON schemas stay
// in Go, since they are structure rather than prose.
package prompts

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed system.md tool_read_file.md tool_find.md tool_write_file.md tool_replace_in_file.md tool_run_shell.md tool_write_todos.md compact.md compaction.md title.md attachment.md image.md
var files embed.FS

var (
	systemTmpl     = template.Must(template.New("system").Parse(mustRead("system.md")))
	compactionTmpl = template.Must(template.New("compaction").Parse(mustRead("compaction.md")))
	attachmentTmpl = template.Must(template.New("attachment").Parse(mustRead("attachment.md")))
	imageTmpl      = template.Must(template.New("image").Parse(mustRead("image.md")))
	titleTmpl      = template.Must(template.New("title").Parse(mustRead("title.md")))
)

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

// Compaction is the message the summary is sent back as, so the entry keeps
// the summary alone and the framing lives here with the other prompts.
func Compaction(summary string) string {
	var b strings.Builder
	if err := compactionTmpl.Execute(&b, struct{ Summary string }{summary}); err != nil {
		panic("prompts: render compaction.md: " + err.Error())
	}
	return b.String()
}

// Attachment is one file the user attached, as the model is shown it.
func Attachment(path, content string) string {
	var b strings.Builder
	if err := attachmentTmpl.Execute(&b, struct{ Path, Content string }{path, content}); err != nil {
		panic("prompts: render attachment.md: " + err.Error())
	}
	return b.String()
}

// Image names an image travelling with the message, since the picture itself
// carries no file name.
func Image(path string) string {
	var b strings.Builder
	if err := imageTmpl.Execute(&b, struct{ Path string }{path}); err != nil {
		panic("prompts: render image.md: " + err.Error())
	}
	return b.String()
}

func ToolReadFile() string      { return mustRead("tool_read_file.md") }
func ToolFind() string          { return mustRead("tool_find.md") }
func ToolWriteFile() string     { return mustRead("tool_write_file.md") }
func ToolReplaceInFile() string { return mustRead("tool_replace_in_file.md") }
func ToolRunShell() string      { return mustRead("tool_run_shell.md") }
func ToolWriteTodos() string    { return mustRead("tool_write_todos.md") }
func Compact() string           { return mustRead("compact.md") }

// Title asks for a name for the task the opening message describes. It carries
// that message rather than following the conversation, so the request stands on
// its own and can be made before the conversation exists.
func Title(text string) string {
	var b strings.Builder
	if err := titleTmpl.Execute(&b, struct{ Text string }{text}); err != nil {
		panic("prompts: render title.md: " + err.Error())
	}
	return b.String()
}
