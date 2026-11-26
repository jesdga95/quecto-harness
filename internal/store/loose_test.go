package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func looseSample(id string) *Session {
	s := sample(id, time.Now().UTC())
	s.Project, s.Loose = "", true
	return s
}

// The id, the loose mark and the project agree on every document written,
// read or indexed.
func TestTheKindOfASessionIsChecked(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	good := looseSample(NewLooseID())
	if err := st.Save(good); err != nil {
		t.Fatal(err)
	}
	bad := []*Session{
		func() *Session { s := looseSample(NewLooseID()); s.Loose = false; return s }(),
		func() *Session { s := looseSample(NewLooseID()); s.Project = "/proj"; return s }(),
		func() *Session { s := sample(NewID(), time.Now()); s.Loose = true; return s }(),
		func() *Session { s := sample(NewID(), time.Now()); s.Project = ""; return s }(),
	}
	for _, s := range bad {
		if err := st.Save(s); err == nil {
			t.Errorf("saved %s loose=%v project=%q", s.ID, s.Loose, s.Project)
		}
		raw, _ := json.Marshal(s)
		if err := os.WriteFile(filepath.Join(dir, s.ID+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Load(s.ID); err == nil {
			t.Errorf("loaded %s loose=%v project=%q", s.ID, s.Loose, s.Project)
		}
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(again.List("")); len(got) != 1 || got[0] != good.ID {
		t.Errorf("a fresh index holds %v, want only %s", got, good.ID)
	}
}

// A mark is read by its name: whatever it holds, the conversation it is filed
// under stays out of sight, and a mark that does not hold that conversation
// finishes nothing.
func TestAMarkThatDoesNotFitHidesAndWaits(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	a, b := looseSample(NewLooseID()), looseSample(NewLooseID())
	for _, s := range []*Session{a, b} {
		if err := st.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(mark{Session: *b})
	must(t, os.WriteFile(filepath.Join(dir, a.ID+".json.deleting"), raw, 0o600))

	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Has(a.ID) || !again.Has(b.ID) {
		t.Errorf("has a=%v b=%v, want only b", again.Has(a.ID), again.Has(b.ID))
	}
	if err := again.Purge(a.ID, func(*Session) error { return nil }); err == nil {
		t.Error("a mark holding another conversation finished a delete")
	}
	if _, err := os.Stat(filepath.Join(dir, a.ID+".json")); err != nil {
		t.Errorf("the conversation's file went: %v", err)
	}
}

// The panel's root is the conversation's scratch folder, opened only through
// real folders.
func TestOpenScratchTakesOnlyRealFolders(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	id := NewLooseID()
	if _, err := st.OpenScratch(id); !errors.Is(err, ErrNoScratch) {
		t.Errorf("an unmade folder = %v, want ErrNoScratch", err)
	}
	if _, err := st.OpenScratch("../x"); err == nil {
		t.Error("opened a folder for an id the harness did not make")
	}
	path, err := st.EnsureScratch(id)
	if err != nil {
		t.Fatal(err)
	}
	must(t, os.WriteFile(filepath.Join(path, "a.txt"), []byte("a"), 0o600))
	root, err := st.OpenScratch(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("a.txt"); err != nil {
		t.Errorf("the root does not hold a.txt: %v", err)
	}
	root.Close()
	for _, d := range []string{path, filepath.Dir(path)} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s is %v, %v", d, info.Mode(), err)
		}
	}
	if filepath.Dir(path) == dir || !strings.HasSuffix(filepath.Dir(path), ".scratch") {
		t.Errorf("the conversation's folder %s is not in the scratch folder beside the state", path)
	}

	other := NewLooseID()
	elsewhere := t.TempDir()
	must(t, os.Symlink(elsewhere, filepath.Join(filepath.Dir(path), other)))
	if _, err := st.OpenScratch(other); err == nil || errors.Is(err, ErrNoScratch) {
		t.Errorf("a link in place of the conversation's folder = %v, want refused", err)
	}
	if _, err := st.EnsureScratch(other); err == nil {
		t.Error("made a folder through a link")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A mark that names a run of another conversation removes nothing of it.
func TestPurgeTakesOnlyItsOwnRuns(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	owner, other := looseSample(NewLooseID()), looseSample(NewLooseID())
	for _, s := range []*Session{owner, other} {
		must(t, st.Save(s))
	}
	theirs := looseSample(NewLooseID())
	theirs.Parent = other.ID
	must(t, st.Save(theirs))
	must(t, os.MkdirAll(filepath.Join(dir, theirs.ID+".files", "spill"), 0o700))
	raw, _ := json.Marshal(mark{Session: *owner, Family: []string{theirs.ID}})
	must(t, os.WriteFile(filepath.Join(dir, owner.ID+".json.deleting"), raw, 0o600))
	must(t, os.Remove(filepath.Join(dir, owner.ID+".json")))

	again, _ := Open(dir)
	released := 0
	if err := again.Purge(owner.ID, func(*Session) error { released++; return nil }); err == nil {
		t.Error("a mark naming another conversation's run finished")
	}
	if released != 0 {
		t.Errorf("released %d documents", released)
	}
	if _, err := os.Stat(filepath.Join(dir, theirs.ID+".files")); err != nil {
		t.Errorf("the other run's folder went: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, theirs.ID+".json")); err != nil {
		t.Errorf("the other run's file went: %v", err)
	}
	if !again.Has(theirs.ID) {
		t.Error("the other conversation lost its run from the index")
	}
	if _, err := again.Load(theirs.ID); err != nil {
		t.Errorf("the other run loads as %v", err)
	}
}

// A mark the disk does not flush is taken back, and one whose removal it does
// not flush either stands: the conversation is out of sight and nothing of it
// is removed until a start can read the folder.
func TestAMarkTheDiskRefuses(t *testing.T) {
	failing := errors.New("injected")
	keepFile, keepDir := syncFile, syncDir
	t.Cleanup(func() { syncFile, syncDir = keepFile, keepDir })

	dir := t.TempDir()
	st, _ := Open(dir)
	a := looseSample(NewLooseID())
	must(t, st.Save(a))
	syncFile = func(string) error { return failing }
	err := st.MarkDeleting(a, nil)
	if err == nil || errors.Is(err, ErrDeletePending) {
		t.Fatalf("a mark that was not flushed = %v, want refused", err)
	}
	if !st.Has(a.ID) {
		t.Error("the refused mark hid the conversation")
	}
	if _, err := os.Stat(filepath.Join(dir, a.ID+".json.deleting")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the mark was left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, a.ID+".json")); err != nil {
		t.Errorf("the conversation's file went: %v", err)
	}

	// The folder is not flushed after the mark, nor after it is taken back:
	// nothing is promised, and a fresh store finds the conversation whole.
	syncFile = keepFile
	syncDir = func(string) error { return failing }
	if err := st.MarkDeleting(a, nil); err == nil || errors.Is(err, ErrDeletePending) {
		t.Fatalf("a mark taken back unflushed = %v, want refused", err)
	}
	syncDir = keepDir
	if again, _ := Open(dir); !again.Has(a.ID) || len(again.Marked()) != 0 {
		t.Errorf("a fresh store has the conversation %v, marks %v", again.Has(a.ID), again.Marked())
	}

	// A mark that cannot be taken back stands, and the next start reads it.
	syncFile = func(string) error {
		must(t, os.Chmod(dir, 0o500))
		return failing
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	err = st.MarkDeleting(a, nil)
	must(t, os.Chmod(dir, 0o700))
	if !errors.Is(err, ErrDeletePending) {
		t.Fatalf("a mark that could not be taken back = %v, want pending", err)
	}
	if st.Has(a.ID) {
		t.Error("the conversation is still listed")
	}
	syncFile = keepFile
	if again, _ := Open(dir); again.Has(a.ID) || len(again.Marked()) != 1 {
		t.Errorf("a fresh store has the conversation %v, marks %v", again.Has(a.ID), again.Marked())
	}
}

// A file of a run without a folder that does not hold the session it is
// filed under leaves the family unknown, and the delete is refused.
func TestAnUnknownRunFileRefusesTheDelete(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	a := looseSample(NewLooseID())
	must(t, st.Save(a))
	must(t, os.WriteFile(filepath.Join(dir, NewLooseID()+".json"), []byte("{}"), 0o600))
	if err := st.MarkDeleting(a, nil); err == nil {
		t.Fatal("marked a conversation while a run file said nothing of itself")
	}
	if !st.Has(a.ID) {
		t.Error("the refused delete hid the conversation")
	}
}

// A recovery removes nothing until the mark it reads is flushed.
func TestPurgeWaitsForTheMarkToBeFlushed(t *testing.T) {
	keepFile := syncFile
	t.Cleanup(func() { syncFile = keepFile })
	dir := t.TempDir()
	st, _ := Open(dir)
	a := looseSample(NewLooseID())
	must(t, st.Save(a))
	kid := looseSample(NewLooseID())
	kid.Parent = a.ID
	must(t, st.Save(kid))
	must(t, os.MkdirAll(filepath.Join(dir, a.ID+".files", "scratch"), 0o700))
	must(t, st.MarkDeleting(a, []string{kid.ID}))

	syncFile = func(string) error { return errors.New("injected") }
	released := 0
	if err := st.Purge(a.ID, func(*Session) error { released++; return nil }); err == nil {
		t.Fatal("purged on a mark that was not flushed")
	}
	if released != 0 {
		t.Errorf("released %d documents", released)
	}
	for _, name := range []string{kid.ID + ".json", a.ID + ".files", a.ID + ".json.deleting"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s went: %v", name, err)
		}
	}
}

// A run whose file is gone and whose folder is not keeps the mark: the delete
// is not finished while a folder no file vouches for is there.
func TestARunFolderWithoutAFileKeepsTheMark(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	a := looseSample(NewLooseID())
	must(t, st.Save(a))
	kid := looseSample(NewLooseID())
	kid.Parent = a.ID
	must(t, st.Save(kid))
	must(t, st.MarkDeleting(a, []string{kid.ID}))
	must(t, os.Remove(filepath.Join(dir, kid.ID+".json")))
	must(t, os.MkdirAll(filepath.Join(dir, kid.ID+".files", "spill"), 0o700))

	if err := st.Purge(a.ID, func(*Session) error { return nil }); err == nil {
		t.Fatal("finished a delete with a folder no file vouches for")
	}
	if _, err := os.Stat(filepath.Join(dir, kid.ID+".files")); err != nil {
		t.Errorf("the folder went: %v", err)
	}
	if len(st.Marked()) != 1 {
		t.Errorf("marks = %v, want the delete kept", st.Marked())
	}
	must(t, os.RemoveAll(filepath.Join(dir, kid.ID+".files")))
	if err := st.Purge(a.ID, func(*Session) error { return nil }); err != nil {
		t.Fatalf("with the folder gone the delete = %v", err)
	}
}

// A purge that cannot reach the conversation's folder keeps the mark.
func TestAPurgeThatCannotReachTheFolderWaits(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir)
	scratch := filepath.Join(t.TempDir(), "scratch")
	st.SetScratch(scratch)
	a := looseSample(NewLooseID())
	must(t, st.Save(a))
	folder, err := st.EnsureScratch(a.ID)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(folder, "work.txt"), []byte("x"), 0o600))
	must(t, st.MarkDeleting(a, nil))
	must(t, os.Chmod(scratch, 0))
	t.Cleanup(func() { os.Chmod(scratch, 0o700) })
	if err := st.Purge(a.ID, func(*Session) error { return nil }); err == nil {
		t.Fatal("a purge that could not reach the folder finished")
	}
	if len(st.Marked()) != 1 {
		t.Errorf("marks = %v, want the delete kept", st.Marked())
	}
	must(t, os.Chmod(scratch, 0o700))
	must(t, st.Purge(a.ID, func(*Session) error { return nil }))
	if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the folder outlived the delete: %v", err)
	}
}
