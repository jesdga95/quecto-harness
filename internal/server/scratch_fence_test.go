package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/agent"
	"github.com/jesdga95/quecto-harness/internal/store"
)

// A project that holds the scratch folders, as a home folder does, stays
// usable, but its mentions and its file panel pass over the scratch folders
// of other conversations. A scratch folder's files are found only for the
// conversation it belongs to.
func TestScratchFoldersOfOtherConversationsAreNotListed(t *testing.T) {
	f := newFixture(t)
	f.engine.SetScratchRoot(filepath.Join(f.project, "scratch-workspaces"))
	if _, err := f.engine.AdmitFolder(f.project); err != nil {
		t.Errorf("a folder holding the scratch folders is refused: %v", err)
	}
	mk := func() (id, dir string) {
		body := decode(t, f.do(t, "POST", "/api/sessions", map[string]any{"scratch": true}))
		id, _ = body["id"].(string)
		dir, _ = body["project"].(string)
		return id, dir
	}
	aID, aDir := mk()
	bID, bDir := mk()
	os.WriteFile(filepath.Join(aDir, "a-notes.md"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(bDir, "b-notes.md"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(f.project, "notes.md"), []byte("x"), 0o600)

	files := func(project, session string) (int, []string) {
		q := url.Values{"project": {project}, "q": {"notes"}}
		if session != "" {
			q.Set("session", session)
		}
		w := f.do(t, "GET", "/api/projects/files?"+q.Encode(), nil)
		if w.Code != http.StatusOK {
			return w.Code, nil
		}
		var out []string
		for _, v := range decode(t, w)["files"].([]any) {
			out = append(out, filepath.Base(v.(string)))
		}
		slices.Sort(out)
		return w.Code, out
	}
	if code, _ := files(aDir, ""); code != http.StatusNotFound {
		t.Errorf("a scratch folder with no session = %d, want 404", code)
	}
	if code, _ := files(aDir, bID); code != http.StatusNotFound {
		t.Errorf("a scratch folder for another session = %d, want 404", code)
	}
	if _, got := files(aDir, aID); !slices.Equal(got, []string{"a-notes.md"}) {
		t.Errorf("the conversation's own folder lists %v", got)
	}
	if _, got := files(f.project, ""); !slices.Equal(got, []string{"notes.md"}) {
		t.Errorf("the project lists %v, want no scratch folder's files", got)
	}

	s, err := f.engine.Create(f.project)
	if err != nil {
		t.Fatal(err)
	}
	list := decode(t, f.do(t, "GET", "/api/sessions/"+s.ID+"/dir?path=scratch-workspaces", nil))
	if entries, _ := list["entries"].([]any); len(entries) != 0 {
		t.Errorf("the panel lists %v in the scratch folders", entries)
	}
	inner := "scratch-workspaces/" + filepath.Base(bDir)
	if w := f.do(t, "GET", "/api/sessions/"+s.ID+"/dir?path="+url.QueryEscape(inner), nil); w.Code != http.StatusForbidden {
		t.Errorf("another conversation's folder in the panel = %d, want 403", w.Code)
	}
}

// The state says whether a scratch folder can be removed here, which takes a
// file identity the system gives.
func TestStateSaysWhetherFoldersAreRemoved(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "GET", "/api/state", nil))
	if got, ok := body["removes_folders"].(bool); !ok || got != store.KnowsFileID {
		t.Errorf("removes_folders = %v, want %v", body["removes_folders"], store.KnowsFileID)
	}
}

// The delete dialog asks which folder stays. A scratch folder its owner
// record lets go is not named; one with no owner record is.
func TestKeptRouteNamesTheFolderThatStays(t *testing.T) {
	f := newFixture(t)
	f.engine.SetScratchRoot(filepath.Join(t.TempDir(), "scratch-workspaces"))
	body := decode(t, f.do(t, "POST", "/api/sessions", map[string]any{"scratch": true}))
	id, _ := body["id"].(string)
	dir, _ := body["project"].(string)
	if got := decode(t, f.do(t, "GET", "/api/sessions/"+id+"/kept", nil)); got["kept"] != "" {
		t.Errorf("kept = %v, want none", got)
	}
	os.Remove(filepath.Join(f.cfg.Paths().Home, "scratch-owners", filepath.Base(dir)+".owner.json"))
	if got := decode(t, f.do(t, "GET", "/api/sessions/"+id+"/kept", nil)); got["kept"] != dir {
		t.Errorf("kept = %v, want %s", got, dir)
	}
	if w := f.do(t, "GET", "/api/sessions/sess_aaaaaaaaaaaaaaaa/kept", nil); w.Code != http.StatusNotFound {
		t.Errorf("an unknown session = %d, want 404", w.Code)
	}
}

