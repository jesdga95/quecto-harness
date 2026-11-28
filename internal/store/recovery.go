package store

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/config"
)

func sameTranscript(a, b os.FileInfo) bool {
	return a != nil && b != nil && a.Mode().IsRegular() && a.Mode() == b.Mode() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && sameChangeTime(a, b)
}

func (s *Sessions) checkTranscript(id string) error {
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()
	path := s.path(id)
	before, _ := os.Stat(path)
	s.mu.RLock()
	previous := s.index[id]
	s.mu.RUnlock()
	if sameTranscript(previous.checked, before) {
		return nil
	}
	summary, err := s.readSummary(id, before)
	if err != nil {
		s.mu.Lock()
		if errors.Is(err, ErrUnreadable) && previous.ID != "" {
			previous.unreadable = true
			previous.checked = nil
			s.index[id] = previous
		} else if errors.Is(err, ErrNotFound) {
			delete(s.index, id)
		}
		s.mu.Unlock()
		return err
	}
	if err := s.relate(&Session{ID: id, Parent: summary.Parent, Project: summary.Project}); err != nil {
		return err
	}
	s.mu.Lock()
	s.index[id] = summary
	s.mu.Unlock()
	return nil
}

func (s *Sessions) readSummary(id string, before os.FileInfo) (Summary, error) {
	if before == nil {
		before, _ = os.Stat(s.path(id))
	}
	sess, size, err := s.readSession(id)
	if err != nil {
		return Summary{}, err
	}
	summary := summarise(sess, size)
	after, _ := os.Stat(s.path(id))
	if sameTranscript(before, after) {
		summary.checked = after
	}
	return summary, nil
}

type failedRemoval struct {
	ID string `json:"id"`
	relation
}

// A failed output removal carries its own order if both ledger copies are lost.
// Output without an order stays available for manual recovery.
func (s *Sessions) recordFailedRemoval(id string) error {
	s.ledMu.Lock()
	r, ok := s.led.Cleanup[id]
	s.ledMu.Unlock()
	if !ok {
		return errors.New("missing cleanup order")
	}
	data, err := json.Marshal(failedRemoval{id, r})
	if err != nil {
		return err
	}
	return config.WriteAtomic(filepath.Join(s.dir, id+".cleanup"), data, 0600)
}

func (s *Sessions) clearFailedRemoval(id string) error {
	err := os.Remove(filepath.Join(s.dir, id+".cleanup"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("move %s aside and retry: %w", filepath.Join(s.dir, id+".cleanup"), err)
	}
	return nil
}

func (s *Sessions) recoverFailedRemovals(entries []os.DirEntry) ledger {
	var l ledger
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".cleanup")
		if !ok || !ValidID(id) || !entry.Type().IsRegular() {
			continue
		}
		data, err := readRecord(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			log.Printf("Read %s: %v: check its saved output in Settings > Projects", entry.Name(), err)
			continue
		}
		var r failedRemoval
		if err := json.Unmarshal(data, &r); err != nil || r.ID != id || (r.Parent != "" && !ValidID(r.Parent)) {
			continue
		}
		if l.Cleanup == nil {
			l.Cleanup = map[string]relation{}
		}
		l.Cleanup[id] = r.relation
	}
	return l
}

// syncRecords commits directory changes before recovery evidence is released.
func (s *Sessions) syncRecords() error {
	dir, err := os.Open(s.dir)
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		return fmt.Errorf("Sync %s: check the sessions volume and retry: %w", s.dir, err)
	}
	return nil
}

func (s *Sessions) preserveMarker(id string) error {
	path := filepath.Join(s.dir, id+".cleanup")
	aside, err := numberedName(path + ".ambiguous")
	if err != nil {
		return err
	}
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("Preserve %s: check permissions and retry: %w", path, err)
	}
	log.Printf("Preserved ambiguous cleanup record at %s", aside)
	return s.syncRecords()
}

// An empty primary can be a cancellation. Only a matching failure marker
// confirms a backup order. Keep the original backup for manual recovery.
func (s *Sessions) reconcileEmptyLedger(path string, entries []os.DirEntry) error {
	backup := path + ".backup"
	l, err := readLedgerCopy(backup)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(l.Cleanup) > 0 || len(l.Parents) > 0 || l.Archiving != "" {
		data, err := readRecord(backup)
		if err != nil {
			return fmt.Errorf("Read backup %s: %w", backup, err)
		}
		if err := preserveDivergent(backup, data, entries); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if rel, recorded := l.Parents[id]; ok && recorded {
			if s.led.Parents == nil {
				s.led.Parents = map[string]relation{}
			}
			s.led.Parents[id] = rel
		}
	}
	s.led.Archiving = l.Archiving
	for id, marker := range s.recoverFailedRemovals(entries).Cleanup {
		if rel, ok := l.Cleanup[id]; ok && rel == marker {
			if s.led.Cleanup == nil {
				s.led.Cleanup = map[string]relation{}
			}
			s.led.Cleanup[id] = rel
		} else if err := s.preserveMarker(id); err != nil {
			return err
		}
	}
	return nil
}

