package store

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestARefusedArchiveKeepsTheDeletionOrder(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess := sample(NewID(), time.Now())
	sess.Parent = NewID()
	if err := st.Save(sess); err != nil {
		t.Fatal(err)
	}
	if err := st.markPending(sess.ID); err != nil {
		t.Fatal(err)
	}
	path := st.path(sess.ID)
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	const immutable = 2 // UF_IMMUTABLE
	if err := syscall.Chflags(path, immutable); err != nil {
		t.Skipf("Cannot mark the transcript immutable: %v", err)
	}
	t.Cleanup(func() { syscall.Chflags(path, 0) })
	if _, err := st.ArchiveUnreadable(sess.ID); err == nil {
		t.Fatal("Archived an immutable transcript")
	}
	reopened, err := Open(st.dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Pending(sess.ID) || !reopened.Pending(sess.ID) || len(reopened.Family(sess.Parent)) != 1 {
		t.Fatal("The failed archive discarded the deletion order or launcher")
	}
}

func TestPartialOutputRemovalRetainsItsMarkerUntilRetry(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	path := s.filesDir(id)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "locked")
	if err := os.WriteFile(file, []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := s.RecoveryItems()
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if err := syscall.Chflags(file, 2); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { syscall.Chflags(file, 0) })
	if err := s.DeleteRetained(id+".files", items[0].Generation); !errors.Is(err, ErrLeftBehind) {
		t.Fatal(err)
	}
	if got, err := outputGeneration(path, false); err != nil || got != items[0].Generation {
		t.Fatalf("Partial removal generation = %q, %v", got, err)
	}
	if err := syscall.Chflags(file, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(s.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("Retry left output behind", err)
	}
}
