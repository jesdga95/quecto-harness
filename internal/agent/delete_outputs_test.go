package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// A delete whose record cannot be removed keeps the session whole: its saved
// output stays, and the delete can be made again once the folder allows it.
func TestAFailedDeleteKeepsTheSavedOutput(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	spill := h.engine.store.SpillDir(s.ID)
	if err := os.MkdirAll(spill, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(spill, "out.txt")
	if err := os.WriteFile(out, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(h.paths.Sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h.paths.Sessions, 0o700) })
	if err := h.engine.Delete(s.ID); err == nil {
		t.Fatal("the delete succeeded with the record in a read-only folder")
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the failed delete took the saved output: %v", err)
	}
	if _, err := h.engine.Get(s.ID); err != nil {
		t.Errorf("the session is gone after a failed delete: %v", err)
	}
	os.Chmod(h.paths.Sessions, 0o700)
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(spill)); !os.IsNotExist(err) {
		t.Errorf("the saved output outlived the delete: %v", err)
	}
}
