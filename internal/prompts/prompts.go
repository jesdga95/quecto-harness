// Package prompts holds every string sent to a model as markdown, embedded by
// name so a missing file fails the build rather than a turn. JSON schemas stay
// in Go, since they are structure rather than prose.
package prompts

import (
	"embed"
	"strings"
	"text/template"
)

//go:embed system.md system_plan.md system_execute.md plan.md plan_approved.md tool_write_plan.md tool_exit_plan_mode.md tool_read.md tool_grep.md tool_web_search.md tool_write.md tool_edit.md tool_bash.md tool_todo_write.md tool_ask_user_question.md tool_subagent.md tool_send_message.md message.md compact.md compaction.md title.md attachment.md image.md denial.md budget_spent.md repeat.md not_run.md outcome_unknown.md plan_continue.md subagent_result.md
var files embed.FS

var (
	systemTmpl = template.Must(template.New("system").Parse(mustRead("system.md")))
	bodyTmpls  = map[string]*template.Template{
		"plan":    parseBody("plan", "system_plan.md"),
		"execute": parseBody("execute", "system_execute.md"),
	}
	planTmpl       = template.Must(template.New("plan").Parse(mustRead("plan.md")))
	approvedTmpl   = template.Must(template.New("approved").Parse(mustRead("plan_approved.md")))
	compactionTmpl = template.Must(template.New("compaction").Parse(mustRead("compaction.md")))
	attachmentTmpl = template.Must(template.New("attachment").Parse(mustRead("attachment.md")))
	imageTmpl      = template.Must(template.New("image").Parse(mustRead("image.md")))
	titleTmpl      = template.Must(template.New("title").Parse(mustRead("title.md")))
	messageTmpl    = template.Must(template.New("message").Parse(mustRead("message.md")))
	compactTmpl    = template.Must(template.New("compact").Parse(mustRead("compact.md")))
	continueTmpl   = template.Must(template.New("continue").Parse(mustRead("plan_continue.md")))
	resultTmpl     = template.Must(template.New("result").Parse(mustRead("subagent_result.md")))
	denialTmpl     = template.Must(template.New("denial").Parse(mustRead("denial.md")))
	repeatTmpl     = template.Must(template.New("repeat").Parse(mustRead("repeat.md")))
)

// parseBody parses one mode's body.
func parseBody(name, file string) *template.Template {
	t := template.Must(template.New(name).Parse(mustRead(file)))
	return t
}

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

// SystemVars are the values system.md takes. Rules are the project's, and
// Instructions is the agent's own text, which follow the built-in prompt in
// the same message. Every string here is inserted as a value and never
// parsed, so a template action inside one is text.
type SystemVars struct {
	Project string
	OS      string
	// Date is the calendar day the session runs on, YYYY-MM-DD in the host's
	// zone. It is the one value the clock moves, and at the day's granularity
	// it moves at most once a day: a running conversation keeps the same first
	// tokens for a whole day, so the prefill cache of it survives. Anything
	// finer would put a new token at the front of every request and re-prefill
	// the whole conversation each turn, so the time of day is left to
	// bash. Empty leaves the line out, which is the prompt the harness
	// sent before the date was possible.
	Date string
	// CanAsk, CanMessage and CanSearch say whether ask_user_question, send_message
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
	// Rules is the project's rules file as the harness read it this turn, and
	// RulesName the file's name as it stands on disk, which the label names.
	// Empty Rules omits the section, which is the prompt before the file was
	// possible.
	Rules     string
	RulesName string
	// Mode picks the body: plan or execute. Anything else is execute.
	Mode string
	// Tools names the tools this turn's request carries, in the order sent.
	Tools []string
	// Plan is an approved plan as Plan renders it, for execute mode. Empty is
	// none.
	Plan string
}

