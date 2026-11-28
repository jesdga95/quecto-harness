package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/tools"
)

func TestFirstDayMoveRetention(t *testing.T) {
	for _, tc := range []struct {
		name     string
		conflict bool
	}{
		{name: "complete"},
		{name: "conflict", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := scratchHarness(t)
			t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
			start := time.Now()
			h.engine.sweepOnce(start)
			s, err := h.engine.CreateScratch()
			if err != nil {
				t.Fatal(err)
			}
			src, _ := s.Where()
			file := "note.txt"
			writeText(t, filepath.Join(src, file), "original note")
			if tc.conflict {
				writeText(t, filepath.Join(h.project, file), "destination note")
			}
			runner, err := tools.NewRunner(src, h.cfg.Config())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
				t.Fatal(err)
			}
			h.engine.applyMove(s)
			if location, scratch := s.Where(); location != h.project || scratch {
				t.Fatal("Conversation did not move to the project")
			}
			if err := h.engine.Delete(s.ID); err != nil {
				t.Fatal(err)
			}
			e := h.reopen(t)
			e.sweepOnce(start.AddDate(1, 0, 0))
			if tc.conflict {
				assertScratchNote(t, filepath.Join(src, file), "original note")
				assertScratchNote(t, filepath.Join(h.project, file), "destination note")
				return
			}
			assertScratchNote(t, filepath.Join(h.project, file), "original note")
			if _, err := os.Lstat(src); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("Fully copied first-day folder still present or unreadable: %v", err)
			}
		})
	}
}

func assertScratchNote(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("Read %s: got %q, want %q, error %v", path, data, want, err)
	}
}

// A scratch folder that keeps anything a move did not copy, or whose copy has
// not run, stays after its conversation is deleted, however long after. The
// conversation names it as kept before the delete.
func TestAPartialMoveIsKeptAfterTheConversationGoes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		file    string
		pending bool
	}{
		{name: "repository", file: filepath.Join(".git", "HEAD")},
		{name: "dependencies", file: filepath.Join("vendor", "lib.php")},
		{name: "pending", file: "note.txt", pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := scratchHarness(t)
			t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
			s, err := h.engine.CreateScratch()
			if err != nil {
				t.Fatal(err)
			}
			src, _ := s.Where()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(src, tc.file)), 0o700); err != nil {
				t.Fatal(err)
			}
			writeText(t, filepath.Join(src, tc.file), "x")
			runner, _ := tools.NewRunner(src, h.cfg.Config())
			if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
				t.Fatal(err)
			}
			if tc.pending {
				os.Chmod(src, 0)
			}
			h.engine.applyMove(s)
			os.Chmod(src, 0o700)
			if sum := h.engine.store.List(""); len(sum) != 1 || sum[0].Kept != src {
				t.Errorf("the summary does not name %s as kept: %+v", src, sum)
			}
			if err := h.engine.Delete(s.ID); err != nil {
				t.Fatal(err)
			}
			h.reopen(t).sweepOnce(time.Now().AddDate(1, 0, 0))
			if _, err := os.Stat(filepath.Join(src, tc.file)); err != nil {
				t.Errorf("the file the move left went: %v", err)
			}
		})
	}
}

// A fully copied folder in which something changed after the copy is kept.
func TestACopiedFolderThatChangedIsKept(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	src, _ := s.Where()
	writeText(t, filepath.Join(src, "note.txt"), "one")
	runner, _ := tools.NewRunner(src, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(s)
	later := time.Now().Add(time.Hour)
	writeText(t, filepath.Join(src, "late.txt"), "two")
	os.Chtimes(filepath.Join(src, "late.txt"), later, later)
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	h.reopen(t).sweepOnce(time.Now().AddDate(1, 0, 0))
	if _, err := os.Stat(filepath.Join(src, "late.txt")); err != nil {
		t.Errorf("a file written after the copy went: %v", err)
	}
}

// A scratch folder with no record of who made it is never removed.
func TestAFolderWithNoOwnerIsKept(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	writeText(t, filepath.Join(dir, "a.txt"), "x")
	owners, _ := filepath.Glob(filepath.Join(h.paths.Home, "scratch-owners", "*.owner.json"))
	if len(owners) != 1 {
		t.Fatalf("owner records = %v, want one", owners)
	}
	os.Remove(owners[0])
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	h.reopen(t).sweepOnce(time.Now().AddDate(1, 0, 0))
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Errorf("a folder with no owner record went: %v", err)
	}
}

// A copied folder whose file was rewritten with its old modification time
// put back is kept: the sweep goes by the change time, which a restore that
// keeps dates cannot set.
func TestACopiedFolderRewrittenWithItsOldDateIsKept(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	src, _ := s.Where()
	note := filepath.Join(src, "note.txt")
	writeText(t, note, "ABC")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(note, old, old)
	runner, _ := tools.NewRunner(src, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(s)
	writeText(t, note, "XYZ")
	os.Chtimes(note, old, old)
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	h.reopen(t).sweepOnce(time.Now().AddDate(1, 0, 0))
	assertScratchNote(t, note, "XYZ")
}

// A copied folder whose copy no longer matches it is kept, whatever the
// times say.
func TestACopiedFolderThatNoLongerMatchesIsKept(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	src, _ := s.Where()
	writeText(t, filepath.Join(src, "note.txt"), "ABC")
	runner, _ := tools.NewRunner(src, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(s)
	writeText(t, filepath.Join(h.project, "note.txt"), "ABD")
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	h.reopen(t).sweepOnce(time.Now().AddDate(1, 0, 0))
	assertScratchNote(t, filepath.Join(src, "note.txt"), "ABC")
}

// A file whose resource fork the copy cannot bring makes the copy partial,
// and its folder stays after the conversation goes.
func TestAResourceForkKeepsTheFolder(t *testing.T) {
	h := scratchHarness(t)
	t.Cleanup(func() { os.RemoveAll(h.paths.Scratch) })
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	src, _ := s.Where()
	file := filepath.Join(src, "icon.rsrc")
	writeText(t, file, "")
	if err := os.WriteFile(filepath.Join(file, "..namedfork", "rsrc"), []byte("resource data"), 0o600); err != nil {
		t.Skipf("this volume takes no resource fork: %v", err)
	}
	runner, _ := tools.NewRunner(src, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(s)
	if s.Carried.Complete() {
		t.Errorf("a copy without the resource fork is complete: %+v", s.Carried)
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	h.reopen(t).sweepOnce(time.Now().AddDate(1, 0, 0))
	assertScratchNote(t, filepath.Join(file, "..namedfork", "rsrc"), "resource data")
}
