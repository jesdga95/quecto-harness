package store

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDirectoryAttributesKeepTheSource(t *testing.T) {
	for _, name := range []string{".", "empty"} {
		t.Run(name, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(src, "empty"), 0700); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("xattr", "-w", "com.quecto.test", "value", filepath.Join(src, name)).CombinedOutput(); err != nil {
				t.Skipf("Cannot write a directory attribute: %s (%v)", out, err)
			}
			c, err := CarryScratch(src, dst)
			if err != nil {
				t.Fatal(err)
			}
			if c.Complete() || c.Attrs != 1 {
				t.Errorf("Copy with directory metadata = %+v", c)
			}
			if err := sameCopy(src, dst); err == nil {
				t.Fatal("A directory missing metadata matched")
			}
		})
	}
}

// A file with an access control list is not lost to the sweep: the copy is
// not called whole, so its folder stays, or it goes to the trash with the list.
func TestAnACLIsNotLostToTheSweep(t *testing.T) {
	home := useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "1:1")
	dir, err := MintScratch(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "note"), "x")
	if out, err := exec.Command("chmod", "+a", "group:everyone allow read", filepath.Join(dir, "note")).CombinedOutput(); err != nil {
		t.Skipf("Cannot add an access control list: %s (%v)", out, err)
	}
	info, _ := os.Lstat(dir)
	if err := l.Own(dir, "s1", info); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	c, err := CarryScratch(dir, dst)
	if err != nil {
		t.Fatal(err)
	}
	if c.Complete() || c.Attrs != 1 {
		t.Errorf("a copy without the access control list = %+v", c)
	}
	if c.Complete() {
		if err := l.Order(dir, "s1", RemoveCopied, dst, began); err != nil {
			t.Fatal(err)
		}
	}
	l.Sweep(nil, time.Now().Add(8*24*time.Hour))
	path := keptOrTrashed(t, dir, home, "note")
	out, err := exec.Command("ls", "-le", path).CombinedOutput()
	if path == "" || err != nil || !strings.Contains(string(out), "everyone allow read") {
		t.Fatalf("%q after the sweep: %s %v", path, out, err)
	}
}

// A partial file the copy proved its own is kept and named when it carries a
// resource fork, in the preview and in the cleanup, and the fork stays.
func TestAPartialWithAResourceForkIsKept(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "new.txt"), "abcdef")
	partial := filepath.Join(dst, "new.txt")
	writeFile(t, partial, "abc")
	if err := os.WriteFile(filepath.Join(partial, "..namedfork", "rsrc"), []byte("fork"), 0o600); err != nil {
		t.Skipf("Cannot write a resource fork: %v", err)
	}
	info, err := os.Lstat(partial)
	if err != nil {
		t.Fatal(err)
	}
	temps := filepath.Join(t.TempDir(), "carry.log")
	line, _ := json.Marshal(tempEntry{Name: "new.txt", ID: FileID(info), Partial: true})
	writeFile(t, temps, string(line)+"\n")
	from, _ := os.OpenRoot(src)
	defer from.Close()
	to, _ := os.OpenRoot(dst)
	defer to.Close()
	if names, err := InspectCarry(from, to, temps); err != nil || len(names) != 1 || names[0] != "new.txt" {
		t.Errorf("preview = %v, %v, want the partial named", names, err)
	}
	if names, err := ClearCarry(from, to, temps); err != nil || len(names) != 1 || names[0] != "new.txt" {
		t.Errorf("cleanup = %v, %v, want the partial kept", names, err)
	}
	if got, err := os.ReadFile(filepath.Join(partial, "..namedfork", "rsrc")); err != nil || string(got) != "fork" {
		t.Errorf("fork = %q, %v", got, err)
	}
}
