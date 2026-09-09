// Package tools executes read_file, find, write_file, replace_in_file and
// run_shell against one project root. It knows nothing about who approved a
// call; its only policy is the path guard, which is a correctness property of
// the tool.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"quecto-harness/internal/config"
)

// Runner executes the file and shell tools against one project root. All paths are
// resolved against Root and may not leave it.
type Runner struct {
	Root        string
	Shell       string
	Timeout     time.Duration
	MaxTimeout  time.Duration
	OutputLimit int
	ReadLimit   int
	ReadLines   int

	// Baseline receives each written file as it stood before the write, when
	// something is keeping them. Nil runs the tools with nothing recorded.
	Baseline func(Snapshot)
}

// Result is what the model is told. Output is already truncated and, for
// run_shell, already carries the exit code line.
type Result struct {
	Output    string
	ExitCode  *int
	Truncated bool
	Bytes     int
	// Meta is what the transcript needs about the call that the sentence the
	// model reads does not carry.
	Meta map[string]any
}

// NewRunner resolves the root once, which is what makes the path guard work
// under a symlinked directory such as /tmp on macOS.
func NewRunner(root string, cfg config.Config) (*Runner, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("open project %s: no such folder, or it cannot be read", root)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return nil, fmt.Errorf("resolve project %s: %w", root, err)
	}
	timeout := cfg.CommandTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	max := 4 * timeout
	if max < 600*time.Second {
		max = 600 * time.Second
	}
	limit := cfg.OutputLimitBytes
	if limit <= 0 {
		limit = 16384
	}
	shell := cfg.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	return &Runner{
		Root:        real,
		Shell:       shell,
		Timeout:     timeout,
		MaxTimeout:  max,
		OutputLimit: limit,
		ReadLimit:   64 << 10,
		ReadLines:   400,
	}, nil
}

type ReadFileArgs struct {
	Path   string `json:"path"`
	Offset *int   `json:"offset"`
	Limit  *int   `json:"limit"`
}

type WriteFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type ReplaceInFileArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

type FindArgs struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Text  string `json:"text"`
	Regex bool   `json:"regex"`
}

type RunShellArgs struct {
	Command  string `json:"command"`
	TimeoutS *int   `json:"timeout_s"`
}

// decodeArgs is lenient about unknown fields, since a small model routinely
// invents one and the call should still run.
func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return errors.New("the arguments were empty")
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("the arguments are not valid JSON: %s", err)
	}
	return nil
}