// A delete carries the folder the dialog said stays. When what would stay has
// changed since, the delete is refused with a conflict and nothing is removed,
// even when the change would let the folder go.
func TestADeleteKeepsWhatTheDialogPromised(t *testing.T) {
	f := newFixture(t)
	f.engine.SetScratchRoot(filepath.Join(t.TempDir(), "scratch-workspaces"))
	body := decode(t, f.do(t, "POST", "/api/sessions", map[string]any{"scratch": true}))
	id, _ := body["id"].(string)
	dir, _ := body["project"].(string)
	writeFileT(t, filepath.Join(dir, "doc.txt"), "the only copy")
	owner := filepath.Join(f.cfg.Paths().Home, "scratch-owners", filepath.Base(dir)+".owner.json")
	record, err := os.ReadFile(owner)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(owner)
	if got := decode(t, f.do(t, "GET", "/api/sessions/"+id+"/kept", nil)); got["kept"] != dir {
		t.Fatalf("kept = %v, want %s", got, dir)
	}
	if w := f.do(t, "DELETE", "/api/sessions/"+id, map[string]any{"kept": ""}); w.Code != http.StatusConflict {
		t.Errorf("a delete told nothing stays = %d, want 409", w.Code)
	}
	os.WriteFile(owner, record, 0o600)
	w := f.do(t, "DELETE", "/api/sessions/"+id, map[string]any{"kept": dir})
	if w.Code != http.StatusConflict {
		t.Errorf("a delete told %s stays = %d, want 409", dir, w.Code)
	}
	if msg, _ := decode(t, w)["error"].(string); msg == "" || msg[:1] != strings.ToUpper(msg[:1]) {
		t.Errorf("error = %q, want a sentence", msg)
	}
	if _, err := os.Stat(filepath.Join(dir, "doc.txt")); err != nil {
		t.Fatalf("the folder the dialog promised to keep went: %v", err)
	}
	if got := decode(t, f.do(t, "GET", "/api/sessions/"+id+"/kept", nil)); got["kept"] != "" {
		t.Fatalf("kept = %v, want none", got)
	}
	if w := f.do(t, "DELETE", "/api/sessions/"+id, map[string]any{"kept": ""}); w.Code != http.StatusNoContent {
		t.Fatalf("the confirmed delete = %d, want 204", w.Code)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder the dialog said goes is still there: %v", err)
	}
}

func writeFileT(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteBodyRequiresAStringKeptFolder(t *testing.T) {
	f := newFixture(t)
	f.engine.SetScratchRoot(filepath.Join(t.TempDir(), "scratch"))
	doc := decode(t, f.do(t, "POST", "/api/sessions", map[string]any{"scratch": true}))
	url := "/api/sessions/" + doc["id"].(string)
	for _, body := range []string{"{}", "null", `{"kept":null}`, `{"kept":3}`, `{"Kept":""}`, `{"kept":""} {}`, `{"kept":"","deferred":"yes"}`, `{"kept":"","deferred":null}`, `{"kept":"","carry_token":null}`} {
		req := newRequest("DELETE", url, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("Delete %s = %d", body, w.Code)
		}
	}
	req := newRequest("DELETE", url, strings.NewReader(""))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("Empty streamed delete = %d", w.Code)
	}
}

func TestDeleteConflictsHaveDistinctCodes(t *testing.T) {
	for err, want := range map[error]string{agent.ErrBusy: "session_busy", agent.ErrKeptChanged: "kept_changed"} {
		w := httptest.NewRecorder()
		writeEngineError(w, err)
		body := decode(t, w)
		if w.Code != http.StatusConflict || body["code"] != want {
			t.Errorf("Conflict = %d %v, want %s", w.Code, body, want)
		}
	}
}

