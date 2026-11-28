package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// Reasons a removal order gives. A folder whose conversation was deleted goes
// at the next chance; one whose files were all copied out waits sweepFull
// from the copy.
const (
	RemoveDeleted = "deleted"
	RemoveCopied  = "copied"
)

// sweepFull is how long a folder whose files were all copied stays after the
// copy, for a change of mind.
const sweepFull = 7 * 24 * time.Hour

// ScratchOwner is written when a scratch folder is made: the folder, the
// session it was made for, its identity then, and the session store.
type ScratchOwner struct {
	Folder  string    `json:"folder"`
	Session string    `json:"session"`
	ID      string    `json:"id"`
	Store   string    `json:"store"`
	Made    time.Time `json:"made"`
	Kept    string    `json:"kept,omitempty"`
}

// RemovalOrder is leave to move a folder to the trash, bound to its owner
// record. Nothing under the folder may change after At. Aside names a private
// folder a removal cut short left it under, which goes back under its name.
type RemovalOrder struct {
	Folder  string    `json:"folder"`
	Session string    `json:"session"`
	ID      string    `json:"id"`
	Store   string    `json:"store"`
	Reason  string    `json:"reason"`
	At      time.Time `json:"at"`
	Aside   string    `json:"aside,omitempty"`
	// To is the folder a copy put the files in, for an order after a copy.
	To string `json:"to,omitempty"`
}

func (o RemovalOrder) complete() bool {
	return o.Folder != "" && o.Session != "" && o.ID != "" && o.Store != "" && o.Reason != "" && !o.At.IsZero()
}

// ScratchLedger keeps the owner records and removal orders of the scratch
// folders in root, in dir under the state home, which the file tools do not
// reach. store is the identity of the session store the records belong to.
type ScratchLedger struct {
	dir, root, store string
	// mu lets one removal run at a time, from the order read to the records
	// forgotten.
	mu sync.Mutex
}

// NewScratchLedger returns the ledger for the scratch folders in root.
func NewScratchLedger(dir, root, store string) *ScratchLedger {
	return &ScratchLedger{dir: dir, root: root, store: store}
}

func (l *ScratchLedger) ownerPath(folder string) string {
	return filepath.Join(l.dir, filepath.Base(folder)+".owner.json")
}

func (l *ScratchLedger) orderPath(folder string) string {
	return filepath.Join(l.dir, filepath.Base(folder)+".remove.json")
}

// Temps is the file listing the names a move out of folder makes in the
// folder it moves to, which a copy cut short clears on the next try.
func (l *ScratchLedger) Temps(folder string) string {
	return filepath.Join(l.dir, filepath.Base(folder)+".carry")
}

// Own records that folder, as info shows it, was made for session.
func (l *ScratchLedger) Own(folder, session string, info os.FileInfo) error {
	if !scratchName.MatchString(filepath.Base(folder)) {
		return fmt.Errorf("%s is not a scratch folder", folder)
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", l.dir, err)
	}
	return writeJSON(l.ownerPath(folder), ScratchOwner{
		Folder: folder, Session: session, ID: FileID(info), Store: l.store, Made: time.Now().UTC(),
	})
}

// ErrOrderNotWritten is an order the disk refused, for a folder whose owner
// record allows it.
var ErrOrderNotWritten = errors.New("the removal order could not be written")

// Order gives the sweep leave to remove folder, for session, which must be
// the one its owner record names. to is where a copy put its files, if one
// did. A folder with no owner record gets no order.
func (l *ScratchLedger) Order(folder, session, reason, to string, at time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	owner, err := l.owner(folder)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		return fmt.Errorf("%w: read %s: %w", ErrOrderNotWritten, l.ownerPath(folder), err)
	}
	if owner.Session != session {
		return fmt.Errorf("%s was made for session %s, not %s", folder, owner.Session, session)
	}
	if owner.Kept != "" {
		owner.Kept = ""
		if err := writeJSON(l.ownerPath(folder), owner); err != nil {
			return fmt.Errorf("%w: %w", ErrOrderNotWritten, err)
		}
	}
	err = writeJSON(l.orderPath(folder), RemovalOrder{
		Folder: folder, Session: session, ID: owner.ID, Store: owner.Store, Reason: reason, At: at.UTC(), To: to,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrOrderNotWritten, err)
	}
	l.clearTrashFailure(folder)
	return nil
}

// Withdraw takes back a removal order, so the folder is kept.
func (l *ScratchLedger) Withdraw(folder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.withdraw(folder)
}

