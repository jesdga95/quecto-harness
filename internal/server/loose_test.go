package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/agent"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/store"
)

// createLoose starts a conversation without a folder through the API.
func (f *fixture) createLoose(t *testing.T) string {
	t.Helper()
	w := f.do(t, "POST", "/api/sessions", map[string]any{"loose": true})
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	body := decode(t, w)
	id, _ := body["id"].(string)
	if !store.IsLoose(id) || body["loose"] != true || body["project"] != "" {
		t.Fatalf("created %v", body)
	}
	return id
}

func listed(t *testing.T, f *fixture, query string) ([]string, int) {
	t.Helper()
	w := f.do(t, "GET", "/api/sessions"+query, nil)
	if w.Code != http.StatusOK {
		return nil, w.Code
	}
	var out []string
	rows, _ := decode(t, w)["sessions"].([]any)
	for _, r := range rows {
		m, _ := r.(map[string]any)
		out = append(out, m["id"].(string))
	}
	return out, w.Code
}

// A conversation is made in a project or without a folder, never both or
// neither, and is listed with every root and on its own.
func TestLooseSessionsAreMadeAndListed(t *testing.T) {
	f := newFixture(t)
	for _, body := range []map[string]any{{"loose": true, "project": f.project}, {}, {"loose": false}} {
		if w := f.do(t, "POST", "/api/sessions", body); w.Code != http.StatusBadRequest {
			t.Errorf("create %v = %d, want 400", body, w.Code)
		}
	}
	id := f.createLoose(t)
	w := f.do(t, "POST", "/api/sessions", map[string]any{"project": f.project})
	inProject, _ := decode(t, w)["id"].(string)
	// Both are written by the engine's writer, so the listing waits for it.
	deadline := time.Now().Add(5 * time.Second)
	for !(f.srv.store.Has(id) && f.srv.store.Has(inProject)) && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	all, _ := listed(t, f, "")
	if !slices.Contains(all, id) || !slices.Contains(all, inProject) {
		t.Errorf("every root = %v", all)
	}
	if again, _ := listed(t, f, "?project="); !slices.Equal(again, all) {
		t.Errorf("an empty project lists %v, want every root %v", again, all)
	}
	if got, _ := listed(t, f, "?project="+url.QueryEscape(f.project)); !slices.Equal(got, []string{inProject}) {
		t.Errorf("the project lists %v", got)
	}
	if got, _ := listed(t, f, "?scope=loose"); !slices.Equal(got, []string{id}) {
		t.Errorf("scope=loose lists %v", got)
	}
	for _, q := range []string{"?scope=all", "?scope=", "?scope=loose&project=" + url.QueryEscape(f.project)} {
		if _, code := listed(t, f, q); code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", q, code)
		}
	}

	body := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
	dir, _ := f.srv.store.ScratchDir(id)
	if body["loose"] != true || body["root"] != dir {
		t.Errorf("the session reads loose=%v root=%v, want %s", body["loose"], body["root"], dir)
	}
	if w := f.do(t, "POST", "/api/sessions/"+id+"/git/draft", nil); w.Code != http.StatusConflict {
		t.Errorf("git draft = %d, want 409", w.Code)
	}
}

