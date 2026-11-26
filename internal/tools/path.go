package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// hiddenError is the refusal for the harness's own state directory, whichever
// way the path reached it.
type hiddenError struct{ arg string }

func (e hiddenError) Error() string {
	return "path is in the harness state folder, which no tool reaches: " + e.arg
}

func hiddenPath(arg string) error { return hiddenError{arg} }

// resolve turns a tool path argument into an absolute path, relative ones
// taken from Root, or returns the message the model is shown. A path outside
// Root resolves; whether it may be used is the permission policy's call.
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
	if r.Scratch != "" && within(r.Scratch, p) {
		return r.resolveScratch(arg, p)
	}

	prefix, tail := splitExisting(p)
	real, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("resolve %s: %s", arg, err)
		}
		real = prefix
	}
	resolved := filepath.Join(real, tail)
	if r.Hidden != "" && within(r.Hidden, resolved) {
		return "", hiddenPath(arg)
	}
	return resolved, nil
}

// scratchPath is the refusal for a path under the scratch folder that leads
// out of it, whichever way it does.
func scratchPath(arg string) error {
	return fmt.Errorf("path leads out of the scratch folder, which no tool follows: %s", arg)
}

// resolveScratch resolves a clean absolute path written under the scratch
// folder. What it leads to has to stay under the folder: a link out of it, to
// the project or anywhere else, and a link that leads nowhere are refused.
func (r *Runner) resolveScratch(arg, p string) (string, error) {
	prefix, tail, err := existingPart(p)
	if err != nil {
		return "", scratchPath(arg)
	}
	real, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		return "", scratchPath(arg)
	}
	resolved := filepath.Join(real, tail)
	if !within(r.Scratch, resolved) {
		return "", scratchPath(arg)
	}
	return resolved, nil
}

// InScratch reports whether a tool path argument is written under the scratch
// folder. It reads the words of the path and follows no link, so the answer
// does not change between the decision and the call; resolve refuses a path
// under the folder that leads out of it.
func (r *Runner) InScratch(p string) bool {
	if r.Scratch == "" || strings.TrimSpace(p) == "" || strings.ContainsRune(p, 0) {
		return false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.Root, p)
	}
	return within(r.Scratch, filepath.Clean(p))
}

// ensureScratch makes the scratch folder and the session folder it sits in,
// readable by the user alone. A name already there has to be a folder and not
// a link to one.
func (r *Runner) ensureScratch() error {
	if r.Scratch == "" {
		if r.ScratchErr != nil {
			return fmt.Errorf("the scratch folder is not available: %s", r.ScratchErr)
		}
		return nil
	}
	for _, dir := range []string{filepath.Dir(r.Scratch), r.Scratch} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("create the scratch folder %s: %s", r.Scratch, err)
		}
		if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
			return fmt.Errorf("the scratch folder %s is not a folder", r.Scratch)
		}
	}
	return nil
}

// resolveInside is resolve for what only the project may be read through,
// such as the browser's file panel.
func (r *Runner) resolveInside(p string) (string, error) {
	abs, err := r.resolve(p)
	if err != nil {
		return "", err
	}
	if !within(r.Root, abs) {
		return "", fmt.Errorf("path is outside the project: %s", p)
	}
	return abs, nil
}

// Outside reports whether a tool path argument lands outside the project, the
// question the permission policy asks before such a call runs. The saved
// output folder and the state folder, which resolve answers for itself, are
// not outside. An empty path means the project.
func (r *Runner) Outside(p string) bool {
	if strings.TrimSpace(p) == "" || strings.ContainsRune(p, 0) {
		return false
	}
	if r.InScratch(p) {
		return false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(r.Root, p)
	}
	real := realPath(filepath.Clean(p))
	switch {
	case within(r.Root, real):
		return false
	case r.Spill != "" && within(r.Spill, real):
		return false
	case r.Hidden != "" && within(r.Hidden, real):
		return false
	}
	return true
}

// resolveRead is resolve for the tools that only read, which also reach the
// folder cut-off output is saved to, by the absolute path the result named.
func (r *Runner) resolveRead(p string) (string, error) {
	if r.Spill != "" && filepath.IsAbs(p) && !strings.ContainsRune(p, 0) && !r.InScratch(p) {
		if real := realPath(filepath.Clean(p)); within(r.Spill, real) {
			return real, nil
		}
	}
	return r.resolve(p)
}

// reachable reports whether a path the walk came to may be read: outside the
// state directory, or inside the saved output.
func (r *Runner) reachable(p string) bool {
	if r.Spill != "" && within(r.Spill, p) {
		return true
	}
	if r.Scratch != "" && within(r.Scratch, p) {
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

// existingPart is splitExisting that walks up only past names that are not
// there, and returns the error for one that cannot be read.
func existingPart(p string) (prefix, tail string, err error) {
	prefix = p
	for {
		_, err := os.Lstat(prefix)
		if err == nil {
			return prefix, tail, nil
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", "", err
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return prefix, tail, nil
		}
		tail = filepath.Join(filepath.Base(prefix), tail)
		prefix = parent
	}
}

// rel is the path as the model asked for it, relative to the project root. A
// path outside the project stays absolute.
func (r *Runner) rel(abs string) string {
	if !within(r.Root, abs) || (r.Scratch != "" && within(r.Scratch, abs)) {
		return abs
	}
	if p, err := filepath.Rel(r.Root, abs); err == nil {
		return p
	}
	return abs
}
