package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jesdga95/quecto-harness/internal/tools"
)

// tailLines is how many lines of a background command the browser and the
// model can see at once.
const tailLines = 200

// tailWindow is how long a tail waits before it is sent, so a command that
// prints a line a millisecond does not send one event per line.
const tailWindow = 250 * time.Millisecond

// maxBackground is how many commands one session may have running at once.
const maxBackground = 8

// Process is a background command the harness started.
type Process struct {
	ID        string   `json:"id"`
	Session   string   `json:"session"`
	Call      string   `json:"call"`
	Command   string   `json:"command"`
	PID       int      `json:"pid"`
	Status    string   `json:"status"`
	StartedAt int64    `json:"started_at"`
	EndedAt   int64    `json:"ended_at,omitempty"`
	ExitCode  *int     `json:"exit_code,omitempty"`
	Bytes     int      `json:"bytes"`
	Tail      []string `json:"tail"`
	Truncated bool     `json:"truncated"`
	Spill     string   `json:"spill,omitempty"`
}

// running is a process plus the state only the registry needs.
type running struct {
	view    Process
	handle  *tools.Proc
	pending []string
	stopped bool
	silent  bool
	// watched is a command a bash call is still waiting on. Nothing is told
	// about it until it is promoted, and its end is the call's result rather
	// than a message.
	watched bool
	// code is the exit code once done is closed.
	code int
	done chan struct{}
}

// Registry holds the background commands of every session. It publishes their
// tail to the browsers attached to that session, and tells the conversation
// when one of them ends.
type Registry struct {
	mu sync.Mutex
	// notices is held while an exit notice is weighed and delivered, and by a
	// silent StopAll, so a notice either lands before the stop or not at all.
	notices sync.Mutex
	procs   map[string]*running
	order   []string
	next    int
	publish func(session string, ev Event)
	notify  func(session, text string)
}

func NewRegistry(publish func(session string, ev Event), notify func(session, text string)) *Registry {
	return &Registry{procs: map[string]*running{}, publish: publish, notify: notify}
}

// Start runs a command in the background and returns the record the transcript
// and the browser need.
func (r *Registry) Start(ctx context.Context, runner *tools.Runner, session, call string, args tools.RunShellArgs) (Process, error) {
	r.mu.Lock()
	live := 0
	for _, pr := range r.procs {
		if pr.view.Session == session && pr.view.Status == "running" {
			live++
		}
	}
	if live >= maxBackground {
		r.mu.Unlock()
		return Process{}, fmt.Errorf("session %s already has %d commands running in the background", session, maxBackground)
	}
	r.next++
	pr := &running{
		view: Process{
			ID:        fmt.Sprintf("p-%d", r.next),
			Session:   session,
			Call:      call,
			Command:   args.Command,
			Status:    "running",
			StartedAt: time.Now().UnixMilli(),
			Tail:      []string{},
		},
		done: make(chan struct{}),
	}
	r.mu.Unlock()

	handle, err := runner.SpawnShell(ctx, args, func(line string) { r.line(pr, line) })
	if err != nil {
		return Process{}, err
	}

	r.mu.Lock()
	pr.handle = handle
	pr.view.PID = handle.PID()
	pr.view.Spill = handle.SpillPath()
	r.procs[pr.view.ID] = pr
	r.order = append(r.order, pr.view.ID)
	alive := r.runningLocked(pr.view.Session)
	r.mu.Unlock()
	r.publish(pr.view.Session, ev("proc_start", map[string]any{
		"id": pr.view.ID, "call": pr.view.Call, "command": pr.view.Command,
		"pid": pr.view.PID, "started_at": pr.view.StartedAt,
	}))
	r.publish(pr.view.Session, ev("procs", map[string]any{"running": alive}))

	go r.flush(pr)
	go func() {
		code, _, _, _ := handle.Wait()
		r.finish(pr, code)
	}()
	return pr.view, nil
}

