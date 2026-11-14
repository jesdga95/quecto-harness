package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/git"
)

// TestGitRoutes drives a project from no repository to a commit through the
// routes the panel calls, against the real git binary.
func TestGitRoutes(t *testing.T) {
	if !git.Available() {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(os.TempDir()))
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	f := newFixture(t)
	q := "?project=" + f.project

	// A folder with no repository is a state, not an error.
	body := decode(t, f.do(t, "GET", "/api/git"+q, nil))
	if body["available"] != true || body["repo"] != false {
		t.Fatalf("before init = %v", body)
	}
	if w := f.do(t, "POST", "/api/git/init", map[string]string{"project": f.project}); w.Code != http.StatusOK {
		t.Fatalf("init = %d: %s", w.Code, w.Body.String())
	}
	if err := os.WriteFile(filepath.Join(f.project, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	body = decode(t, f.do(t, "GET", "/api/git"+q, nil))
	st, _ := body["status"].(map[string]any)
	unstaged, _ := st["unstaged"].([]any)
	if body["repo"] != true || len(unstaged) != 1 {
		t.Fatalf("after a write = %v", body)
	}
	if file, _ := unstaged[0].(map[string]any); file["path"] != "a.txt" || file["state"] != "untracked" {
		t.Fatalf("file = %v", file)
	}

	if w := f.do(t, "POST", "/api/git/stage", map[string]any{"project": f.project, "paths": []string{"a.txt"}}); w.Code != http.StatusOK {
		t.Fatalf("stage = %d: %s", w.Code, w.Body.String())
	}
	w := f.do(t, "POST", "/api/git/commit", map[string]any{"project": f.project, "message": "First"})
	if w.Code != http.StatusOK {
		t.Fatalf("commit = %d: %s", w.Code, w.Body.String())
	}
	if hash, _ := decode(t, w)["hash"].(string); hash == "" {
		t.Error("the commit reply carries no hash")
	}

	body = decode(t, f.do(t, "GET", "/api/git/log"+q, nil))
	commits, _ := body["commits"].([]any)
	if len(commits) != 1 {
		t.Fatalf("log = %v", body)
	}
	if c, _ := commits[0].(map[string]any); c["subject"] != "First" {
		t.Errorf("commit = %v", c)
	}

	// A failure carries git's account of it, under the operation's name.
	w = f.do(t, "POST", "/api/git/commit", map[string]any{"project": f.project, "message": "Nothing"})
	if w.Code != http.StatusConflict {
		t.Fatalf("empty commit = %d: %s", w.Code, w.Body.String())
	}
	if msg, _ := decode(t, w)["error"].(string); len(msg) < len("Commit failed: ") || msg[:15] != "Commit failed: " {
		t.Errorf("error = %q", msg)
	}

	if w := f.do(t, "GET", "/api/git?project=/nowhere", nil); w.Code != http.StatusNotFound {
		t.Errorf("an unknown project = %d", w.Code)
	}
}
