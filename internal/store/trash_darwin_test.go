package store

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestVolumeTrashRejectsALinkedTrashesDirectory(t *testing.T) {
	root, target := t.TempDir(), t.TempDir()
	savedHome, savedTop := TrashHome, TrashTop
	TrashHome = func() string { return "" }
	TrashTop = func(string) string { return root }
	t.Cleanup(func() { TrashHome, TrashTop = savedHome, savedTop })
	if err := os.Symlink(target, filepath.Join(root, ".Trashes")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, strconv.Itoa(os.Getuid())), 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := trashOf(root, info); err == nil {
		t.Fatalf("Accepted linked trash %s", c.files)
	}
}