// withdraw is Withdraw for a caller holding l.mu.
func (l *ScratchLedger) withdraw(folder string) error {
	if owner, err := l.owner(folder); err == nil && owner.Store == l.store && owner.Kept != "" {
		owner.Kept = ""
		if err := writeJSON(l.ownerPath(folder), owner); err != nil {
			return err
		}
	}
	if err := os.Remove(l.orderPath(folder)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	l.clearTrashFailure(folder)
	return nil
}

// Forget removes both records of a folder that is gone.
func (l *ScratchLedger) Forget(folder string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forget(folder)
}

// forget is Forget for a caller holding l.mu.
func (l *ScratchLedger) forget(folder string) {
	if l.withdraw(folder) == nil {
		os.Remove(l.ownerPath(folder))
	}
}

// bound reads the owner record and removal order of folder, and reports why
// the order does not hold for it when it does not.
func (l *ScratchLedger) bound(folder string) (ScratchOwner, RemovalOrder, error) {
	var order RemovalOrder
	owner, err := l.owner(folder)
	if err != nil {
		return owner, order, fmt.Errorf("no owner record: %w", err)
	}
	if err := readJSON(l.orderPath(folder), &order); err != nil {
		return owner, order, fmt.Errorf("no removal order: %w", err)
	}
	switch {
	case l.store == "" || owner.ID == "":
		err = errors.New("the folder's identity is not known")
	case order.Folder != owner.Folder || order.Session != owner.Session || order.ID != owner.ID:
		err = errors.New("the removal order is not bound to the owner record")
	case order.Store != l.store || owner.Store != l.store:
		err = errors.New("the records belong to another session store")
	}
	return owner, order, err
}

// Owns reports whether folder may be ordered removed for session: its owner
// record names that session and this store, with an identity.
func (l *ScratchLedger) Owns(folder, session string) bool {
	owner, err := l.owner(folder)
	return err == nil && owner.Session == session && l.store != "" && owner.Store == l.store && owner.matchesFolder(folder)
}

// Missing reports whether folder is not on disk and no removal has it under
// a private name, so there is nothing of it to keep.
func (l *ScratchLedger) Missing(folder string) bool {
	if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	var order RemovalOrder
	orderErr := readJSON(l.orderPath(folder), &order)
	if orderErr != nil && !errors.Is(orderErr, fs.ErrNotExist) || order.Aside != "" {
		return false
	}
	var record trashFailureRecord
	if err := readJSON(l.failurePath(folder), &record); err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	if record.Order.Folder == "" || record.Failure.Path == "" {
		return false
	}
	if record.Order.Folder == folder && record.Order.Store == l.store && record.Failure.Waiting && record.Failure.Trash != "" {
		return orderErr == nil && order.complete() && record.Order != order
	}
	return true
}

// ForgetMissing releases the session's own records once its source is gone.
func (l *ScratchLedger) ForgetMissing(folder, session string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.Missing(folder) {
		return
	}
	owner, err := l.owner(folder)
	if err == nil && owner.Store == l.store && owner.Session == session {
		l.forget(folder)
	}
}

func (o ScratchOwner) matchesFolder(folder string) bool {
	info, err := os.Lstat(folder)
	return err == nil && info.IsDir() && o.ID != "" && FileID(info) == o.ID
}

// KeptCopy names the source that will remain after deletion. A bound folder
// under a private name returns to its original location if that name is free.
func (l *ScratchLedger) KeptCopy(folder string, hold bool) string {
	for {
		path, order := l.copyToCheck(folder, hold)
		if order.Reason == "" {
			return path
		}
		matches := order.stableCopy(path, sameCopy)
		// The directory or its order may have changed during the read.
		current, latest := l.copyToCheck(folder, hold)
		if latest.Reason == "" {
			return current
		}
		if current != path || latest != order {
			continue
		}
		if !matches {
			return path
		}
		return ""
	}
}

// stableCopy retries once if a file changes during the comparison. A copy
// that keeps changing leaves the source on disk.
func (order RemovalOrder) stableCopy(path string, compare func(string, string) error) bool {
	before := order.copyVersions(path)
	for range 2 {
		if before == nil || compare(path, order.To) != nil {
			return false
		}
		after := order.copyVersions(path)
		if slices.Equal(before, after) {
			return true
		}
		before = after
	}
	return false
}

type copyVersion struct {
	name, sourceID, destinationID     string
	sourceChanged, destinationChanged time.Time
}

// copyVersions reads only destination paths named by the source. The project
// can contain unrelated directories that are larger than the copy.
func (order RemovalOrder) copyVersions(path string) []copyVersion {
	from, err := os.OpenRoot(path)
	if err != nil {
		return nil
	}
	defer from.Close()
	to, err := os.OpenRoot(order.To)
	if err != nil {
		return nil
	}
	defer to.Close()
	var versions []copyVersion
	err = fs.WalkDir(from.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		a, err := entry.Info()
		if err != nil {
			return err
		}
		source, ok := changeTime(a)
		if !ok || (path == order.Folder && source.After(order.At)) {
			return fs.ErrInvalid
		}
		b, err := to.Lstat(name)
		if err != nil {
			return err
		}
		dest, ok := changeTime(b)
		if !ok {
			return fs.ErrInvalid
		}
		versions = append(versions, copyVersion{name, FileID(a), FileID(b), source, dest})
		return nil
	})
	if err != nil {
		return nil
	}
	return versions
}

// copyToCheck returns the source and its current order. An empty order means
// the path can be reported without comparing any files.
func (l *ScratchLedger) copyToCheck(folder string, hold bool) (string, RemovalOrder) {
	owner, order, err := l.bound(folder)
	if _, statErr := os.Lstat(folder); errors.Is(statErr, os.ErrNotExist) {
		if err != nil || !removalName.MatchString(order.Aside) || !inRoot(l.root, folder) {
			return "", RemovalOrder{}
		}
		if owner.matchesFolder(filepath.Join(filepath.Dir(folder), order.Aside)) {
			return folder, RemovalOrder{}
		}
		return "", RemovalOrder{}
	}
	if hold || err != nil || order.Reason != RemoveCopied || !owner.matchesFolder(folder) {
		return folder, RemovalOrder{}
	}
	return folder, order
}

