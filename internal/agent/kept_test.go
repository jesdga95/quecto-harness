package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// Kept names the folder a delete leaves on disk: a scratch folder with no
// owner record, and one a full copy came from that changed after the copy.
// A scratch folder its owner record allows to go is not named.
func TestKeptNamesTheFolderADeleteLeaves(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	owned, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := h.engine.Kept(owned.ID); err != nil || got != "" {
		t.Errorf("an owned folder: kept = %q, %v, want none", got, err)
	}

	old, _ := h.engine.CreateScratch()
	oldDir, _ := old.Where()
	os.Remove(filepath.Join(h.paths.Home, "scratch-owners", filepath.Base(oldDir)+".owner.json"))
	if got, _ := h.engine.Kept(old.ID); got != oldDir {
		t.Errorf("a folder with no owner record: kept = %q, want %q", got, oldDir)
	}

	moved, _ := h.engine.CreateScratch()
	src, _ := moved.Where()
	writeText(t, filepath.Join(src, "note.txt"), "one")
	runner, _ := tools.NewRunner(src, h.cfg.Config())
	if _, err := h.engine.changeDirectory(moved, runner, moveCall(h.project), h.project); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(moved)
	if got, _ := h.engine.Kept(moved.ID); got != "" {
		t.Errorf("a full copy: kept = %q, want none", got)
	}
	later := time.Now().Add(time.Hour)
	writeText(t, filepath.Join(src, "late.txt"), "two")
	os.Chtimes(filepath.Join(src, "late.txt"), later, later)
	if got, _ := h.engine.Kept(moved.ID); got != src {
		t.Errorf("a full copy that changed: kept = %q, want %q", got, src)
	}
}

func TestKeptNamesAReplacementFolder(t *testing.T) {
	for _, moved := range []bool{false, true} {
		h := scratchHarness(t)
		s, err := h.engine.CreateScratch()
		if err != nil {
			t.Fatal(err)
		}
		dir, _ := s.Where()
		if moved {
			runner, err := tools.NewRunner(dir, h.cfg.Config())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
				t.Fatal(err)
			}
			h.engine.applyMove(s)
		}
		if err := os.Rename(dir, dir+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		old := time.Unix(1, 0)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		if got, err := h.engine.Kept(s.ID); err != nil || got != dir {
			t.Errorf("Moved %t: kept = %q, %v; want %q", moved, got, err, dir)
		}
	}
}

// A delete and a sweep reach the scratch folders through one ledger, so a
// sweep that comes while a delete is moving the folder waits for it.
func TestOneLedgerServesEveryRemoval(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	moving, resume := make(chan struct{}), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(resume) }) })
	var calls atomic.Int32
	saved := store.TrashHome
	store.TrashHome = func() string {
		if calls.Add(1) == 1 {
			close(moving)
			<-resume
		}
		return home
	}
	t.Cleanup(func() { store.TrashHome = saved })
	deleted := make(chan error, 1)
	go func() { deleted <- h.engine.Delete(s.ID) }()
	select {
	case <-moving:
	case err := <-deleted:
		t.Fatalf("The delete ended before moving the folder: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("The delete never moved the folder")
	}
	swept := make(chan []string, 1)
	go func() { swept <- h.engine.sweepOnce(time.Now().AddDate(1, 0, 0)) }()
	for deadline := time.Now().Add(10 * time.Second); !waitingOnLedger(); time.Sleep(10 * time.Millisecond) {
		if n := calls.Load(); n != 1 {
			t.Fatalf("The sweep reached the trash %d times while the delete held the folder", n-1)
		}
		if time.Now().After(deadline) {
			t.Fatal("The sweep never reached the ledger")
		}
	}
	release.Do(func() { close(resume) })
	for _, wait := range []func() bool{
		func() bool {
			select {
			case err := <-deleted:
				if err != nil {
					t.Error(err)
				}
				return true
			case <-time.After(10 * time.Second):
				return false
			}
		},
		func() bool {
			select {
			case moved := <-swept:
				if len(moved) != 0 {
					t.Errorf("The sweep moved %v after the delete", moved)
				}
				return true
			case <-time.After(10 * time.Second):
				return false
			}
		},
	} {
		if !wait() {
			t.Fatal("A removal did not finish")
		}
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("The folder is still in place: %v", err)
	}
}

// waitingOnLedger reports whether a goroutine is waiting for the scratch
// ledger's lock inside a removal.
func waitingOnLedger() bool {
	buf := make([]byte, 1<<22)
	stacks := string(buf[:runtime.Stack(buf, true)])
	for _, g := range strings.Split(stacks, "\n\n") {
		if strings.Contains(g, "sync.(*Mutex)") && strings.Contains(g, "ScratchLedger).Remove") {
			return true
		}
	}
	return false
}