// System renders the shared head around the body for the session's mode. The
// body is rendered first and inserted as a value. Rules are trimmed, so a
// file of whitespace alone leaves the prompt the one sent without it.
func System(v SystemVars) string {
	v.Rules = strings.TrimSpace(v.Rules)
	body, ok := bodyTmpls[v.Mode]
	if !ok {
		body = bodyTmpls["execute"]
	}
	var bb strings.Builder
	if err := body.Execute(&bb, v); err != nil {
		panic("prompts: render the " + v.Mode + " body: " + err.Error())
	}
	head := struct {
		SystemVars
		Tools string
		Body  string
	}{v, strings.Join(v.Tools, ", "), strings.TrimRight(bb.String(), " \t\r\n")}
	var b strings.Builder
	if err := systemTmpl.Execute(&b, head); err != nil {
		panic("prompts: render system.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
}

// PlanVars is a plan as the model is shown it.
type PlanVars struct {
	Title      string
	Goal       string
	Code       []string
	Tests      []string
	Validation []string
	Migration  string
	Risks      []string
	Steps      []string
	Open       []string
}

// Plan renders an approved plan for the execute prompt.
func Plan(v PlanVars) string {
	var b strings.Builder
	if err := planTmpl.Execute(&b, v); err != nil {
		panic("prompts: render plan.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
}

// PlanContinue is the message that sends a turn back to an approved plan it
// stopped short of, naming how much is left and the step to take next.
func PlanContinue(left, total int, next string) string {
	var b strings.Builder
	v := struct {
		Left, Total int
		Next        string
	}{left, total, next}
	if err := continueTmpl.Execute(&b, v); err != nil {
		panic("prompts: render plan_continue.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
}

// PlanApproved is the message that starts the turn carrying out a plan.
func PlanApproved(title string) string {
	var b strings.Builder
	if err := approvedTmpl.Execute(&b, struct{ Title string }{title}); err != nil {
		panic("prompts: render plan_approved.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
}

// Compact asks for the summary a compaction keeps. kept says the latest
// messages stay as they are, so the summary covers what came before them.
func Compact(kept bool) string {
	var b strings.Builder
	if err := compactTmpl.Execute(&b, struct{ Kept bool }{kept}); err != nil {
		panic("prompts: render compact.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
}

// Compaction is the message the summary is sent back as, so the entry keeps
// the summary alone and the framing lives here with the other prompts.
func Compaction(summary, todos string) string {
	var b strings.Builder
	if err := compactionTmpl.Execute(&b, struct{ Summary, Todos string }{summary, todos}); err != nil {
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

// Denial is what the model is told about a call the user refused, with the
// reason they gave, if any.
func Denial(reason string) string {
	var b strings.Builder
	if err := denialTmpl.Execute(&b, struct{ Reason string }{strings.TrimSpace(reason)}); err != nil {
		panic("prompts: render denial.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
}

// Repeat is the note added to the result of a call the model has made count
// times in a row unchanged. firm adds the line for a model that keeps on.
func Repeat(tool string, count int, firm bool) string {
	var b strings.Builder
	v := struct {
		Tool  string
		Count int
		Firm  bool
	}{tool, count, firm}
	if err := repeatTmpl.Execute(&b, v); err != nil {
		panic("prompts: render repeat.md: " + err.Error())
	}
	return strings.TrimRight(b.String(), " \t\r\n")
}

// SubagentResult is what a subagent call waited for, as its tool result.
func SubagentResult(agent, text string) string {
	var b strings.Builder
	if err := resultTmpl.Execute(&b, struct{ Agent, Text string }{agent, text}); err != nil {
		panic("prompts: render subagent_result.md: " + err.Error())
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

func ToolRead() string            { return mustRead("tool_read.md") }
func ToolGrep() string            { return mustRead("tool_grep.md") }
func ToolWebSearch() string       { return mustRead("tool_web_search.md") }
func ToolWrite() string           { return mustRead("tool_write.md") }
func ToolEdit() string            { return mustRead("tool_edit.md") }
func ToolBash() string            { return mustRead("tool_bash.md") }
func ToolTodoWrite() string       { return mustRead("tool_todo_write.md") }
func ToolAskUserQuestion() string { return mustRead("tool_ask_user_question.md") }
func ToolSubagent() string        { return mustRead("tool_subagent.md") }
func ToolSendMessage() string     { return mustRead("tool_send_message.md") }
func ToolExitPlanMode() string    { return mustRead("tool_exit_plan_mode.md") }
func ToolWritePlan() string       { return mustRead("tool_write_plan.md") }
func BudgetSpent() string         { return mustRead("budget_spent.md") }
func NotRun() string              { return mustRead("not_run.md") }
func OutcomeUnknown() string      { return mustRead("outcome_unknown.md") }

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
