package server

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestStorageTracksRefusedDeferredTrashUntilResolution(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.engine.SetScratchRoot(root)
	newLedger := func() *store.ScratchLedger {
		return store.NewScratchLedger(filepath.Join(f.cfg.Paths().Home, "scratch-owners"), root, f.srv.store.Identity())
	}
	ledger := newLedger()
	now := time.Now()
	dir, err := store.MintScratch(root, now)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Own(dir, "session", info); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Order(dir, "session", store.RemoveCopied, t.TempDir(), time.Now()); err != nil {
		t.Fatal(err)
	}
	status := func(t *testing.T) []any {
		t.Helper()
		w := f.do(t, "GET", "/api/storage", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("Storage = %d %s", w.Code, w.Body)
		}
		list, ok := decode(t, w)["trash_failures"].([]any)
		if !ok {
			t.Fatal("Missing trash failures")
		}
		return list
	}
	t.Run("refused", func(t *testing.T) {
		unavailableTrash(t)
		ledger.Sweep(nil, now)
		if got := status(t); len(got) != 0 {
			t.Fatalf("Grace period produced %v", got)
		}
		ledger.Sweep(nil, now.Add(8*24*time.Hour))
		got := status(t)
		if len(got) != 1 {
			t.Fatalf("Pending failures = %v", got)
		}
		failure := got[0].(map[string]any)
		if reason, _ := failure["reason"].(string); failure["path"] != dir || !strings.Contains(reason, "trash") {
			t.Fatalf("Failed move = %v", failure)
		}
	})
	if t.Failed() {
		return
	}
	t.Run("latest failure", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("Requires an unprivileged account")
		}
		home := t.TempDir()
		files := home
		if runtime.GOOS == "linux" {
			files = filepath.Join(home, "files")
			for _, dir := range []string{files, filepath.Join(home, "info")} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := os.Chmod(home, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(files, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(files, 0700) })
		saved := store.TrashHome
		store.TrashHome = func() string { return home }
		t.Cleanup(func() { store.TrashHome = saved })
		newLedger().Sweep(nil, now.Add(9*24*time.Hour))
		got := status(t)
		if len(got) != 1 {
			t.Fatalf("Latest failure = %v", got)
		}
		reason, _ := got[0].(map[string]any)["reason"].(string)
		if !strings.Contains(reason, "permission denied") {
			t.Fatalf("Expected the rename failure, got %q", reason)
		}
	})
	if moved := newLedger().Sweep(nil, now.Add(9*24*time.Hour)); len(moved) != 1 {
		t.Fatalf("Retry moved %v", moved)
	}
	if got := status(t); len(got) != 0 {
		t.Fatalf("Resolved failures = %v", got)
	}
}

// A retained folder whose owner record, removal order or notice cannot be read
// is listed with that record.
func TestStorageNamesTheDamagedRecordOfARetainedFolder(t *testing.T) {
	unavailableTrash(t)
	for _, suffix := range []string{".trash-failure.json", ".owner.json", ".remove.json"} {
		t.Run(suffix, func(t *testing.T) {
			f := newFixture(t)
			f.engine.SetScratchRoot(t.TempDir())
			deleteScratch := func() string {
				t.Helper()
				s, err := f.engine.CreateScratch()
				if err != nil {
					t.Fatal(err)
				}
				w := f.do(t, "DELETE", "/api/sessions/"+s.ID, nil)
				if w.Code != http.StatusOK {
					t.Fatalf("Delete = %d %s", w.Code, w.Body)
				}
				dir, _ := s.Where()
				return dir
			}
			damaged := deleteScratch()
			kept := deleteScratch()
			record := filepath.Join(f.cfg.Paths().Home, "scratch-owners", filepath.Base(damaged)+suffix)
			writeFileT(t, record, "{")

			w := f.do(t, "GET", "/api/storage", nil)
			if w.Code != http.StatusOK {
				t.Fatalf("Storage = %d %s", w.Code, w.Body)
			}
			failures, _ := decode(t, w)["trash_failures"].([]any)
			byPath := map[any]map[string]any{}
			for _, item := range failures {
				byPath[item.(map[string]any)["path"]] = item.(map[string]any)
			}
			want := 2
			if len(failures) != want || byPath[kept] == nil || byPath[kept]["record"] != nil {
				t.Fatalf("Failures = %v", failures)
			}
			if byPath[damaged]["record"] != record {
				t.Fatalf("Damaged folder = %v, want record %s", byPath[damaged], record)
			}
			// The reason the move failed is kept beside the damage.
			if reason, _ := byPath[damaged]["reason"].(string); !strings.Contains(reason, "trash") || !strings.Contains(reason, "could not be read") {
				t.Fatalf("Damaged folder reason = %q", reason)
			}
		})
	}
}

// A folder edited after its removal was ordered stays, and Storage names it
// for as long as it is there, after the order is settled.
func TestStorageNamesAFolderKeptForAChangeAfterItsOrder(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.engine.SetScratchRoot(root)
	ledger := store.NewScratchLedger(filepath.Join(f.cfg.Paths().Home, "scratch-owners"), root, f.srv.store.Identity())
	now := time.Now()
	dir, err := store.MintScratch(root, now)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Own(dir, "session", info); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Order(dir, "session", store.RemoveDeleted, "", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	writeFileT(t, filepath.Join(dir, "doc"), "written after the order")
	for _, later := range []time.Time{now.AddDate(1, 0, 0), now.AddDate(2, 0, 0)} {
		if moved := ledger.Sweep(nil, later); len(moved) != 0 {
			t.Fatalf("Moved %v", moved)
		}
		w := f.do(t, "GET", "/api/storage", nil)
		failures, _ := decode(t, w)["trash_failures"].([]any)
		if len(failures) != 1 {
			t.Fatalf("Storage = %d %s", w.Code, w.Body)
		}
		kept := failures[0].(map[string]any)
		if reason, _ := kept["reason"].(string); kept["path"] != dir || kept["kept"] != true || !strings.Contains(reason, "changed") {
			t.Fatalf("Kept folder = %v", kept)
		}
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Paths().Home, "scratch-owners", filepath.Base(dir)+".remove.json")); !os.IsNotExist(err) {
		t.Fatalf("The order outlived the change: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "doc")); err != nil || string(got) != "written after the order" {
		t.Fatalf("Kept contents = %q, %v", got, err)
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
