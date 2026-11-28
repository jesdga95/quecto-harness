package store

import (
	"errors"
	"os"
	"testing"
)

func TestCopyUsesTheBytesOfAFileReportingZeroSize(t *testing.T) {
	const name = "ostype"
	root := "/proc/sys/kernel"
	data, err := os.ReadFile(root + "/" + name)
	if err != nil || len(data) == 0 {
		t.Skip("Requires Linux procfs")
	}
	from, to := openRoots(t, root, t.TempDir())
	info, err := from.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Skip("Requires a zero-size procfs entry")
	}
	n, err := copyOne(from, to, name, carryFile, &tempLog{})
	if err != nil && !errors.Is(err, errAttrs) {
		t.Fatal(err)
	}
	saved, err := to.ReadFile(name)
	if err != nil || string(saved) != string(data) || n != int64(len(data)) {
		t.Fatalf("Copy = %q (%d), want %q: %v", saved, n, data, err)
	}
}
