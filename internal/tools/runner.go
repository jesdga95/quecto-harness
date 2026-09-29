// Package tools executes read, grep, web_search, write,
// edit and bash. It knows nothing about who approved a call;
// the path guard is its only policy, and web_search has no path to guard,
// since it leaves the project rather than reading it.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"quecto-harness/internal/config"
	"quecto-harness/internal/search"
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

	// Search is the provider web_search reaches, nil where none is configured.
	// SearchHits, SearchChars and SearchTimeout are its limits, read from the
	// same config the offer is computed from, so a tool that is sent and a
	// tool that is built cannot disagree about them.
	Search        search.Provider
	SearchHits    int
	SearchChars   int
	SearchTimeout time.Duration

	// Baseline receives each written file as it stood before the write, when
	// something is keeping them. Nil runs the tools with nothing recorded.
	Baseline func(Snapshot)

	// Seen is what the session has read, which write and edit check a file
	// against. Nil runs the tools with no such check.
	Seen *Seen

	// Hidden is the harness's own state directory, which no tool reaches even
	// when the project contains it: it holds the token and the allowlist.
	Hidden string

	// Spill is the folder where a command whose output was cut saves all of
	// it. read and grep reach it by absolute path; empty saves nothing.
	Spill string
}

// Hide sets the directory no tool may reach, resolved the way Root is.
func (r *Runner) Hide(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		r.Hidden = realPath(abs)
	}
}

// SpillTo sets the folder cut-off output is saved to. It may not exist yet;
// the first command that needs it creates it.
func (r *Runner) SpillTo(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		r.Spill = realPath(abs)
	}
}

// Result is what the model is told. Output is already truncated and, for
// bash, already carries the exit code line.
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
	searchTimeout := cfg.Search.Timeout
	if searchTimeout <= 0 {
		searchTimeout = config.DefaultSearchTimeout
	}
	// The ask never exceeds what the output limit can carry: the endpoint
	// bills what it is asked for, and the result is cut to the limit after.
	return &Runner{
		Root:          real,
		Shell:         shell,
		Timeout:       timeout,
		MaxTimeout:    max,
		OutputLimit:   limit,
		ReadLimit:     64 << 10,
		ReadLines:     400,
		SearchHits:    cfg.Search.MaxHits,
		SearchChars:   min(cfg.Search.MaxChars, limit),
		SearchTimeout: searchTimeout,
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
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
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
