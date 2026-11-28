package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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

// A commit draft asked for while the conversation moves reads the folder
// under the session's lock, so the detector finds no race with the move.
func TestADraftDuringAMoveReadsTheFolderSafely(t *testing.T) {
	f := newFixture(t)
	f.engine.SetScratchRoot(filepath.Join(t.TempDir(), "scratch-workspaces"))
	if w := f.do(t, "PUT", "/api/config", map[string]any{"permissions": "full"}); w.Code != http.StatusOK {
		t.Fatalf("config = %d: %s", w.Code, w.Body.String())
	}
	id, _ := decode(t, f.do(t, "POST", "/api/sessions", map[string]any{"scratch": true}))["id"].(string)
	f.waitIdle(t, id)
	f.nextFrames(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"change_directory","arguments":`+
			strconv.Quote(`{"path":`+strconv.Quote(f.project)+`}`)+`}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	f.setFrames(`data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`, `data: [DONE]`)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			f.do(t, "POST", "/api/sessions/"+id+"/git/draft", map[string]any{})
			if sess, err := f.engine.Get(id); err == nil {
				if p, _ := sess.Where(); p == f.project {
					return
				}
			}
		}
	}()
	if w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "move"}); w.Code != http.StatusAccepted {
		t.Fatalf("message = %d: %s", w.Code, w.Body.String())
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the conversation did not move")
	}
	f.waitIdle(t, id)
}

// A repository whose tree holds the scratch folders is refused by every Git
// route, and a repository is not made over them.
func TestTheGitPanelRefusesATreeHoldingScratchFolders(t *testing.T) {
	if !git.Available() {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(os.TempDir()))
	f := newFixture(t)
	f.engine.SetScratchRoot(filepath.Join(f.project, "scratch-workspaces"))
	if w := f.do(t, "POST", "/api/git/init", map[string]string{"project": f.project}); w.Code != http.StatusForbidden {
		t.Errorf("init over the scratch folders = %d, want 403", w.Code)
	}
	if err := git.Init(t.Context(), f.project); err != nil {
		t.Fatal(err)
	}
	body := decode(t, f.do(t, "POST", "/api/sessions", map[string]any{"scratch": true}))
	dir, _ := body["project"].(string)
	writeFileT(t, filepath.Join(dir, "theirs.txt"), "x")

	q := "?project=" + f.project
	for _, path := range []string{"/api/git" + q, "/api/git/log" + q, "/api/git/patch" + q} {
		if w := f.do(t, "GET", path, nil); w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", path, w.Code)
		}
	}
	if w := f.do(t, "POST", "/api/git/stage", map[string]any{"project": f.project, "paths": []string{"."}}); w.Code != http.StatusForbidden {
		t.Errorf("stage = %d, want 403", w.Code)
	}
}
