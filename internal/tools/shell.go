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

// RunShell runs one command in the project root and returns its merged output
// with an exit code line. Each call is a new shell.
func (r *Runner) RunShell(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args RunShellArgs
	if err := decodeArgs(raw, &args); err != nil {
		return Result{}, fmt.Errorf("run_shell: %s", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return Result{}, errors.New("run_shell needs a command")
	}

	timeout := r.Timeout
	if args.TimeoutS != nil {
		requested := time.Duration(*args.TimeoutS) * time.Second
		switch {
		case requested < time.Second:
			timeout = time.Second
		case requested > r.MaxTimeout:
			timeout = r.MaxTimeout
		default:
			timeout = requested
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	buf := newLimitBuffer(r.OutputLimit)
	cmd := exec.CommandContext(runCtx, r.Shell, "-c", args.Command)
	cmd.Dir = r.Root
	cmd.Env = os.Environ()
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
