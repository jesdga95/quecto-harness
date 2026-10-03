package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// waitEnded returns a session's processes once none of them is running.
func waitEnded(t *testing.T, h *harness, session string) []Process {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		procs := h.engine.Processes(session)
		running := false
		for _, p := range procs {
			if p.Status == "running" {
				running = true
			}
		}
		if len(procs) > 0 && !running {
			return procs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no background process ended")
	return nil
}

// A background command answers at once with the process it started, streams its
// tail, and tells the conversation how it ended.
func TestBackgroundProcessRunsAndPingsBack(t *testing.T) {
	h := newHarness(t, "stream_background.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist })
	h.cfg.AddAllow(h.project, "seq")
	s := h.session(t)
	h.engine.Send(s.ID, "run that in the background", nil)
	waitIdle(t, s)

	out := toolOutput(t, s, "bash")
	if !strings.Contains(out, "Running in the background") {
		t.Errorf("the bash answer = %q", out)
	}
	starts := h.rec.ofType("proc_start")
	if len(starts) != 1 {
		t.Fatalf("proc_start events = %d, want one", len(starts))
	}
	if starts[0].Fields["pid"] == nil || starts[0].Fields["pid"] == 0 {
		t.Errorf("proc_start = %v, want a pid", starts[0].Fields)
	}

	procs := waitEnded(t, h, s.ID)
	if procs[0].Status != "done" {
		t.Errorf("status = %s, want done", procs[0].Status)
	}
	if procs[0].ExitCode == nil || *procs[0].ExitCode != 0 {
		t.Errorf("exit code = %v", procs[0].ExitCode)
	}
	if len(procs[0].Tail) != 3 {
		t.Errorf("tail = %v, want the three lines it printed", procs[0].Tail)
	}
	if procs[0].Spill == "" {
		t.Error("a background command keeps its whole output on disk")
	}

	notice := waitForEntryFrom(t, s, "Background process")
	if !strings.Contains(notice.Content, "exited with code 0") {
		t.Errorf("the notice = %q", notice.Content)
	}
	if !strings.Contains(notice.Content, "1") {
		t.Errorf("the notice carries no tail: %q", notice.Content)
	}
	if len(h.rec.ofType("proc_output")) == 0 {
		t.Error("no proc_output event reached the browser")
	}
	counts := h.rec.ofType("procs")
	if len(counts) != 2 {
		t.Fatalf("procs events = %d, want one at the start and one at the end", len(counts))
	}
	if counts[0].Fields["running"] != 1 || counts[1].Fields["running"] != 0 {
		t.Errorf("running counts = %v and %v, want 1 then 0", counts[0].Fields["running"], counts[1].Fields["running"])
	}
}

// Stopping one reports it as stopped and still tells the conversation.
func TestStoppingABackgroundProcess(t *testing.T) {
	h := newHarness(t, "stream_background_long.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist })
	h.cfg.AddAllow(h.project, "sleep")
	s := h.session(t)
	h.engine.Send(s.ID, "start it and leave it running", nil)
	waitIdle(t, s)

	procs := h.engine.Processes(s.ID)
	if len(procs) != 1 || procs[0].Status != "running" {
		t.Fatalf("processes = %v", procs)
	}
	if err := h.engine.StopProcess(s.ID, procs[0].ID); err != nil {
		t.Fatal(err)
	}
	ended := waitEnded(t, h, s.ID)
	if ended[0].Status != "stopped" {
		t.Errorf("status = %s, want stopped", ended[0].Status)
	}
	notice := waitForEntryFrom(t, s, "Background process")
	if !strings.Contains(notice.Content, "was stopped") {
		t.Errorf("the notice = %q", notice.Content)
	}
}

// A session that is stopped hears nothing about the commands it started, so
// nothing there opens a turn again.
func TestStoppingASessionSilencesItsProcesses(t *testing.T) {
	h := newHarness(t, "stream_background_long.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist })
	h.cfg.AddAllow(h.project, "sleep")
	s := h.session(t)
	h.engine.Send(s.ID, "start it", nil)
	waitIdle(t, s)
	if len(h.engine.Processes(s.ID)) != 1 {
		t.Fatalf("processes = %v", h.engine.Processes(s.ID))
	}
	h.engine.Stop(s.ID)
	ended := waitEnded(t, h, s.ID)
	if ended[0].Status != "stopped" {
		t.Errorf("status = %s, want stopped", ended[0].Status)
	}
	s.mu.Lock()
	entries := append([]store.Entry{}, s.Entries...)
	s.mu.Unlock()
	for _, e := range entries {
		if e.From == "Background process" {
			t.Error("a session that was stopped heard about its own process")
		}
	}
}

// Stopping a session reaches a subagent that has finished its turn: its
// command is stopped and the subagent hears nothing that would start a turn.
func TestStoppingASessionReachesAnIdleSubagent(t *testing.T) {
	h := newHarness(t, "stream_background_long.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist })
	h.cfg.AddAllow(h.project, "sleep")
	parent := h.session(t)
	child := h.session(t)
	h.engine.Send(child.ID, "start it", nil)
	waitIdle(t, child)
	// The link is made once the turn is over, so the finished turn reports
	// to nobody and the child is idle when its launcher is stopped.
	h.engine.wg.Wait()
	child.Parent = parent.ID
	if procs := h.engine.Processes(child.ID); len(procs) != 1 || procs[0].Status != "running" {
		t.Fatalf("processes = %v", procs)
	}

	if _, err := h.engine.Stop(parent.ID); err != nil {
		t.Fatal(err)
	}
	ended := waitEnded(t, h, child.ID)
	if ended[0].Status != "stopped" {
		t.Errorf("status = %s, want stopped", ended[0].Status)
	}
	time.Sleep(100 * time.Millisecond)
	if child.State() != StateIdle {
		t.Errorf("the subagent is in %s after its launcher was stopped", child.State())
	}
	child.mu.Lock()
	entries := append([]store.Entry{}, child.Entries...)
	child.mu.Unlock()
	for _, e := range entries {
		if e.From == "Background process" {
			t.Error("a subagent whose launcher was stopped heard about its process")
		}
	}
}

// A background flag that is not a JSON boolean is the model's mistake. The call
// answers with the field, what it held and what it takes, and nothing runs.
func TestABackgroundFlagThatIsNotABooleanRunsNothing(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	runner, err := tools.NewRunner(h.project, h.cfg.Config())
	if err != nil {
		t.Fatal(err)
	}
	call := llm.ToolCall{ID: "call_b", Type: "function", Function: llm.FunctionCall{
		Name:      "bash",
		Arguments: `{"command":"touch ran","background":"True"}`,
	}}
	if _, err := h.engine.runBash(context.Background(), s, runner, call); err == nil ||
		!strings.Contains(err.Error(), `bash: background got string "True", expected JSON boolean true or false`) {
		t.Errorf("the call answered %v", err)
	}
	if procs := h.engine.Processes(s.ID); len(procs) != 0 {
		t.Errorf("processes = %v, want none", procs)
	}
	if _, err := os.Stat(filepath.Join(h.project, "ran")); err == nil {
		t.Error("the refused call ran its command")
	}
}
