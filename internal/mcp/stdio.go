package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// StdioSpec is a server started as a child process that speaks
// newline-delimited JSON-RPC on stdin and stdout.
type StdioSpec struct {
	Command string
	Args    []string
	// Env is added to the harness's own environment, as KEY=value.
	Env []string
	// Dir is the working directory; empty is the harness's own.
	Dir string
	// Stderr receives what the server logs. Nil discards it.
	Stderr io.Writer
}

type stdio struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	tail  *tail

	in   chan message
	done chan struct{}

	wmu sync.Mutex

	mu     sync.Mutex
	err    error
	closed bool
}

// startStdio starts the process. Its lifetime is not tied to any context: a
// server outlives the turn that started it, and ends on Close.
func startStdio(spec StdioSpec) (*stdio, error) {
	if spec.Command == "" {
		return nil, errors.New("no command is set")
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Env = append(os.Environ(), spec.Env...)
	cmd.Dir = spec.Dir
	newProcessGroup(cmd)
	t := &tail{to: spec.Stderr}
	cmd.Stderr = t
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.Command, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.Command, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.Command, err)
	}
	s := &stdio{cmd: cmd, stdin: stdin, tail: t, in: make(chan message, 64), done: make(chan struct{})}
	go s.read(stdout)
	return s, nil
}

func (s *stdio) read(stdout io.Reader) {
	defer close(s.done)
	defer close(s.in)
	_ = readLines(stdout, func(line []byte) {
		var m message
		if json.Unmarshal(line, &m) != nil || (m.Method == "" && len(m.ID) == 0) {
			return
		}
		s.in <- m
	})
	werr := s.cmd.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.err = ErrClosed
		return
	}
	reason := "the server exited"
	if werr != nil {
		reason += " (" + werr.Error() + ")"
	}
	if line := s.tail.lastLine(); line != "" {
		reason += ": " + line
	}
	s.err = fmt.Errorf("%w: %s", ErrClosed, reason)
}

func (s *stdio) Send(_ context.Context, frame []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.stdin.Write(append(frame, '\n')); err != nil {
		select {
		case <-s.done:
			return s.Err()
		default:
		}
		return fmt.Errorf("write to the server: %w", err)
	}
	return nil
}

func (s *stdio) Recv() <-chan message { return s.in }

func (s *stdio) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close ends the process group: stdin first, then SIGTERM, then SIGKILL two
// seconds later if it is still there.
func (s *stdio) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.done
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.stdin.Close()
	pid := s.cmd.Process.Pid
	_ = killGroup(pid, syscall.SIGTERM)
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		_ = killGroup(pid, syscall.SIGKILL)
		<-s.done
	}
	return nil
}