// errChangedSinceOrder is a folder changed after its removal was ordered,
// which no later check can undo.
var errChangedSinceOrder = errors.New("it changed after its removal was ordered")

// checkSource verifies that the source is unchanged and its copy still exists.
func (o RemovalOrder) checkSource(folder string) error {
	latest, ok := newest(folder)
	if !ok {
		return errors.New("part of it could not be read, so it is checked again later")
	}
	if latest.After(o.At) {
		return errChangedSinceOrder
	}
	if o.Reason == RemoveCopied {
		if err := sameCopy(folder, o.To); err != nil {
			return fmt.Errorf("its copy in %s no longer matches it, so it stays until they match again: %v", o.To, err)
		}
	}
	return nil
}

// changedReason says what a change after the order of o came after: the copy
// for an order a copy wrote, which takes the time the copy began, or the
// delete of the conversation.
func (o RemovalOrder) changedReason() string {
	if o.Reason == RemoveCopied {
		return "it changed after the copy of its files began"
	}
	return "it changed after its conversation was deleted"
}

// Keep cancels only the order belonging to this session and folder.
func (l *ScratchLedger) Keep(folder, session string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var order RemovalOrder
	err := readJSON(l.orderPath(folder), &order)
	if errors.Is(err, os.ErrNotExist) {
		// A reason an earlier order left gives way to the keep asked for now.
		if owner, err := l.owner(folder); err == nil && owner.Store == l.store && l.store != "" && owner.Session == session && owner.Kept != "" {
			return l.withdraw(folder)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if order.Folder != folder || order.Session != session {
		return errors.New("the removal order belongs to another folder or session")
	}
	if order.Aside != "" {
		owner, bound, err := l.bound(folder)
		if err != nil {
			return err
		}
		now := time.Now()
		if _, done, err := l.checkUnchecked(owner, bound, now); done {
			return err
		}
		if _, done, err := l.finishAside(owner, bound, now); done {
			if errors.Is(err, errFolderRestored) {
				return nil
			}
			return err
		}
	}
	return l.withdraw(folder)
}

func (l *ScratchLedger) owner(folder string) (ScratchOwner, error) {
	var o ScratchOwner
	err := readJSON(l.ownerPath(folder), &o)
	if err == nil && o.Folder != folder {
		err = fmt.Errorf("the owner record of %s names %s", folder, o.Folder)
	}
	return o, err
}

// Remove trashes the recorded folder after its wait if it is unchanged and
// any required copy still matches. It returns the destination.
func (l *ScratchLedger) Remove(folder string, now time.Time) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	owner, order, err := l.bound(folder)
	if err != nil {
		return "", err
	}
	switch order.Reason {
	case RemoveDeleted:
	case RemoveCopied:
		if now.Sub(order.At) < sweepFull {
			return "", errors.New("the wait after the copy is not over")
		}
	default:
		return "", fmt.Errorf("the removal order gives no known reason")
	}
	if trash, done, err := l.checkUnchecked(owner, order, now); done {
		return trash, err
	}
	if order.Aside != "" {
		if trash, done, err := l.finishAside(owner, order, now); done {
			var aside *AsideMoveError
			var unchecked *UncheckedMove
			if errors.As(err, &unchecked) {
				return "", l.recordUnchecked(order, unchecked)
			}
			if errors.As(err, &aside) {
				return "", l.recordTrashFailure(order, aside.path, aside.err)
			}
			return trash, err
		}
	}
	info, err := os.Lstat(folder)
	if errors.Is(err, os.ErrNotExist) {
		if !l.Missing(folder) {
			return "", errors.New("its trash notice could not be read, so its records are kept")
		}
		l.forget(folder)
		return "", err
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || FileID(info) != owner.ID {
		return "", errors.New("the folder is not the one that was made")
	}
	if err := order.checkSource(folder); err != nil {
		if errors.Is(err, errChangedSinceOrder) {
			return "", l.settleKept(owner, order, folder, errors.New(order.changedReason()))
		}
		return "", l.recordWaiting(order, folder, err)
	}
	if _, latest, err := l.bound(folder); err != nil || latest != order {
		return "", errors.New("the removal order changed during the check")
	}
	beforeTrash(folder)
	trash, err := trashScratch(l.root, folder, owner.ID, l.prepareTrash(order))
	if err != nil {
		var unchecked *UncheckedMove
		if errors.As(err, &unchecked) {
			return "", l.recordUnchecked(order, unchecked)
		}
		if errors.Is(err, errNotTheFolder) {
			l.withdraw(folder)
		} else if l.root != "" && inRoot(l.root, folder) && scratchName.MatchString(filepath.Base(folder)) && owner.matchesFolder(folder) {
			return "", l.recordTrashFailure(order, folder, err)
		}
		return "", err
	}
	return l.trashed(folder, folder, trash, now)
}

// TrashFailure describes the last refused move of a folder with an active
// order, or a folder whose order was settled by keeping it.
type TrashFailure struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Kept   bool   `json:"kept,omitempty"`
	// Record is a damaged record of the folder that holds it until repaired.
	Record string `json:"record,omitempty"`
	// Trash is where a move put the folder when that could not be checked,
	// or, when Foreign, the other entry it moved by mistake.
	Trash   string `json:"trash,omitempty"`
	Waiting bool   `json:"waiting,omitempty"`
	Foreign bool   `json:"foreign,omitempty"`
	// File is a notice of a file a copy left in a project, not a folder.
	File bool `json:"file,omitempty"`
}

type failedTrashMove struct {
	failure TrashFailure
	cause   error
}

func (e *failedTrashMove) Error() string { return e.cause.Error() }
func (e *failedTrashMove) Unwrap() error { return e.cause }

// RetainedFolder returns the outcome of an attempted move that left the folder in place.
func RetainedFolder(err error) *TrashFailure {
	var move *failedTrashMove
	if errors.As(err, &move) {
		return &move.failure
	}
	return nil
}

const trashFailureSuffix = ".trash-failure.json"

type trashFailureRecord struct {
	ExpectedID string       `json:"expected_id,omitempty"`
	ForeignID  string       `json:"foreign_id,omitempty"`
	Order      RemovalOrder `json:"order"`
	Failure    TrashFailure `json:"failure"`
}

func (l *ScratchLedger) failurePath(folder string) string {
	return filepath.Join(l.dir, filepath.Base(folder)+trashFailureSuffix)
}

func (l *ScratchLedger) clearTrashFailure(folder string) {
	if err := os.Remove(l.failurePath(folder)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("Could not clear the trash status for %s: %v", folder, err)
	}
}

func (l *ScratchLedger) recordTrashFailure(order RemovalOrder, path string, err error) error {
	failure := TrashFailure{Path: path, Reason: err.Error()}
	if writeErr := writeJSON(l.failurePath(order.Folder), trashFailureRecord{Order: order, Failure: failure}); writeErr != nil {
		log.Printf("Could not record the refused trash move for %s: %v", path, writeErr)
	}
	return &failedTrashMove{failure: failure, cause: err}
}

// recordWaiting records why a folder whose order still holds was not moved
// this time, for as long as that order waits.
func (l *ScratchLedger) recordWaiting(order RemovalOrder, folder string, cause error) error {
	failure := TrashFailure{Path: folder, Reason: cause.Error(), Waiting: true}
	if err := writeJSON(l.failurePath(folder), trashFailureRecord{Order: order, Failure: failure}); err != nil {
		log.Printf("Could not record why %s was not moved: %v", folder, err)
	}
	return &failedTrashMove{failure: failure, cause: cause}
}

// prepareTrash saves both names before a rename can make the source disappear.
func (l *ScratchLedger) prepareTrash(order RemovalOrder) func(string, string) error {
	return func(path, trash string) error {
		failure := TrashFailure{Path: path, Trash: trash, Waiting: true, Reason: "its entry in the trash has not been read back yet"}
		return writeJSON(l.failurePath(order.Folder), trashFailureRecord{Order: order, Failure: failure})
	}
}

// recordUnchecked records a move into the trash whose result is not known.
// The order stays for the next removal to check, and another entry moved by
// mistake gets its own notice while it is in the trash.
func (l *ScratchLedger) recordUnchecked(order RemovalOrder, move *UncheckedMove) error {
	failure := TrashFailure{Path: move.Path, Trash: move.Trash, Reason: move.Error()}
	record, path := trashFailureRecord{Order: order, Failure: failure}, l.failurePath(order.Folder)
	if move.Foreign {
		record = foreignRecord(order, move)
		path = record.foreignPath(l.dir)
	} else {
		record.Failure.Waiting = true
		record.Failure.Reason = fmt.Sprintf("the entry at the recorded trash path could not be checked (%v)", move.Err)
	}
	if err := writeJSON(path, record); err != nil {
		log.Printf("Could not record the move of %s to %s: %v", move.Path, move.Trash, err)
	} else if move.Foreign {
		l.clearTrashFailure(order.Folder)
	}
	return &failedTrashMove{failure: record.Failure, cause: move}
}

const foreignSuffix = ".foreign.json"

func foreignRecord(order RemovalOrder, move *UncheckedMove) trashFailureRecord {
	return trashFailureRecord{Order: order, ForeignID: move.observedID, Failure: TrashFailure{
		Path: move.Path, Trash: move.Trash, Reason: move.Error(), Foreign: true,
	}}
}

func (r trashFailureRecord) foreignPath(dir string) string {
	key := sha256.Sum256([]byte(r.Failure.Trash + "\x00" + r.ForeignID))
	return filepath.Join(dir, fmt.Sprintf("%s.%x%s", filepath.Base(r.Order.Folder), key[:12], foreignSuffix))
}

const copyTrashSuffix = ".copy-trash.json"

// The process holding the state directory registers each notice before it is
// written. A sweep may settle it after the writer finishes or the process exits.
var activeCopyMoves sync.Map

// trashCopyFile records both paths before an interrupted copy's file leaves
// the project. Its notice outlives the copy journal until the move is verified.
func trashCopyFile(journal, path, id, origin string) (string, error) {
	base, ok := strings.CutSuffix(filepath.Base(journal), ".carry")
	if !ok || !scratchName.MatchString(base) {
		return trashEntry(path, id, origin, false, nil)
	}
	dir := filepath.Dir(journal)
	var owner ScratchOwner
	if err := readJSON(filepath.Join(dir, base+".owner.json"), &owner); err != nil || filepath.Base(owner.Folder) != base || owner.Store == "" {
		// Without an owner no notice can be tied to the folder; the file
		// still leaves the project for the trash rather than stay.
		log.Printf("Moving %s to the trash with no notice: the copy journal has no matching owner", path)
		return trashEntry(path, id, origin, false, nil)
	}
	noticeName := base + "." + randomHex(12) + copyTrashSuffix
	activeCopyMoves.Store(noticeName, struct{}{})
	defer activeCopyMoves.Delete(noticeName)
	notice := filepath.Join(dir, noticeName)
	record := trashFailureRecord{
		Order: RemovalOrder{Folder: owner.Folder, Store: owner.Store}, ExpectedID: id,
		Failure: TrashFailure{Path: path, Waiting: true, Reason: "its entry in the trash has not been read back yet"},
	}
	dest, err := trashEntry(path, id, origin, false, func(_, trash string) error {
		record.Failure.Trash = trash
		return writeJSON(notice, record)
	})
	var move *UncheckedMove
	if errors.As(err, &move) {
		if move.Foreign {
			record = foreignRecord(record.Order, move)
			return "", errors.Join(err, writeJSON(notice, record))
		}
		return "", err
	}
	if removeErr := os.Remove(notice); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
		log.Printf("Could not clear the trash status for %s: %v", path, removeErr)
	}
	return dest, err
}