func TestDeleteAcceptsOnlyTheConfirmedCopyList(t *testing.T) {
	f := newFixture(t)
	doc := &store.Session{ID: store.NewID(), Project: f.project, LeftIn: t.TempDir(), CarryTo: f.project, Carried: &store.Carried{To: f.project, Interrupted: 1, InterruptedNames: []string{"remaining.txt"}}}
	if err := f.srv.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	route := "/api/sessions/" + doc.ID
	p := decode(t, f.do(t, "GET", route+"/kept", nil))
	n, ok := p["carry"].(map[string]any)
	if !ok || n["destination"] != f.project || p["carry_token"] == "" {
		t.Fatalf("Preview = %v", p)
	}
	body := map[string]any{"kept": p["kept"]}
	if w := f.do(t, "DELETE", route, body); w.Code != http.StatusConflict {
		t.Fatalf("Missing confirmation = %d", w.Code)
	}
	body["carry_token"] = p["carry_token"]
	if w := f.do(t, "DELETE", route, body); w.Code != http.StatusNoContent {
		t.Fatalf("Confirmed deletion = %d %s", w.Code, w.Body)
	}
}

func TestBodylessAndProjectDeletesAcceptSavedCopyResults(t *testing.T) {
	for _, project := range []bool{false, true} {
		f := newFixture(t)
		src := t.TempDir()
		doc := &store.Session{ID: store.NewID(), Project: f.project, LeftIn: src, CarryTo: f.project, Carried: &store.Carried{To: f.project, Interrupted: 1}}
		if err := f.srv.store.Save(doc); err != nil {
			t.Fatal(err)
		}
		if project {
			other := f.newSession(t)
			w := f.do(t, "DELETE", "/api/projects", map[string]string{"path": f.project})
			if w.Code != http.StatusOK || decode(t, w)["sessions_deleted"] != float64(2) || f.cfg.HasProject(f.project) {
				t.Errorf("Project deletion = %d %s", w.Code, w.Body)
			}
			if _, err := f.srv.store.Load(other); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("Other conversation: %v", err)
			}
		} else if w := f.do(t, "DELETE", "/api/sessions/"+doc.ID, nil); w.Code != http.StatusNoContent {
			t.Errorf("Bodyless deletion = %d %s", w.Code, w.Body)
		}
		if _, err := f.srv.store.Load(doc.ID); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Interrupted conversation: %v", err)
		}
		if _, err := os.Stat(src); err != nil {
			t.Errorf("Retained source: %v", err)
		}
	}
}

func TestDeleteKeepsNamesTheProjectRefusesToRemove(t *testing.T) {
	for _, route := range []string{"session", "project", "confirmed"} {
		t.Run(route, func(t *testing.T) {
			f := newFixture(t)
			src := t.TempDir()
			blocked := filepath.Join(f.project, "locked")
			if err := os.Mkdir(blocked, 0700); err != nil {
				t.Fatal(err)
			}
			// The first temporary file can be removed. The second directory refuses unlink.
			names := []string{".carry-first", "locked/.carry-second"}
			var lines []string
			for _, name := range names {
				p := filepath.Join(f.project, name)
				writeFileT(t, p, "partial")
				info, err := os.Stat(p)
				if err != nil {
					t.Fatal(err)
				}
				row, err := json.Marshal(map[string]string{"name": name, "id": store.FileID(info)})
				if err != nil {
					t.Fatal(err)
				}
				lines = append(lines, string(row))
			}
			parent, err := os.Stat(f.project)
			if err != nil {
				t.Fatal(err)
			}
			origin, err := os.Stat(src)
			if err != nil {
				t.Fatal(err)
			}
			doc := &store.Session{ID: store.NewID(), Project: f.project, LeftIn: src, LeftInID: store.FileID(origin), CarryTo: f.project, CarryToID: store.FileID(parent)}
			if err := f.srv.store.Save(doc); err != nil {
				t.Fatal(err)
			}
			ledger := filepath.Join(f.cfg.Paths().Home, "scratch-owners")
			if err := os.MkdirAll(ledger, 0700); err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(ledger, filepath.Base(src)+".carry")
			writeFileT(t, journal, strings.Join(lines, "\n")+"\n")
			if err := os.Chmod(blocked, 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(blocked, 0700) })
			if probe, err := os.CreateTemp(blocked, "probe"); err == nil {
				probe.Close()
				t.Skip("Directory allows writes with mode 0500")
			} else if !errors.Is(err, os.ErrPermission) {
				t.Fatal(err)
			}
			endpoint := "/api/sessions/" + doc.ID
			switch route {
			case "project":
				if w := f.do(t, "DELETE", "/api/projects", map[string]string{"path": f.project}); w.Code != http.StatusOK || f.cfg.HasProject(f.project) {
					t.Fatalf("Remove project = %d %s", w.Code, w.Body)
				}
			case "session":
				if w := f.do(t, "DELETE", endpoint, nil); w.Code != http.StatusNoContent {
					t.Fatalf("Bodyless delete = %d %s", w.Code, w.Body)
				}
			case "confirmed":
				preview := decode(t, f.do(t, "GET", endpoint+"/kept", nil))
				if _, err := os.Stat(filepath.Join(f.project, names[0])); err != nil {
					t.Fatal("Preview modified the project", err)
				}
				w := f.do(t, "DELETE", endpoint, preview)
				if w.Code != http.StatusConflict || decode(t, w)["code"] != "kept_changed" {
					t.Fatalf("First confirmation = %d %s", w.Code, w.Body)
				}
				p := decode(t, f.do(t, "GET", endpoint+"/kept", nil))
				notice, ok := p["carry"].(map[string]any)
				if !ok || notice["recovery"] != journal+".kept.json" || notice["unavailable"] == true || p["carry_token"] == "" {
					t.Fatalf("Updated preview = %v", p)
				}
				if w := f.do(t, "DELETE", endpoint, map[string]any{"kept": p["kept"], "carry_token": p["carry_token"]}); w.Code != http.StatusNoContent {
					t.Fatalf("Second confirmation = %d %s", w.Code, w.Body)
				}
			}
			var record agent.CarryNotice
			raw, err := os.ReadFile(journal + ".kept.json")
			if err != nil || json.Unmarshal(raw, &record) != nil || !slices.Equal(record.Names, names[1:]) || record.Count != 1 {
				t.Fatalf("Recovery record = %s, %v", raw, err)
			}
			if _, err := os.Stat(filepath.Join(f.project, names[0])); !os.IsNotExist(err) {
				t.Errorf("Writable file remains: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(f.project, names[1])); err != nil || string(got) != "partial" {
				t.Errorf("Retained file = %q, %v", got, err)
			}
		})
	}
}

