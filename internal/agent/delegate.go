package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/prompts"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// taskTitle bounds the name a subagent run is filed under, which is cut from
// the task rather than asked of the model.
const taskTitle = 60

// rulesFile is the project's own rules. It is read once per turn and carried
// in the system prompt, which is what makes its cost a fixed part of every
// request rather than a file the model has to go find.
const rulesFile = "AGENTS.md"

// brief is what the prompt and the request need beyond the agent and the
// project: which tools were sent, who can take work, the handle of the
// session that gave this one its task, and the project's rules as this turn
// read them.
type brief struct {
	offer    tools.Offer
	agents   []prompts.AgentBrief
	launcher string
	// rules is the content of the project's rules file and rulesName the
	// file's name as it stands on disk. Both empty is a project with none.
	rules     string
	rulesName string
}

// findRulesFile names the rules file in a project root, exact case first and
// then any casing of it, so a file a person named agents.md is read on a
// case-sensitive filesystem as well.
func findRulesFile(project string) (string, error) {
	entries, err := os.ReadDir(project)
	if err != nil {
		return "", err
	}
	var folded string
	for _, d := range entries {
		if !d.Type().IsRegular() {
			continue
		}
		if d.Name() == rulesFile {
			return d.Name(), nil
		}
		if folded == "" && strings.EqualFold(d.Name(), rulesFile) {
			folded = d.Name()
		}
	}
	return folded, nil
}

// readRules reads the project's rules file, in any casing. A file over the
// attachment cap is skipped whole with a note saying so, and a read error is
// the same: the turn runs without the rules rather than failing over them,
// and the model can still read the file itself.
func readRules(project string) (content, name, note string) {
	name, err := findRulesFile(project)
	if err != nil || name == "" {
		return "", "", ""
	}
	info, err := os.Stat(filepath.Join(project, name))
	if err != nil || info.Size() == 0 {
		return "", "", ""
	}
	if info.Size() > tools.MaxAttachment {
		return "", name, fmt.Sprintf("%s is %d kb, the limit is %d kb: it is not in the prompt",
			name, info.Size()/1024, tools.MaxAttachment/1024)
	}
	raw, err := os.ReadFile(filepath.Join(project, name))
	if err != nil {
		return "", name, fmt.Sprintf("%s could not be read: check the permissions on the file", name)
	}
	return string(raw), name, ""
}

// briefFor reads what a session may do this turn. A subagent hands work to
// nobody and asks nobody, so it is offered neither run_agent nor ask_question;
// what it has instead is the launcher waiting on it. The rules file is read
// outside the lock, like the image bytes a message carries.
func (e *Engine) briefFor(s *Session, cfg config.Config) brief {
	s.mu.Lock()
	parent, mine, mode, project := s.Parent, s.AgentID, s.Mode, s.Project
	s.mu.Unlock()
	rules, rulesName, note := readRules(project)
	e.showRulesNote(s, note)
	b := brief{rules: rules, rulesName: rulesName}
	if parent != "" {
		// A subagent hands work to nobody and asks nobody, but it still
		// searches: research delegated down is what web_search is for.
		b.offer = tools.Offer{Mode: mode, Message: true, Search: cfg.Search.Enabled()}
		b.launcher = parent
		return b
	}
	b.offer = tools.Offer{Mode: mode, Ask: true, Message: true, Search: cfg.Search.Enabled()}
	me, _ := cfg.AgentByID(mine)
	// What is named is what the caller may hand work to, so a list that names
	// nothing offers the tool at all, the way a roster with no enabled agents
	// did.
	for _, a := range cfg.Agents {
		if !a.Enabled || !slices.Contains(me.Calls, a.ID) {
			continue
		}
		// This turn is already one of its own agent's tasks, so a launch onto
		// that agent has one fewer place to go. An agent with none left is not
		// listed: naming one that every launch would refuse only buys a wasted
		// call.
		free := a.Concurrency
		if a.ID == mine {
			free--
		}
		if free < 1 {
			continue
		}
		b.agents = append(b.agents, prompts.AgentBrief{Name: a.Name, Role: role(a)})
	}
	b.offer.Agents = len(b.agents) > 0
	return b
}

