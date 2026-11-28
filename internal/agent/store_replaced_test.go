package agent

import (
	"os"
	"testing"
	"time"
)

// When the sessions folder on disk is no longer the one the store opened, no
// scratch folder is removed, by a delete or by the sweep.
func TestNoRemovalAfterTheSessionsFolderIsReplaced(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	if err := os.Rename(h.paths.Sessions, h.paths.Sessions+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(h.paths.Sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	h.engine.Delete(s.ID)
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("a delete removed the folder after the store was replaced: %v", err)
	}
	h.engine.sweepOnce(time.Now().Add(time.Hour))
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the sweep removed the folder after the store was replaced: %v", err)
	}
}

func TestKeptWithAReplacedSessionStore(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	if err := os.Rename(h.paths.Sessions, h.paths.Sessions+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(h.paths.Sessions, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := h.engine.Kept(s.ID); err != nil || got != dir {
		t.Fatalf("Kept = %q, %v; want %q", got, err, dir)
	}
}
