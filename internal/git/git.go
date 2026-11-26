// Package git reads and changes a project's repository through the git binary.
// Every path it takes and returns is relative to the project folder, which may
// sit below the top of the repository. Commands run from the top with the
// project's prefix put back on, so a repository larger than the project is
// read only where the project is.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// readTimeout bounds a command that only reads. writeTimeout covers one that
// may run hooks, and remoteTimeout one that talks to another machine.
const (
	readTimeout   = 30 * time.Second
	writeTimeout  = 2 * time.Minute
	remoteTimeout = 3 * time.Minute
)

// ErrMissing is returned when no git binary is on PATH.
var ErrMissing = errors.New("git is not installed")

// ErrNotRepo is returned for a folder that is not inside a repository.
var ErrNotRepo = errors.New("not a git repository")

// Available reports whether a git binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// Repo is a project folder resolved against the repository holding it.
type Repo struct {
	// Dir is the project folder.
	Dir string
	// Top is the top of the working tree.
	Top string
	// Prefix is Dir relative to Top with a trailing slash, or empty when the
	// project is the whole repository.
	Prefix string
}

// Open resolves dir to its repository.
func Open(ctx context.Context, dir string) (*Repo, error) {
	if !Available() {
		return nil, ErrMissing
	}
	top, err := revParse(ctx, dir, "--show-toplevel")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return nil, ErrNotRepo
		}
		return nil, err
	}
	if top == "" {
		return nil, ErrNotRepo
	}
	prefix, err := revParse(ctx, dir, "--show-prefix")
	if err != nil {
		return nil, err
	}
	return &Repo{Dir: dir, Top: top, Prefix: prefix}, nil
}

// revParse asks rev-parse for one value, which may hold any character a
// folder name can, so only the newline git ends it with is taken off.
func revParse(ctx context.Context, dir, flag string) (string, error) {
	out, err := run(ctx, dir, readTimeout, nil, true, "rev-parse", flag)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// Dirs are the folders the repository is kept in: the top of its working
// tree, its git directory and the common one, which a linked worktree keeps
// apart from the tree.
func (r *Repo) Dirs(ctx context.Context) ([]string, error) {
	dirs := []string{r.Top}
	for _, flag := range []string{"--absolute-git-dir", "--git-common-dir"} {
		d, err := revParse(ctx, r.Top, flag)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(d) {
			d = filepath.Join(r.Top, d)
		}
		dirs = append(dirs, d)
	}
	return dirs, nil
}

// Init creates a repository in dir.
func Init(ctx context.Context, dir string) error {
	if !Available() {
		return ErrMissing
	}
	_, err := run(ctx, dir, writeTimeout, nil, false, "init")
	return err
}

// read runs a command that changes nothing, without taking optional locks, so
// a refresh never contends with a commit the agent is making.
func (r *Repo) read(ctx context.Context, args ...string) ([]byte, error) {
	return run(ctx, r.Top, readTimeout, nil, true, args...)
}

func (r *Repo) write(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return run(ctx, r.Top, writeTimeout, stdin, false, args...)
}

func (r *Repo) remote(ctx context.Context, args ...string) ([]byte, error) {
	return run(ctx, r.Top, remoteTimeout, nil, false, args...)
}

// full turns a project path into one relative to the top of the repository.
func (r *Repo) full(path string) string { return r.Prefix + path }

// local turns a repository path into a project path, and reports false for a
// path outside the project.
func (r *Repo) local(path string) (string, bool) {
	if r.Prefix == "" {
		return path, true
	}
	if !strings.HasPrefix(path, r.Prefix) {
		return "", false
	}
	return strings.TrimPrefix(path, r.Prefix), true
}

// scope is the pathspec that limits a command to the project.
func (r *Repo) scope() []string {
	if r.Prefix == "" {
		return nil
	}
	return []string{"--", r.Prefix}
}

func (r *Repo) fulls(paths []string) []string {
	out := make([]string, 0, len(paths)+1)
	out = append(out, "--")
	for _, p := range paths {
		out = append(out, r.full(p))
	}
	return out
}

// inside resolves a project path to a file under the project folder, refusing
// one that climbs out of it.
func (r *Repo) inside(path string) (string, error) {
	abs := filepath.Join(r.Dir, filepath.FromSlash(path))
	rel, err := filepath.Rel(r.Dir, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the project", path)
	}
	return abs, nil
}

// run executes git in dir. A failure carries what git printed, since that is
// the only account of why.
func run(ctx context.Context, dir string, limit time.Duration, stdin []byte, readOnly bool, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	full := append([]string{"-c", "color.ui=false", "-c", "core.quotePath=false", "-c", "advice.detachedHead=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_LITERAL_PATHSPECS=1",
		"GIT_EDITOR=:",
		"GIT_MERGE_AUTOEDIT=no",
		"LC_ALL=C",
	)
	if readOnly {
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	}
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("git %s took longer than %s and was stopped", args[0], limit)
	}
	if err != nil {
		msg := cleanStderr(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.Bytes(), &Error{Op: args[0], Message: msg}
	}
	return stdout.Bytes(), nil
}

// Error is a git command that failed, with what it printed.
type Error struct {
	Op      string
	Message string
}

func (e *Error) Error() string { return "git " + e.Op + ": " + e.Message }

// cleanStderr drops the hint lines git prints for a terminal and keeps the
// lines that say what went wrong.
func cleanStderr(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "hint:") {
			continue
		}
		for _, p := range []string{"fatal: ", "error: "} {
			line = strings.TrimPrefix(line, p)
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}
