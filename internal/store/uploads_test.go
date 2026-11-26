package store

import (
	"os"
	"path/filepath"
	"testing"
)

// Owns takes the files Save wrote and nothing that only looks like one.
func TestUploadsOwnsWhatItSaved(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	u, err := OpenUploads(filepath.Join(base, "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := u.Save("note.txt", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if !u.Owns(saved) {
		t.Errorf("Owns(%s) = false for a saved file", saved)
	}

	outside := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	loose := filepath.Join(base, "uploads", "loose.txt")
	if err := os.WriteFile(loose, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkFile := filepath.Join(filepath.Dir(saved), "link.txt")
	if err := os.Symlink(outside, linkFile); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(base, "uploads", NewFileID())
	if err := os.Symlink(base, linkDir); err != nil {
		t.Fatal(err)
	}
	notID := filepath.Join(base, "uploads", "nested")
	if err := os.MkdirAll(notID, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notID, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		outside, loose, linkFile, filepath.Join(linkDir, "secret.txt"),
		filepath.Join(notID, "f.txt"), "uploads/x/y", saved + "/../" + filepath.Base(saved),
	} {
		if u.Owns(p) {
			t.Errorf("Owns(%s) = true", p)
		}
	}
}
