package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// caseBlind is a project holding the state folder on a volume that ignores
// case, or skips the test where the volume tells case apart.
func caseBlind(t *testing.T) (base, state string) {
	t.Helper()
	base, _ = filepath.EvalSymlinks(t.TempDir())
	state = filepath.Join(base, ".quecto-harness")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"token": "needle token\n", "config.json": "{}\n"} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, ".QUECTO-HARNESS")); err != nil {
		t.Skip("the volume tells case apart")
	}
	return base, state
}

// Another casing of the state folder, written directly, through a link, or as
// another casing of the whole path, reaches nothing for any tool.
func TestAnotherCasingDoesNotReachTheState(t *testing.T) {
	base, state := caseBlind(t)
	if err := os.Symlink(filepath.Join(base, ".QUECTO-HARNESS"), filepath.Join(base, "lnk")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "notes.txt"), []byte("needle notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(base, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	r.Hide(state)
	ctx := context.Background()
	upper := strings.ToUpper(base)

	paths := []string{
		".QUECTO-HARNESS/token",
		".Quecto-Harness/config.json",
		"lnk/token",
		filepath.Join(upper, ".quecto-harness", "token"),
	}
	for _, p := range paths {
		arg, _ := json.Marshal(map[string]string{"path": p})
		if _, err := r.ReadFile(ctx, arg); err == nil || !strings.Contains(err.Error(), "state folder") {
			t.Errorf("read %s = %v, want it refused", p, err)
		}
		if r.Outside(p) {
			t.Errorf("Outside(%s) = true: the state folder would be put to a card as a folder outside", p)
		}
		w, _ := json.Marshal(map[string]string{"path": p, "content": "{}"})
		if _, err := r.WriteFile(ctx, w); err == nil {
			t.Errorf("write %s was allowed", p)
		}
		e, _ := json.Marshal(map[string]string{"path": p, "old_string": "{}", "new_string": "[]"})
		if _, err := r.ReplaceInFile(ctx, e); err == nil {
			t.Errorf("edit %s was allowed", p)
		}
		if _, err := ViewFile(base, state, p, 1<<20); !errors.Is(err, ErrHidden) && !errors.Is(err, ErrOutside) {
			t.Errorf("ViewFile(%s) = %v, want it refused", p, err)
		}
	}
	if _, err := r.WriteFile(ctx, json.RawMessage(`{"path":".QUECTO-HARNESS/new.json","content":"{}"}`)); err == nil {
		t.Error("a new file in another casing of the state folder was allowed")
	}
	if _, _, err := ListDir(base, state, ".QUECTO-HARNESS"); !errors.Is(err, ErrHidden) {
		t.Errorf("ListDir(.QUECTO-HARNESS) = %v, want it hidden", err)
	}
	for _, path := range []string{".QUECTO-HARNESS", ""} {
		arg, _ := json.Marshal(map[string]string{"text": "needle", "path": path})
		res, err := r.Find(ctx, arg)
		if path != "" {
			if err == nil {
				t.Errorf("grep in %q = %q, want it refused", path, res.Output)
			}
			continue
		}
		if err != nil || strings.Contains(res.Output, "token") || !strings.Contains(res.Output, "notes.txt") {
			t.Errorf("grep from the root = %q, %v, want the notes alone", res.Output, err)
		}
	}
}

// A project root written in another casing still walks past the state folder.
func TestARootInAnotherCasingSkipsTheState(t *testing.T) {
	base, state := caseBlind(t)
	if err := os.WriteFile(filepath.Join(base, "notes.txt"), []byte("needle notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	upper := strings.ToUpper(base)
	r, err := NewRunner(upper, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	r.Hide(state)
	res, err := r.Find(context.Background(), json.RawMessage(`{"text":"needle"}`))
	if err != nil || strings.Contains(res.Output, "token") {
		t.Errorf("grep = %q, %v, want the state folder skipped", res.Output, err)
	}
	_, entries, err := ListDir(upper, state, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, ".quecto-harness") {
			t.Errorf("listing shows the state folder: %+v", entries)
		}
	}
	if found, _ := FindFiles(upper, state, "token", 30); len(found) != 0 {
		t.Errorf("FindFiles = %v, want the state folder skipped", found)
	}
}

// A state folder replaced after the runner was made is caught by another
// casing all the same.
func TestAReplacedStateFolderIsStillCaughtByAnotherCasing(t *testing.T) {
	base, state := caseBlind(t)
	r, err := NewRunner(base, config.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	r.Hide(state)
	if err := os.RemoveAll(state); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "token"), []byte("needle token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arg, _ := json.Marshal(map[string]string{"path": ".QUECTO-HARNESS/token"})
	if _, err := r.ReadFile(context.Background(), arg); err == nil || !strings.Contains(err.Error(), "state folder") {
		t.Errorf("read of the replaced folder in another casing = %v, want it refused", err)
	}
}
