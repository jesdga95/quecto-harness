package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/store"
)

// Folders the tests move to the trash go to one of their own, never the
// person's trash.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "quecto-trash-")
	if err != nil {
		panic(err)
	}
	store.TrashHome = func() string { return dir }
	store.TrashTop = func(string) string { return "" }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestFailedScratchCreationKeepsARecoverableFolder(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(strconv.FormatBool(available), func(t *testing.T) {
			h := scratchHarness(t)
			home := filepath.Join(t.TempDir(), "Trash")
			if available {
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(home, []byte("unavailable"), 0600); err != nil {
				t.Fatal(err)
			}
			saved := store.TrashHome
			store.TrashHome = func() string { return home }
			t.Cleanup(func() { store.TrashHome = saved })
			if err := os.Chmod(h.paths.Sessions, 0500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(h.paths.Sessions, 0700) })
			_, err := h.engine.CreateScratch()
			if err == nil {
				t.Fatal("Expected a session write error")
			}
			parent := h.paths.Scratch
			if available {
				parent = home
				if runtime.GOOS == "linux" {
					parent = filepath.Join(home, "files")
				}
			}
			paths, globErr := filepath.Glob(filepath.Join(parent, "scratch-*"))
			if globErr != nil || len(paths) != 1 || !strings.Contains(err.Error(), filepath.Base(paths[0])) {
				t.Fatalf("Recovery paths = %v, creation error = %v", paths, err)
			}
		})
	}
}
