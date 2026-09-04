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
	rel, err := filepath.Rel(r.Root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", outsideProject(arg)
	}
	return resolved, nil
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

// rel is the path as the model asked for it, relative to the project root.
func (r *Runner) rel(abs string) string {
	if p, err := filepath.Rel(r.Root, abs); err == nil {
		return p
	}
	return abs
}
