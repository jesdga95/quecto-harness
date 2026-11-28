package server

import (
	"os"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/store"
)

// Folders the tests move to the trash go to one of their own, never the
// person's trash.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "quecto-trash-")
	if err != nil {
		panic(err)
	}
	store.TrashHome = func() string { return dir }
	store.TrashTop = func(string) string { return "" }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