var divergentRecord = regexp.MustCompile(`^ledger\.json\.backup\.divergent-[1-9][0-9]*$`)
var ambiguousMarker = regexp.MustCompile(`^(sess_[a-z2-7]{16})\.cleanup\.ambiguous-[1-9][0-9]*$`)

func preserveDivergent(backup string, data []byte, entries []os.DirEntry) error {
	for _, e := range entries {
		if e.Type().IsRegular() && divergentRecord.MatchString(e.Name()) {
			old, err := os.ReadFile(filepath.Join(filepath.Dir(backup), e.Name()))
			if err == nil && bytes.Equal(old, data) {
				return nil
			}
		}
	}
	saved, err := numberedName(backup + ".divergent")
	if err != nil {
		return err
	}
	if err := config.WriteAtomic(saved, data, 0600); err != nil {
		return fmt.Errorf("Preserve backup %s: %w", backup, err)
	}
	log.Printf("Preserved divergent ledger at %s", saved)
	return nil
}

// RecoveryItem names one retained artifact or a readable run whose launcher is gone.
type RecoveryItem struct {
	Name       string `json:"name"`
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Generation string `json:"generation,omitempty"`
	Repair     string `json:"repair,omitempty"`
}

func retainedItem(name string) (RecoveryItem, bool) {
	if match := archivePattern.FindStringSubmatch(name); match != nil {
		return RecoveryItem{Name: name, ID: match[1], Kind: "archive"}, true
	}
	if id, ok := strings.CutSuffix(name, ".files"); ok && ValidID(id) {
		return RecoveryItem{Name: name, ID: id, Kind: "output"}, true
	}
	return RecoveryItem{}, false
}

var outputMarker = regexp.MustCompile(`^\.recovery-([A-Z2-7]{26,})$`)

// ErrRecoveryConflict requires a fresh confirmation or repair of saved output.
var ErrRecoveryConflict = errors.New("saved output requires inspection")

var errOutputChanged = &fileError{kind: ErrRecoveryConflict, msg: "saved output has changed. Check its contents and confirm deletion again"}

// The marker belongs to this output directory and goes away with it.
// A directory without a marker receives one when first listed for recovery.
func outputGeneration(path string, create bool) (string, error) {
	s, err := readOutputSnapshot(path, create, relation{})
	return s.generation(), err
}

func readOutputSnapshot(path string, create bool, pending relation) (outputSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return outputSnapshot{}, err
	}
	if !info.IsDir() {
		return outputSnapshot{}, errOutputChanged
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return outputSnapshot{}, err
	}
	defer root.Close()
	current, err := root.Stat(".")
	if err != nil {
		return outputSnapshot{}, err
	}
	if !os.SameFile(info, current) {
		return outputSnapshot{}, errOutputChanged
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return outputSnapshot{}, err
	}
	if len(entries) == 0 && pending.OutputGeneration != "" {
		if saved, ok := pending.outputSnapshot(); ok {
			same, err := sameEmptyOutput(root, current, saved)
			if err != nil {
				return outputSnapshot{}, err
			}
			if same {
				return saved, nil
			}
		}
	}
	return snapshotIn(root, current, entries, create)
}

