package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The first scratch folder is recorded and stays; another asked for later is
// refused, and one refused at first is not recorded.
func TestTheScratchFolderIsSettledOnce(t *testing.T) {
	base := t.TempDir()
	p := DiscoverPathsIn(filepath.Join(base, "state"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(p.Home, "scratch")
	if err := p.SettleScratch(inside); err == nil {
		t.Fatal("settled a scratch folder inside the state folder")
	}
	if _, err := os.Stat(p.ScratchRecord); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused folder was recorded: %v", err)
	}
	first := filepath.Join(base, "first ")
	if err := p.SettleScratch(first); err != nil {
		t.Fatal(err)
	}
	again := DiscoverPathsIn(p.Home)
	if err := again.SettleScratch(""); err != nil || again.Scratch != first {
		t.Fatalf("a later start settled %q, %v, want %q", again.Scratch, err, first)
	}
	if err := again.SettleScratch(filepath.Join(base, "second")); err == nil {
		t.Error("a later start moved the scratch folder")
	}
	if info, err := os.Stat(first); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the scratch folder is %v, %v", info, err)
	}
}

// A scratch folder that was already there and others can read is refused.
func TestASharedScratchFolderIsRefused(t *testing.T) {
	base := t.TempDir()
	p := DiscoverPathsIn(filepath.Join(base, "state"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := p.SettleScratch(open); err == nil {
		t.Error("settled a scratch folder others can read")
	}
	if _, err := os.Stat(p.ScratchRecord); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused folder was recorded: %v", err)
	}
}

// A scratch folder the user cannot write is refused before it is recorded.
func TestAScratchFolderTheUserCannotWriteIsRefused(t *testing.T) {
	base := t.TempDir()
	p := DiscoverPathsIn(filepath.Join(base, "state"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	shut := filepath.Join(base, "shut")
	if err := os.Mkdir(shut, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shut, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shut, 0o700) })
	if err := p.SettleScratch(shut); err == nil {
		t.Error("settled a scratch folder the user cannot write")
	}
	if _, err := os.Stat(p.ScratchRecord); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused folder was recorded: %v", err)
	}
}