// The panel of a conversation without a folder reads its folder and nothing
// a link inside it leads to outside, by relative or absolute path.
func TestTheLoosePanelStaysInItsFolder(t *testing.T) {
	f := newFixture(t)
	id := f.createLoose(t)
	get := func(path string) *httptestResponse {
		w := f.do(t, "GET", path, nil)
		return &httptestResponse{w.Code, w.Body.String()}
	}

	if w := get("/api/sessions/" + id + "/dir"); w.code != http.StatusOK {
		t.Errorf("an unmade folder lists %d: %s", w.code, w.body)
	}
	if w := get("/api/sessions/" + id + "/file?path=a.txt"); w.code != http.StatusNotFound {
		t.Errorf("a file in an unmade folder = %d", w.code)
	}

	dir, err := f.srv.store.EnsureScratch(id)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o600))
	must(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))
	must(t, os.Symlink("a.txt", filepath.Join(dir, "in.txt")))
	must(t, os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "out.txt")))
	must(t, os.Symlink(outside, filepath.Join(dir, "away")))
	must(t, os.Mkdir(filepath.Join(dir, "notes "), 0o700))
	if w := get("/api/sessions/" + id + "/dir?path=" + url.QueryEscape("notes ")); w.code != http.StatusOK {
		t.Errorf("a folder whose name ends in a space lists %d: %s", w.code, w.body)
	}

	body := decode(t, f.do(t, "GET", "/api/sessions/"+id+"/dir", nil))
	var names []string
	for _, e := range body["entries"].([]any) {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	if !slices.Equal(names, []string{"notes ", "sub", "a.txt", "in.txt"}) || body["root"] != dir {
		t.Errorf("the folder lists %v at %v", names, body["root"])
	}

	for path, want := range map[string]int{
		"a.txt":                              http.StatusOK,
		"in.txt":                             http.StatusOK,
		filepath.Join(dir, "a.txt"):          http.StatusOK,
		"out.txt":                            http.StatusForbidden,
		"away/secret.txt":                    http.StatusForbidden,
		"../" + id + ".json":                 http.StatusForbidden,
		filepath.Join(outside, "secret.txt"): http.StatusForbidden,
		"sub":                                http.StatusBadRequest,
	} {
		w := get("/api/sessions/" + id + "/file?path=" + url.QueryEscape(path))
		if w.code != want {
			t.Errorf("file %s = %d, want %d: %s", path, w.code, want, w.body)
		}
	}

	files := decode(t, f.do(t, "GET", "/api/sessions/"+id+"/files?q=txt", nil))["files"].([]any)
	var found []string
	for _, v := range files {
		found = append(found, v.(string))
	}
	if !slices.Equal(found, []string{"a.txt", "in.txt"}) {
		t.Errorf("mentions find %v", found)
	}
}

// A card in a conversation without a folder refuses allow always and stays
// to be answered once.
func TestALooseCardRefusesAllowAlways(t *testing.T) {
	f := newFixture(t)
	id := f.createLoose(t)
	req := perms.Request{ID: perms.NewRequestID(), SessionID: id, Tool: "bash", Command: "ls -la", Prefix: "ls -la", OnceOnly: true}
	done := make(chan perms.Decision, 1)
	go func() {
		d, _ := f.perms.Ask(context.Background(), req)
		done <- d
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := f.perms.Lookup(req.ID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the card never appeared")
		}
		time.Sleep(2 * time.Millisecond)
	}
	w := f.do(t, "POST", "/api/permissions/"+req.ID, map[string]any{"decision": "always", "prefix": "ls"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("allow always = %d, want 400", w.Code)
	}
	if w := f.do(t, "POST", "/api/permissions/"+req.ID, map[string]any{"decision": "once"}); w.Code != http.StatusOK {
		t.Fatalf("allow once = %d: %s", w.Code, w.Body.String())
	}
	if d := <-done; d.Kind != perms.DecideOnce {
		t.Errorf("the call got %s", d.Kind)
	}
	if got := f.cfg.Allowlist(""); len(got) != 0 {
		t.Errorf("a rule was kept: %v", got)
	}
}

type httptestResponse struct {
	code int
	body string
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// The echo of a named message keeps the entry's number through the hub,
// apart from the stream's own cursor.
func TestTheEchoKeepsItsEntryThroughTheHub(t *testing.T) {
	f := newFixture(t)
	id := f.createLoose(t)
	for range 7 {
		f.hub.Publish(id, agent.Event{Type: "status", Fields: map[string]any{"state": "idle"}})
	}
	sent, err := f.engine.SendAs(id, "hello", nil, "c-echo")
	if err != nil {
		t.Fatal(err)
	}
	_, stop, replay := f.hub.Subscribe(id, 1)
	defer stop()
	for _, fr := range replay {
		if fr.Type != "message" {
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(fr.Data, &got); err != nil {
			t.Fatal(err)
		}
		if got["entry"] != float64(sent.Seq) || got["client"] != "c-echo" || got["seq"] != float64(fr.Seq) {
			t.Errorf("the echo reads %v, want entry %d and cursor %d", got, sent.Seq, fr.Seq)
		}
		return
	}
	t.Fatal("no echo reached the hub")
}
