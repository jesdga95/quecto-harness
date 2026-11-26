package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// ErrNoScratch is a conversation without a folder whose folder has not been
// made yet, which no turn has needed.
var ErrNoScratch = errors.New("the conversation has no folder yet")

// EnsureScratch makes the folder a conversation without a folder works in,
// readable by the user alone, and returns its path. A name already there has
// to be a folder and not a link to one.
func (s *Sessions) EnsureScratch(id string) (string, error) {
	if err := os.MkdirAll(s.scratch, 0o700); err != nil {
		return "", fmt.Errorf("create the scratch folder %s: %w", s.scratch, err)
	}
	dir, err := s.ScratchDir(id)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("create the conversation folder %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("the conversation folder %s is not a folder", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the conversation folder %s can be read by other users: make it private with chmod 700", dir)
	}
	return dir, nil
}

// OpenScratch opens the folder of a conversation as a root nothing read
// through it can leave. It is opened from the scratch folder and must be the
// folder its name holds, not a link.
func (s *Sessions) OpenScratch(id string) (*os.Root, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("no scratch folder for %q: not a session id", id)
	}
	real, err := filepath.EvalSymlinks(s.scratch)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoScratch
	}
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", s.scratch, err)
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", real, err)
	}
	defer root.Close()
	return descend(root, id)
}

// descend opens the folder name under parent, refusing a link or anything
// that changed between the look and the open.
func descend(parent *os.Root, name string) (*os.Root, error) {
	before, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoScratch
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if !before.IsDir() || before.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is not a folder", name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	after, err := child.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		child.Close()
		return nil, fmt.Errorf("%s changed while it was opened", name)
	}
	return child, nil
}

// ErrDeletePending is a delete that has begun and is not finished: the
// conversation is gone from every listing and the rest is done at the next
// start.
var ErrDeletePending = errors.New("the conversation is deleted, and what is left of it is removed at the next start")

// mark is what a delete leaves in place of a conversation without a folder:
// its document and every run of its family, so finishing the delete reads
// nothing else.
type mark struct {
	Session
	Family []string `json:"family"`
}

// MarkDeleting begins the delete of a conversation without a folder. A mark
// holding owner and the runs named in kids is written and flushed, then the
// conversation's file is removed. A failure before the mark is on disk changes
// nothing; one after it returns ErrDeletePending. From the mark on, neither
// the conversation nor its runs are listed or loaded, and Purge finishes it.
func (s *Sessions) MarkDeleting(owner *Session, kids []string) error {
	id := owner.ID
	if !IsLoose(id) || !ValidID(id) || owner.Parent != "" || checkKind(owner) != nil {
		return fmt.Errorf("Delete session %s: only a conversation without a folder is marked", id)
	}
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()
	if _, err := os.Stat(s.path(id)); errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	family, err := s.familyOf(id, kids)
	if err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	data, err := json.Marshal(mark{Session: *owner, Family: family})
	if err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	path := s.path(id)
	if err := config.WriteAtomicNoSync(path+markSuffix, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	err = syncFile(path + markSuffix)
	if err == nil {
		err = syncDir(s.dir)
	}
	if err != nil {
		// A mark the disk may not keep is taken back and the delete refused.
		// Only a mark still in place, which the next start reads, lets the
		// delete count as begun.
		rmErr := os.Remove(path + markSuffix)
		if rmErr == nil || errors.Is(rmErr, fs.ErrNotExist) {
			if syncDir(s.dir) != nil {
				return fmt.Errorf("Delete session %s: flush %s: %w: nothing was deleted, but the disk may keep the mark, and then the delete finishes at the next start", id, s.dir, err)
			}
			return fmt.Errorf("Delete session %s: flush %s: %w: nothing was deleted", id, s.dir, err)
		}
		s.hide(id, family)
		return fmt.Errorf("Delete session %s: %v: %w", id, err, ErrDeletePending)
	}
	s.hide(id, family)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Delete session %s: %v: %w", id, err, ErrDeletePending)
	}
	return nil
}

