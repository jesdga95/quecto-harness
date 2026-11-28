package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkFindFilesFence walks 20000 files with and without a fence whose
// root is shallower than the files, so every entry is checked against it.
func BenchmarkFindFilesFence(b *testing.B) {
	base := b.TempDir()
	proj := filepath.Join(base, "proj")
	for i := 0; i < 200; i++ {
		d := filepath.Join(proj, fmt.Sprintf("a%03d", i), "b", "c")
		os.MkdirAll(d, 0o755)
		for j := 0; j < 100; j++ {
			os.WriteFile(filepath.Join(d, fmt.Sprintf("f%03d.txt", j)), nil, 0o644)
		}
	}
	root := filepath.Join(base, "s")
	os.MkdirAll(filepath.Join(root, "x"), 0o755)
	for _, c := range []struct {
		name  string
		fence Fence
	}{{"none", Fence{}}, {"fence", Fence{Root: root}}} {
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := FindFiles(proj, "", "zzz", 30, c.fence); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
