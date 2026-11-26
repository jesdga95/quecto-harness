package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrStateOverlap is a project that holds the harness's own state folder or
// lies inside it. Every way of reading a project would then reach the token,
// the allowlist and the sessions, so nothing works on such a project.
var ErrStateOverlap = errors.New("the project overlaps the harness state folder")

// overlapError says which way a project overlaps the state folder, and is
// ErrStateOverlap to errors.Is.
type overlapError struct{ msg string }

func (e overlapError) Error() string        { return e.msg }
func (e overlapError) Is(target error) bool { return target == ErrStateOverlap }

// CheckProject refuses a folder that holds the state folder or lies inside
// it. A folder that cannot be resolved is an error of its own, since it
// proves nothing either way.
func (s *Store) CheckProject(dir string) error {
	home := s.paths.Home
	// The harness reaches its state by the path it was given, so a project
	// holding any name that path goes through, a link or a folder, is one a
	// stash, a discard or a branch switch could cut it off through.
	dirs, err := usedDirs(home)
	if err != nil {
		return fmt.Errorf("Check %s against the harness state folder: %w", dir, err)
	}
	for _, d := range dirs {
		in, err := under(d, dir)
		if err != nil {
			return fmt.Errorf("Check %s against the harness state folder: %w", dir, err)
		}
		if in {
			return overlapError{fmt.Sprintf("%s contains %s, where the harness keeps its state, or a folder on the way to it, so it cannot be a project. Choose a folder that does not", dir, home)}
		}
	}
	in, err := under(dir, home)
	if err != nil {
		return fmt.Errorf("Check %s against the harness state folder: %w", dir, err)
	}
	if in {
		return overlapError{fmt.Sprintf("%s is inside %s, where the harness keeps its state, so it cannot be a project. Choose a folder outside it", dir, home)}
	}
	holds, err := under(home, dir)
	if err != nil {
		return fmt.Errorf("Check %s against the harness state folder: %w", dir, err)
	}
	if holds {
		return overlapError{fmt.Sprintf("%s contains %s, where the harness keeps its state, so it cannot be a project. Choose a folder that does not contain it", dir, home)}
	}
	return nil
}

// InState reports whether a path, its links followed, lands in the state
// folder. A path that cannot be resolved is an error.
func (s *Store) InState(path string) (bool, error) {
	return under(path, s.paths.Home)
}

// usedDirs lists the folders whose entries reaching path goes through, the
// folder holding each name it passes, link or not. It walks the path the way
// the system resolves it: a name at a time, a link replaced by its target,
// and .. taken from the folder reached so far, so no name is cleaned away
// before it is looked at. A walk that fails is an error.
func usedDirs(path string) ([]string, error) {
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		path = wd + string(filepath.Separator) + path
	}
	sep := string(filepath.Separator)
	pending := strings.Split(path, sep)
	cur, hops := sep, 0
	var out []string
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		info, err := os.Lstat(next)
		if err != nil {
			return nil, err
		}
		out = append(out, cur)
		if info.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		if hops++; hops > 255 {
			return nil, fmt.Errorf("resolve %s: too many links", path)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return nil, err
		}
		if filepath.IsAbs(target) {
			cur = sep
		}
		pending = append(strings.Split(target, sep), pending...)
	}
	return out, nil
}

// Under reports whether path, its links followed, is dir or lies inside it,
// comparing folders by identity.
func Under(path, dir string) (bool, error) { return under(path, dir) }

// under reports whether path, its links followed, is dir or lies inside it.
// Folders are compared by identity rather than by name, so a volume that
// ignores case, or a second name for the same folder, cannot hide a match.
func under(path, dir string) (bool, error) {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false, err
	}
	target, err := os.Stat(realDir)
	if err != nil {
		return false, err
	}
	for p := realPath; ; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			return false, err
		}
		if os.SameFile(info, target) {
			return true, nil
		}
		if filepath.Dir(p) == p {
			return false, nil
		}
	}
}