// showRulesNote records that the project's rules are not in the prompt, so
// the transcript says why. A note already shown is not repeated, and a file
// that fits again warns nothing.
func (e *Engine) showRulesNote(s *Session, note string) {
	s.mu.Lock()
	if note == s.rulesNote {
		s.mu.Unlock()
		return
	}
	s.rulesNote = note
	var entry store.Entry
	if note != "" {
		entry = s.appendEntry(store.Entry{Kind: "status", Content: note, InContext: false, Visible: true})
	}
	s.mu.Unlock()
	if note == "" {
		return
	}
	e.save(s)
	e.publish(s.ID, ev("status_entry", map[string]any{"content": entry.Content}))
}

// role is what the delegation list says an agent is for: its description, or
// the first line of its instructions when it has none, which is where the
// answer was read from before the field existed.
func role(a config.Agent) string {
	text := a.Description
	if text == "" {
		text = a.Instructions
	}
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}

// rootOf is the conversation a session belongs to, which is where its
// permission cards are drawn.
func (e *Engine) rootOf(s *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Parent != "" {
		return s.Parent
	}
	return s.ID
}

// busy counts the sessions running one agent that have a turn under way,
// whoever started them. A model pinned to one slot answers one request at a
// time, so a conversation and a subagent on the same agent are one queue.
func (e *Engine) busy(agentID string) int {
	n := 0
	for _, s := range e.liveSessions() {
		s.mu.Lock()
		mine, state := s.AgentID, s.state
		s.mu.Unlock()
		if mine == agentID && state != StateIdle {
			n++
		}
	}
	return n
}

// liveChildren are the subagent runs of one session that are still working. A
// child that is not working is idle in the store and costs nothing.
func (e *Engine) liveChildren(parentID string) []*Session {
	var out []*Session
	for _, s := range e.liveSessions() {
		if s.Parent == parentID && s.State() != StateIdle {
			out = append(out, s)
		}
	}
	return out
}

// runAgent starts a subagent on the named agent and answers with its handle.
// It does not wait: the subagent works while this turn carries on, and what it
// found arrives later as a message.
func (e *Engine) runAgent(s *Session, call llm.ToolCall) (tools.Result, error) {
	var args struct {
		Agent string `json:"agent"`
		Task  string `json:"task"`
	}
	if err := decodeArgs(call.Function.Arguments, &args); err != nil {
		return tools.Result{}, fmt.Errorf("run_agent: %s", err)
	}
	task := strings.TrimSpace(args.Task)
	if task == "" {
		return tools.Result{}, errors.New("run_agent: task is empty, say what the agent is to do")
	}

	cfg := e.cfg.Config()
	s.mu.Lock()
	mine, project, mode := s.AgentID, s.Project, s.Mode
	s.mu.Unlock()
	me, _ := cfg.AgentByID(mine)

	target, err := findAgent(cfg, me, args.Agent)
	if err != nil {
		return tools.Result{}, err
	}
	if err := usable(target); err != nil {
		return tools.Result{}, fmt.Errorf("run_agent: %s is not ready: %s", target.Name, err)
	}
	// The limit belongs to the agent being asked, not to the one asking: what
	// it counts is requests to one model server. This session is one of them,
	// so an agent that runs one at a time cannot take work from itself.
	if live := e.busy(target.ID); target.Concurrency > 0 && live >= target.Concurrency {
		return tools.Result{}, fmt.Errorf("run_agent: %s runs %d at a time and that many are working now: wait for one to finish, or pick another agent",
			target.Name, target.Concurrency)
	}

	child := newSession(store.NewID(), project, target.ID, target.Model)
	child.Parent = s.ID
	// A subagent works in the mode of the session that launched it, so a
	// planner's researcher changes no files either.
	child.Mode = mode
	child.Title = store.Title(cut(task, taskTitle))
	child.Named = true
	e.mu.Lock()
	e.live[child.ID] = child
	e.mu.Unlock()
	e.save(child)

	if _, err := e.deliverFrom(child, sender{Name: me.Name, Session: s.ID}, task, nil); err != nil {
		return tools.Result{}, fmt.Errorf("run_agent: %s", err)
	}
	e.publish(s.ID, ev("child", map[string]any{
		"call_id": call.ID, "session": child.ID, "agent": target.Name,
		"title": child.Title, "state": string(StatePreflight)}))
	// The handle is on the entry as well as in the sentence, so a page loaded
	// later still links the row to the transcript it started.
	return tools.Result{
		Output: fmt.Sprintf("started %s (%s)", target.Name, child.ID),
		Meta:   map[string]any{"session": child.ID, "agent": target.Name},
	}, nil
}

