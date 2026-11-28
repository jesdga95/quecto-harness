package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/store"
)

func unavailableTrash(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "unavailable")
	writeFileT(t, path, "occupied")
	home := store.TrashHome
	store.TrashHome = func() string { return path }
	t.Cleanup(func() { store.TrashHome = home })
}

func TestDeletedSessionReportsItsRetainedFolder(t *testing.T) {
	unavailableTrash(t)
	for _, confirmed := range []bool{false, true} {
		f := newFixture(t)
		f.engine.SetScratchRoot(filepath.Join(t.TempDir(), "scratch"))
		s, err := f.engine.CreateScratch()
		if err != nil {
			t.Fatal(err)
		}
		dir, _ := s.Where()
		writeFileT(t, filepath.Join(dir, "note"), "work")
		route := "/api/sessions/" + s.ID
		var body any
		if confirmed {
			body = decode(t, f.do(t, "GET", route+"/kept", nil))
		}
		w := f.do(t, "DELETE", route, body)
		if w.Code != http.StatusOK {
			t.Fatalf("Completed delete = %d %s", w.Code, w.Body)
		}
		failure := decode(t, w)
		if reason, _ := failure["reason"].(string); failure["path"] != dir || !strings.Contains(reason, "trash") {
			t.Fatalf("Retained folder = %v", failure)
		}
		if w := f.do(t, "GET", route, nil); w.Code != http.StatusNotFound {
			t.Fatalf("Deleted session = %d", w.Code)
		}
		if got, err := os.ReadFile(filepath.Join(dir, "note")); err != nil || string(got) != "work" {
			t.Fatalf("Retained contents = %q, %v", got, err)
		}
	}
}

// Removing a project deletes its sessions, and a folder one of those deletes
// keeps because its move to the trash failed is in the response, also when
// the removal stops after the deletes.
func TestRemovingAProjectReportsItsRetainedFolders(t *testing.T) {
	for _, stops := range []bool{false, true} {
		t.Run(strconv.FormatBool(stops), func(t *testing.T) {
			if stops && os.Getuid() == 0 {
				t.Skip("Requires an unprivileged account")
			}
			unavailableTrash(t)
			f := newFixture(t)
			f.engine.SetScratchRoot(filepath.Join(t.TempDir(), "scratch"))
			s, err := f.engine.CreateScratch()
			if err != nil {
				t.Fatal(err)
			}
			dir, _ := s.Where()
			writeFileT(t, filepath.Join(dir, "note"), "work")
			if _, err := f.cfg.AddProject(dir); err != nil {
				t.Fatal(err)
			}
			want := http.StatusOK
			if stops {
				home := filepath.Dir(f.cfg.Paths().Config)
				if err := os.Chmod(home, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(home, 0o700) })
				want = http.StatusInternalServerError
			}
			w := f.do(t, "DELETE", "/api/projects", map[string]string{"path": dir})
			if w.Code != want {
				t.Fatalf("Remove project = %d %s", w.Code, w.Body)
			}
			body := decode(t, w)
			retained, _ := body["retained_folders"].([]any)
			if body["sessions_deleted"] != float64(1) || len(retained) != 1 || retained[0].(map[string]any)["path"] != dir {
				t.Fatalf("Response = %v", body)
			}
			if got, err := os.ReadFile(filepath.Join(dir, "note")); err != nil || string(got) != "work" {
				t.Fatalf("Retained contents = %q, %v", got, err)
			}
		})
	}
}
