package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOnlyConfirmedOutputContentsCanBeDeleted(t *testing.T) {
	for _, action := range []string{"confirmation", "startup"} {
		for _, change := range []string{"added", "edited", "link"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				s, id, path := newRetainedDirectory(t)
				sibling := filepath.Join(path, "first")
				if err := os.WriteFile(sibling, []byte("original"), 0600); err != nil {
					t.Fatal(err)
				}
				sub := filepath.Join(path, "spill")
				if err := os.Mkdir(sub, 0700); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(sub, "result")
				if err := os.WriteFile(file, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(path, "link")
				if err := os.Symlink("spill/result", link); err != nil {
					t.Fatal(err)
				}
				gen := listedOutputGeneration(t, s)
				if action == "startup" {
					if err := s.orderOutput(id, gen); err != nil {
						t.Fatal(err)
					}
				}
				switch change {
				case "added":
					file = filepath.Join(sub, "extra")
					if err := os.WriteFile(file, []byte("new"), 0600); err != nil {
						t.Fatal(err)
					}
				case "edited":
					info, err := os.Stat(file)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, []byte("new"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
				case "link":
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("changed", link); err != nil {
						t.Fatal(err)
					}
				}
				if action == "startup" {
					var err error
					s, err = Open(s.dir)
					if err != nil || s.Pending(id) {
						t.Fatal("Stale order", err)
					}
				} else if err := s.DeleteRetained(id+".files", gen); !errors.Is(err, ErrRecoveryConflict) {
					t.Errorf("Stale confirmation = %v", err)
				}
				if data, err := os.ReadFile(sibling); err != nil || string(data) != "original" {
					t.Fatalf("Unchanged sibling = %q, %v", data, err)
				}
				if change == "link" {
					if target, err := os.Readlink(link); err != nil || target != "changed" {
						t.Fatal(target, err)
					}
				} else if data, err := os.ReadFile(file); err != nil || string(data) != "new" {
					t.Fatalf("Saved contents = %q, %v", data, err)
				}
				fresh := listedOutputGeneration(t, s)
				if fresh == gen {
					t.Fatal("Reused an obsolete confirmation")
				}
				if err := s.DeleteRetained(id+".files", fresh); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func newRetainedDirectory(t *testing.T) (*Sessions, string, string) {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	path := s.filesDir(id)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	return s, id, path
}

func listedOutputGeneration(t *testing.T, s *Sessions) string {
	t.Helper()
	items, err := s.RecoveryItems()
	if err != nil || len(items) != 1 || items[0].Generation == "" {
		t.Fatal(items, err)
	}
	return items[0].Generation
}

func TestListingAnEmptyPendingOutputPreservesItsOrder(t *testing.T) {
	s, id, path := newRetainedDirectory(t)
	gen := listedOutputGeneration(t, s)
	if err := s.orderOutput(id, gen); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(path)
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
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
	if !stable {
		t.Skip("The filesystem cannot identify an empty directory across mounts")
	}
	if err := os.Remove(filepath.Join(path, files[0].Name())); err != nil {
		t.Fatal(err)
	}
	if listedOutputGeneration(t, s) != gen {
		t.Error("Listing invalidated the pending order")
	}
	if done, err := s.FinishCleanup(id); !done || err != nil {
		t.Fatal(done, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("Empty output remains", err)
	}
}

func TestCleanupAcceptsMissingAuthorizedEntries(t *testing.T) {
	s, id, path := newRetainedDirectory(t)
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gen := listedOutputGeneration(t, s)
	if err := s.orderOutput(id, gen); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, "one")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(s.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("Partial cleanup did not finish", err)
	}
}

func TestDeleteRestoredTranscriptAfterItsOutputIsGone(t *testing.T) {
	s, id, path := newRetainedDirectory(t)
	if err := s.orderOutput(id, listedOutputGeneration(t, s)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(sample(id, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(s.path(id)); !os.IsNotExist(err) {
		t.Fatal("Transcript remains", err)
	}
}

func TestRetainedOutputWithAnUnreadableEmptyChild(t *testing.T) {
	s, id, path := newRetainedDirectory(t)
	if err := os.Mkdir(filepath.Join(path, "empty"), 0000); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRetained(id+".files", listedOutputGeneration(t, s)); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryKeepsDataWrittenIntoTheMarker(t *testing.T) {
	s, id, path := newRetainedDirectory(t)
	gen := listedOutputGeneration(t, s)
	if err := s.orderOutput(id, gen); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	marker := filepath.Join(path, entries[0].Name())
	if err := os.WriteFile(marker, []byte("saved"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(s.dir); err != nil {
		t.Fatal(err)
	}
	items, err := s.RecoveryItems()
	if err != nil || len(items) != 1 || items[0].Kind != "record" || !strings.Contains(items[0].Repair, marker) {
		t.Fatal(items, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "saved" {
		t.Fatal(string(data), err)
	}
}
