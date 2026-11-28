package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCarryPreservesNestedVendorAssets(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	rel := filepath.Join("static", "vendor", "library.js")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(src, rel)), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, rel), "local asset")
	if _, err := CarryScratch(src, dst); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dst, rel))
	if err != nil || string(data) != "local asset" {
		t.Fatalf("Nested project asset missing from the copy: %v", err)
	}
}
