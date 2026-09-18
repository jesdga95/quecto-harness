// Package prompts holds every string sent to a model as markdown, embedded by
// name so a missing file fails the build rather than a turn. JSON schemas stay
// in Go, since they are structure rather than prose.
package prompts

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed system.md tool_read_file.md tool_find.md tool_web_search.md tool_write_file.md tool_replace_in_file.md tool_run_shell.md tool_write_todos.md tool_ask_question.md tool_run_agent.md tool_send_message.md message.md compact.md compaction.md title.md attachment.md image.md
var files embed.FS

var (
	systemTmpl     = template.Must(template.New("system").Parse(mustRead("system.md")))
	compactionTmpl = template.Must(template.New("compaction").Parse(mustRead("compaction.md")))
	attachmentTmpl = template.Must(template.New("attachment").Parse(mustRead("attachment.md")))
	imageTmpl      = template.Must(template.New("image").Parse(mustRead("image.md")))
	titleTmpl      = template.Must(template.New("title").Parse(mustRead("title.md")))
	messageTmpl    = template.Must(template.New("message").Parse(mustRead("message.md")))
)

func mustRead(name string) string {
	b, err := files.ReadFile(name)
	if err != nil {
		panic("prompts: " + name + " is not embedded: add it to the go:embed list")
	}
	return strings.TrimRight(string(b), " \t\r\n")
}

// AgentBrief is one agent as the prompt names it to another: what it is called
// and what it is for. The model behind it is not named, since which model an
// agent runs is not what the one delegating chooses on.
type AgentBrief struct {
	Name string
	// Role is the one line saying what the agent is for.
	Role string
}

// SystemVars are the values system.md takes. Instructions is the agent's own
// text, which follows the built-in prompt in the same message. Every string
// here is inserted as a value and never parsed, so a template action inside
// one is text.
type SystemVars struct {
	Project string
	OS      string
	// Date is the calendar day the session runs on, YYYY-MM-DD in the host's
	// zone. It is the one value the clock moves, and at the day's granularity
	// it moves at most once a day: a running conversation keeps the same first
	// tokens for a whole day, so the prefill cache of it survives. Anything
	// finer would put a new token at the front of every request and re-prefill
	// the whole conversation each turn, so the time of day is left to
	// run_shell. Empty leaves the line out, which is the prompt the harness
	// sent before the date was possible.
	Date string
	// CanAsk, CanMessage and CanSearch say whether ask_question, send_message
	// and web_search are in this turn's request. The prompt names the tools
	// that were sent, or the model reaches for one it does not have.
	CanAsk     bool
	CanMessage bool
	CanSearch  bool
	// Agents are the ones this session may hand work to. Launcher is the handle
	// of the session that gave this one its task, empty when a person did.
	Agents       []AgentBrief
	Launcher     string
	Instructions string
}

func System(v SystemVars) string {
	var b strings.Builder
	if err := systemTmpl.Execute(&b, v); err != nil {
		panic("prompts: render system.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
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

// Message is one agent's message to another, as the model is shown it. A
// message from a person carries no such line, so this is what says which of
// the two a turn is answering.
func Message(from, session, text string) string {
	var b strings.Builder
	v := struct{ From, Session, Text string }{from, session, text}
	if err := messageTmpl.Execute(&b, v); err != nil {
		panic("prompts: render message.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
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
func ToolWebSearch() string     { return mustRead("tool_web_search.md") }
func ToolWriteFile() string     { return mustRead("tool_write_file.md") }
func ToolReplaceInFile() string { return mustRead("tool_replace_in_file.md") }
func ToolRunShell() string      { return mustRead("tool_run_shell.md") }
func ToolWriteTodos() string    { return mustRead("tool_write_todos.md") }
func ToolAskQuestion() string   { return mustRead("tool_ask_question.md") }
func ToolRunAgent() string      { return mustRead("tool_run_agent.md") }
func ToolSendMessage() string   { return mustRead("tool_send_message.md") }
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