// Watch starts a command for a bash call that waits on it. It is not listed
// or announced unless Promote moves it to the background.
func (r *Registry) Watch(ctx context.Context, runner *tools.Runner, session, call string, args tools.RunShellArgs) (*running, error) {
	pr := &running{
		view: Process{
			Session:   session,
			Call:      call,
			Command:   args.Command,
			Status:    "running",
			StartedAt: time.Now().UnixMilli(),
			Tail:      []string{},
		},
		watched: true,
		done:    make(chan struct{}),
	}
	handle, err := runner.WatchShell(ctx, args, func(line string) { r.line(pr, line) })
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	pr.handle = handle
	pr.view.PID = handle.PID()
	pr.view.Spill = handle.SpillPath()
	r.mu.Unlock()
	go func() {
		code, _, _, _ := handle.Wait()
		r.finish(pr, code)
	}()
	return pr, nil
}

// Promote moves a watched command to the background: it is listed, its tail
// is published, and its end is told to the conversation. It reports false
// when the command has already ended.
func (r *Registry) Promote(pr *running) bool {
	r.mu.Lock()
	if pr.view.Status != "running" {
		r.mu.Unlock()
		return false
	}
	r.next++
	pr.view.ID = fmt.Sprintf("p-%d", r.next)
	pr.watched = false
	pr.pending = append([]string(nil), pr.view.Tail...)
	pr.handle.Keep()
	r.procs[pr.view.ID] = pr
	r.order = append(r.order, pr.view.ID)
	alive := r.runningLocked(pr.view.Session)
	r.mu.Unlock()
	r.publish(pr.view.Session, ev("proc_start", map[string]any{
		"id": pr.view.ID, "call": pr.view.Call, "command": pr.view.Command,
		"pid": pr.view.PID, "started_at": pr.view.StartedAt,
	}))
	r.publish(pr.view.Session, ev("procs", map[string]any{"running": alive}))
	go r.flush(pr)
	return true
}

// line records one line of output and queues it for the next tail event. A
// watched command keeps only its tail, which Promote queues.
func (r *Registry) line(pr *running, line string) {
	r.mu.Lock()
	if len(pr.view.Tail) >= tailLines {
		pr.view.Tail = pr.view.Tail[1:]
		pr.view.Truncated = true
	}
	pr.view.Tail = append(pr.view.Tail, line)
	if !pr.watched {
		pr.pending = append(pr.pending, line)
	}
	r.mu.Unlock()
}

// flush sends the queued lines at most once per tailWindow.
func (r *Registry) flush(pr *running) {
	ticker := time.NewTicker(tailWindow)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.send(pr)
		case <-pr.done:
			r.send(pr)
			return
		}
	}
}

func (r *Registry) send(pr *running) {
	r.mu.Lock()
	lines := pr.pending
	pr.pending = nil
	r.mu.Unlock()
	if len(lines) == 0 {
		return
	}
	r.publish(pr.view.Session, ev("proc_output", map[string]any{
		"id": pr.view.ID, "lines": lines, "bytes": pr.handle.Total(),
	}))
}

// finish closes a process, reports how it ended, and tells the conversation.
// A watched command only closes: the call waiting on it reports the end.
func (r *Registry) finish(pr *running, code int) {
	r.mu.Lock()
	pr.code = code
	if pr.watched {
		pr.view.Status = "done"
		r.mu.Unlock()
		close(pr.done)
		return
	}
	if pr.stopped {
		pr.view.Status = "stopped"
		pr.view.ExitCode = intPtr(130)
	} else {
		pr.view.Status = "done"
		pr.view.ExitCode = intPtr(code)
	}
	pr.view.EndedAt = time.Now().UnixMilli()
	pr.view.Bytes = pr.handle.Total()
	alive := r.runningLocked(pr.view.Session)
	r.mu.Unlock()
	close(pr.done)
	r.send(pr)
	r.publish(pr.view.Session, ev("proc_end", map[string]any{
		"id": pr.view.ID, "status": pr.view.Status, "exit_code": pr.view.ExitCode,
		"bytes": pr.view.Bytes, "ended_at": pr.view.EndedAt,
	}))
	r.publish(pr.view.Session, ev("procs", map[string]any{"running": alive}))

	r.notices.Lock()
	defer r.notices.Unlock()
	r.mu.Lock()
	silent := pr.silent
	r.mu.Unlock()
	if !silent {
		r.notify(pr.view.Session, exitNotice(pr.view))
	}
}

