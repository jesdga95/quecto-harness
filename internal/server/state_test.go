package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/mcp"
)

// overlapping registers the state folder as a project the way an older build
// could have, and files a session in it, both behind the checks a new one
// makes.
func overlapping(t *testing.T, f *fixture) (home, id string) {
	t.Helper()
	home = f.cfg.Paths().Home
	if _, err := f.cfg.Update(func(c *config.Config) { c.Projects = append(c.Projects, home) }); err != nil {
		t.Fatal(err)
	}
	id = "sess_aaaaaaaaaaaaaaaa"
	doc := `{"id":"` + id + `","project":` + strconv.Quote(home) + `,"entries":[]}`
	if err := os.WriteFile(filepath.Join(f.cfg.Paths().Sessions, id+".json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "secret.txt"), []byte("state secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, id
}

// A project that overlaps the state folder cannot be added, and one an older
// build registered answers 409 to everything that would work on it, while an
// ordinary project goes on working.
func TestAProjectOverlappingTheStateIsRefused(t *testing.T) {
	f := newFixture(t)
	home, id := overlapping(t, f)
	// The folder holding the state, such as the home folder, is a project,
	// and no repository is made there.
	if w := f.do(t, "POST", "/api/projects", map[string]string{"path": filepath.Dir(home)}); w.Code != http.StatusOK {
		t.Errorf("add the folder holding the state = %d, want 200", w.Code)
	}
	holding, _ := filepath.EvalSymlinks(filepath.Dir(home))
	if w := f.do(t, "POST", "/api/git/init", map[string]string{"project": holding}); w.Code != http.StatusConflict {
		t.Errorf("git init in the folder holding the state = %d %s, want 409", w.Code, w.Body.String())
	}
	q := url.QueryEscape(home)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/sessions/" + id + "/file?path=secret.txt", nil},
		{"GET", "/api/sessions/" + id + "/dir?path=", nil},
		{"GET", "/api/projects/files?q=secret&project=" + q, nil},
		{"GET", "/api/git?project=" + q, nil},
		{"POST", "/api/git/init", map[string]string{"project": home}},
	} {
		if w := f.do(t, c.method, c.path, c.body); w.Code != http.StatusConflict {
			t.Errorf("%s %s = %d %s, want 409", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if w := f.do(t, "POST", "/api/sessions", map[string]string{"project": home}); w.Code != http.StatusConflict {
		t.Errorf("create a session there = %d, want 409", w.Code)
	}
	if w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]any{"text": "hi", "mode": "plan", "effort": "high"}); w.Code != http.StatusConflict {
		t.Errorf("send there = %d, want 409", w.Code)
	}
	doc := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
	if entries, _ := doc["entries"].([]any); len(entries) != 0 {
		t.Errorf("the refused message left %d entries", len(entries))
	}
	if doc["mode"] == "plan" || doc["effort"] == "high" {
		t.Errorf("the refused message changed the session: mode %v, effort %v", doc["mode"], doc["effort"])
	}
	if _, err := os.Stat(filepath.Join(home, ".git")); err == nil {
		t.Error("a repository was made in the state folder")
	}
	if w := f.do(t, "GET", "/api/projects/files?q=a&project="+url.QueryEscape(f.project), nil); w.Code != http.StatusOK {
		t.Errorf("the ordinary project = %d, want 200", w.Code)
	}
}

// A file in the state folder cannot be attached or previewed; a pasted file,
// which is kept there, and an ordinary file still can.
func TestAttachingFromTheStateIsRefused(t *testing.T) {
	f := newFixture(t)
	home := f.cfg.Paths().Home
	secret := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(secret, []byte("state secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ordinary := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(ordinary, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err := f.engine.Create(f.project)
	if err != nil {
		t.Fatal(err)
	}
	if w := f.do(t, "POST", "/api/sessions/"+sess.ID+"/message", map[string]any{"text": "look", "attachments": []string{secret}}); w.Code != http.StatusForbidden {
		t.Errorf("attach a state file = %d, want 403", w.Code)
	}
	// A path that is not in its plain form is refused, so the file weighed
	// is the file opened: through a link, a/../b is not where it reads.
	climb := filepath.Join(filepath.Dir(ordinary), "x") + "/../" + filepath.Base(ordinary)
	if w := f.do(t, "POST", "/api/sessions/"+sess.ID+"/message", map[string]any{"text": "look", "attachments": []string{climb}}); w.Code != http.StatusBadRequest {
		t.Errorf("attach by a path not in plain form = %d, want 400", w.Code)
	}

	data := base64.StdEncoding.EncodeToString([]byte("pasted\n"))
	pasted, _ := decode(t, f.do(t, "POST", "/api/fs/paste", map[string]any{"name": "p.txt", "data": data}))["path"].(string)
	if pasted == "" {
		t.Fatal("no pasted path")
	}
	f.block(t)
	if w := f.do(t, "POST", "/api/sessions/"+sess.ID+"/message", map[string]any{"text": "look", "attachments": []string{pasted, ordinary}}); w.Code >= 400 {
		t.Errorf("attach a pasted and an ordinary file = %d %s", w.Code, w.Body.String())
	}

	saved := filePicker
	t.Cleanup(func() { filePicker = saved })
	filePicker = func(context.Context) ([]string, error) { return []string{secret, ordinary}, nil }
	f.do(t, "POST", "/api/fs/pick", map[string]string{"kind": "file"})
	if w := f.do(t, "GET", "/api/fs/picked?path="+url.QueryEscape(secret), nil); w.Code != http.StatusForbidden {
		t.Errorf("preview a picked state file = %d, want 403", w.Code)
	}
	if w := f.do(t, "GET", "/api/fs/picked?path="+url.QueryEscape(ordinary), nil); w.Code != http.StatusOK {
		t.Errorf("preview a picked ordinary file = %d, want 200", w.Code)
	}
}

// gitInit runs git init with args, away from the user's Git configuration,
// and skips the test when git is not installed.
func gitInit(t *testing.T, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cmd := exec.Command("git", append([]string{"init", "-q"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}

// A project whose repository holds the state folder in its working tree is
// refused by every git route, though the project itself does not overlap it.
func TestARepositoryHoldingTheStateIsRefused(t *testing.T) {
	f := newFixture(t)
	top := filepath.Dir(f.cfg.Paths().Home)
	project := filepath.Join(top, "proj")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if w := f.do(t, "POST", "/api/projects", map[string]string{"path": project}); w.Code != http.StatusOK {
		t.Fatalf("add project = %d %s", w.Code, w.Body.String())
	}
	gitInit(t, top)
	real, _ := filepath.EvalSymlinks(project)
	q := url.QueryEscape(real)
	for _, path := range []string{"/api/git?project=" + q, "/api/git/patch?project=" + q} {
		if w := f.do(t, "GET", path, nil); w.Code != http.StatusConflict {
			t.Errorf("GET %s = %d %s, want 409", path, w.Code, w.Body.String())
		}
	}
}

// A repository whose git directory is kept in the state folder is refused,
// though its working tree is apart from it.
func TestARepositoryKeptInTheStateIsRefused(t *testing.T) {
	f := newFixture(t)
	gitInit(t, "--separate-git-dir="+filepath.Join(f.cfg.Paths().Home, "repo.git"), f.project)
	if w := f.do(t, "GET", "/api/git?project="+url.QueryEscape(f.project), nil); w.Code != http.StatusConflict {
		t.Errorf("GET /api/git = %d %s, want 409", w.Code, w.Body.String())
	}
}

// The MCP probe refuses a server run once per project when the project it
// would run in overlaps the state folder, before anything starts.
func TestTheMCPProbeRefusesAnOverlappingProject(t *testing.T) {
	f := newFixture(t)
	m := mcp.NewManager(filepath.Join(f.cfg.Paths().Home, "mcp"), "", "test")
	f.engine.SetMCP(m)
	t.Cleanup(m.Close)
	home := f.cfg.Paths().Home
	if _, err := f.cfg.Update(func(c *config.Config) { c.Projects = append([]string{home}, c.Projects...) }); err != nil {
		t.Fatal(err)
	}
	w := f.do(t, "POST", "/api/mcp/probe", map[string]any{"name": "x", "command": "/no/such/server", "cwd": "{project}"})
	if w.Code != http.StatusConflict {
		t.Errorf("probe = %d %s, want 409", w.Code, w.Body.String())
	}
}

// A project whose folder is gone answers 404 with what happened, not 500.
func TestAProjectThatIsGoneAnswers404(t *testing.T) {
	f := newFixture(t)
	gone := t.TempDir()
	if _, err := f.cfg.AddProject(gone); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	q := url.QueryEscape(real)
	for _, path := range []string{"/api/git?project=" + q, "/api/projects/files?q=x&project=" + q} {
		w := f.do(t, "GET", path, nil)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "is gone") {
			t.Errorf("GET %s = %d %s, want 404 saying the folder is gone", path, w.Code, w.Body.String())
		}
	}
}

// A state folder that is gone is not reported as a project that is gone.
func TestAMissingStateIsNotAMissingProject(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(f.cfg.Paths().Home); err != nil {
		t.Fatal(err)
	}
	w := f.do(t, "GET", "/api/git?project="+url.QueryEscape(f.project), nil)
	if strings.Contains(w.Body.String(), "project folder is gone") {
		t.Errorf("GET /api/git = %d %s, blamed the project", w.Code, w.Body.String())
	}
}

// Creating a session, resending and compacting in a project whose folder is
// gone answer 404 with the same sentence as every other route.
func TestAProjectThatIsGoneAnswers404ToTheEngine(t *testing.T) {
	f := newFixture(t)
	gone := t.TempDir()
	if _, err := f.cfg.AddProject(gone); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(gone)
	w := f.do(t, "POST", "/api/sessions", map[string]string{"project": real})
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	id, _ := decode(t, w)["id"].(string)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		body any
	}{
		{"/api/sessions", map[string]string{"project": real}},
		{"/api/sessions/" + id + "/resend", map[string]any{"seq": 1, "text": "ho"}},
		{"/api/sessions/" + id + "/compact", nil},
	} {
		w := f.do(t, "POST", c.path, c.body)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "The project folder is gone: it may") {
			t.Errorf("POST %s = %d %s, want 404 saying the folder is gone", c.path, w.Code, w.Body.String())
		}
	}
}

func TestAFileReplacingAProjectDirectoryAnswers404(t *testing.T) {
	for _, component := range []string{"project", "ancestor"} {
		t.Run(component, func(t *testing.T) {
			f := newFixture(t)
			base := t.TempDir()
			folder := filepath.Join(base, "project")
			if err := os.Mkdir(folder, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := f.cfg.AddProject(folder); err != nil {
				t.Fatal(err)
			}
			project, err := filepath.EvalSymlinks(folder)
			if err != nil {
				t.Fatal(err)
			}
			replaced := folder
			if component == "ancestor" {
				replaced = base
			}
			if err := os.RemoveAll(replaced); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(replaced, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			w := f.do(t, "POST", "/api/sessions", map[string]string{"project": project})
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "The project folder is gone") {
				t.Errorf("Create session after replacement = %d %s", w.Code, w.Body.String())
			}
		})
	}
}