func outputMarkerID(root *os.Root, current os.FileInfo, entries []fs.DirEntry, create bool) (string, error) {
	var markers []string
	var token string
	for _, e := range entries {
		if match := outputMarker.FindStringSubmatch(e.Name()); match != nil && e.Type().IsRegular() {
			markers = append(markers, e.Name())
			token = match[1]
		}
	}
	if len(markers) > 1 {
		return "", &fileError{kind: ErrRecoveryConflict, msg: fmt.Sprintf("multiple recovery markers in %s: move %s out of the directory, then refresh the recovery list", root.Name(), strings.Join(markers, ", "))}
	}
	if token == "" && create {
		token = strings.ToUpper(rand.Text())
		f, err := root.OpenFile(".recovery-"+token, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
	}
	if token == "" {
		return "", nil
	}
	dirID, _, err := outputDirectoryID(root, current)
	if err != nil {
		return "", err
	}
	marker, err := root.Lstat(".recovery-" + token)
	if err != nil {
		return "", err
	}
	if !marker.Mode().IsRegular() {
		return "", errOutputChanged
	}
	if marker.Size() != 0 {
		return "", &fileError{kind: ErrRecoveryConflict, msg: fmt.Sprintf("recovery marker %s contains data: move it out of the directory and check saved output again", filepath.Join(root.Name(), marker.Name()))}
	}
	return token + "." + dirID + "." + markerIdentity(marker), nil
}

func (s *Sessions) RecoveryItems() ([]RecoveryItem, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	present := make(map[string]bool, len(entries))
	for _, entry := range entries {
		present[entry.Name()] = true
	}
	items := []RecoveryItem{}
	for _, entry := range entries {
		name := entry.Name()
		if item, ok := retainedItem(name); ok {
			if item.Kind == "archive" || !present[item.ID+".json"] {
				if item.Kind == "output" {
					lock := s.lockFor(item.ID)
					lock.Lock()
					s.ledMu.Lock()
					pending := s.led.Cleanup[item.ID]
					s.ledMu.Unlock()
					var snapshot outputSnapshot
					snapshot, err = readOutputSnapshot(filepath.Join(s.dir, name), true, pending)
					item.Generation = snapshot.generation()
					lock.Unlock()
					if err != nil {
						item.Kind = "record"
						item.Repair = "Check saved output: " + err.Error()
					}
				}
				items = append(items, item)
			}
			continue
		}
		if divergentRecord.MatchString(name) || ambiguousMarker.MatchString(name) {
			items = append(items, RecoveryItem{Name: name, Kind: "record"})
			continue
		}
		id, ok := strings.CutSuffix(name, ".json")
		if !ok {
			continue
		}
		s.mu.RLock()
		run := s.index[id]
		s.mu.RUnlock()
		if !run.unreadable && run.Parent != "" && !present[run.Parent+".json"] {
			items = append(items, RecoveryItem{Name: name, ID: id, Kind: "run"})
		}
	}
	return items, nil
}

// DeleteRetained removes only the named artifact. A current transcript always
// keeps the output shared with its archived copies.
func (s *Sessions) DeleteRetained(name, generation string) error {
	item, ok := retainedItem(name)
	if !ok {
		return ErrNotFound
	}
	lock := s.lockFor(item.ID)
	lock.Lock()
	defer lock.Unlock()
	path := filepath.Join(s.dir, name)
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if item.Kind == "archive" {
				return errors.New("The archive has changed: refresh the list before deleting it")
			}
			return ErrNotFound
		}
		return fmt.Errorf("Check %s: verify access to the sessions folder: %w", name, err)
	}
	if item.Kind == "archive" {
		s.ledMu.Lock()
		defer s.ledMu.Unlock()
		if s.led.Archiving == name {
			if err := s.clearFailedRemoval(item.ID); err != nil {
				return fmt.Errorf("Finish archiving %s: %w", item.ID, err)
			}
			s.led.Archiving = ""
		}
		// Both copies must reflect the current orders before the archive
		// that witnessed the rename can be removed.
		if err := s.writeLedgerLocked(true); err != nil {
			return fmt.Errorf("Save cleanup records before deleting %s: %w", name, err)
		}
		if err := s.syncRecords(); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("Delete %s: check its permissions and retry: %w", name, err)
		}
		return nil
	}
	if _, err := os.Lstat(s.path(item.ID)); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("Check session %s: verify access to the sessions folder: %w", item.ID, err)
		}
		return fmt.Errorf("Session %s has a transcript again: check transcripts before deleting its output", item.ID)
	}
	s.ledMu.Lock()
	pending := s.led.Cleanup[item.ID]
	s.ledMu.Unlock()
	if generation == "" {
		return fmt.Errorf("Check saved output: %w", errOutputChanged)
	}
	freshOrder := pending.OutputGeneration != generation
	if freshOrder {
		if err := s.orderOutput(item.ID, generation); err != nil {
			return fmt.Errorf("Check saved output: %w", err)
		}
	}
	return s.removeOutput(item.ID, freshOrder)
}

// removeConfirmedOutput keeps the recovery marker until every payload entry
// has gone. An empty directory with the same identity can finish a pending
// removal interrupted after the marker was removed.
func removeConfirmedOutput(path string, order relation, freshOrder bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	saved, ok := order.outputSnapshot()
	if !ok || !info.IsDir() {
		return errOutputChanged
	}
	token, markerID, _ := strings.Cut(saved.identity, ".")
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errOutputChanged
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		same, err := sameEmptyOutput(root, opened, saved)
		if err != nil {
			return err
		}
		if !same {
			return errOutputChanged
		}
	} else {
		id, err := outputMarkerID(root, opened, entries, false)
		if err != nil {
			return err
		}
		if id != saved.identity {
			return errOutputChanged
		}
		approved := saved.approved()
		check := func(digest string) error {
			if !approved[digest] {
				return errOutputChanged
			}
			return nil
		}
		marker := ".recovery-" + token
		// A new order already checked the full tree under the session lock.
		// A retry checks every remaining entry before removing the first one.
		if !freshOrder {
			if err := walkOutputEntries(root, entries, marker, check, false); err != nil {
				return err
			}
		}
		if err := walkOutputEntries(root, entries, marker, check, true); err != nil {
			return err
		}
		info, err := root.Lstat(marker)
		if err != nil {
			return err
		}
		_, stamp, _ := strings.Cut(markerID, ".")
		if !info.Mode().IsRegular() || info.Size() != 0 || markerIdentity(info) != stamp {
			return errOutputChanged
		}
		if err := root.Remove(marker); err != nil {
			return err
		}
	}
	now, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(opened, now) {
		return errOutputChanged
	}
	return removeEmptyOutput(path)
}

func sameEmptyOutput(root *os.Root, info os.FileInfo, saved outputSnapshot) (bool, error) {
	_, rest, _ := strings.Cut(saved.identity, ".")
	dirID, _, _ := strings.Cut(rest, ".")
	id, stable, err := outputDirectoryID(root, info)
	return stable && dirID == id, err
}
