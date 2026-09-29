package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// outsideProject is the refusal for every cause, so a probing model learns
// nothing from the difference between them.
func outsideProject(arg string) error {
	return fmt.Errorf("path is outside the project: %s", arg)
}

// resolve turns a tool path argument into an absolute path inside Root, or
// returns the message the model is shown.
func (r *Runner) resolve(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("path contains a null byte")
	}
	arg := p
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.Root, p)
	}
	p = filepath.Clean(p)

	prefix, tail := splitExisting(p)
	real, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve %s: %s", arg, err)
		}
		real = prefix
	}
	for _, part := range strings.Split(tail, string(filepath.Separator)) {
		if part == ".." {
			return "", outsideProject(arg)
		}
	}
	resolved := filepath.Join(real, tail)
	if !within(r.Root, resolved) || (r.Hidden != "" && within(r.Hidden, resolved)) {
		return "", outsideProject(arg)
	}
	return resolved, nil
}

// resolveRead is resolve for the tools that only read, which also reach the
// folder cut-off output is saved to, by the absolute path the result named.
func (r *Runner) resolveRead(p string) (string, error) {
	if r.Spill != "" && filepath.IsAbs(p) && !strings.ContainsRune(p, 0) {
		if real := realPath(filepath.Clean(p)); within(r.Spill, real) {
			return real, nil
		}
	}
	return r.resolve(p)
}

// reachable reports whether a path the walk came to may be read: inside the
// project and outside the state directory, or inside the saved output.
func (r *Runner) reachable(p string) bool {
	if r.Spill != "" && within(r.Spill, p) {
		return true
	}
	return r.Hidden == "" || !within(r.Hidden, p)
}

// realPath follows the links in the part of p that exists.
func realPath(p string) string {
	prefix, tail := splitExisting(p)
	real, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return p
	}
	return filepath.Join(real, tail)
}

// within reports whether p is base or lies under it. Both are clean and
// absolute.
func within(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// splitExisting walks up until a component exists, so a write target under a
// directory that is not there yet still gets its parent symlink-resolved.
func splitExisting(p string) (prefix, tail string) {
	prefix = p
	for {
		if _, err := os.Lstat(prefix); err == nil {
			return prefix, tail
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return prefix, tail
		}
		base := filepath.Base(prefix)
		if tail == "" {
			tail = base
		} else {
			tail = filepath.Join(base, tail)
		}
		prefix = parent
	}
}

// rel is the path as the model asked for it, relative to the project root. A
// path outside the project, which only saved output can be, stays absolute.
func (r *Runner) rel(abs string) string {
	if !within(r.Root, abs) {
		return abs
	}
	if p, err := filepath.Rel(r.Root, abs); err == nil {
		return p
	}
	return abs
}