func TestProjectDeletionRequiresSeparateConsentForDamagedCopyRecord(t *testing.T) {
	f := newFixture(t)
	source := t.TempDir()
	doc := &store.Session{ID: store.NewID(), Project: f.project, LeftIn: source, CarryTo: f.project}
	if err := f.srv.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.cfg.Paths().Home, "scratch-owners")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, filepath.Base(source)+".carry")
	writeFileT(t, journal, "{\n")
	remove := func() *httptest.ResponseRecorder {
		return f.do(t, "DELETE", "/api/projects", map[string]string{"path": f.project})
	}
	if w := remove(); w.Code != 500 || !strings.Contains(w.Body.String(), journal+" is damaged at line 1") || !f.cfg.HasProject(f.project) {
		t.Fatalf("Project removal = %d %s", w.Code, w.Body)
	}
	route := "/api/sessions/" + doc.ID
	preview := decode(t, f.do(t, "GET", route+"/kept", nil))
	carry, ok := preview["carry"].(map[string]any)
	if !ok || carry["damaged_line"] != float64(1) || carry["unreadable"] != true {
		t.Fatalf("Damaged record preview = %v", preview)
	}
	if w := f.do(t, "DELETE", route, preview); w.Code != 204 {
		t.Fatalf("Confirmed deletion = %d %s", w.Code, w.Body)
	}
	if w := remove(); w.Code != 200 || f.cfg.HasProject(f.project) {
		t.Fatalf("Project removal after confirmation = %d %s", w.Code, w.Body)
	}
	if raw, err := os.ReadFile(journal); err != nil || string(raw) != "{\n" {
		t.Fatalf("Retained record = %q, %v", raw, err)
	}
}

func TestLegacyConfirmationCannotAuthorizeDeferredRemoval(t *testing.T) {
	f := newFixture(t)
	scratch := filepath.Join(t.TempDir(), "scratch")
	f.engine.SetScratchRoot(scratch)
	body := decode(t, f.do(t, "POST", "/api/sessions", map[string]any{"scratch": true}))
	route := "/api/sessions/" + body["id"].(string)
	writeFileT(t, filepath.Join(f.cfg.Paths().Sessions, store.NewID()+".json"), "{")
	st, err := store.Open(f.cfg.Paths().Sessions)
	if err != nil {
		t.Fatal(err)
	}
	f.srv.store = st
	f.srv.engine = agent.NewEngine(t.Context(), f.cfg, st, f.files, agent.Clients{Pool: f.srv.pool}, f.srv.searches, f.perms, f.hub)
	f.srv.engine.SetScratchRoot(scratch)
	p := decode(t, f.do(t, "GET", route+"/kept", nil))
	if p["deferred"] != true {
		t.Fatalf("Preview = %v", p)
	}
	if w := f.do(t, "DELETE", route, map[string]any{"kept": p["kept"]}); w.Code != 409 || decode(t, w)["code"] != "kept_changed" {
		t.Fatalf("Legacy confirmation = %d %s", w.Code, w.Body)
	}
	if w := f.do(t, "DELETE", route, p); w.Code != 204 {
		t.Fatalf("Current confirmation = %d %s", w.Code, w.Body)
	}
}