// entryNoticeBase accepts per-entry notices and older per-folder notices.
func entryNoticeBase(name string) string {
	base, _, _ := strings.Cut(name, ".")
	if !scratchName.MatchString(base) || !(strings.HasSuffix(name, foreignSuffix) || strings.HasSuffix(name, copyTrashSuffix)) {
		return ""
	}
	return base
}

// checkUnchecked looks again at a folder an earlier move put in the trash
// without being able to check it. done is false when there is none, or when
// the trash no longer holds anything under that name.
func (l *ScratchLedger) checkUnchecked(owner ScratchOwner, order RemovalOrder, now time.Time) (trash string, done bool, err error) {
	var record trashFailureRecord
	path := l.failurePath(order.Folder)
	err = readJSON(path, &record)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil || record.Order.Folder == "" || record.Failure.Path == "" {
		cause := fmt.Errorf("inspect %s before retrying the move", path)
		failure := TrashFailure{Path: order.Folder, Record: path, Waiting: true, Reason: "its trash notice could not be read"}
		return "", true, &failedTrashMove{failure: failure, cause: errors.Join(cause, err)}
	}
	if record.Order != order || !record.Failure.Waiting || record.Failure.Trash == "" {
		return "", false, nil
	}
	info, err := lstatMoved(record.Failure.Trash)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		l.clearTrashFailure(order.Folder)
		return "", false, nil
	case err != nil:
		return "", true, &failedTrashMove{failure: record.Failure, cause: err}
	case !isEntry(info, owner.ID, true):
		if owner.matchesFolder(record.Failure.Path) {
			l.clearTrashFailure(order.Folder)
			return "", false, nil
		}
		cause := errors.New("another entry is now at the recorded trash path")
		if record.Failure.Reason != cause.Error() {
			record.Failure.Reason = cause.Error()
			if err := writeJSON(l.failurePath(order.Folder), record); err != nil {
				return "", true, errors.Join(cause, err)
			}
		}
		return "", true, &failedTrashMove{failure: record.Failure, cause: cause}
	}
	trash, err = l.trashed(order.Folder, record.Failure.Path, record.Failure.Trash, now)
	return trash, true, err
}