// familyOf names every run of the conversation id: those the caller holds,
// those indexed, and any file of a run without a folder the index lacks that
// names it as launcher. A file that cannot be read leaves the family unknown,
// and the delete waits for it.
func (s *Sessions) familyOf(id string, kids []string) ([]string, error) {
	family := slices.Clone(kids)
	s.mu.RLock()
	known := make(map[string]bool, len(s.index))
	for kid, v := range s.index {
		known[kid] = true
		if v.Parent == id && !slices.Contains(family, kid) {
			family = append(family, kid)
		}
	}
	s.mu.RUnlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !IsLoose(name) || !namePattern.MatchString(name) {
			continue
		}
		kid := strings.TrimSuffix(name, ".json")
		if kid == id || known[kid] || slices.Contains(family, kid) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			return nil, fmt.Errorf("%s cannot be read, so the runs of the conversation are not all known: %w", name, err)
		}
		var sess Session
		if err := json.Unmarshal(raw, &sess); err != nil || sess.ID != kid || checkKind(&sess) != nil {
			return nil, fmt.Errorf("%s does not hold the session it is filed under, so the runs of the conversation are not all known", name)
		}
		if sess.Parent == id {
			family = append(family, kid)
		}
	}
	for _, kid := range family {
		if !ValidID(kid) || !IsLoose(kid) || kid == id {
			return nil, fmt.Errorf("%q is not a run without a folder", kid)
		}
	}
	slices.Sort(family)
	return family, nil
}

// hide takes a conversation being deleted and its runs out of the index.
func (s *Sessions) hide(id string, family []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for kid, v := range s.index {
		if v.Parent == id || slices.Contains(family, kid) {
			delete(s.index, kid)
		}
	}
	delete(s.index, id)
	s.dying[id] = family
}

// Marked lists the conversations whose delete has begun and not finished.
func (s *Sessions) Marked() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.dying))
	for id := range s.dying {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// readMarkFile reads the mark a delete left for id. A mark that cannot be read
// or does not hold that conversation is an error, and the delete waits.
func (s *Sessions) readMarkFile(id string) (*mark, error) {
	raw, err := os.ReadFile(s.path(id) + markSuffix)
	if err != nil {
		return nil, err
	}
	var m mark
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("the mark is not valid JSON: %w", err)
	}
	if m.ID != id || checkKind(&m.Session) != nil || !m.Loose || m.Parent != "" {
		return nil, errors.New("the mark does not hold the conversation it is filed under")
	}
	for _, kid := range m.Family {
		if !ValidID(kid) || !IsLoose(kid) || kid == id {
			return nil, fmt.Errorf("the mark names %q, which is not a run without a folder", kid)
		}
	}
	return &m, nil
}

// Purge finishes the delete MarkDeleting began, with what the mark names.
// Each run goes first and the conversation last; each has release called on
// its document, then its folder removed, then its file. The mark goes last,
// so a Purge that stops part way runs again.
func (s *Sessions) Purge(id string, release func(*Session) error) error {
	s.mu.RLock()
	_, ok := s.dying[id]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	m, err := s.readMarkFile(id)
	if err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	// Nothing is removed on a mark the disk may not keep.
	err = syncFile(s.path(id) + markSuffix)
	if err == nil {
		err = syncDir(s.dir)
	}
	if err != nil {
		return fmt.Errorf("Delete session %s: flush the mark: %w", id, err)
	}
	for _, kid := range m.Family {
		if err := s.eraseRun(kid, id, release); err != nil {
			return err
		}
	}
	if err := release(&m.Session); err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	// The mark proves the conversation's own folders and any file a crash
	// left beside them are this conversation's.
	if err := os.RemoveAll(s.filesDir(id)); err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	dir, err := s.ScratchDir(id)
	if err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	if err := os.Remove(s.path(id) + markSuffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	s.mu.Lock()
	delete(s.dying, id)
	delete(s.locks, id)
	delete(s.written, id)
	s.mu.Unlock()
	return nil
}

// eraseRun removes what one run of owner holds: release on its document,
// then its folder, then its file. Only a file that says it is a run without a
// folder of owner proves the folder is the run's, so a run whose file is gone
// leaves its folder alone.
func (s *Sessions) eraseRun(id, owner string, release func(*Session) error) error {
	raw, err := os.ReadFile(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		// A folder no file vouches for is left, and so is the mark.
		_, err := os.Lstat(s.filesDir(id))
		switch {
		case err == nil:
			return fmt.Errorf("Delete session %s: its folder has no file to say whose it is: it is left in place", id)
		case !errors.Is(err, fs.ErrNotExist):
			return fmt.Errorf("Delete session %s: %w", id, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return fmt.Errorf("Delete session %s: %w: the file is not valid JSON", id, err)
	}
	if sess.ID != id || checkKind(&sess) != nil || !sess.Loose || sess.Parent != owner {
		return fmt.Errorf("Delete session %s: the file is not a run of %s: it is left in place", id, owner)
	}
	if err := release(&sess); err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	// A link put in place of the folder is removed as a link.
	if err := os.RemoveAll(s.filesDir(id)); err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	s.mu.Lock()
	delete(s.locks, id)
	delete(s.written, id)
	s.mu.Unlock()
	return nil
}

// syncFile flushes a file already written. It and syncDir are variables so a
// test can make the disk refuse.
var syncFile = func(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// syncDir flushes a folder, so a rename inside it survives a crash.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