// exitNotice is what a finished command sends back to the conversation: how it
// ended and the last lines it printed, so the next turn has something to act on
// without asking for the tail again.
func exitNotice(pr Process) string {
	var b strings.Builder
	if pr.Status == "stopped" {
		b.WriteString(fmt.Sprintf("Background process %s (%s) was stopped.", pr.ID, pr.Command))
	} else {
		b.WriteString(fmt.Sprintf("Background process %s (%s) exited with code %d.", pr.ID, pr.Command, *pr.ExitCode))
	}
	tail := pr.Tail
	if len(tail) > 20 {
		tail = tail[len(tail)-20:]
	}
	if len(tail) > 0 {
		b.WriteString("\nLast lines of its output:")
		for _, line := range tail {
			b.WriteString("\n  " + line)
		}
	}
	return b.String()
}

// runningLocked counts a session's commands still alive. The caller holds the
// lock.
func (r *Registry) runningLocked(session string) int {
	n := 0
	for _, pr := range r.procs {
		if pr.view.Session == session && pr.view.Status == "running" {
			n++
		}
	}
	return n
}

// Background is how many commands each session still has running, for the
// sessions that have at least one.
func (r *Registry) Background() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for _, pr := range r.procs {
		if pr.view.Status == "running" {
			out[pr.view.Session]++
		}
	}
	return out
}

// Stop ends one process of a session. A process that has already gone is not
// an error.
func (r *Registry) Stop(session, id string) error {
	r.mu.Lock()
	pr := r.procs[id]
	if pr == nil || pr.view.Session != session || pr.view.Status != "running" {
		r.mu.Unlock()
		return nil
	}
	pr.stopped = true
	r.mu.Unlock()
	return pr.handle.Stop()
}

// StopAll ends every command of a session that is still running. silent keeps
// the conversation from hearing about them, which is what a session being
// stopped or deleted wants: nothing there should start a turn again. It also
// covers a command that has ended but whose notice has not been sent yet.
func (r *Registry) StopAll(session string, silent bool) {
	if silent {
		r.notices.Lock()
		defer r.notices.Unlock()
	}
	r.mu.Lock()
	var targets []*running
	for _, pr := range r.procs {
		if pr.view.Session != session {
			continue
		}
		if silent {
			pr.silent = true
		}
		if pr.view.Status == "running" && !pr.stopped {
			pr.stopped = true
			targets = append(targets, pr)
		}
	}
	r.mu.Unlock()
	for _, pr := range targets {
		_ = pr.handle.Stop()
	}
}

// List returns a session's processes in the order they started.
func (r *Registry) List(session string) []Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Process, 0, len(r.order))
	for _, id := range r.order {
		if pr := r.procs[id]; pr != nil && pr.view.Session == session {
			out = append(out, pr.view)
		}
	}
	return out
}

// Output is a process's output cut to limit bytes.
func (r *Registry) Output(session, id string, limit int) (Process, string, bool, bool) {
	r.mu.Lock()
	pr := r.procs[id]
	if pr == nil || pr.view.Session != session {
		r.mu.Unlock()
		return Process{}, "", false, false
	}
	view := pr.view
	r.mu.Unlock()
	out, trunc := pr.handle.Output(limit)
	return view, out, trunc, true
}

func intPtr(v int) *int { return &v }

// Processes lists the background commands of a session.
func (e *Engine) Processes(id string) []Process { return e.procs.List(id) }

// Background counts the commands each session still has running, for the
// sessions that have at least one.
func (e *Engine) Background() map[string]int { return e.procs.Background() }

// StopProcess ends one background command of a session.
func (e *Engine) StopProcess(id, procID string) error { return e.procs.Stop(id, procID) }

// StopProcesses ends every background command of a session and of every
// subagent under it, whether or not that subagent is in a turn. A session being
// stopped or deleted keeps the news to itself, so nothing there starts a turn
// again.
func (e *Engine) StopProcesses(id string, silent bool) {
	e.procs.StopAll(id, silent)
	for _, s := range e.liveSessions() {
		if s.Parent == id {
			e.StopProcesses(s.ID, silent)
		}
	}
}

// ProcessOutput is one command's output cut to limit bytes. The last bool says
// whether the process exists.
func (e *Engine) ProcessOutput(id, procID string, limit int) (Process, string, bool, bool) {
	return e.procs.Output(id, procID, limit)
}
