package mcp

import (
	"os"
	"syscall"
	"testing"
)

func TestCatalogVolumeThatIgnoresOwnershipUsesMemory(t *testing.T) {
	original := catalogStatfs
	t.Cleanup(func() { catalogStatfs = original })
	catalogStatfs = func(_ string, st *syscall.Statfs_t) error { st.Flags = 0x00200000; return nil }
	s := fakeServer("local")
	m := newTestManager(t, s)
	if err := m.publishList(m.instanceFor(s, ""), []Tool{{Name: "live"}}, 0); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(m, s.Name); !got.Listed || got.Tools[0] != "live" {
		t.Fatal(got)
	}
	entries, err := os.ReadDir(m.cacheDir)
	if !os.IsNotExist(err) && (err != nil || len(entries) != 0) {
		t.Fatalf("Disk cache = %v, %v", entries, err)
	}
}
