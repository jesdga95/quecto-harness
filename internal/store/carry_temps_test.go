package store

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func carryWith(t *testing.T, src, dst, temps string) Carried {
	t.Helper()
	from, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer from.Close()
	to, err := os.OpenRoot(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer to.Close()
	c, err := CarryRoots(from, to, temps)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func leftTemps(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".carry-") {
			found = append(found, p)
		}
		return nil
	})
	return found
}

// On a volume that takes no hard links each file is still copied whole under
// its final name, and a name already there is kept.
func TestCarryWithoutHardLinks(t *testing.T) {
	saved := linkFile
	linkFile = func(*os.Root, string, string) error {
		return &os.LinkError{Op: "link", Err: syscall.ENOTSUP}
	}
	t.Cleanup(func() { linkFile = saved })
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "sub"), 0o700)
	writeFile(t, filepath.Join(src, "a.txt"), "new")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "b")
	writeFile(t, filepath.Join(dst, "a.txt"), "mine")
	temps := filepath.Join(t.TempDir(), "carry.log")
	c := carryWith(t, src, dst, temps)
	if c.Copied != 1 || c.There != 1 || c.Failed != 0 {
		t.Errorf("carried = %+v", c)
	}
	if replay := carryWith(t, src, dst, temps); replay.Copied != 1 || replay.There != 1 || replay.Interrupted != 0 {
		t.Fatalf("Replay = %+v", replay)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); string(b) != "b" {
		t.Errorf("the copied file = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(b) != "mine" {
		t.Errorf("the file already there = %q, want it kept", b)
	}
	if left := leftTemps(t, dst); len(left) > 0 {
		t.Errorf("temporary files are left: %v", left)
	}
	if names, err := InspectCarry(nil, nil, temps); err != nil || len(names) > 0 {
		t.Errorf("the list still names %v, %v after the copy", names, err)
	}
	if entries, _ := os.ReadDir(home); len(entries) > 0 {
		t.Errorf("the copy put %d entries in the trash", len(entries))
	}
}

// The run after a copy cut short clears exactly the temporary and half-written
// files it recorded, and leaves a person's own file of the same shape or one
// whose identity is not the recorded one.
func TestCarryRemovesTheTempsOfACopyCutShort(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "half.txt"), "whole")
	ours := ".carry-0123456789ab"
	writeFile(t, filepath.Join(dst, ours), "partial")
	writeFile(t, filepath.Join(dst, "half.txt"), "wh")
	writeFile(t, filepath.Join(dst, ".carry-x"), "a person's file")
	writeFile(t, filepath.Join(dst, ".carry-y"), "another")
	id := func(name string) string {
		info, err := os.Lstat(filepath.Join(dst, name))
		if err != nil {
			t.Fatal(err)
		}
		return FileID(info)
	}
	var lines []string
	for _, e := range []tempEntry{
		{Name: ours, ID: id(ours)},
		{Name: "half.txt", ID: id("half.txt"), Partial: true},
		{Name: ".carry-y", ID: "0:0"},
	} {
		b, _ := json.Marshal(e)
		lines = append(lines, string(b))
	}
	temps := filepath.Join(t.TempDir(), "carry.log")
	if err := os.WriteFile(temps, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := carryWith(t, src, dst, temps)
	if c.Copied != 1 || c.There != 0 {
		t.Errorf("carried = %+v", c)
	}
	if _, err := os.Lstat(filepath.Join(dst, ours)); !os.IsNotExist(err) {
		t.Errorf("the recorded temporary file is still there: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "half.txt")); string(b) != "whole" {
		t.Errorf("the half-written file = %q, want it copied again", b)
	}
	for _, name := range []string{".carry-x", ".carry-y"} {
		if _, err := os.Lstat(filepath.Join(dst, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
}

func TestCarryKeepsAnEditedPartialCopy(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	name := "notes.txt"
	writeFile(t, filepath.Join(src, name), "original text")
	path := filepath.Join(dst, name)
	writeFile(t, path, "orig")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := json.Marshal(tempEntry{Name: name, ID: FileID(info), Partial: true})
	if err != nil {
		t.Fatal(err)
	}
	temps := filepath.Join(t.TempDir(), "carry.log")
	if err := os.WriteFile(temps, append(entry, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "my edit")
	result := carryWith(t, src, dst, temps)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if result.Interrupted != 1 || result.There != 0 || string(got) != "my edit" {
		t.Fatalf("Destination = %q, copy = %+v", got, result)
	}
}

// A partial copy whose source changed after the copy stopped is still the
// copy's own file: it is listed, kept as it is, and counted as interrupted.
func TestAPartialWhoseSourceChangedIsInterrupted(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	name := "notes.txt"
	path := filepath.Join(dst, name)
	writeFile(t, path, "orig")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := json.Marshal(tempEntry{Name: name, ID: FileID(info), Partial: true})
	temps := filepath.Join(t.TempDir(), "carry.log")
	if err := os.WriteFile(temps, append(entry, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, name), "rewritten after the copy stopped")
	from, _ := os.OpenRoot(src)
	defer from.Close()
	to, _ := os.OpenRoot(dst)
	defer to.Close()
	if names, err := InspectCarry(from, to, temps); err != nil || len(names) != 1 || names[0] != name {
		t.Fatalf("InspectCarry = %v, %v, want %s", names, err, name)
	}
	c := carryWith(t, src, dst, temps)
	if got, _ := os.ReadFile(path); c.Interrupted != 1 || c.There != 0 || c.Complete() || string(got) != "orig" {
		t.Fatalf("Destination = %q, copy = %+v", got, c)
	}
}

// Where a file's identity changes once data is written, as on FAT and exFAT,
// names a copy cut short left no longer match the list; they are kept and
// counted as interrupted, never as files the folder already had.
func TestAnInterruptedCopyWhoseMakerIsUnknownIsKept(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "half.txt"), "whole")
	var lines []string
	for _, e := range []struct {
		name    string
		partial bool
	}{{"half.txt", true}, {".carry-0123456789ab", false}} {
		path := filepath.Join(dst, e.name)
		writeFile(t, path, "")
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(tempEntry{Name: e.name, ID: FileID(info), Partial: e.partial})
		lines = append(lines, string(b))
		// The data lands under a new identity.
		writeFile(t, path+".new", "wh")
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	temps := filepath.Join(t.TempDir(), "carry.log")
	if err := os.WriteFile(temps, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := carryWith(t, src, dst, temps)
	if c.Interrupted != 2 || c.There != 0 || c.Copied != 0 || c.Complete() {
		t.Errorf("carried = %+v, want two interrupted and nothing else", c)
	}
	for _, name := range []string{"half.txt", ".carry-0123456789ab"} {
		if b, _ := os.ReadFile(filepath.Join(dst, name)); string(b) != "wh" {
			t.Errorf("%s = %q, want it kept as it was", name, b)
		}
	}
}

func TestPartialChangesDuringVerificationAreLocal(t *testing.T) {
	for _, change := range []string{"truncated", "removed", "replaced"} {
		t.Run(change, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			name := "note.txt"
			writeFile(t, filepath.Join(src, name), "original")
			path := filepath.Join(dst, name)
			writeFile(t, path, "orig")
			partial, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "truncated":
				err = os.Truncate(path, 1)
			case "removed":
				err = os.Remove(path)
			case "replaced":
				writeFile(t, path+".new", "orig")
				err = os.Rename(path+".new", path)
			}
			if err != nil {
				t.Fatal(err)
			}
			from, err := os.OpenRoot(src)
			if err != nil {
				t.Fatal(err)
			}
			defer from.Close()
			to, err := os.OpenRoot(dst)
			if err != nil {
				t.Fatal(err)
			}
			defer to.Close()
			if got, err := compareCopyPrefix(from, to, name, partial); err != nil || got != partialUnknown {
				t.Fatalf("Changed partial = %v, %v", got, err)
			}
		})
	}
}

// A copy run again before its result was saved, as after a crash between
// the two, counts the files it already copied as copied, not as files the
// folder already had. A file changed since it was copied is not one of them.
func TestACopyRunAgainBeforeItsResultIsSavedIsComplete(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(src, "sub"), 0o700)
	writeFile(t, filepath.Join(src, "a.txt"), "a")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "b")
	temps := filepath.Join(t.TempDir(), "carry.log")
	if first := carryWith(t, src, dst, temps); first.Copied != 2 || !first.Complete() {
		t.Fatalf("first copy = %+v", first)
	}
	again := carryWith(t, src, dst, temps)
	if again.Copied != 2 || !again.Complete() {
		t.Errorf("the copy run again = %+v, want both files copied", again)
	}
	replaced := filepath.Join(dst, "a.txt")
	writeFile(t, replaced+".new", "mine")
	if err := os.Rename(replaced+".new", replaced); err != nil {
		t.Fatal(err)
	}
	if third := carryWith(t, src, dst, temps); third.Copied != 1 || third.There != 1 {
		t.Errorf("after a copied file was replaced = %+v, want it counted as there", third)
	}
}

// A partial copy that changes after it was hashed and before it is checked
// again is not taken for a prefix of its source.
func TestAPartialChangedAfterItsHashIsKept(t *testing.T) {
	for _, change := range []string{"none", "appended"} {
		t.Run(change, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			name := "note.txt"
			writeFile(t, filepath.Join(src, name), "original")
			path := filepath.Join(dst, name)
			writeFile(t, path, "orig")
			partial, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			saved := partialHashed
			t.Cleanup(func() { partialHashed = saved })
			partialHashed = func(string) {
				if change == "none" {
					return
				}
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				f.WriteString(" and my edit")
				f.Close()
			}
			from, _ := os.OpenRoot(src)
			defer from.Close()
			to, _ := os.OpenRoot(dst)
			defer to.Close()
			want := partialMatches
			if change != "none" {
				want = partialUnknown
			}
			if got, err := compareCopyPrefix(from, to, name, partial); err != nil || got != want {
				t.Fatalf("compareCopyPrefix = %v, %v, want %v", got, err, want)
			}
		})
	}
}

