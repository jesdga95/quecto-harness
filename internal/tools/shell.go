package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// plainEnv keeps colour codes and pagers out of command output, and stops git
// waiting on an editor nobody can type into. Later entries override the
// user's own settings of the same names.
var plainEnv = []string{"NO_COLOR=1", "TERM=dumb", "PAGER=cat", "GIT_PAGER=cat", "GIT_EDITOR=true"}

// RunShell runs one command in the project root and returns its merged output
// with an exit code line. Each call is a new shell.
func (r *Runner) RunShell(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args RunShellArgs
	if err := decodeArgs(raw, &args); err != nil {
		return Result{}, fmt.Errorf("bash: %s", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, errors.New("bash needs a command")
	}

	timeout := r.shellTimeout(args)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	buf := newLimitBuffer(r.OutputLimit)
	spill := r.openSpill()
	if spill != nil {
		defer spill.Close()
		buf.spill, buf.spillCap = spill, spillCap
	}
	cmd := exec.CommandContext(runCtx, r.Shell, "-c", args.Command)
	cmd.Dir = r.Root
	cmd.Env = append(os.Environ(), plainEnv...)
	cmd.Stdin = nil
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.WaitDelay = 2 * time.Second
	newProcessGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		killGroup(cmd.Process.Pid, syscall.SIGTERM)
		time.AfterFunc(2*time.Second, func() { killGroup(cmd.Process.Pid, syscall.SIGKILL) })
		return nil
	}

	runErr := cmd.Run()

	timedOut := runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil
	stopped := ctx.Err() != nil

	out, truncated := buf.Render(r.OutputLimit)
	var b strings.Builder
	b.WriteString(out)
	if b.Len() > 0 && !strings.HasSuffix(out, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(keepSpill(spill, buf, truncated))
	if timedOut {
		fmt.Fprintf(&b, "\ncommand timed out after %ds and was killed\n", int(timeout/time.Second))
	}
	if stopped {
		b.WriteString("\nstopped\n")
		return Result{Output: strings.TrimRight(b.String(), "\n"), Truncated: truncated, Bytes: buf.Total()}, nil
	}

	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	} else if runErr != nil {
		code = -1
	}
	b.WriteString("\n")
	if code < 0 {
		b.WriteString("exit code: killed by signal " + signalName(cmd) + "\n")
	} else {
		fmt.Fprintf(&b, "exit code: %d\n", code)
	}
	exit := code
	return Result{
		Output:    strings.TrimRight(b.String(), "\n"),
		ExitCode:  &exit,
		Truncated: truncated,
		Bytes:     buf.Total(),
	}, nil
}

// spillCap bounds the saved copy of one command's output.
const spillCap = 16 << 20

// openSpill starts the file a command's whole output is saved to, or nil when
// nothing is saved. A folder that cannot be made saves nothing.
func (r *Runner) openSpill() *os.File {
	if r.Spill == "" {
		return nil
	}
	if err := os.MkdirAll(r.Spill, 0o700); err != nil {
		return nil
	}
	f, err := os.CreateTemp(r.Spill, "bash-*.txt")
	if err != nil {
		return nil
	}
	return f
}

// keepSpill keeps the saved output of a command that was cut and names it,
// and removes it otherwise. It answers with the line the result carries.
func keepSpill(f *os.File, buf *limitBuffer, truncated bool) string {
	if f == nil {
		return ""
	}
	buf.mu.Lock()
	whole, saved := buf.spill != nil, buf.spilled
	buf.mu.Unlock()
	if !truncated || !whole {
		f.Close()
		os.Remove(f.Name())
		return ""
	}
	if saved < buf.Total() {
		return fmt.Sprintf("\nthe first %d bytes of the output are saved to %s: read it with offset and limit, or grep it\n", saved, f.Name())
	}
	return fmt.Sprintf("\nthe whole output, %d bytes, is saved to %s: read it with offset and limit, or grep it\n", saved, f.Name())
}

// ShellTimeout is how long the call in raw would be given. Arguments that do
// not decode take the default, which is what running them does.
func (r *Runner) ShellTimeout(raw json.RawMessage) time.Duration {
	var args RunShellArgs
	if decodeArgs(raw, &args) != nil {
		return r.Timeout
	}
	return r.shellTimeout(args)
}

// shellTimeout clamps what the call asked for between a second and MaxTimeout.
// A call that names nothing takes the configured default.
func (r *Runner) shellTimeout(args RunShellArgs) time.Duration {
	if args.TimeoutS == nil {
		return r.Timeout
	}
	requested := time.Duration(*args.TimeoutS) * time.Second
	switch {
	case requested < time.Second:
		return time.Second
	case requested > r.MaxTimeout:
		return r.MaxTimeout
	default:
		return requested
	}
}

// signalName reports which signal killed the process, or "unknown" when the
// platform did not say.
func signalName(cmd *exec.Cmd) string {
	if cmd.ProcessState == nil {
		return "unknown"
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return "unknown"
	}
	return status.Signal().String()
}