// settleKept withdraws an order that can no longer hold for folder and
// records why the folder is kept, for as long as its owner record stands.
func (l *ScratchLedger) settleKept(owner ScratchOwner, order RemovalOrder, folder string, cause error) error {
	failure := TrashFailure{Path: folder, Reason: cause.Error(), Kept: true}
	owner.Kept = failure.Reason
	if err := writeJSON(l.failurePath(folder), trashFailureRecord{Order: order, Failure: failure}); err != nil {
		cause = errors.Join(cause, fmt.Errorf("record why %s is kept: %w", folder, err))
	} else if err := writeJSON(l.ownerPath(folder), owner); err != nil {
		cause = errors.Join(cause, fmt.Errorf("record why %s is kept: %w", folder, err))
	} else if err := os.Remove(l.orderPath(folder)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		cause = errors.Join(cause, fmt.Errorf("withdraw the order for %s: %w", folder, err))
	}
	return &failedTrashMove{failure: failure, cause: cause}
}

// retainedOwner identifies a retained folder after its notice was removed.
func (l *ScratchLedger) retainedOwner(name string) (ScratchOwner, bool) {
	base, ok := strings.CutSuffix(name, ".owner.json")
	if !ok || !scratchName.MatchString(base) {
		return ScratchOwner{}, false
	}
	if _, err := os.Lstat(l.failurePath(base)); !errors.Is(err, fs.ErrNotExist) {
		return ScratchOwner{}, false
	}
	var owner ScratchOwner
	if readJSON(l.ownerPath(base), &owner) != nil || owner.Kept == "" || owner.Store != l.store ||
		filepath.Base(owner.Folder) != base || !inRoot(l.root, owner.Folder) {
		return ScratchOwner{}, false
	}
	_, err := os.Lstat(l.orderPath(base))
	return owner, errors.Is(err, fs.ErrNotExist)
}