func TestCopyReplayRechecksFilesEditedInPlace(t *testing.T) {
	for _, side := range []string{"source", "destination"} {
		t.Run(side, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			journal := filepath.Join(t.TempDir(), "copy")
			writeFile(t, filepath.Join(src, "note"), "draft")
			if first := carryWith(t, src, dst, journal); !first.Complete() {
				t.Fatal("Initial copy incomplete")
			}
			dir := src
			if side == "destination" {
				dir = dst
			}
			writeFile(t, filepath.Join(dir, "note"), "other")
			result := carryWith(t, src, dst, journal)
			if result.Copied != 0 || result.There != 1 || result.Complete() {
				t.Fatalf("Replay = %+v", result)
			}
		})
	}
}

func TestCarryRecordKeepsCompleteEntriesAtEOF(t *testing.T) {
	const entry = `{"name":"retained","kept":true}`
	for _, tc := range []struct {
		name, data string
		count      int
		damaged    bool
	}{
		{"Torn last line", entry + "\n" + `{"name":`, 1, false},
		{"Torn first line", `{"name":`, 0, false},
		{"Complete last line", entry, 1, false},
		{"Damaged terminated line", entry + "\n{\n", 0, true},
		{"Damaged interior line with torn tail", "{\n" + entry + "\n{", 0, true},
		{"Wrong type at EOF", entry + "\n" + `{"name":42}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "carry")
			writeFile(t, path, tc.data)
			names, err := InspectCarry(nil, nil, path)
			if tc.damaged {
				if !errors.Is(err, ErrCarryRecord) || !strings.Contains(err.Error(), "damaged") {
					t.Fatalf("Record error = %v", err)
				}
			} else if err != nil || len(names) != tc.count || (tc.count == 1 && names[0] != "retained") {
				t.Fatalf("Names = %v, %v", names, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != tc.data {
				t.Fatalf("Preview modified the record")
			}
		})
	}
}

// trashedFile is what the file named base holds in the trash at home.
func trashedFile(t *testing.T, home, base string) string {
	t.Helper()
	files := home
	if runtime.GOOS == "linux" {
		files = filepath.Join(home, "files")
	}
	b, err := os.ReadFile(filepath.Join(files, base))
	if err != nil {
		t.Fatalf("%s is not in the trash: %v", base, err)
	}
	return string(b)
}

// journal writes entries as a copy's list of names and returns its path.
func journal(t *testing.T, entries ...tempEntry) string {
	t.Helper()
	var lines []string
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	path := filepath.Join(t.TempDir(), "carry.log")
	writeFile(t, path, strings.Join(lines, "\n")+"\n")
	return path
}

func fileID(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return FileID(info)
}

func openRoots(t *testing.T, src, dst string) (*os.Root, *os.Root) {
	t.Helper()
	from, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { from.Close() })
	to, err := os.OpenRoot(dst)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { to.Close() })
	return from, to
}

// A partial copy rewritten after its hash with its modification time put
// back, or whose source is, is kept under its name.
func TestClearingKeepsAPartialChangedAfterItsHash(t *testing.T) {
	for _, side := range []string{"destination", "source"} {
		t.Run(side, func(t *testing.T) {
			useTrash(t)
			src, dst := t.TempDir(), t.TempDir()
			writeFile(t, filepath.Join(src, "note"), "original")
			path := filepath.Join(dst, "note")
			writeFile(t, path, "orig")
			temps := journal(t, tempEntry{Name: "note", ID: fileID(t, path), Partial: true})
			changed, body, want := path, "edit", "edit"
			if side == "source" {
				changed, body, want = filepath.Join(src, "note"), "ORIGINAL", "orig"
			}
			saved := partialHashed
			t.Cleanup(func() { partialHashed = saved })
			partialHashed = func(string) {
				info, err := os.Lstat(changed)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, changed, body)
				if err := os.Chtimes(changed, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			from, to := openRoots(t, src, dst)
			kept, err := ClearCarry(from, to, temps)
			if got, _ := os.ReadFile(path); err != nil || !slices.Equal(kept, []string{"note"}) || string(got) != want {
				t.Fatalf("Kept %v, %v; the partial holds %q", kept, err, got)
			}
		})
	}
}

// A partial copy that matches its source goes to the trash, not away, since
// it can change after its last check.
func TestAPartialThatMatchesItsSourceGoesToTheTrash(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "original")
	path := filepath.Join(dst, "note")
	writeFile(t, path, "orig")
	temps := journal(t, tempEntry{Name: "note", ID: fileID(t, path), Partial: true})
	from, to := openRoots(t, src, dst)
	if kept, err := ClearCarry(from, to, temps); err != nil || len(kept) != 0 {
		t.Fatalf("Kept %v, %v", kept, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("The partial is still in place: %v", err)
	}
	if got := trashedFile(t, home, "note"); got != "orig" {
		t.Fatalf("The trash holds %q", got)
	}
}

type editingRead struct{ edit func() }

func (r editingRead) Read([]byte) (int, error) {
	r.edit()
	return 0, errors.New("copy interrupted")
}

// An edit to a file a direct copy is writing, made as the copy fails, is
// still there afterwards, in the trash.
func TestAFailedDirectCopyKeepsAnEditToItsFile(t *testing.T) {
	home := useTrash(t)
	dst := t.TempDir()
	_, to := openRoots(t, t.TempDir(), dst)
	path := filepath.Join(dst, "note")
	read := editingRead{func() { writeFile(t, path, "my edit") }}
	log, err := openTemps(filepath.Join(t.TempDir(), "carry"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.f.Close()
	if err := fillNew(to, "note", read, 0600, log, false, ""); err == nil {
		t.Fatal("Expected a copy failure")
	}
	if got, err := os.ReadFile(path); err == nil {
		if string(got) != "my edit" {
			t.Fatalf("The file holds %q", got)
		}
		return
	}
	if got := trashedFile(t, home, "note"); got != "my edit" {
		t.Fatalf("The trash holds %q", got)
	}
}

// A temporary file a copy cut short left without its final name, edited
// since, goes to the trash under the name it was copied for.
func TestAnEditedTemporaryWithoutItsFinalNameGoesToTheTrash(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "work")
	tmp := filepath.Join(dst, ".carry-123456789abc")
	writeFile(t, tmp, "work")
	temps := journal(t, tempEntry{Name: filepath.Base(tmp), ID: fileID(t, tmp), LinkName: "note"})
	writeFile(t, tmp, "my edit")
	from, to := openRoots(t, src, dst)
	if kept, err := ClearCarry(from, to, temps); err != nil || len(kept) != 0 {
		t.Fatalf("Kept %v, %v", kept, err)
	}
	if _, err := os.Lstat(tmp); !os.IsNotExist(err) {
		t.Fatalf("The temporary file is still in place: %v", err)
	}
	if got := trashedFile(t, home, "note"); got != "my edit" {
		t.Fatalf("The trash holds %q", got)
	}
}

func TestFailedLinkPreservesWritesThroughAnOpenFile(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "source")
	var opened *os.File
	saved := linkFile
	t.Cleanup(func() { linkFile = saved })
	linkFile = func(r *os.Root, tmp, rel string) error {
		var err error
		opened, err = r.OpenFile(tmp, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { opened.Close() })
		return os.ErrPermission
	}
	carryWith(t, src, dst, filepath.Join(t.TempDir(), "carry"))
	if opened == nil {
		t.Fatal("The copy did not reach the link")
	}
	if _, err := opened.WriteAt([]byte("edited"), 0); err != nil {
		t.Fatal(err)
	}
	if got := trashedFile(t, home, "note"); got != "edited" {
		t.Fatalf("Recoverable contents = %q", got)
	}
}

// A file past the room left for the copy is not copied, and leaves nothing in
// the destination or the trash.
func TestAFilePastTheRoomLeavesNothingBehind(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "big"), "more than four bytes")
	from, to := openRoots(t, src, dst)
	log, err := openTemps(filepath.Join(t.TempDir(), "carry"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.f.Close()
	if _, err := copyOne(from, to, "big", 4, log); !errors.Is(err, errTooLarge) {
		t.Fatalf("Copy error = %v", err)
	}
	for _, dir := range []string{dst, home} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s holds %d entries", dir, len(entries))
		}
	}
	if len(log.kept) != 0 {
		t.Errorf("Kept %v", log.kept)
	}
}

func TestFailedCopySyncPreservesAnEarlierEdit(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "source")
	from, to := openRoots(t, src, dst)
	saved := syncCopy
	t.Cleanup(func() { syncCopy = saved })
	syncCopy = func(out *os.File) error {
		writeFile(t, out.Name(), "edited")
		return syscall.EIO
	}
	if _, err := copyOne(from, to, "note", carryFile, &tempLog{}); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Copy error = %v", err)
	}
	if got := trashedFile(t, home, "note"); got != "edited" {
		t.Fatalf("Recoverable contents = %q", got)
	}
}

// A temporary file nothing touched after a copy failed before its final name
// is removed, since its bytes are still in the source, and the trash stays
// empty.
func TestAnUntouchedTemporaryOfAFailedCopyIsRemoved(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "source")
	from, to := openRoots(t, src, dst)
	saved := syncCopy
	t.Cleanup(func() { syncCopy = saved })
	syncCopy = func(*os.File) error { return syscall.EIO }
	if _, err := copyOne(from, to, "note", carryFile, &tempLog{}); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Copy error = %v", err)
	}
	for _, dir := range []string{dst, home} {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("%s holds %d entries", dir, len(entries))
		}
	}
}

// A direct copy that fails partway, untouched since, is removed when its bytes
// are the start of the file it was copied from.
func TestAnUntouchedFailedDirectCopyIsRemoved(t *testing.T) {
	home := useTrash(t)
	dst := t.TempDir()
	_, to := openRoots(t, t.TempDir(), dst)
	writeFile(t, filepath.Join(dst, ".carry-source"), "source")
	read := io.MultiReader(strings.NewReader("sou"), editingRead{func() {}})
	log, err := openTemps(filepath.Join(t.TempDir(), "carry"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.f.Close()
	if err := fillNew(to, "note", read, 0600, log, false, ".carry-source"); err == nil {
		t.Fatal("Expected a copy failure")
	}
	if _, err := os.Lstat(filepath.Join(dst, "note")); !os.IsNotExist(err) {
		t.Fatalf("The partial copy is still there: %v", err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("The trash holds %d entries", len(entries))
	}
}

// A file put under a temporary name after it was found to be a second link
// of its final name is not removed with it.
func TestARedundantNameReplacedBeforeItsRemovalIsKept(t *testing.T) {
	useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "work")
	tmp := filepath.Join(dst, ".carry-123456789abc")
	writeFile(t, tmp, "work")
	if err := os.Link(tmp, filepath.Join(dst, "note")); err != nil {
		t.Fatal(err)
	}
	temps := journal(t, tempEntry{Name: filepath.Base(tmp), ID: fileID(t, tmp), LinkName: "note"})
	saved := beforeDrop
	t.Cleanup(func() { beforeDrop = saved })
	beforeDrop = func(string, string) {
		if err := os.Remove(tmp); err != nil {
			t.Fatal(err)
		}
		writeFile(t, tmp, "someone else's file")
	}
	from, to := openRoots(t, src, dst)
	kept, err := ClearCarry(from, to, temps)
	if err != nil || len(kept) != 1 {
		t.Fatalf("Kept %v, %v", kept, err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, kept[0])); err != nil || string(got) != "someone else's file" {
		t.Fatalf("%s holds %q, %v", kept[0], got, err)
	}
}

func TestFailedSyncKeepsWritesThroughAnOpenFile(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "source")
	from, to := openRoots(t, src, dst)
	saved := syncCopy
	t.Cleanup(func() { syncCopy = saved })
	var writer *os.File
	syncCopy = func(out *os.File) error {
		var err error
		writer, err = os.OpenFile(out.Name(), os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { writer.Close() })
		return syscall.EIO
	}
	if _, err := copyOne(from, to, "note", carryFile, &tempLog{}); !errors.Is(err, syscall.EIO) {
		t.Fatal(err)
	}
	if _, err := writer.WriteAt([]byte("edited"), 0); err != nil {
		t.Fatal(err)
	}
	if got := trashedFile(t, home, "note"); got != "edited" {
		t.Fatalf("Trash holds %q", got)
	}
}

func TestCleanupPreservesAnOccupiedPrivateName(t *testing.T) {
	_, to := openRoots(t, t.TempDir(), t.TempDir())
	writeFile(t, filepath.Join(to.Name(), "note"), "original")
	info, err := to.Stat("note")
	if err != nil {
		t.Fatal(err)
	}
	hook := beforeDrop
	t.Cleanup(func() { beforeDrop = hook })
	var occupied string
	beforeDrop = func(_, private string) { occupied = private; writeFile(t, filepath.Join(to.Name(), private), "other") }
	left, err := dropName(to, "note", info, func(*os.Root, string, os.FileInfo) bool { return true }, false, (&tempLog{}).add)
	if left != "note" || !errors.Is(err, os.ErrExist) {
		t.Fatalf("Left %q, %v", left, err)
	}
	for name, want := range map[string]string{"note": "original", occupied: "other"} {
		got, err := to.ReadFile(name)
		if err != nil || string(got) != want {
			t.Fatalf("%s holds %q, %v", name, got, err)
		}
	}
}

// A file a cleanup cut short left under its private name goes back under its
// own name when that is free, and is named when it is not.
func TestAnInterruptedCleanupRestoresItsPrivateFile(t *testing.T) {
	for _, taken := range []bool{false, true} {
		t.Run(strconv.FormatBool(taken), func(t *testing.T) {
			_, to := openRoots(t, t.TempDir(), t.TempDir())
			writeFile(t, filepath.Join(to.Name(), "note"), "work")
			info, err := to.Stat("note")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "carry")
			log, err := openTemps(path)
			if err != nil {
				t.Fatal(err)
			}
			defer log.f.Close()
			var private string
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				dropName(to, "note", info, func(_ *os.Root, name string, _ os.FileInfo) bool {
					private = name
					runtime.Goexit()
					return false
				}, false, log.add)
			}()
			<-stopped
			if private == "" {
				t.Fatal("Cleanup did not reach the private name")
			}
			if taken {
				writeFile(t, filepath.Join(to.Name(), "note"), "a new file")
			}
			names, err := ClearCarry(nil, to, path)
			want, at := []string(nil), "note"
			if taken {
				want, at = []string{private}, private
			}
			if err != nil || !slices.Equal(names, want) {
				t.Fatalf("Recovery = %v, %v", names, err)
			}
			if got, err := to.ReadFile(at); err != nil || string(got) != "work" {
				t.Fatalf("Recovered %q, %v", got, err)
			}
		})
	}
}

// A temporary second link a cleanup cut short left under its private name is
// checked again on the next run, so the copy completes.
func TestACutShortCleanupOfASecondLinkCompletes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "work")
	tmp := ".carry-123456789abc"
	private := ".carry-cba987654321"
	writeFile(t, filepath.Join(dst, "note"), "work")
	if err := os.Link(filepath.Join(dst, "note"), filepath.Join(dst, private)); err != nil {
		t.Fatal(err)
	}
	id := fileID(t, filepath.Join(dst, "note"))
	temps := journal(t,
		tempEntry{Name: tmp, ID: id, LinkName: "note"},
		tempEntry{Name: private, Kept: true, From: tmp, ID: id},
	)
	c := carryWith(t, src, dst, temps)
	if !c.Complete() || c.Interrupted != 0 || len(leftTemps(t, dst)) != 0 {
		t.Fatalf("Copy = %+v, temporary files = %v", c, leftTemps(t, dst))
	}
}

func TestStorageNamesForeignProjectFilesMovedDuringCopyCleanup(t *testing.T) {
	useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	ledger := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, ledger, root, dst, nil)
	from, to := openRoots(t, dir, dst)
	path := ledger.Temps(dir)
	var records []tempEntry
	for _, name := range []string{".carry-aaaaaaaaaaaa", ".carry-bbbbbbbbbbbb"} {
		file := filepath.Join(dst, name)
		writeFile(t, file, "partial")
		records = append(records, tempEntry{Name: name, ID: fileID(t, file), LinkName: name + "-final"})
	}
	if err := writeCarryEntries(path, records); err != nil {
		t.Fatal(err)
	}
	var current string
	onRename(t, func(path string) {
		current = path
		if err := os.Rename(path, path+"-aside"); err != nil {
			t.Fatal(err)
		}
		writeFile(t, path, "foreign")
	})
	onLstatMoved(t, func(path string) (os.FileInfo, error) {
		writeFile(t, current, "occupied")
		return os.Lstat(path)
	})
	if _, err := ClearCarry(from, to, path); err != nil {
		t.Fatal(err)
	}
	notices, err := ledger.TrashFailures()
	if err != nil || len(notices) != len(records) {
		t.Fatalf("Storage = %+v, %v", notices, err)
	}
	for _, f := range notices {
		if !f.Foreign || filepath.Dir(f.Path) != dst {
			t.Fatalf("Unexpected notice %+v", f)
		}
		if got, err := os.ReadFile(f.Trash); err != nil || string(got) != "foreign" {
			t.Fatalf("Trash holds %q, %v", got, err)
		}
	}
}

func TestAnUnreadableTrashedProjectFileCanBeCheckedAfterRestart(t *testing.T) {
	useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	state := t.TempDir()
	l := NewScratchLedger(state, root, "store")
	dir := ownedOrder(t, l, root, dst, nil)
	from, to := openRoots(t, dir, dst)
	const name = ".carry-111111111111"
	file := filepath.Join(dst, name)
	writeFile(t, file, "edited")
	path := l.Temps(dir)
	if err := writeCarryEntries(path, []tempEntry{{Name: name, LinkName: "note", ID: fileID(t, file)}}); err != nil {
		t.Fatal(err)
	}
	onLstatMoved(t, func(string) (os.FileInfo, error) { return nil, os.ErrPermission })
	if _, err := ClearCarry(from, to, path); err != nil {
		t.Fatal(err)
	}
	l = NewScratchLedger(state, root, "store")
	notices, err := l.TrashFailures()
	if err != nil || len(notices) != 1 || !notices[0].Waiting || notices[0].Path != file {
		t.Fatalf("Storage after restart = %+v, %v", notices, err)
	}
	trash := notices[0].Trash
	if err := os.Rename(trash, trash+"-aside"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, trash, "other")
	lstatMoved = os.Lstat
	notices, err = l.TrashFailures()
	if err != nil || len(notices) != 1 || !strings.Contains(notices[0].Reason, "another entry") {
		t.Fatalf("Replacement = %+v, %v", notices, err)
	}
	if err := os.Rename(trash+"-aside", trash); err != nil {
		t.Fatal(err)
	}
	l.Sweep([]string{dir}, time.Now())
	if notices, err = l.TrashFailures(); err != nil || len(notices) != 0 {
		t.Fatalf("Verified entry = %+v, %v", notices, err)
	}
}

// On a volume without a rename that refuses to replace, the temporary second
// link of a copied file is still removed once it is checked again, so the copy
// completes. If the final name no longer shares its file, it is kept.
func TestASecondLinkGoesWithoutAnExclusiveRename(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(strconv.FormatBool(replaced), func(t *testing.T) {
			home := useTrash(t)
			src, dst := t.TempDir(), t.TempDir()
			writeFile(t, filepath.Join(src, "note"), "work")
			saved := dropRename
			t.Cleanup(func() { dropRename = saved })
			dropRename = func(int, string, string) error {
				if replaced {
					final := filepath.Join(dst, "note")
					if err := os.Remove(final); err != nil {
						t.Fatal(err)
					}
					writeFile(t, final, "work")
				}
				return &os.LinkError{Op: "rename", Err: syscall.EINVAL}
			}
			c := carryWith(t, src, dst, filepath.Join(t.TempDir(), "carry"))
			left := leftTemps(t, dst)
			if replaced {
				// No longer a second link, it is not removed in place.
				if len(left) == 0 && trashedFile(t, home, "note") != "work" {
					t.Fatalf("Copy = %+v, the temporary file is gone", c)
				}
				return
			}
			if !c.Complete() || c.Copied != 1 || len(left) != 0 {
				t.Fatalf("Copy = %+v, temporary files = %v", c, left)
			}
			if got, err := os.ReadFile(filepath.Join(dst, "note")); err != nil || string(got) != "work" {
				t.Fatalf("Copied file = %q, %v", got, err)
			}
		})
	}
}

// A move of a project file recorded before a crash and never made leaves the
// file in place and nothing in the trash; the sweep drops its notice.
func TestARecordedProjectMoveNeverMadeIsSettled(t *testing.T) {
	home := useTrash(t)
	root, dst, state := t.TempDir(), t.TempDir(), t.TempDir()
	l := NewScratchLedger(state, root, "store")
	dir := ownedOrder(t, l, root, dst, nil)
	file := filepath.Join(dst, ".carry-111111111111")
	writeFile(t, file, "edited")
	notice := filepath.Join(state, filepath.Base(dir)+".0123456789abcdef01234567"+copyTrashSuffix)
	if err := writeJSON(notice, trashFailureRecord{
		Order: RemovalOrder{Folder: dir, Store: "store"}, ExpectedID: fileID(t, file),
		Failure: TrashFailure{Path: file, Trash: filepath.Join(home, "note"), Waiting: true, Reason: "the move to this trash path has not been verified"},
	}); err != nil {
		t.Fatal(err)
	}
	if listed, err := l.TrashFailures(); err != nil || len(listed) != 0 {
		t.Fatalf("Storage = %+v, %v", listed, err)
	}
	l.Sweep(nil, time.Now())
	if _, err := os.Lstat(notice); !os.IsNotExist(err) {
		t.Fatalf("The notice outlived the move it recorded: %v", err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "edited" {
		t.Fatalf("The file = %q, %v", got, err)
	}
}

func TestCopyCleanupKeepsAReplacementWithoutExclusiveRename(t *testing.T) {
	useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "work")
	rename, drop := dropRename, beforeDrop
	t.Cleanup(func() { dropRename, beforeDrop = rename, drop })
	fallback := false
	dropRename = func(int, string, string) error { fallback = true; return syscall.EINVAL }
	beforeDrop = func(name, private string) {
		if !fallback {
			return
		}
		file := filepath.Join(dst, name)
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
		writeFile(t, file, "independent contents")
	}
	result := carryWith(t, src, dst, filepath.Join(t.TempDir(), "carry"))
	if result.Interrupted == 0 {
		t.Fatalf("Replacement unreported: %+v", result)
	}
	found := false
	for _, name := range result.InterruptedNames {
		err := filepath.WalkDir(filepath.Join(dst, name), func(path string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.Type().IsRegular() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if string(data) == "independent contents" {
					found = true
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !found {
		t.Fatal("Lost the file that replaced the temporary link")
	}
}

func TestSweepKeepsTheNoticeOfAnActiveCopyTrashMove(t *testing.T) {
	useTrash(t)
	root, dst, state := t.TempDir(), t.TempDir(), t.TempDir()
	l := NewScratchLedger(state, root, "store")
	dir := ownedOrder(t, l, root, dst, nil)
	file := filepath.Join(dst, ".carry-111111111111")
	writeFile(t, file, "edited")
	onRename(t, func(string) { l.Sweep([]string{dir}, time.Now()) })
	onLstatMoved(t, func(path string) (os.FileInfo, error) {
		if _, err := os.Lstat(path); err != nil {
			return nil, err
		}
		return nil, os.ErrPermission
	})
	_, err := trashCopyFile(l.Temps(dir), file, fileID(t, file), filepath.Join(dst, "note"))
	var move *UncheckedMove
	if !errors.As(err, &move) {
		t.Fatalf("Move = %v", err)
	}
	listed, err := l.TrashFailures()
	if err != nil || len(listed) != 1 || !listed[0].Waiting || listed[0].Trash != move.Trash || listed[0].Path != file {
		t.Fatalf("Storage lost the pending move: %+v, %v", listed, err)
	}
}

// On a volume with neither hard links nor a rename that refuses to replace,
// as FAT and exFAT, a copy still completes and leaves no temporary file.
func TestACopyCompletesWithoutLinksOrExclusiveRename(t *testing.T) {
	home := useTrash(t)
	savedLink, savedRename := linkFile, dropRename
	t.Cleanup(func() { linkFile, dropRename = savedLink, savedRename })
	linkFile = func(*os.Root, string, string) error { return &os.LinkError{Op: "link", Err: syscall.ENOTSUP} }
	dropRename = func(int, string, string) error { return &os.LinkError{Op: "rename", Err: syscall.ENOTSUP} }
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "a")
	writeFile(t, filepath.Join(src, "b.txt"), "b")
	c := carryWith(t, src, dst, filepath.Join(t.TempDir(), "carry"))
	if !c.Complete() || c.Copied != 2 || c.Interrupted != 0 {
		t.Fatalf("Copy = %+v", c)
	}
	if left := leftTemps(t, dst); len(left) != 0 {
		t.Fatalf("Temporary files = %v", left)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("The trash holds %d entries", len(entries))
	}
}

// A file that fails its check in the private directory goes back under its
// own name rather than stay hidden there.
func TestAFileThatFailsItsCheckReturnsToItsName(t *testing.T) {
	saved := dropRename
	t.Cleanup(func() { dropRename = saved })
	dropRename = func(int, string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EINVAL} }
	dst := t.TempDir()
	_, to := openRoots(t, t.TempDir(), dst)
	writeFile(t, filepath.Join(dst, "note"), "work")
	info, err := os.Lstat(filepath.Join(dst, "note"))
	if err != nil {
		t.Fatal(err)
	}
	checks := 0
	held := func(*os.Root, string, os.FileInfo) bool { checks++; return checks == 1 }
	left, err := dropName(to, "note", info, held, false, (&tempLog{}).add)
	if left != "note" || err == nil {
		t.Fatalf("Left %q, %v", left, err)
	}
	if got, err := os.ReadFile(filepath.Join(dst, "note")); err != nil || string(got) != "work" {
		t.Fatalf("note = %q, %v", got, err)
	}
	if left := leftTemps(t, dst); len(left) != 0 {
		t.Fatalf("Private names left = %v", left)
	}
}

// On a volume without links, a temporary file left untouched when its final
// name was taken meanwhile is removed, not trashed, and the other file stays.
func TestAnUntouchedTemporaryIsRemovedWhenItsFinalNameIsTaken(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "work")
	saved := linkFile
	t.Cleanup(func() { linkFile = saved })
	linkFile = func(*os.Root, string, string) error {
		writeFile(t, filepath.Join(dst, "note"), "someone else's")
		return &os.LinkError{Op: "link", Err: syscall.ENOTSUP}
	}
	carryWith(t, src, dst, filepath.Join(t.TempDir(), "carry"))
	if left := leftTemps(t, dst); len(left) != 0 {
		t.Fatalf("Temporary files = %v", left)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("The trash holds %d entries", len(entries))
	}
	if got, err := os.ReadFile(filepath.Join(dst, "note")); err != nil || string(got) != "someone else's" {
		t.Fatalf("note = %q, %v", got, err)
	}
}

// A file a copy left goes to the trash even when the owner record of the
// folder it was copied from cannot be read.
func TestAFileLeftByACopyIsTrashedWithoutItsOwnerRecord(t *testing.T) {
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	tmp := filepath.Join(dst, ".carry-123456789abc")
	writeFile(t, tmp, "work")
	temps := filepath.Join(t.TempDir(), "scratch-2026-11-27-abcdef.carry")
	if err := writeCarryEntries(temps, []tempEntry{{Name: filepath.Base(tmp), ID: fileID(t, tmp), LinkName: "note"}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, tmp, "my edit")
	from, to := openRoots(t, src, dst)
	if kept, err := ClearCarry(from, to, temps); err != nil || len(kept) != 0 {
		t.Fatalf("Kept %v, %v", kept, err)
	}
	if got := trashedFile(t, home, "note"); got != "my edit" {
		t.Fatalf("The trash holds %q", got)
	}
}

func TestUnsupportedRestoreReportsThePrivateFile(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(strconv.FormatBool(replay), func(t *testing.T) {
			oldRename, oldLink := dropRename, linkFile
			t.Cleanup(func() { dropRename, linkFile = oldRename, oldLink })
			dropRename = func(int, string, string) error { return syscall.ENOTSUP }
			linkFile = func(*os.Root, string, string) error { return syscall.ENOTSUP }
			_, to := openRoots(t, t.TempDir(), t.TempDir())
			const name = "note"
			const stage = ".carry-111111111111"
			var left string
			if replay {
				if err := to.Mkdir(stage, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(stage, "File-123456abcdef")
				writeFile(t, filepath.Join(to.Name(), path), "kept")
				sidecar := filepath.Join(to.Name(), stage, "._"+filepath.Base(path))
				writeFile(t, sidecar, "metadata")
				id := fileID(t, filepath.Join(to.Name(), path))
				log := journal(t, tempEntry{Name: stage, From: name, Kept: true, ID: id})
				for range 2 {
					names, err := ClearCarry(nil, to, log)
					if err != nil || !slices.Equal(names, []string{path}) {
						t.Fatalf("Retained %v, %v", names, err)
					}
				}
				if b, err := os.ReadFile(sidecar); err != nil || string(b) != "metadata" {
					t.Fatalf("Metadata = %q, %v", b, err)
				}
				left = path
			} else {
				writeFile(t, filepath.Join(to.Name(), name), "kept")
				info, err := to.Stat(name)
				if err != nil {
					t.Fatal(err)
				}
				checks := 0
				left, err = dropName(to, name, info, func(*os.Root, string, os.FileInfo) bool { checks++; return checks == 1 }, false, (&tempLog{}).add)
				if left == name || err == nil {
					t.Fatalf("Unsupported restoration returned %q, %v", left, err)
				}
			}
			if b, err := to.ReadFile(left); err != nil || string(b) != "kept" {
				t.Fatalf("%q = %q, %v", left, b, err)
			}
		})
	}
}

func TestBlockedRestoreKeepsTheOriginalCleanupRecord(t *testing.T) {
	useTrash(t)
	_, to := openRoots(t, t.TempDir(), t.TempDir())
	const tmp, private = ".carry-111111111111", ".carry-222222222222"
	writeFile(t, filepath.Join(to.Name(), "final"), "copied")
	if err := to.Link("final", private); err != nil {
		t.Fatal(err)
	}
	id := fileID(t, filepath.Join(to.Name(), private))
	log := journal(t, tempEntry{Name: tmp, ID: id, LinkName: "final"}, tempEntry{Name: private, From: tmp, Kept: true, ID: id})
	writeFile(t, filepath.Join(to.Name(), tmp), "occupied")
	if _, err := ClearCarry(nil, to, log); err != nil {
		t.Fatal(err)
	}
	if b, err := to.ReadFile(tmp); err != nil || string(b) != "occupied" {
		t.Fatal("Lost occupied name", err)
	}
	if err := to.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	left, err := ClearCarry(nil, to, log)
	if err != nil || len(left) != 0 || len(leftTemps(t, to.Name())) != 0 {
		t.Fatalf("Retained %v, %v", left, err)
	}
	proofs, err := copiedEntries(log)
	if err != nil || proofs["final"].ID != id {
		t.Fatal("Lost completed copy", proofs, err)
	}
}

func TestInterruptedPrivateCopyOnAVolumeWithoutLinks(t *testing.T) {
	oldRename, oldLink := dropRename, linkFile
	t.Cleanup(func() { dropRename, linkFile = oldRename, oldLink })
	dropRename = func(int, string, string) error { return syscall.ENOTSUP }
	linkFile = func(*os.Root, string, string) error { return syscall.ENOTSUP }
	from, to := openRoots(t, t.TempDir(), t.TempDir())
	const tmp, stage = ".carry-111111111111", ".carry-222222222222"
	if err := to.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(stage, "File-123456abcdef")
	for _, file := range []string{filepath.Join(from.Name(), "final"), filepath.Join(to.Name(), "final"), filepath.Join(to.Name(), child)} {
		writeFile(t, file, "copied")
	}
	id := fileID(t, filepath.Join(to.Name(), child))
	log := journal(t, tempEntry{Name: tmp, ID: id, LinkName: "final"}, tempEntry{Name: "final", Copied: true, ID: fileID(t, filepath.Join(to.Name(), "final"))}, tempEntry{Name: stage, From: tmp, Kept: true, ID: id})
	trash := useTrash(t)
	c, err := CarryRoots(from, to, log)
	if err != nil || !c.Complete() || c.Copied != 1 || len(leftTemps(t, to.Name())) != 0 {
		t.Fatalf("Copy = %+v, %v", c, err)
	}
	if names, err := os.ReadDir(trash); err != nil || len(names) != 0 {
		t.Fatalf("Trash = %v, %v", names, err)
	}
}

func TestReplayRetiresOnlyItsOwnEmptyStagingDirectory(t *testing.T) {
	oldRename, oldLink := dropRename, linkFile
	t.Cleanup(func() { dropRename, linkFile = oldRename, oldLink })
	dropRename = func(int, string, string) error { return syscall.ENOTSUP }
	linkFile = func(*os.Root, string, string) error { return syscall.ENOTSUP }
	for _, replace := range []bool{false, true} {
		t.Run(strconv.FormatBool(replace), func(t *testing.T) {
			_, to := openRoots(t, t.TempDir(), t.TempDir())
			writeFile(t, filepath.Join(to.Name(), "note"), "saved")
			info, err := to.Stat("note")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "carry")
			log, err := openTemps(path)
			if err != nil {
				t.Fatal(err)
			}
			checks := 0
			left, _ := dropName(to, "note", info, func(*os.Root, string, os.FileInfo) bool { checks++; return checks == 1 }, true, log.add)
			if err := log.f.Close(); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Dir(left)
			if stage == "." {
				t.Fatal("No staged file", left)
			}
			if err := to.Rename(left, "recovered"); err != nil {
				t.Fatal(err)
			}
			if replace {
				if err := to.Rename(stage, "aside"); err != nil {
					t.Fatal(err)
				}
				if err := to.Mkdir(stage, 0700); err != nil {
					t.Fatal(err)
				}
			}
			names, err := ClearCarry(nil, to, path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := to.Lstat(stage); replace {
				if err != nil || !slices.Contains(names, stage) {
					t.Fatalf("Replacement = %v, %v", names, err)
				}
			} else if !os.IsNotExist(err) || len(names) != 0 {
				t.Fatalf("Empty staging directory = %v, %v", names, err)
			}
		})
	}
}

// An empty file is created empty under its final name with no temporary file,
// on any volume, and counted as copied.
func TestAnEmptyFileTakesNoTemporaryName(t *testing.T) {
	savedLink, savedRename := linkFile, dropRename
	t.Cleanup(func() { linkFile, dropRename = savedLink, savedRename })
	linkFile = func(*os.Root, string, string) error {
		t.Error("An empty file was given a temporary name")
		return &os.LinkError{Op: "link", Err: syscall.ENOTSUP}
	}
	dropRename = func(int, string, string) error { return &os.LinkError{Op: "rename", Err: syscall.ENOTSUP} }
	home := useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "__init__.py"), "")
	journal := filepath.Join(t.TempDir(), "carry")
	c := carryWith(t, src, dst, journal)
	if !c.Complete() || c.Copied != 1 || c.Interrupted != 0 {
		t.Fatalf("Copy = %+v", c)
	}
	if info, err := os.Lstat(filepath.Join(dst, "__init__.py")); err != nil || info.Size() != 0 {
		t.Fatalf("Empty file = %v, %v", info, err)
	}
	if left := leftTemps(t, dst); len(left) != 0 {
		t.Fatalf("Temporary files = %v", left)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("The trash holds %d entries", len(entries))
	}
	if again := carryWith(t, src, dst, journal); !again.Complete() || again.Copied != 1 {
		t.Fatalf("The copy run again = %+v", again)
	}
}

// An empty private folder a cleanup made before it could record its identity
// is removed on the next run, so the copy can complete.
func TestAnEmptyPrivateFolderWithoutItsIdentityIsRemoved(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const private = ".carry-cba987654321"
	if err := os.Mkdir(filepath.Join(dst, private), 0o700); err != nil {
		t.Fatal(err)
	}
	temps := journal(t, tempEntry{Name: private, Kept: true, From: ".carry-123456789abc", ID: "1:1"})
	from, to := openRoots(t, src, dst)
	if kept, err := ClearCarry(from, to, temps); err != nil || len(kept) != 0 {
		t.Fatalf("Kept %v, %v", kept, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, private)); !os.IsNotExist(err) {
		t.Fatalf("The private folder is still there: %v", err)
	}
}

func TestAnEmptyCopyWithAFailedJournalIsRecoveredOrNamed(t *testing.T) {
	useTrash(t)
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "empty"), "")
	from, to := openRoots(t, src, dst)
	log, err := openTemps(filepath.Join(t.TempDir(), "carry"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := copyOne(from, to, "empty", carryFile, log); err == nil {
		t.Fatal("Copied despite a failed journal")
	}
	if _, err := to.Lstat("empty"); !errors.Is(err, os.ErrNotExist) && !slices.Contains(log.kept, "empty") {
		t.Fatal("Left an unreported file after the journal failed", log.kept, err)
	}
}

func TestAnEmptyStageNeedsAPrivateNameWithoutARecordedIdentity(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	const name = "project-folder"
	if err := os.Mkdir(filepath.Join(dst, name), 0700); err != nil {
		t.Fatal(err)
	}
	temps := journal(t, tempEntry{Name: name, Kept: true, From: ".carry-123456789abc", ID: "1:1"})
	from, to := openRoots(t, src, dst)
	if kept, err := ClearCarry(from, to, temps); err != nil || !slices.Equal(kept, []string{name}) {
		t.Fatal(kept, err)
	}
	if _, err := to.Lstat(name); err != nil {
		t.Fatal(err)
	}
}

func TestReplayRecognizesACompletedCopyAtItsFinalName(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(src, "note"), "")
	_, to := openRoots(t, src, dst)
	path := filepath.Join(t.TempDir(), "journal")
	log, err := openTemps(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fillNew(to, "note", strings.NewReader(""), 0600, log, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := log.f.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := readCarryEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		if e.Done || e.Copied {
			// Stop at the first durable record of completion.
			if err := writeCarryEntries(path, entries[:i+1]); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	info, err := to.Lstat("note")
	if err != nil {
		t.Fatal(err)
	}
	c := carryWith(t, src, dst, path)
	if !c.Complete() || c.Copied != 1 {
		t.Fatal(c)
	}
	after, err := to.Lstat("note")
	if err != nil || !os.SameFile(info, after) {
		t.Fatal("Replaced the completed copy", err)
	}
}