// sendMessage delivers one agent's message to the other end of a launch. It is
// the same path a typed message takes, so it joins a turn that is running and
// starts one that is not.
func (e *Engine) sendMessage(s *Session, call llm.ToolCall) (tools.Result, error) {
	var args struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if err := decodeArgs(call.Function.Arguments, &args); err != nil {
		return tools.Result{}, fmt.Errorf("send_message: %s", err)
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return tools.Result{}, errors.New("send_message: text is empty")
	}

	to := strings.TrimSpace(args.To)
	s.mu.Lock()
	parent, mine := s.Parent, s.AgentID
	s.mu.Unlock()
	if strings.EqualFold(to, "parent") {
		to = parent
	}
	if to == "" {
		return tools.Result{}, errors.New("send_message: nobody gave you this task, so there is no parent to answer")
	}

	target, err := e.Get(to)
	if err != nil {
		return tools.Result{}, e.unreachable(s, args.To)
	}
	target.mu.Lock()
	reachable := target.ID == parent || target.Parent == s.ID
	target.mu.Unlock()
	if !reachable {
		return tools.Result{}, e.unreachable(s, args.To)
	}

	me, _ := e.cfg.Config().AgentByID(mine)
	sent, err := e.deliverFrom(target, sender{Name: me.Name, Session: s.ID}, text, nil)
	if err != nil {
		return tools.Result{}, fmt.Errorf("send_message: %s", err)
	}
	// A sender with nothing left to do has no way to wait for the answer, so
	// the result says what ends the wait rather than leaving it to work it out.
	if sent.Queued {
		return tools.Result{Output: "sent, and it joins the turn already running. a reply starts a new turn, so end this one unless you have work left"}, nil
	}
	return tools.Result{Output: "sent. a reply starts a new turn, so end this one unless you have work left"}, nil
}

// unreachable answers a send_message addressed to something this session
// cannot reach, naming the handles that would have worked.
func (e *Engine) unreachable(s *Session, to string) error {
	s.mu.Lock()
	parent := s.Parent
	s.mu.Unlock()
	var handles []string
	if parent != "" {
		handles = append(handles, parent)
	}
	for _, c := range e.liveSessions() {
		if c.Parent == s.ID {
			handles = append(handles, c.ID)
		}
	}
	if len(handles) == 0 {
		return fmt.Errorf("send_message: %q is not a handle you can reach: you have launched nobody", to)
	}
	return fmt.Errorf("send_message: %q is not a handle you can reach: send to %s",
		to, strings.Join(handles, " or "))
}

// report rolls the files a subagent wrote onto its launcher and hands over
// what the turn finished with, which is the subagent's result. send_message
// carries a question or progress mid-task and does not stand in for that, so
// a turn that used it reports its answer too.
func (e *Engine) report(child *Session) {
	child.mu.Lock()
	parentID, agentID := child.Parent, child.AgentID
	child.mu.Unlock()
	parent, err := e.Get(parentID)
	if err != nil {
		return
	}
	e.rollUp(child, parent)

	from, _ := e.cfg.Config().AgentByID(agentID)
	limit := e.cfg.Config().OutputLimitBytes
	if limit <= 0 {
		limit = 16384
	}
	text, _ := tools.Truncate(e.answerOf(child), limit)
	if _, err := e.deliverFrom(parent, sender{Name: from.Name, Session: child.ID}, text, nil); err != nil {
		e.publish(parent.ID, ev("error", map[string]any{"kind": "internal", "message": err.Error()}))
	}
}

