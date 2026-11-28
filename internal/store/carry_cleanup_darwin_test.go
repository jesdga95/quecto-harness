package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestImmediateCopyCleanupKeepsTemporaryMetadata(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "work")
	saved := linkFile
	t.Cleanup(func() { linkFile = saved })
	var name string
	linkFile = func(r *os.Root, tmp, rel string) error {
		name = tmp
		if err := os.WriteFile(filepath.Join(dst, tmp, "..namedfork", "rsrc"), []byte("fork"), 0600); err != nil {
			t.Fatal(err)
		}
		return os.ErrPermission
	}
	c := carryWith(t, src, dst, filepath.Join(t.TempDir(), "carry"))
	if len(c.InterruptedNames) != 1 || c.InterruptedNames[0] != name {
		t.Fatalf("Retained names = %v", c.InterruptedNames)
	}
	if data, err := os.ReadFile(filepath.Join(dst, name, "..namedfork", "rsrc")); err != nil || string(data) != "fork" {
		t.Fatalf("Resource fork = %q, %v", data, err)
	}
}

type failedCopyRead struct{ addFork func() }

func (r failedCopyRead) Read([]byte) (int, error) {
	r.addFork()
	return 0, errors.New("copy interrupted")
}

func TestFailedDirectCopyKeepsPartialMetadata(t *testing.T) {
	dst := t.TempDir()
	to, err := os.OpenRoot(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer to.Close()
	name := "note"
	fork := filepath.Join(dst, name, "..namedfork", "rsrc")
	read := failedCopyRead{func() {
		if err := os.WriteFile(fork, []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	log, err := openTemps(filepath.Join(t.TempDir(), "carry"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.f.Close()
	if err := fillNew(to, name, read, 0600, log, false, ""); err == nil {
		t.Fatal("Expected a copy failure")
	}
	names, err := InspectCarry(nil, to, log.f.Name())
	if err != nil || len(names) != 1 || names[0] != name {
		t.Fatalf("Retained names = %v, %v", names, err)
	}
	if got, err := os.ReadFile(fork); err != nil || string(got) != "retained" {
		t.Fatalf("Resource fork = %q, %v", got, err)
	}
}

func TestCopyIntoAnInheritedACLDropsTheTemporaryLink(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if out, err := exec.Command("chmod", "+a", "everyone allow read,file_inherit,directory_inherit", dst).CombinedOutput(); err != nil {
		t.Skipf("Cannot set an inheritable ACL: %s", out)
	}
	writeFile(t, filepath.Join(src, "note"), "contents")
	c := carryWith(t, src, dst, filepath.Join(t.TempDir(), "carry"))
	if !c.Complete() || c.Copied != 1 || len(leftTemps(t, dst)) != 0 {
		t.Fatalf("Copy = %+v, temporary files = %v", c, leftTemps(t, dst))
	}
	out, err := exec.Command("ls", "-le", filepath.Join(dst, "note")).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "inherited") {
		t.Fatalf("Destination ACL = %s, %v", out, err)
	}
}

func TestReplayOfTemporaryLinkWithMetadata(t *testing.T) {
	for _, state := range []string{"linked", "logged", "replaced", "symlink", "missing"} {
		t.Run(state, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			const tmp = ".carry-123456789abc"
			final := filepath.Join(dst, "note")
			writeFile(t, filepath.Join(src, "note"), "work")
			path := filepath.Join(dst, tmp)
			writeFile(t, path, "work")
			fork := filepath.Join(path, "..namedfork", "rsrc")
			if err := os.WriteFile(fork, []byte("fork"), 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Link(path, final); err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(t.TempDir(), "carry")
			id := FileID(info)
			records := []map[string]any{{"name": tmp, "id": id, "link_name": "note"}}
			switch state {
			case "logged":
				delete(records[0], "link_name")
				records = append(records, map[string]any{"name": "note", "id": id, "copied": true})
			case "replaced", "symlink", "missing":
				if err := os.Remove(final); err != nil {
					t.Fatal(err)
				}
				if state == "replaced" {
					writeFile(t, final, "replacement")
				}
				if state == "symlink" {
					if err := os.Symlink(tmp, final); err != nil {
						t.Fatal(err)
					}
				}
			}
			var lines bytes.Buffer
			for _, entry := range records {
				if err := json.NewEncoder(&lines).Encode(entry); err != nil {
					t.Fatal(err)
				}
			}
			if state == "linked" {
				lines.WriteString(`{"name":`)
			}
			writeFile(t, journal, lines.String())
			to, err := os.OpenRoot(dst)
			if err != nil {
				t.Fatal(err)
			}
			defer to.Close()
			names, err := InspectCarry(nil, to, journal)
			linked := state == "linked" || state == "logged"
			if err != nil || (linked && len(names) != 0) || (!linked && !slices.Equal(names, []string{tmp})) {
				t.Errorf("Preview = %v, %v", names, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal(err)
			}
			c := carryWith(t, src, dst, journal)
			if linked {
				if !c.Complete() || c.Copied != 1 {
					t.Errorf("Replay = %+v", c)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("Temporary link remains: %v", err)
				}
				fork = filepath.Join(final, "..namedfork", "rsrc")
			} else if !slices.Equal(c.InterruptedNames, []string{tmp}) {
				t.Fatalf("Retained names = %v", c.InterruptedNames)
			}
			if got, err := os.ReadFile(fork); err != nil || string(got) != "fork" {
				t.Fatalf("Resource fork = %s, %v", got, err)
			}
		})
	}
}

func TestFailedCopyKeepsAResourceForkAddedDuringCleanup(t *testing.T) {
	useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "work")
	from, to := openRoots(t, src, dst)
	syncHook, dropHook := syncCopy, beforeDrop
	t.Cleanup(func() { syncCopy = syncHook; beforeDrop = dropHook })
	syncCopy = func(*os.File) error { return errors.New("copy interrupted") }
	var fork string
	beforeDrop = func(name, _ string) {
		fork = filepath.Join(dst, name, "..namedfork", "rsrc")
		if err := os.WriteFile(fork, []byte("retained"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := copyOne(from, to, "note", carryFile, &tempLog{}); err == nil {
		t.Fatal("Expected the copy to fail")
	}
	if got, err := os.ReadFile(fork); err != nil || string(got) != "retained" {
		t.Fatalf("Resource fork holds %q, %v", got, err)
	}
}

func TestAnUnidentifiedEmptyStageKeepsItsExtendedAttributes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const stage = ".carry-123456789abc"
	dir := filepath.Join(dst, stage)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command("xattr", "-w", "org.quecto.test", "kept", dir).CombinedOutput(); err != nil {
		t.Fatal(string(b), err)
	}
	temps := journal(t, tempEntry{Name: stage, Kept: true, From: ".carry-111111111111", ID: "1:1"})
	from, to := openRoots(t, src, dst)
	if kept, err := ClearCarry(from, to, temps); err != nil || !slices.Equal(kept, []string{stage}) {
		t.Error(kept, err)
	}
	if b, err := exec.Command("xattr", "-p", "org.quecto.test", dir).CombinedOutput(); err != nil || strings.TrimSpace(string(b)) != "kept" {
		t.Fatal(string(b), err)
	}
}
