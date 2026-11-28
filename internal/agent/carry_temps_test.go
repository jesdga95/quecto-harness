package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/store"
)

// A copy cut off by a restart left a temporary file in the project. The copy
// that finishes after the restart removes it, and leaves a person's own file
// whose name has the same shape.
func TestACopyCutOffLeavesNoTemporaryFile(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	writeText(t, filepath.Join(dir, "new.txt"), "new")
	fromInfo, _ := os.Stat(dir)
	toInfo, _ := os.Stat(h.project)
	s.mu.Lock()
	s.Project, s.Scratch, s.LeftIn, s.CarryTo = h.project, false, dir, h.project
	s.LeftInID, s.CarryToID = store.FileID(fromInfo), store.FileID(toInfo)
	s.mu.Unlock()
	h.engine.save(s)

	left := filepath.Join(h.project, ".carry-0123456789ab")
	writeText(t, left, "ne")
	mine := filepath.Join(h.project, ".carry-x")
	writeText(t, mine, "a person's file")
	info, _ := os.Lstat(left)
	line, _ := json.Marshal(map[string]string{"name": ".carry-0123456789ab", "id": store.FileID(info)})
	temps := h.engine.scratchLedger().Temps(dir)
	os.MkdirAll(filepath.Dir(temps), 0o700)
	if err := os.WriteFile(temps, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	e := h.reopen(t)
	r, err := e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.applyMove(r)
	if _, err := os.Lstat(left); !os.IsNotExist(err) {
		t.Errorf("the temporary file of the cut-off copy is still there: %v", err)
	}
	if _, err := os.Lstat(mine); err != nil {
		t.Errorf("a person's own file was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.project, "new.txt")); err != nil {
		t.Errorf("the file was not copied: %v", err)
	}
}

// cutOffCopy leaves what a copy cut short by a crash leaves: a conversation
// moved out of its scratch folder with the copy still due, a temporary file
// and a half-written one in the project, both on the copy's list.
func cutOffCopy(t *testing.T, h *harness) (s *Session, dir string, left []string) {
	t.Helper()
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ = s.Where()
	writeText(t, filepath.Join(dir, "new.txt"), "new")
	fromInfo, _ := os.Stat(dir)
	toInfo, _ := os.Stat(h.project)
	s.mu.Lock()
	s.Project, s.Scratch, s.LeftIn, s.CarryTo = h.project, false, dir, h.project
	s.LeftInID, s.CarryToID = store.FileID(fromInfo), store.FileID(toInfo)
	s.mu.Unlock()
	h.engine.save(s)

	var lines []byte
	for _, e := range []struct {
		name, text string
		partial    bool
	}{{".carry-0123456789ab", "ne", false}, {"new.txt", "ne", true}} {
		path := filepath.Join(h.project, e.name)
		writeText(t, path, e.text)
		info, _ := os.Lstat(path)
		line, _ := json.Marshal(map[string]any{"name": e.name, "id": store.FileID(info), "partial": e.partial})
		lines = append(append(lines, line...), '\n')
		left = append(left, path)
	}
	temps := h.engine.scratchLedger().Temps(dir)
	os.MkdirAll(filepath.Dir(temps), 0o700)
	if err := os.WriteFile(temps, lines, 0o600); err != nil {
		t.Fatal(err)
	}
	return s, dir, left
}

// Deleting the conversation before the copy runs again removes what the copy
// cut short left in the project, and its list.
func TestADeleteAfterACutOffCopyLeavesNothingInTheProject(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	mine := filepath.Join(h.project, ".carry-x")
	writeText(t, mine, "a person's file")
	e := h.reopen(t)
	if err := e.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range left {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s is still in the project: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Lstat(mine); err != nil {
		t.Errorf("a person's own file was removed: %v", err)
	}
	if _, err := os.Stat(e.scratchLedger().Temps(dir)); !os.IsNotExist(err) {
		t.Errorf("the list of the cut-off copy outlived the conversation: %v", err)
	}
}

// A copy that finds its folder gone still removes the temporary file an
// earlier try left. The half-written file stays, since nothing is left to
// check it against.
func TestACopyThatFindsItsFolderGoneClearsItsTemporaryFile(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	e := h.reopen(t)
	r, err := e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.applyMove(r)
	if _, err := os.Lstat(left[0]); !os.IsNotExist(err) {
		t.Errorf("the temporary file is still in the project: %v", err)
	}
	if _, err := os.Lstat(left[1]); err != nil {
		t.Errorf("a file that could not be checked was removed: %v", err)
	}
	if r.Carried == nil || !r.Carried.Gone || r.Carried.Interrupted != 1 {
		t.Errorf("carried = %+v, want the folder gone and one file kept", r.Carried)
	}
	if _, err := os.Stat(e.scratchLedger().Temps(dir)); !os.IsNotExist(err) {
		t.Errorf("the list outlived the copy: %v", err)
	}
}

func TestDeleteWithFolderConsentWaitsForTheInterruptedDestination(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		h := scratchHarness(t)
		s, dir, left := cutOffCopy(t, h)
		e := h.reopen(t)
		if err := os.Rename(h.project, h.project+".away"); err != nil {
			t.Fatal(err)
		}
		if replaced {
			if err := os.Mkdir(h.project, 0700); err != nil {
				t.Fatal(err)
			}
			writeText(t, filepath.Join(h.project, "new.txt"), "replacement")
		}
		if err := e.DeleteConfirmed(s.ID, DeletionPreview{Kept: dir}); !errors.Is(err, ErrKeptChanged) {
			t.Fatalf("Missing copy confirmation: %v", err)
		}
		if _, err := os.Stat(e.scratchLedger().Temps(dir)); err != nil {
			t.Fatal(err)
		}
		if replaced {
			assertScratchNote(t, filepath.Join(h.project, "new.txt"), "replacement")
			if err := os.RemoveAll(h.project); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Rename(h.project+".away", h.project); err != nil {
			t.Fatal(err)
		}
		if err := e.Delete(s.ID); err != nil {
			t.Fatal(err)
		}
		for _, p := range left {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("Partial remains: %v", err)
			}
		}
	}
}

func TestRetryKeepsInterruptedCountUntilTheMoveIsSaved(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	writeText(t, left[1]+".new", "ne")
	if err := os.Rename(left[1]+".new", left[1]); err != nil {
		t.Fatal(err)
	}
	e := h.reopen(t)
	run, err := e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the copy result from replacing the session record.
	record := filepath.Join(h.paths.Sessions, s.ID+".json")
	if err := os.Rename(record, record+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(record, 0700); err != nil {
		t.Fatal(err)
	}
	if e.carry(run) {
		t.Fatal("Saved over a directory")
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(record+".saved", record); err != nil {
		t.Fatal(err)
	}
	e = h.reopen(t)
	run, err = e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !e.carry(run) || run.Carried.Interrupted != 1 || run.Carried.There != 0 {
		t.Fatalf("Retried copy = %+v", run.Carried)
	}
	if _, err := os.Stat(e.scratchLedger().Temps(dir)); !os.IsNotExist(err) {
		t.Fatalf("Committed copy kept its log: %v", err)
	}
}

func TestConfirmedDeleteKeepsTheInterruptedNames(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		h := scratchHarness(t)
		s, dir, left := cutOffCopy(t, h)
		if err := os.Rename(h.project, h.project+".away"); err != nil {
			t.Fatal(err)
		}
		if replaced {
			if err := os.Mkdir(h.project, 0700); err != nil {
				t.Fatal(err)
			}
			writeText(t, left[1], "replacement")
			if !h.engine.carry(s) || s.Carried.Reason == "" {
				t.Fatal("Replacement left the copy pending")
			}
		}
		e := h.reopen(t)
		p, err := e.PreviewDelete(s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Carry == nil || !p.Carry.Unavailable || p.Carry.Destination != h.project || len(p.Carry.Names) != 2 || p.Token == "" {
			t.Fatalf("Preview = %+v, carry = %+v", p, p.Carry)
		}
		if err := e.DeleteConfirmed(s.ID, DeletionPreview{Kept: p.Kept, Token: "obsolete"}); !errors.Is(err, ErrKeptChanged) {
			t.Fatalf("Stale confirmation: %v", err)
		}
		if err := e.DeleteConfirmed(s.ID, p); err != nil {
			t.Fatal(err)
		}
		var record CarryNotice
		raw, err := os.ReadFile(e.scratchLedger().Temps(dir) + ".kept.json")
		if err != nil || json.Unmarshal(raw, &record) != nil || record.Destination != h.project || !slices.Equal(record.Names, []string{filepath.Base(left[0]), "new.txt"}) {
			t.Fatalf("Recovery record = %s, %v", raw, err)
		}
		if replaced {
			assertScratchNote(t, left[1], "replacement")
		}
	}
}

func TestDeletePreviewListsPartialsBeforeAndAfterRetry(t *testing.T) {
	h := scratchHarness(t)
	s, _, left := cutOffCopy(t, h)
	writeText(t, left[1]+".new", "mine")
	if err := os.Rename(left[1]+".new", left[1]); err != nil {
		t.Fatal(err)
	}
	preview := func(e *Engine) DeletionPreview {
		t.Helper()
		p, err := e.PreviewDelete(s.ID)
		if err != nil || p.Carry == nil || p.Carry.Unavailable || p.Carry.Destination != h.project || !slices.Equal(p.Carry.Names, []string{"new.txt"}) {
			t.Fatalf("Preview = %+v, carry = %+v, err = %v", p, p.Carry, err)
		}
		return p
	}
	p := preview(h.engine)
	assertScratchNote(t, left[0], "ne")
	if err := h.engine.DeleteConfirmed(s.ID, DeletionPreview{Kept: p.Kept}); !errors.Is(err, ErrKeptChanged) {
		t.Fatalf("Delete without names: %v", err)
	}
	if !h.engine.carry(s) {
		t.Fatal("Copy did not finish")
	}
	e := h.reopen(t)
	p = preview(e)
	if err := e.DeleteConfirmed(s.ID, p); err != nil {
		t.Fatal(err)
	}
	assertScratchNote(t, left[1], "mine")
}

func TestUnreadableCopyRecordLeavesDeletionRetryable(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	journal := h.engine.scratchLedger().Temps(dir)
	if err := os.Rename(journal, journal+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(s.ID); err == nil || strings.Count(err.Error(), journal) != 1 || strings.Contains(err.Error(), "destination") {
		t.Fatalf("Copy error = %v, want it to name the record", err)
	}
	assertScratchNote(t, left[0], "ne")
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(journal+".saved", journal); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
}

func TestBodylessDeleteArchivesAnUnavailableDestination(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	if err := os.Rename(h.project, h.project+".away"); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	var record CarryNotice
	raw, err := os.ReadFile(h.engine.scratchLedger().Temps(dir) + ".kept.json")
	if err != nil || json.Unmarshal(raw, &record) != nil || record.Destination != h.project || !slices.Equal(record.Names, []string{filepath.Base(left[0]), "new.txt"}) {
		t.Fatalf("Archived names = %s, %v", raw, err)
	}
	assertScratchNote(t, filepath.Join(dir, "new.txt"), "new")
}

func TestUnreadableDestinationCanBeConfirmed(t *testing.T) {
	for _, which := range []string{"folder", "partial"} {
		t.Run(which, func(t *testing.T) {
			h := scratchHarness(t)
			s, dir, left := cutOffCopy(t, h)
			target, mode := h.project, os.FileMode(0700)
			if which == "partial" {
				target, mode = left[1], 0600
			}
			if err := os.Chmod(target, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(target, mode) })
			probe := left[1]
			if _, err := os.ReadFile(probe); !errors.Is(err, os.ErrPermission) {
				t.Skip("Filesystem does not enforce the denied read")
			}
			p, err := h.engine.PreviewDelete(s.ID)
			if err != nil || p.Carry == nil || !p.Carry.Unavailable || !slices.Equal(p.Carry.Names, []string{filepath.Base(left[0]), "new.txt"}) {
				t.Fatalf("Preview = %+v, notice = %+v, error = %v", p, p.Carry, err)
			}
			var wire map[string]any
			raw, err := json.Marshal(p.Carry)
			if err != nil || json.Unmarshal(raw, &wire) != nil {
				t.Fatal("Invalid copy notice")
			}
			record := h.engine.scratchLedger().Temps(dir) + ".kept.json"
			if wire["recovery"] != record {
				t.Fatalf("Recovery path = %v, want %s", wire["recovery"], record)
			}
			if err := h.engine.DeleteConfirmed(s.ID, p); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(record)
			if err != nil || !strings.Contains(string(data), "new.txt") {
				t.Fatalf("Recovery record = %q, %v", data, err)
			}
			if err := os.Chmod(target, mode); err != nil {
				t.Fatal(err)
			}
			for _, file := range left {
				assertScratchNote(t, file, "ne")
			}
		})
	}
}

func TestBodylessDeleteArchivesACopyThatLostItsDestination(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	writeText(t, left[1]+".new", "mine")
	if err := os.Rename(left[1]+".new", left[1]); err != nil {
		t.Fatal(err)
	}
	if !h.engine.carry(s) || s.Carried.Interrupted != 1 {
		t.Fatal("Copy did not record its partial file")
	}
	if err := os.Rename(h.project, h.project+".away"); err != nil {
		t.Fatal(err)
	}
	e := h.reopen(t)
	if err := e.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	var record CarryNotice
	raw, err := os.ReadFile(e.scratchLedger().Temps(dir) + ".kept.json")
	if err != nil || json.Unmarshal(raw, &record) != nil || !slices.Equal(record.Names, []string{"new.txt"}) {
		t.Fatalf("Recovery record = %q, %v", raw, err)
	}
	assertScratchNote(t, filepath.Join(dir, "new.txt"), "new")
}

func TestDeleteWithUnreadableSourceKeepsThePartial(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	if _, err := os.ReadFile(filepath.Join(dir, "new.txt")); !errors.Is(err, os.ErrPermission) {
		t.Skip("Source still permits reads")
	}
	p, err := h.engine.PreviewDelete(s.ID)
	if err != nil || p.Carry == nil || p.Carry.Unavailable || !slices.Equal(p.Carry.Names, []string{"new.txt"}) {
		t.Fatalf("Notice = %+v, %v", p.Carry, err)
	}
	if err := h.engine.DeleteConfirmed(s.ID, p); err != nil {
		t.Fatal(err)
	}
	assertScratchNote(t, left[1], "ne")
	if _, err := os.Stat(left[0]); !os.IsNotExist(err) {
		t.Fatalf("Temporary file remains: %v", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	assertScratchNote(t, filepath.Join(dir, "new.txt"), "new")
}

// A confirmed delete of a conversation whose copy record cannot be read goes
// ahead, keeping the record and the folder the copy came from.
func TestAnUnreadableCopyRecordIsKeptByAConfirmedDelete(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	journal := h.engine.scratchLedger().Temps(dir)
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := h.engine.PreviewDelete(s.ID)
	if err != nil || p.Kept != dir || p.Carry == nil || !p.Carry.Unreadable || p.Carry.Record != journal || p.Token == "" {
		t.Fatalf("preview = %+v, carry = %+v, %v", p, p.Carry, err)
	}
	if err := h.engine.DeleteConfirmed(s.ID, DeletionPreview{Kept: p.Kept}); !errors.Is(err, ErrKeptChanged) {
		t.Fatalf("a delete not told of the record = %v, want ErrKeptChanged", err)
	}
	if err := h.engine.DeleteConfirmed(s.ID, p); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Get(s.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the session after the delete = %v", err)
	}
	for _, path := range []string{journal, filepath.Join(dir, "new.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s went with the delete: %v", path, err)
		}
	}
	assertScratchNote(t, left[0], "ne")
}

func TestDamagedCopyRecordRequiresItsOwnConfirmation(t *testing.T) {
	for _, kind := range []string{"malformed", "dangling link"} {
		t.Run(kind, func(t *testing.T) {
			h := scratchHarness(t)
			s, dir, left := cutOffCopy(t, h)
			journal := h.engine.scratchLedger().Temps(dir)
			if kind == "malformed" {
				writeText(t, journal, "{\n")
			} else {
				if err := os.Remove(journal); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(journal+".missing", journal); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := h.engine.PreviewDelete(s.ID)
			if err != nil || preview.Carry == nil || !preview.Carry.Unreadable || preview.Token == "" {
				t.Fatalf("Preview = %+v, %v", preview, err)
			}
			reason := "is damaged at line 1"
			if kind == "dangling link" {
				reason = "cannot be read"
			}
			if err := h.engine.Delete(s.ID); err == nil || !strings.Contains(err.Error(), reason) {
				t.Fatalf("Deletion diagnostic = %v, want %q", err, reason)
			}
			if err := h.engine.DeleteConfirmed(s.ID, preview); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(journal); err != nil {
				t.Fatal(err)
			}
			assertScratchNote(t, left[1], "ne")
			assertScratchNote(t, filepath.Join(dir, "new.txt"), "new")
		})
	}
}

func TestMissingCarrySourceWithUnreadableRecordStaysPending(t *testing.T) {
	h := scratchHarness(t)
	s, dir, left := cutOffCopy(t, h)
	journal := h.engine.scratchLedger().Temps(dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(journal, journal+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if h.engine.carry(s) {
		t.Fatal("Move was settled while its copy record was unreadable")
	}
	p, err := h.engine.PreviewDelete(s.ID)
	if err != nil || p.Kept != "" || p.Carry == nil || !p.Carry.Unreadable {
		t.Fatalf("Preview after retry = %+v, %v", p, err)
	}
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(journal+".saved", journal); err != nil {
		t.Fatal(err)
	}
	if !h.engine.carry(s) {
		t.Fatal("Move did not finish after repair")
	}
	assertScratchNote(t, left[1], "ne")
}

func TestDeleteOfMovedSessionForgetsMissingSource(t *testing.T) {
	h := scratchHarness(t)
	s, dir, _ := cutOffCopy(t, h)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	p, err := h.engine.PreviewDelete(s.ID)
	if err != nil || p.Kept != "" {
		t.Fatalf("Preview = %+v, %v", p, err)
	}
	if err := h.engine.DeleteConfirmed(s.ID, p); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.paths.Home, "scratch-owners", filepath.Base(dir)+".owner.json")
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Owner record remains: %v", err)
	}
}

func TestTornCopyRecordResumesAfterRestart(t *testing.T) {
	for _, action := range []string{"resume", "delete"} {
		t.Run(action, func(t *testing.T) {
			h := scratchHarness(t)
			s, dir, left := cutOffCopy(t, h)
			journal := h.engine.scratchLedger().Temps(dir)
			valid, err := os.ReadFile(journal)
			if err != nil {
				t.Fatal(err)
			}
			writeText(t, journal, string(valid)+`{"name":".carry-ab`)
			unknown := filepath.Join(h.project, ".carry-ab")
			writeText(t, unknown, "keep")
			e := h.reopen(t)
			p, err := e.PreviewDelete(s.ID)
			if err != nil || p.Carry != nil {
				t.Fatalf("Preview = %+v, %v", p, err)
			}
			if action == "resume" {
				run, err := e.Get(s.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !e.carry(run) {
					t.Fatal("Copy did not resume")
				}
				assertScratchNote(t, left[1], "new")
			} else {
				if err := e.Delete(s.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(left[1]); !os.IsNotExist(err) {
					t.Fatalf("Partial after deletion: %v", err)
				}
			}
			for _, path := range []string{left[0], journal} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("Cleanup of %s: %v", path, err)
				}
			}
			assertScratchNote(t, unknown, "keep")
		})
	}
}
