package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// A delete whose removal order cannot be written stops with the conversation
// and its folder in place, and can be made again.
func TestADeleteStopsWhenTheRemovalOrderIsNotWritten(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	owners := filepath.Join(h.paths.Home, "scratch-owners")
	if err := os.Chmod(owners, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(owners, 0o700) })
	if err := h.engine.Delete(s.ID); err == nil {
		t.Fatal("the delete went on without its removal order")
	}
	if _, err := h.engine.Get(s.ID); err != nil {
		t.Errorf("the conversation is gone: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the folder is gone: %v", err)
	}
	os.Chmod(owners, 0o700)
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatalf("the second delete = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder outlived the second delete: %v", err)
	}
}

func TestDeleteWaitsForAnUnreadableOwner(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	owner := filepath.Join(h.paths.Home, "scratch-owners", filepath.Base(dir)+".owner.json")
	original, err := os.ReadFile(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(owner, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(s.ID); err == nil {
		t.Fatal("Delete removed the transcript while its owner record was unreadable")
	}
	if _, err := h.store.Load(s.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(owner, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("Scratch folder after the retry: %v", err)
	}
}
