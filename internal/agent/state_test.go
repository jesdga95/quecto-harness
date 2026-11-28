package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// A session in a project that overlaps the state folder takes no work, and is
// left as it was: a resend does not rewind it and a compaction does not start.
func TestASessionOverlappingTheStateTakesNoWork(t *testing.T) {
	h := newHarness(t)
	home := h.cfg.Paths().Home
	if _, err := h.cfg.Update(func(c *config.Config) { c.Projects = append(c.Projects, home) }); err != nil {
		t.Fatal(err)
	}
	id := "sess_aaaaaaaaaaaaaaaa"
	doc := `{"id":"` + id + `","project":` + strconv.Quote(home) +
		`,"entries":[{"seq":1,"kind":"user","role":"user","content":"hi","in_context":true,"visible":true}]}`
	if err := os.WriteFile(filepath.Join(h.paths.Sessions, id+".json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Create(home); !errors.Is(err, config.ErrStateOverlap) {
		t.Errorf("Create = %v", err)
	}
	if _, err := h.engine.Send(id, "again", nil); !errors.Is(err, config.ErrStateOverlap) {
		t.Errorf("Send = %v", err)
	}
	if _, err := h.engine.Resend(id, 1, nil); !errors.Is(err, config.ErrStateOverlap) {
		t.Errorf("Resend = %v", err)
	}
	if err := h.engine.Compact(id); !errors.Is(err, config.ErrStateOverlap) {
		t.Errorf("Compact = %v", err)
	}
	s, err := h.engine.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	got, state, _, _ := s.Snapshot()
	if len(got.Entries) != 1 || got.Entries[0].Content != "hi" || state != StateIdle {
		t.Errorf("the session changed: %d entries, %s", len(got.Entries), state)
	}
}