// answerOf is what a subagent finished with: its last reply, or the sentence
// saying why there is none.
func (e *Engine) answerOf(child *Session) string {
	child.mu.Lock()
	defer child.mu.Unlock()
	answer := ""
	for i := len(child.Entries) - 1; i >= 0; i-- {
		if child.Entries[i].Kind == "assistant" && strings.TrimSpace(child.Entries[i].Content) != "" {
			answer = child.Entries[i].Content
			break
		}
	}
	switch {
	case child.state == StateStopped:
		return joinMessages(answer, "The task was stopped before it finished.")
	case child.state == StateFailed:
		return joinMessages(answer, "The task failed and did not finish.")
	case answer == "":
		return "The task ended with nothing to report."
	}
	return answer
}

// rollUp copies a subagent's baselines onto its launcher, so the files it
// wrote are in the launcher's change panel. A path the launcher wrote first
// keeps the launcher's older copy, which is the state the work started from.
func (e *Engine) rollUp(child, parent *Session) {
	child.mu.Lock()
	from := append([]store.Baseline(nil), child.Baselines...)
	childID := child.ID
	child.mu.Unlock()
	if len(from) == 0 {
		return
	}

	added := false
	for _, b := range from {
		parent.mu.Lock()
		held := false
		for _, have := range parent.Baselines {
			if have.Path == b.Path {
				held = true
				break
			}
		}
		parent.mu.Unlock()
		if held {
			continue
		}
		if b.Blob != "" {
			data, err := e.store.Blob(childID, b.Blob)
			if err != nil {
				// Nothing was kept, so the file is left out rather than listed
				// against a copy that is not there.
				continue
			}
			if err := e.store.WriteBlob(parent.ID, b.Blob, data); err != nil {
				continue
			}
		}
		parent.mu.Lock()
		parent.Baselines = append(parent.Baselines, b)
		parent.mu.Unlock()
		added = true
	}
	if added {
		e.save(parent)
	}
	// The launcher's own stream carries no tool output for a file a subagent
	// wrote, and a path it already kept a copy of moves the diff without moving
	// the list, so this is the only thing that says its change panel is stale.
	e.publish(parent.ID, ev("changed", nil))
}

// findAgent matches an agent by name, which is what the model was given. It
// may match only what the caller was told: an enabled agent on the caller's
// list. A disabled one and one off the list are refused by name and left out
// of the names a misspelled call is told.
func findAgent(cfg config.Config, me config.Agent, name string) (config.Agent, error) {
	want := strings.TrimSpace(name)
	var names []string
	for _, a := range cfg.Agents {
		if !a.Enabled || !slices.Contains(me.Calls, a.ID) {
			continue
		}
		if strings.EqualFold(a.Name, want) {
			return a, nil
		}
		names = append(names, a.Name)
	}
	if len(names) == 0 {
		return config.Agent{}, errors.New("run_agent: there are no agents to run this")
	}
	return config.Agent{}, fmt.Errorf("run_agent: no agent named %q. There is %s",
		want, strings.Join(names, ", "))
}

// cut shortens text to n characters on a word boundary where there is one.
func cut(text string, n int) string {
	if len(text) <= n {
		return text
	}
	head := text[:n]
	if i := strings.LastIndexByte(head, ' '); i > n/2 {
		head = head[:i]
	}
	return head
}

// decodeArgs reads a call's arguments, tolerating a field the model invented.
func decodeArgs(raw string, v any) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("the arguments were empty")
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return fmt.Errorf("the arguments are not valid JSON: %s", err)
	}
	return nil
}
