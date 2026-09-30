package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Proc is a command that has been started. RunShell waits for it and renders
// the result; a command started in the background is handed to the registry,
// which reads its lines as they arrive.
type Proc struct {
	cmd     *exec.Cmd
	buf     *limitBuffer
	spill   *os.File
	reader  *io.PipeReader
	pipe    *io.PipeWriter
	pumped  chan struct{}
	cancel  context.CancelFunc
	runCtx  context.Context
	parent  context.Context
	timeout time.Duration
	onLine  func(string)
	keep    bool
}

// PID is the process group the command runs in, zero before it has started.
func (p *Proc) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// SpillPath is where the whole output is written, empty when nothing keeps it.
func (p *Proc) SpillPath() string {
	if p.spill == nil {
		return ""
	}
	return p.spill.Name()
}

// Total is the number of bytes the command has printed.
func (p *Proc) Total() int { return p.buf.Total() }

// Output is the command's output cut to limit bytes.
func (p *Proc) Output(limit int) (string, bool) { return p.buf.Render(limit) }

// maxLine is the longest piece of output handed to onLine at once. A longer
// line arrives as several pieces.
const maxLine = 64 << 10

// pump reads the command's output to its end, so a child never blocks on a
// full pipe. Every byte goes to the buffer as it was printed, and each line,
// or each maxLine piece of a longer one, goes to onLine without its newline.
func (p *Proc) pump() {
	defer close(p.pumped)
	rd := bufio.NewReaderSize(p.reader, maxLine)
	split := false
	for {
		chunk, err := rd.ReadSlice('\n')
		if len(chunk) > 0 {
			p.buf.Write(chunk)
			// The newline that ends a split line is not a line of its own.
			if p.onLine != nil && !(split && string(chunk) == "\n") {
				line := strings.TrimSuffix(string(chunk), "\n")
				p.onLine(strings.TrimSuffix(line, "\r"))
			}
		}
		split = err == bufio.ErrBufferFull
		if err != nil && !split {
			return
		}
	}
}

// Wait runs the command to its end. It reports the exit code, and whether the
// command was cut short by its own deadline or by the caller stopping it.
func (p *Proc) Wait() (code int, timedOut, stopped bool, err error) {
	runErr := p.cmd.Wait()
	if p.cancel != nil {
		p.cancel()
	}
	_ = p.pipe.Close()
	<-p.pumped
	if p.spill != nil {
		_ = p.spill.Close()
	}
	timedOut = p.runCtx.Err() == context.DeadlineExceeded && p.parent.Err() == nil
	stopped = p.parent.Err() != nil
	code = 0
	if p.cmd.ProcessState != nil {
		code = p.cmd.ProcessState.ExitCode()
	} else if runErr != nil {
		code = -1
	}
	return code, timedOut, stopped, runErr
}

// Stop ends the command's process group, with SIGKILL two seconds later if it
// is still there.
func (p *Proc) Stop() error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := killGroup(p.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		return err
	}
	time.AfterFunc(2*time.Second, func() { _ = killGroup(p.cmd.Process.Pid, syscall.SIGKILL) })
	return nil
}

// RunShell runs one command in the project root and returns its merged output
// with an exit code line. Each call is a new shell.
func (r *Runner) RunShell(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args RunShellArgs
	if err := decodeArgs(raw, &args); err != nil {
		return Result{}, fmt.Errorf("bash: %s", err)
	}
	p, err := r.start(ctx, args, nil)
	if err != nil {
		return Result{}, err
	}
	code, timedOut, stopped, _ := p.Wait()
	return p.result(r.OutputLimit, code, timedOut, stopped), nil
}

// SpawnShell starts a command and returns without waiting for it. A background
// command carries no deadline. onLine receives each line of output as it
// arrives, and may be nil.
func (r *Runner) SpawnShell(ctx context.Context, args RunShellArgs, onLine func(string)) (*Proc, error) {
	return r.start(ctx, args, onLine)
}

// start builds the command, its process group, the buffer and the spill file,
// and runs it.
func (r *Runner) start(ctx context.Context, args RunShellArgs, onLine func(string)) (*Proc, error) {
	if strings.TrimSpace(args.Command) == "" {
		return nil, errors.New("bash needs a command")
	}
	timeout := r.shellTimeout(args)
	p := &Proc{
		buf:     newLimitBuffer(r.OutputLimit),
		parent:  ctx,
		timeout: timeout,
		pumped:  make(chan struct{}),
		onLine:  onLine,
		keep:    args.Background,
	}
	if args.Background {
		p.runCtx = ctx
	} else {
		p.runCtx, p.cancel = context.WithTimeout(ctx, timeout)
	}
	reader, writer := io.Pipe()
	p.reader, p.pipe = reader, writer
	go p.pump()

	cmd := exec.CommandContext(p.runCtx, r.Shell, "-c", args.Command)
	cmd.Dir = r.Root
	cmd.Env = append(os.Environ(), plainEnv...)
	cmd.Stdin = nil
	cmd.Stdout = writer
	cmd.Stderr = writer
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
	p.cmd = cmd

	p.spill = r.openSpill()
	if p.spill != nil {
		p.buf.spill, p.buf.spillCap = p.spill, spillCap
	}
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		if p.spill != nil {
			_ = p.spill.Close()
			os.Remove(p.spill.Name())
		}
		if p.cancel != nil {
			p.cancel()
		}
		return nil, fmt.Errorf("bash: %w", err)
	}
	return p, nil
}

// result renders what the command printed, in the shape the model reads.
func (p *Proc) result(limit, code int, timedOut, stopped bool) Result {
	out, truncated := p.Output(limit)
	var b strings.Builder
	b.WriteString(out)
	if b.Len() > 0 && !strings.HasSuffix(out, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(keepSpill(p.spill, p.buf, truncated, p.keep))
	if timedOut {
		fmt.Fprintf(&b, "\ncommand timed out after %ds and was killed\n", int(p.timeout/time.Second))
	}
	if stopped {
		b.WriteString("\nstopped\n")
		return Result{Output: strings.TrimRight(b.String(), "\n"), Truncated: truncated, Bytes: p.Total()}
	}
	b.WriteString("\n")
	if code < 0 {
		b.WriteString("exit code: killed by signal " + signalName(p.cmd) + "\n")
	} else {
		fmt.Fprintf(&b, "exit code: %d\n", code)
	}
	exit := code
	return Result{
		Output:    strings.TrimRight(b.String(), "\n"),
		ExitCode:  &exit,
		Truncated: truncated,
		Bytes:     p.Total(),
	}
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

// keepSpill names the saved output, and removes the file when nothing needs it.
// A background command keeps its file whatever its size, since the panel opens
// it and read can reach it by path.
func keepSpill(f *os.File, buf *limitBuffer, truncated, keep bool) string {
	if f == nil {
		return ""
	}
	buf.mu.Lock()
	whole, saved := buf.spill != nil, buf.spilled
	buf.mu.Unlock()
	if !keep && (!truncated || !whole) {
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

// ShellBackground reports whether a call asks to run in the background.
func (r *Runner) ShellBackground(raw json.RawMessage) bool {
	var args RunShellArgs
	if decodeArgs(raw, &args) != nil {
		return false
	}
	return args.Background
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