// TrashFailures lists refused moves still bound to the same folder and removal order.
func (l *ScratchLedger) TrashFailures() ([]TrashFailure, error) {
	failures := []TrashFailure{}
	entries, err := os.ReadDir(l.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return failures, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if owner, ok := l.retainedOwner(entry.Name()); ok {
			if owner.matchesFolder(owner.Folder) {
				failures = append(failures, TrashFailure{Path: owner.Folder, Reason: owner.Kept, Kept: true})
			}
			continue
		}
		if failure, _ := l.entryNotice(entry.Name()); failure != nil {
			failures = append(failures, *failure)
			continue
		}
		base, ok := strings.CutSuffix(entry.Name(), trashFailureSuffix)
		if !ok || !scratchName.MatchString(base) {
			continue
		}
		var record trashFailureRecord
		if err := readJSON(filepath.Join(l.dir, entry.Name()), &record); err != nil || record.Order.Folder == "" || record.Failure.Path == "" {
			if failure := l.damagedNotice(base); failure != nil {
				failures = append(failures, *failure)
			}
			continue
		}
		folder := record.Order.Folder
		if record.Order.Store != l.store || filepath.Base(folder) != base || !inRoot(l.root, folder) {
			continue
		}
		if damaged := l.damagedRecord(record); damaged != "" {
			reason := "its record could not be read"
			if record.Failure.Reason != "" {
				reason = record.Failure.Reason + ", and its record could not be read"
			}
			failure := record.Failure
			failure.Reason, failure.Record = reason, damaged
			failures = append(failures, failure)
			continue
		}
		if failure := l.standing(record); failure != nil {
			failures = append(failures, *failure)
		}
	}
	return failures, nil
}

// entryNotice reports a misplaced entry or an unverified project file.
// A settled notice can be cleared by the sweep.
func (l *ScratchLedger) entryNotice(name string) (failure *TrashFailure, settled bool) {
	if _, active := activeCopyMoves.Load(name); active {
		return nil, false
	}
	base := entryNoticeBase(name)
	if base == "" {
		return nil, false
	}
	var r trashFailureRecord
	record := filepath.Join(l.dir, name)
	if err := readJSON(record, &r); err != nil || r.Order.Folder == "" || r.Failure.Trash == "" {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false
		}
		return &TrashFailure{Path: record, Record: record, Waiting: true, File: strings.HasSuffix(name, copyTrashSuffix),
			Reason: "this notice of a move to the trash could not be read"}, false
	}
	if r.Order.Store != l.store || filepath.Base(r.Order.Folder) != base {
		return nil, false
	}
	defer func() {
		if failure != nil {
			failure.File = strings.HasSuffix(name, copyTrashSuffix)
		}
	}()
	if r.Failure.Foreign {
		info, err := os.Lstat(r.Failure.Trash)
		if errors.Is(err, fs.ErrNotExist) || err == nil && r.ForeignID != "" && FileID(info) != r.ForeignID {
			return nil, true
		}
		return &r.Failure, false
	}
	if !r.Failure.Waiting || r.ExpectedID == "" {
		return nil, false
	}
	info, err := lstatMoved(r.Failure.Trash)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, true
	case err != nil:
		return &r.Failure, false
	case isEntry(info, r.ExpectedID, false):
		return nil, true
	default:
		r.Failure.Reason = "another entry is now at the recorded trash path"
		return &r.Failure, false
	}
}

// A readable owner identifies the folder when its trash notice is damaged.
func (l *ScratchLedger) damagedNotice(base string) *TrashFailure {
	var owner ScratchOwner
	err := readJSON(l.ownerPath(base), &owner)
	if err != nil || owner.Session == "" || owner.ID == "" || filepath.Base(owner.Folder) != base || !inRoot(l.root, owner.Folder) {
		// Neither record says where the folder is; its name is still known.
		return &TrashFailure{Path: filepath.Join(l.root, base), Waiting: true, Record: l.failurePath(base),
			Reason: "its trash notice and its owner record could not be read"}
	}
	if owner.Store != l.store {
		return nil
	}
	path := owner.Folder
	_, order, err := l.bound(path)
	waiting := err == nil
	if waiting && removalName.MatchString(order.Aside) {
		aside := filepath.Join(filepath.Dir(path), order.Aside)
		if owner.matchesFolder(aside) {
			path = aside
		}
	}
	if !errors.Is(err, fs.ErrNotExist) && !order.complete() {
		return &TrashFailure{Path: path, Waiting: true, Record: l.failurePath(base), Reason: "its trash notice and its removal order could not be read"}
	}
	if !waiting && !errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if !owner.matchesFolder(path) {
		if !waiting {
			return nil
		}
		return &TrashFailure{Path: path, Waiting: true, Record: l.failurePath(base),
			Reason: "its trash notice could not be read, and it may already be in the trash"}
	}
	return &TrashFailure{Path: path, Kept: !waiting, Waiting: waiting, Record: l.failurePath(base), Reason: "its trash notice could not be read"}
}