// A delete told a scratch folder stays keeps it. When its owner record is
// missing as the person is asked and back by the delete, the delete is
// refused rather than remove the folder it promised to keep.
func TestADeleteKeepsTheFolderItWasToldStays(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	owners := filepath.Join(h.paths.Home, "scratch-owners")
	for _, restored := range []bool{true, false} {
		s, err := h.engine.CreateScratch()
		if err != nil {
			t.Fatal(err)
		}
		dir, _ := s.Where()
		writeText(t, filepath.Join(dir, "doc.txt"), "the only copy")
		owner := filepath.Join(owners, filepath.Base(dir)+".owner.json")
		record, _ := os.ReadFile(owner)
		os.Remove(owner)
		told, err := h.engine.Kept(s.ID)
		if err != nil || told != dir {
			t.Fatalf("kept = %q, %v, want %s", told, err, dir)
		}
		if restored {
			os.WriteFile(owner, record, 0o600)
			if err := h.engine.DeleteConfirmed(s.ID, DeletionPreview{Kept: told}); !errors.Is(err, ErrKeptChanged) {
				t.Errorf("restored: delete = %v, want ErrKeptChanged", err)
			}
		} else if err := h.engine.DeleteConfirmed(s.ID, DeletionPreview{Kept: told}); err != nil {
			t.Fatal(err)
		}
		h.engine.sweepOnce(time.Now().AddDate(1, 0, 0))
		if _, err := os.Stat(filepath.Join(dir, "doc.txt")); err != nil {
			t.Errorf("restored %t: the folder the delete was told stays went: %v", restored, err)
		}
	}
}

// A run whose record cannot be read when its launcher is deleted may still
// name the launcher's folder, so the person is told the folder stays. Once
// the run reads again it is listed on its own, and deleting it removes the
// folder it was the last to hold.
func TestARunUnreadAtItsLaunchersDeleteIsListedAndSettled(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	writeText(t, filepath.Join(dir, "doc.txt"), "the only copy")
	run := newSession(store.NewID(), dir, h.agent.ID, h.agent.Model, "")
	run.Parent, run.Scratch = s.ID, true
	// An imported run has no entry in the family ledger until its first successful read.
	doc, _, _, _ := run.Snapshot()
	record, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(h.paths.Sessions, run.ID+".json")
	writeText(t, file, "{")

	e := h.reopen(t)
	told, err := e.PreviewDelete(s.ID)
	if err != nil || told.Kept != dir || !told.Deferred {
		t.Fatalf("preview with a run unread = %+v, %v, want %s kept until it reads", told, err, dir)
	}
	if err := e.DeleteConfirmed(s.ID, told); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(file, record, 0o600)
	e.store.Reread()
	listed := false
	for _, v := range e.store.ListScratch() {
		listed = listed || v.ID == run.ID
	}
	if !listed {
		t.Fatal("the run whose launcher is gone is not listed")
	}
	if got, err := e.Kept(run.ID); err != nil || got != "" {
		t.Errorf("kept for the run = %q, %v, want none", got, err)
	}
	if err := e.DeleteConfirmed(run.ID, DeletionPreview{}); err != nil {
		t.Fatal(err)
	}
	e.sweepOnce(time.Now())
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder outlived the last run that held it: %v", err)
	}
}

