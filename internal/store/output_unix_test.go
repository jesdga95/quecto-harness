//go:build darwin || linux

package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCopiedRecoveryMarkerRequiresFreshConfirmation(t *testing.T) {
	for _, action := range []string{"request", "restart"} {
		t.Run(action, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id := NewID()
			path := s.filesDir(id)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			items, err := s.RecoveryItems()
			if err != nil || len(items) != 1 {
				t.Fatal(items, err)
			}
			generation := items[0].Generation
			if action == "restart" {
				if err := s.orderOutput(id, generation); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := os.ReadDir(path)
			if err != nil || len(entries) != 1 {
				t.Fatal(entries, err)
			}
			// Replace the marker inside the same directory to hold the directory
			// inode constant even on filesystems that rarely recycle one.
			marker := filepath.Join(path, entries[0].Name())
			copyPath := filepath.Join(path, "copy")
			if err := os.WriteFile(copyPath, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(copyPath, marker); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(path, "restored")
			if err := os.WriteFile(payload, []byte("saved"), 0600); err != nil {
				t.Fatal(err)
			}
			if action == "request" {
				if err := s.DeleteRetained(id+".files", generation); !errors.Is(err, ErrRecoveryConflict) {
					t.Errorf("Stale confirmation = %v", err)
				}
			} else if s, err = Open(s.dir); err != nil || s.Pending(id) {
				t.Fatal("Stale order", err)
			}
			if data, err := os.ReadFile(payload); err != nil || string(data) != "saved" {
				t.Fatalf("Restored payload = %q, %v", data, err)
			}
			items, err = s.RecoveryItems()
			if err != nil || len(items) != 1 || items[0].Generation == generation {
				t.Fatal("No fresh confirmation", items, err)
			}
			if err := s.DeleteRetained(id+".files", items[0].Generation); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type remountedInfo struct {
	os.FileInfo
	stat syscall.Stat_t
}

func (i remountedInfo) Sys() any { return &i.stat }

func TestOutputOrderSurvivesAChangedDeviceNumber(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	path := s.filesDir(id)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	items, err := s.RecoveryItems()
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	before := remountedInfo{info, *info.Sys().(*syscall.Stat_t)}
	before.stat.Dev++
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := snapshotIn(root, before, entries, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.orderOutput(id, previous.generation()); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(s.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Pending output survived remount: %v", err)
	}
}

func TestInterruptedRemovalNeedsEvidenceForAnEmptyDirectory(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		dir := t.TempDir()
		name := NewID() + ".files"
		path := filepath.Join(dir, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		gen, err := outputGeneration(path, true)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 1 {
			t.Fatal(entries, err)
		}
		root, err := os.OpenRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := root.Stat(".")
		if err != nil {
			t.Fatal(err)
		}
		_, stable, err := outputDirectoryID(root, info)
		root.Close()
		if err != nil {
			t.Fatal(err)
		}
		if legacy {
			st := info.Sys().(*syscall.Stat_t)
			gen = fmt.Sprintf("%s.%x-%x", strings.TrimPrefix(entries[0].Name(), ".recovery-"), st.Dev, st.Ino)
		}
		if err := os.Remove(filepath.Join(path, entries[0].Name())); err != nil {
			t.Fatal(err)
		}
		err = removeConfirmedOutput(path, relation{OutputGeneration: gen}, false)
		if legacy || !stable {
			if !errors.Is(err, ErrRecoveryConflict) {
				t.Fatalf("Unverified empty output: %v", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		} else if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("Empty output remains: %v", err)
		}
	}
}
