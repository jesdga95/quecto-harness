package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrashOnFreshHomeVolumeCreatesDataDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".local", "share", "Trash")
	saved := TrashHome
	TrashHome = func() string { return home }
	t.Cleanup(func() { TrashHome = saved })
	src := filepath.Join(root, "scratch")
	if err := os.Mkdir(src, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := moveToTrash(src, FileID(info), src)
	if err != nil || dest != filepath.Join(home, "files", "scratch") {
		t.Fatalf("Home trash destination = %s, %v", dest, err)
	}
}
