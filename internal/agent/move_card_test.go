package agent

import (
	"testing"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// The card for a move out of a scratch folder says when the folder moved to
// already holds files, which the copy keeps as they are.
func TestAMoveCardSaysTheFolderIsNotEmpty(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	req, ask, err := h.engine.directoryRequest(s, moveCall(h.project), config.ModeAsk)
	if err != nil || !ask {
		t.Fatalf("request = %v, %v", ask, err)
	}
	if !req.Occupied || PermissionEvent(req).Fields["occupied"] != true {
		t.Error("the card does not say the folder holds files")
	}
	empty := t.TempDir()
	req, _, err = h.engine.directoryRequest(s, moveCall(empty), config.ModeAsk)
	if err != nil {
		t.Fatal(err)
	}
	if req.Occupied {
		t.Error("an empty folder is said to hold files")
	}
}
