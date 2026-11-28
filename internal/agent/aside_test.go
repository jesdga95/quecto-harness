package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/store"
)

// A folder set aside for removal that no removal order names is kept by the
// sweep, though an identity file beside it matches.
func TestASweepKeepsAFolderSetAsideWithNoOrder(t *testing.T) {
	h := scratchHarness(t)
	os.MkdirAll(h.paths.Scratch, 0o700)
	aside := filepath.Join(h.paths.Scratch, ".removal-aaaaaaaaaaaa")
	os.MkdirAll(aside, 0o700)
	writeText(t, filepath.Join(aside, "keep.txt"), "x")
	info, _ := os.Lstat(aside)
	writeText(t, aside+".id", store.FileID(info))
	h.engine.sweepOnce(time.Now())
	if _, err := os.Stat(filepath.Join(aside, "keep.txt")); err != nil {
		t.Errorf("the folder set aside was removed: %v", err)
	}
}
