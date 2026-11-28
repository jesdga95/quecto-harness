package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrStateOverlap is a project inside the harness state folder, or a
// repository that holds it, which Git is not run on.
var ErrStateOverlap = errors.New("the project overlaps the harness state folder")

// ErrProjectGone is a project whose folder is no longer there.
var ErrProjectGone = errors.New("the project folder is gone")

// gone reports a project folder that no longer exists as ErrProjectGone.
func gone(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) || (err == nil && !info.IsDir()) {
		return ErrProjectGone
	}
	return nil
}

// overlapError says which way a project overlaps the state folder, and is
// ErrStateOverlap to errors.Is.
type overlapError struct{ msg string }

func (e overlapError) Error() string        { return e.msg }
func (e overlapError) Is(target error) bool { return target == ErrStateOverlap }

// CheckProject refuses a project inside the state folder. A project that
// holds it is allowed, since the file tools never reach the state. A folder
// that cannot be resolved is an error.
func (s *Store) CheckProject(dir string) error {
	if err := gone(dir); err != nil {
		return err
	}
	home := s.paths.Home
	in, err := Under(dir, home)
	if err != nil {
		return fmt.Errorf("Check %s against the harness state folder: %w", dir, err)
	}
	if in {
		return overlapError{fmt.Sprintf("%s is inside %s, where the harness keeps its state, so it cannot be a project. Choose a folder outside it", dir, home)}
	}
	return nil
}

// CheckRepo refuses Git on a repository that holds the state folder, or any
// folder or link on the way to it, since a stash, discard or branch switch
// reaches the whole tree. It also applies CheckProject.
func (s *Store) CheckRepo(dir string) error {
	if err := gone(dir); err != nil {
		return err
	}
	home := s.paths.Home
	dirs, err := usedDirs(home)
	if err != nil {
		return fmt.Errorf("Check %s against the harness state folder: %w", dir, err)
	}
	for _, d := range dirs {
		in, err := Under(d, dir)
		if err != nil {
			return fmt.Errorf("Check %s against the harness state folder: %w", dir, err)
		}
		if in {
			return overlapError{fmt.Sprintf("The repository at %s contains %s, where the harness keeps its state, or a folder on the way to it, so Git is not run on it here", dir, home)}
		}
	}
	return s.CheckProject(dir)
}

// InState reports whether a path, its links followed, lands in the state
// folder. A path that cannot be resolved is an error.
func (s *Store) InState(path string) (bool, error) {
	return Under(path, s.paths.Home)
}

// usedDirs lists the folders that reaching path goes through, walked a name at
// a time as the system resolves it, each link replaced by its target and ..
// taken from the folder reached so far. A walk that fails is an error.
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

// Under reports whether path, its links followed, is dir or lies inside it.
// Folders are compared by identity rather than by name, so a volume that
// ignores case, or a second name for the same folder, cannot hide a match.
func Under(path, dir string) (bool, error) {
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
