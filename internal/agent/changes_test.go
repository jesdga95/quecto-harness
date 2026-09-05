package agent

import (
	"os"
	"path/filepath"
	"testing"

	"quecto-harness/internal/config"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

func changeFor(t *testing.T, files []FileChange, path string) FileChange {
	t.Helper()
	for _, f := range files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%s is not in the change list: %+v", path, files)
	return FileChange{}
}

// The baseline is the file as the session found it, so two writes to one path
// still compare against what was there before the first.
func TestTwoWritesKeepOneBaseline(t *testing.T) {
	h := newHarness(t, "stream_write_once.txt", "stream_plain.txt",
		"stream_write_twice.txt", "stream_plain.txt")
	h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })
	s := h.session(t)
	// A named session skips the title request, so the fixtures are consumed
	// by the two turns in the order they are listed.
	if err := h.engine.Rename(s.ID, "Named"); err != nil {
		t.Fatal(err)
	}

	h.engine.Send(s.ID, "write once", nil)
	waitIdle(t, s)
	h.engine.Send(s.ID, "write again", nil)
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	if len(doc.Baselines) != 1 {
		t.Fatalf("baselines = %+v, want one", doc.Baselines)
	}
	if doc.Baselines[0].Path != "file.txt" || doc.Baselines[0].State != store.BaselineText {
		t.Fatalf("baseline = %+v", doc.Baselines[0])
	}
	kept, err := h.store.Blob(s.ID, doc.Baselines[0].Blob)
	if err != nil || string(kept) != "one\ntwo\n" {
		t.Fatalf("the copy is %q %v, want the file as the session found it", kept, err)
	}

	files, err := h.engine.Changes(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	f := changeFor(t, files, "file.txt")
	if f.Status != StatusChanged {
		t.Errorf("status = %q, want %q", f.Status, StatusChanged)
	}
	// "one/two" became "one/four", not "one/three" then "one/four".
	if f.Added != 1 || f.Removed != 1 {
		t.Errorf("counts = +%d -%d, want +1 -1", f.Added, f.Removed)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("hunks = %+v", f.Hunks)
	}
	var added, removed string
	for _, l := range f.Hunks[0].Lines {
		switch l.Kind {
		case "added":
			added = l.Text
		case "removed":
			removed = l.Text
		}
	}
	if removed != "two" || added != "four" {
		t.Errorf("the hunk turns %q into %q, want two into four", removed, added)
	}
}

// Each state a file can be in has a row, so the list stays truthful about
// everything the session touched.
func TestChangesReportsEveryState(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(h.project, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Files as they stood before the session wrote, recorded the way a write
	// records them.
	write("changed.txt", "before\n")
	write("same.txt", "steady\n")
	write("gone.txt", "doomed\n")
	for _, name := range []string{"changed.txt", "same.txt", "gone.txt"} {
		data, _ := os.ReadFile(filepath.Join(h.project, name))
		h.engine.recordBaseline(s, tools.Snapshot{Path: name, State: tools.SnapshotText, Content: data})
	}
	h.engine.recordBaseline(s, tools.Snapshot{Path: "made.txt", State: tools.SnapshotNew})
	h.engine.recordBaseline(s, tools.Snapshot{Path: "blob.bin", State: tools.SnapshotBinary})
	h.engine.recordBaseline(s, tools.Snapshot{Path: "huge.txt", State: tools.SnapshotLarge})

	// What each of them looks like now.
	write("changed.txt", "after\n")
	write("made.txt", "fresh\n")
	write("blob.bin", "text now, but it was not before\n")
	write("huge.txt", "small now\n")
	if err := os.Remove(filepath.Join(h.project, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write("binary.bin", "\x00\x01\x02\x00")
	h.engine.recordBaseline(s, tools.Snapshot{Path: "binary.bin", State: tools.SnapshotText, Content: []byte("was text\n")})

	files, err := h.engine.Changes(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"changed.txt": StatusChanged,
		"same.txt":    StatusSame,
		"gone.txt":    StatusDeleted,
		"made.txt":    StatusNew,
		"blob.bin":    StatusBinary,
		"huge.txt":    StatusLarge,
		"binary.bin":  StatusBinary,
	}
	if len(files) != len(want) {
		t.Fatalf("the list has %d rows, want %d: %+v", len(files), len(want), files)
	}
	for path, status := range want {
		if got := changeFor(t, files, path).Status; got != status {
			t.Errorf("%s = %q, want %q", path, got, status)
		}
	}
	if f := changeFor(t, files, "made.txt"); f.Added != 1 || f.Removed != 0 {
		t.Errorf("a created file reads +%d -%d, want +1 -0", f.Added, f.Removed)
	}
	if f := changeFor(t, files, "same.txt"); len(f.Hunks) != 0 {
		t.Errorf("an unchanged file carries hunks: %+v", f.Hunks)
	}
	// The order is the order the files were first written.
	if files[0].Path != "changed.txt" || files[len(files)-1].Path != "binary.bin" {
		t.Errorf("order = %q", pathsOf(files))
	}
}

// A session that has written nothing has an empty list rather than an error.
func TestChangesOnAnUntouchedSession(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	files, err := h.engine.Changes(s.ID)
	if err != nil || len(files) != 0 {
		t.Fatalf("changes = %+v %v", files, err)
	}
}

// The copies belong to the session, so deleting it takes them with it.
func TestDeletingASessionRemovesItsCopies(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := h.session(t)
	h.engine.recordBaseline(s, tools.Snapshot{Path: "file.txt",
		State: tools.SnapshotText, Content: []byte("one\ntwo\n")})

	doc, _, _, _ := s.Snapshot()
	name := doc.Baselines[0].Blob
	if _, err := h.store.Blob(s.ID, name); err != nil {
		t.Fatalf("the copy was not kept: %v", err)
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Blob(s.ID, name); err == nil {
		t.Error("the copy outlived the session it belonged to")
	}
}

func pathsOf(files []FileChange) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}