func TestProjectDeletionRecoversTheCompleteLinesBeforeATornAppend(t *testing.T) {
	f := newFixture(t)
	src := t.TempDir()
	dst, err := os.Stat(f.project)
	if err != nil {
		t.Fatal(err)
	}
	doc := &store.Session{ID: store.NewID(), Project: f.project, LeftIn: src, CarryTo: f.project, CarryToID: store.FileID(dst)}
	if err := f.srv.store.Save(doc); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(f.cfg.Paths().Home, "scratch-owners")
	if err := os.MkdirAll(ledger, 0700); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(ledger, filepath.Base(src)+".carry")
	writeFileT(t, filepath.Join(f.project, "note"), "retained")
	writeFileT(t, journal, `{"name":"note","kept":true}`+"\n"+`{"name":".carry-ab`)
	w := f.do(t, "DELETE", "/api/projects", map[string]string{"path": f.project})
	if w.Code != http.StatusOK || f.cfg.HasProject(f.project) {
		t.Fatalf("Project removal = %d %s", w.Code, w.Body)
	}
	var record agent.CarryNotice
	raw, err := os.ReadFile(journal + ".kept.json")
	if err != nil || json.Unmarshal(raw, &record) != nil || !slices.Equal(record.Names, []string{"note"}) {
		t.Fatalf("Recovery list = %s, %v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(f.project, "note")); err != nil || string(raw) != "retained" {
		t.Fatalf("Retained file = %s, %v", raw, err)
	}
}

func TestConfirmedDeleteNamesAnAsideThatNeedsManualRecovery(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(t.TempDir(), "scratch")
	f.engine.SetScratchRoot(root)
	s, err := f.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	record := filepath.Join(f.cfg.Paths().Home, "scratch-owners", filepath.Base(dir))
	raw, err := os.ReadFile(record + ".owner.json")
	if err != nil {
		t.Fatal(err)
	}
	var order store.RemovalOrder
	if err := json.Unmarshal(raw, &order); err != nil {
		t.Fatal(err)
	}
	order.Aside = ".removal-0123456789ab"
	order.Reason = store.RemoveDeleted
	raw, err = json.Marshal(order)
	if err != nil {
		t.Fatal(err)
	}
	writeFileT(t, record+".remove.json", string(raw))
	writeFileT(t, filepath.Join(dir, "note"), "keep")
	aside := filepath.Join(filepath.Dir(dir), order.Aside)
	if err := os.Rename(dir, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(t.TempDir(), "unavailable")
	writeFileT(t, full, "keep")
	saved := store.TrashHome
	store.TrashHome = func() string { return full }
	t.Cleanup(func() { store.TrashHome = saved })
	route := "/api/sessions/" + s.ID
	preview := decode(t, f.do(t, "GET", route+"/kept", nil))
	w := f.do(t, "DELETE", route, preview)
	body := decode(t, w)
	if w.Code != http.StatusConflict || body["code"] != "aside_recovery" || !strings.Contains(w.Body.String(), aside) {
		t.Fatalf("Manual recovery = %d %v", w.Code, body)
	}
	if _, err := os.Stat(record + ".remove.json"); err != nil {
		t.Fatal(err)
	}
	rescued := filepath.Join(t.TempDir(), "saved")
	if err := os.Rename(aside, rescued); err != nil {
		t.Fatal(err)
	}
	preview = decode(t, f.do(t, "GET", route+"/kept", nil))
	if w := f.do(t, "DELETE", route, preview); w.Code != http.StatusNoContent {
		t.Fatalf("Retry = %d %s", w.Code, w.Body)
	}
	if got, err := os.ReadFile(filepath.Join(rescued, "note")); err != nil || string(got) != "keep" {
		t.Fatalf("Saved contents = %q, %v", got, err)
	}
}