// damagedRecord identifies an incomplete owner or order for a folder still in
// place or a move awaiting verification in the trash.
func (l *ScratchLedger) damagedRecord(record trashFailureRecord) string {
	folder, path := record.Order.Folder, record.Failure.Path
	if path != folder && !(removalName.MatchString(record.Order.Aside) && path == filepath.Join(filepath.Dir(folder), record.Order.Aside)) {
		return ""
	}
	if !record.Failure.Waiting || record.Failure.Trash == "" {
		if info, err := os.Lstat(path); err != nil || !isEntry(info, record.Order.ID, true) {
			return ""
		}
	}
	var owner ScratchOwner
	ownerPath := l.ownerPath(folder)
	err := readJSON(ownerPath, &owner)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ""
	case err != nil, owner.Folder == "", owner.Session == "", owner.Store == "", owner.ID == "":
		return ownerPath
	case record.Failure.Kept, owner.Folder != folder, owner.Session != record.Order.Session, owner.Store != l.store, owner.ID != record.Order.ID:
		return ""
	}
	var order RemovalOrder
	orderPath := l.orderPath(folder)
	err = readJSON(orderPath, &order)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ""
	case err != nil, !order.complete():
		return orderPath
	}
	return ""
}

// standing is the notice a readable failure record gives while its folder is
// where it says. A folder whose owner record or order was removed is kept, and
// an order a settled notice could not withdraw is the record to remove.
func (l *ScratchLedger) standing(record trashFailureRecord) *TrashFailure {
	folder, path := record.Order.Folder, record.Failure.Path
	if record.Failure.Waiting && record.Failure.Trash != "" {
		_, order, err := l.bound(folder)
		if err == nil {
			if order == record.Order {
				return &record.Failure
			}
			return nil
		}
		// The notice still says where the folder went.
		failure := record.Failure
		for _, r := range []struct{ path, name string }{{l.ownerPath(folder), "owner record"}, {l.orderPath(folder), "removal order"}} {
			if _, statErr := os.Lstat(r.path); errors.Is(statErr, fs.ErrNotExist) {
				failure.Reason += ", and its " + r.name + " is missing"
				failure.Record = l.failurePath(folder)
				return &failure
			}
		}
		return nil
	}
	if path != folder && !(removalName.MatchString(record.Order.Aside) && path == filepath.Join(filepath.Dir(folder), record.Order.Aside)) {
		return nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !isEntry(info, record.Order.ID, true) {
		return nil
	}
	kept := func(reason string) *TrashFailure {
		return &TrashFailure{Path: path, Reason: reason, Kept: true}
	}
	owner, err := l.owner(folder)
	if errors.Is(err, fs.ErrNotExist) {
		return kept("its owner record is missing, so it is no longer moved to the trash")
	}
	if err != nil || owner.Store != l.store || owner.ID != record.Order.ID || owner.Session != record.Order.Session {
		return nil
	}
	var order RemovalOrder
	err = readJSON(l.orderPath(folder), &order)
	switch {
	case errors.Is(err, fs.ErrNotExist) && record.Failure.Kept:
		return &record.Failure
	case errors.Is(err, fs.ErrNotExist):
		return kept("its removal order is missing, so it is no longer moved to the trash")
	case err != nil, order != record.Order:
		return nil
	case record.Failure.Kept:
		failure := record.Failure
		failure.Reason += ", and its removal order could not be withdrawn"
		failure.Record = l.orderPath(folder)
		return &failure
	}
	return &record.Failure
}

// beforeTrash runs between the last check of a folder and its move to the
// trash. Tests replace it to change the folder or its copy in between.
var beforeTrash = func(folder string) {}

// Trashed records where a folder went, in place of its other records.
type Trashed struct {
	Folder string    `json:"folder"`
	Trash  string    `json:"trash"`
	At     time.Time `json:"at"`
}

func (l *ScratchLedger) trashedPath(folder string) string {
	return filepath.Join(l.dir, filepath.Base(folder)+".trashed.json")
}

// trashed releases the removal order once its confirmed destination is saved.
func (l *ScratchLedger) trashed(folder, path, trash string, at time.Time) (string, error) {
	if err := writeJSON(l.trashedPath(folder), Trashed{Folder: folder, Trash: trash, At: at.UTC()}); err != nil {
		failure := TrashFailure{Path: path, Trash: trash, Waiting: true, Reason: "the confirmed trash location could not be saved"}
		return "", &failedTrashMove{failure: failure, cause: err}
	}
	log.Printf("Moved %s to the trash as %s", path, trash)
	l.clearTrashFailure(folder)
	if err := l.withdraw(folder); err != nil {
		log.Printf("Could not withdraw the order for %s, moved to %s: %v", folder, trash, err)
		return trash, nil
	}
	os.Remove(l.ownerPath(folder))
	return trash, nil
}

// Trash is where folder was moved to the trash, empty when it was not.
func (l *ScratchLedger) Trash(folder string) string {
	var t Trashed
	if readJSON(l.trashedPath(folder), &t) != nil || t.Folder != folder {
		return ""
	}
	return t.Trash
}

// AsideMoveError identifies the retained folder when its volume refuses the move.
type AsideMoveError struct {
	path string
	err  error
}

func (e *AsideMoveError) Error() string {
	return fmt.Sprintf("Could not move %s (%v). Move this folder to an unused location, then retry", e.path, e.err)
}

func (e *AsideMoveError) Unwrap() error { return e.err }

var errFolderRestored = errors.New("its removal was cut short, so it is back under its own name and kept")

// finishAside restores the recorded folder to its original name or trashes it
// if that name is occupied. An absent aside with a visible source leaves
// the order for Remove to evaluate.
func (l *ScratchLedger) finishAside(owner ScratchOwner, order RemovalOrder, now time.Time) (trash string, done bool, err error) {
	if !removalName.MatchString(order.Aside) {
		return "", true, errors.New("the removal order names no private folder")
	}
	if !inRoot(l.root, owner.Folder) {
		return "", true, fmt.Errorf("%s is not in %s", owner.Folder, l.root)
	}
	aside := filepath.Join(filepath.Dir(owner.Folder), order.Aside)
	info, err := os.Lstat(aside)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(owner.Folder); errors.Is(err, os.ErrNotExist) {
			l.forget(owner.Folder)
			return "", true, nil
		}
		return "", false, nil
	}
	if err != nil {
		return "", true, err
	}
	if !info.IsDir() || FileID(info) != owner.ID {
		l.withdraw(owner.Folder)
		return "", true, fmt.Errorf("%s was not the folder to remove, and is kept", aside)
	}
	beforeRename(aside)
	err = renameNoReplace(aside, owner.Folder)
	if err == nil && !owner.matchesFolder(owner.Folder) {
		if renameNoReplace(owner.Folder, aside) != nil {
			return "", true, fmt.Errorf("another entry took the place of %s as it was restored, and is now %s", aside, owner.Folder)
		}
		l.withdraw(owner.Folder)
		return "", true, fmt.Errorf("%s was not the folder to remove, and is kept", aside)
	}
	if err == nil {
		if err := l.withdraw(owner.Folder); err != nil {
			return "", true, err
		}
		return "", true, errFolderRestored
	}
	if errors.Is(err, fs.ErrExist) {
		trash, err = trashEntry(aside, owner.ID, owner.Folder, true, l.prepareTrash(order))
		if err == nil {
			trash, err = l.trashed(owner.Folder, aside, trash, now)
			return trash, true, err
		}
	}
	if !owner.matchesFolder(aside) {
		return "", true, err
	}
	return "", true, &AsideMoveError{path: aside, err: err}
}