func TestConfirmedDeleteKeepsTheFolderLeftByAMove(t *testing.T) {
	for _, reason := range []string{"project edit", "legacy order"} {
		t.Run(reason, func(t *testing.T) {
			h := scratchHarness(t)
			s, err := h.engine.CreateScratch()
			if err != nil {
				t.Fatal(err)
			}
			from, _ := s.Where()
			writeText(t, filepath.Join(from, "note"), "original")
			runner, err := tools.NewRunner(from, h.cfg.Config())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
				t.Fatal(err)
			}
			h.engine.applyMove(s)
			dst := filepath.Join(h.project, "note")
			switch reason {
			case "project edit":
				writeText(t, dst, "edited")
			case "legacy order":
				if err := h.engine.scratchLedger().Order(from, s.ID, store.RemoveCopied, "", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			e := h.reopen(t)
			if kept, err := e.Kept(s.ID); err != nil || kept != from {
				t.Fatalf("Kept folder = %q, %v", kept, err)
			}
			if err := e.DeleteConfirmed(s.ID, DeletionPreview{}); !errors.Is(err, ErrKeptChanged) {
				t.Fatalf("Stale confirmation = %v", err)
			}
			if err := e.DeleteConfirmed(s.ID, DeletionPreview{Kept: from}); err != nil {
				t.Fatal(err)
			}
			order := filepath.Join(h.paths.Home, "scratch-owners", filepath.Base(from)+".remove.json")
			if _, err := os.Stat(order); !os.IsNotExist(err) {
				t.Fatalf("Confirmed retention left an order: %v", err)
			}
			writeText(t, dst, "original")
			h.reopen(t).sweepOnce(time.Now().AddDate(1, 0, 0))
			assertScratchNote(t, filepath.Join(from, "note"), "original")
		})
	}
}

func TestStaleConfirmationLeavesTheInterruptedCopyAlone(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	e := h.reopen(t)
	if err := e.DeleteConfirmed(s.ID, DeletionPreview{Kept: "obsolete"}); !errors.Is(err, ErrKeptChanged) {
		t.Fatal(err)
	}
	for _, path := range left {
		assertScratchNote(t, path, "ne")
	}
	if _, err := os.Stat(e.scratchLedger().Temps(dir)); err != nil {
		t.Fatal(err)
	}
}

func TestOrphanedRunsShareTheirLaunchersFolder(t *testing.T) {
	h := scratchHarness(t)
	parent, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := parent.Where()
	var ids []string
	for range 2 {
		child := newSession(store.NewID(), dir, h.agent.ID, h.agent.Model, "")
		child.Parent, child.Scratch = parent.ID, true
		if err := h.engine.commit(child); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, child.ID)
	}
	if err := os.Remove(filepath.Join(h.paths.Sessions, parent.ID+".json")); err != nil {
		t.Fatal(err)
	}
	e := h.reopen(t)
	if kept, err := e.Kept(ids[0]); err != nil || kept != dir {
		t.Fatalf("First run would keep %q, %v", kept, err)
	}
	if err := e.DeleteConfirmed(ids[0], DeletionPreview{Kept: dir}); err != nil {
		t.Fatal(err)
	}
	if kept, err := e.Kept(ids[1]); err != nil || kept != "" {
		t.Fatalf("Last run would keep %q, %v", kept, err)
	}
	if err := e.DeleteConfirmed(ids[1], DeletionPreview{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Folder after last deletion: %v", err)
	}
}

// A scratch folder removed by hand is not promised to stay, and deleting its
// conversation forgets the folder's records.
func TestAFolderRemovedByHandIsNotPromised(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	told, err := h.engine.Kept(s.ID)
	if err != nil || told != "" {
		t.Fatalf("kept = %q, %v, want none", told, err)
	}
	if err := h.engine.DeleteConfirmed(s.ID, DeletionPreview{Kept: told}); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(h.paths.Home, "scratch-owners", filepath.Base(dir)+".owner.json")
	if _, err := os.Stat(owner); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the owner record of the removed folder = %v, want it gone", err)
	}
}

// A folder kept at a confirmed delete only because an unrelated session file
// could not be read is removed by the sweep once every file reads: a scratch
// folder, and the source of a move that was copied whole.
func TestAFolderHeldOnlyByAnUnreadFileGoesOnceItReads(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	plain, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	plainDir, _ := plain.Where()
	writeText(t, filepath.Join(plainDir, "doc.txt"), "draft")
	moved, _ := h.engine.CreateScratch()
	from, _ := moved.Where()
	writeText(t, filepath.Join(from, "note"), "original")
	runner, _ := tools.NewRunner(from, h.cfg.Config())
	if _, err := h.engine.changeDirectory(moved, runner, moveCall(h.project), h.project); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(moved)
	stays, _ := h.engine.CreateScratch()
	staysDir, _ := stays.Where()
	writeText(t, filepath.Join(staysDir, "doc.txt"), "the only copy")
	os.Remove(filepath.Join(h.paths.Home, "scratch-owners", filepath.Base(staysDir)+".owner.json"))
	other := filepath.Join(h.paths.Sessions, store.NewID()+".json")
	writeText(t, other, "{")

	e := h.reopen(t)
	for _, c := range []struct {
		id, dir  string
		deferred bool
	}{{plain.ID, plainDir, true}, {moved.ID, from, true}, {stays.ID, staysDir, false}} {
		p, err := e.PreviewDelete(c.id)
		if err != nil || p.Kept != c.dir || p.Deferred != c.deferred {
			t.Fatalf("preview = %+v, %v, want %s kept, deferred %t", p, err, c.dir, c.deferred)
		}
		swapped := p
		swapped.Deferred = !p.Deferred
		if err := e.DeleteConfirmed(c.id, swapped); !errors.Is(err, ErrKeptChanged) {
			t.Fatalf("a delete told the other outcome = %v, want ErrKeptChanged", err)
		}
		if err := e.DeleteConfirmed(c.id, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	h.reopen(t).sweepOnce(time.Now().AddDate(1, 0, 0))
	for _, dir := range []string{plainDir, from} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s outlived the unread file: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(staysDir, "doc.txt")); err != nil {
		t.Errorf("the folder promised to stay went: %v", err)
	}
}
