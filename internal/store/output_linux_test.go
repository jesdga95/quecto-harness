package store

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestExt4RejectsRestoredOutputAfterInodeReuse(t *testing.T) {
	for _, action := range []string{"confirmation", "pending"} {
		t.Run(action, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var fs syscall.Statfs_t
			if err := syscall.Statfs(s.dir, &fs); err != nil {
				t.Fatal(err)
			}
			if fs.Type != 0xef53 {
				t.Skip("Requires an ext filesystem")
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
			gen := items[0].Generation
			if action == "pending" {
				if err := s.orderOutput(id, gen); err != nil {
					t.Fatal(err)
				}
			}
			old, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			backup := t.TempDir()
			if err := os.CopyFS(backup, os.DirFS(path)); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; ; attempt++ {
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.CopyFS(path, os.DirFS(backup)); err != nil {
					t.Fatal(err)
				}
				now, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if os.SameFile(old, now) {
					break
				}
				if attempt == 63 {
					t.Skip("The filesystem did not reuse the directory inode")
				}
			}
			file := filepath.Join(path, "restored")
			if err := os.WriteFile(file, []byte("restored"), 0600); err != nil {
				t.Fatal(err)
			}
			if action == "pending" {
				if s, err = Open(s.dir); err != nil || s.Pending(id) {
					t.Fatal("Pending order", err)
				}
			} else if err := s.DeleteRetained(id+".files", gen); !errors.Is(err, ErrRecoveryConflict) {
				t.Errorf("Stale confirmation = %v", err)
			}
			if b, err := os.ReadFile(file); err != nil || string(b) != "restored" {
				t.Fatalf("Restored file = %q, %v", b, err)
			}
		})
	}
}