// Sweep moves to the trash the folders whose removal orders hold and that no
// session claims, and returns them. A folder with no order is never taken.
func (l *ScratchLedger) Sweep(claims []string, now time.Time) []string {
	held := map[string]bool{}
	for _, c := range claims {
		held[filepath.Clean(c)] = true
		held[config.ResolveLinks(c)] = true
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		base, ok := strings.CutSuffix(e.Name(), ".remove.json")
		if !ok || !scratchName.MatchString(base) {
			continue
		}
		var owner ScratchOwner
		if err := readJSON(filepath.Join(l.dir, base+".owner.json"), &owner); err != nil ||
			filepath.Base(owner.Folder) != base || held[filepath.Clean(owner.Folder)] {
			continue
		}
		if _, err := l.Remove(owner.Folder, now); err == nil {
			removed = append(removed, owner.Folder)
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("%s is kept: %v", owner.Folder, err)
		}
	}
	l.forgetGone(entries)
	return removed
}

// renewKeptNotice writes again, from the owner record, the notice of a folder
// kept with no order whose notice cannot be read, so it stays listed and its
// records go once it is gone. A notice that may say where a folder went stays.
func (l *ScratchLedger) renewKeptNotice(base string) {
	var owner ScratchOwner
	if readJSON(l.ownerPath(base), &owner) != nil || owner.Store != l.store || filepath.Base(owner.Folder) != base || owner.Session == "" || !inRoot(l.root, owner.Folder) {
		return
	}
	if _, err := os.Lstat(l.orderPath(base)); !errors.Is(err, fs.ErrNotExist) || !owner.matchesFolder(owner.Folder) {
		return
	}
	record := trashFailureRecord{
		Order:   RemovalOrder{Folder: owner.Folder, Session: owner.Session, ID: owner.ID, Store: owner.Store},
		Failure: TrashFailure{Path: owner.Folder, Kept: true, Reason: "its earlier notice could not be read"},
	}
	if owner.Kept == "" {
		owner.Kept = record.Failure.Reason
		if err := writeJSON(l.ownerPath(base), owner); err != nil {
			log.Printf("Could not save why %s is kept: %v", owner.Folder, err)
			return
		}
	} else {
		record.Failure.Reason = owner.Kept
	}
	if err := writeJSON(l.failurePath(base), record); err != nil {
		log.Printf("Could not write the notice of %s again: %v", owner.Folder, err)
	}
}

// forgetGone drops the records of folders a notice named that are no longer
// on disk, and the notices of other entries that have left the trash.
func (l *ScratchLedger) forgetGone(entries []os.DirEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range entries {
		if owner, ok := l.retainedOwner(e.Name()); ok {
			if l.Missing(owner.Folder) {
				l.forget(owner.Folder)
			}
			continue
		}
		if _, settled := l.entryNotice(e.Name()); settled {
			os.Remove(filepath.Join(l.dir, e.Name()))
			continue
		}
		base, ok := strings.CutSuffix(e.Name(), trashFailureSuffix)
		if !ok || !scratchName.MatchString(base) || l.root == "" {
			continue
		}
		var record trashFailureRecord
		if readJSON(filepath.Join(l.dir, e.Name()), &record) != nil || record.Order.Folder == "" || record.Failure.Path == "" {
			l.renewKeptNotice(base)
			continue
		}
		if record.Order.Store != l.store || filepath.Base(record.Order.Folder) != base || record.Failure.Waiting && record.Failure.Trash != "" {
			continue
		}
		if l.Missing(record.Order.Folder) {
			l.forget(record.Order.Folder)
		}
	}
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return config.WriteAtomic(path, append(data, '\n'), 0o600)
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
