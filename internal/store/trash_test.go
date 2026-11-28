package store

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every test trashes into a folder of its own, never the person's trash.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "quecto-trash-")
	if err != nil {
		panic(err)
	}
	TrashHome = func() string { return dir }
	TrashTop = func(string) string { return "" }
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// useTrash points the home trash at a new folder for one test.
func useTrash(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	saved := TrashHome
	TrashHome = func() string { return home }
	t.Cleanup(func() { TrashHome = saved })
	return home
}

// keptOrTrashed is the path of rel in dir, or in the copy of dir in the trash
// at home. Empty when it is in neither.
func keptOrTrashed(t *testing.T, dir, home, rel string) string {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(dir, rel)); err == nil {
		return filepath.Join(dir, rel)
	}
	for _, pattern := range []string{
		filepath.Join(home, filepath.Base(dir)+"*", rel),
		filepath.Join(home, "files", filepath.Base(dir)+"*", rel),
	} {
		if found, _ := filepath.Glob(pattern); len(found) == 1 {
			return found[0]
		}
	}
	return ""
}

// ownedOrder makes a scratch folder in root holding files, with the same files
// in dst, and orders it removed after a copy to dst.
func ownedOrder(t *testing.T, l *ScratchLedger, root, dst string, files map[string]string) string {
	t.Helper()
	dir, err := MintScratch(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		writeFile(t, filepath.Join(dir, name), body)
		writeFile(t, filepath.Join(dst, name), body)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Own(dir, "s1", info); err != nil {
		t.Fatal(err)
	}
	if err := l.Order(dir, "s1", RemoveCopied, dst, time.Now()); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A file open for writing when its folder is swept keeps what is written
// through it afterwards, in the trash.
func TestAWriteAfterTheSweepIsRecoverable(t *testing.T) {
	home := useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "1:1")
	dir := ownedOrder(t, l, root, dst, map[string]string{"a": "old!"})
	f, err := os.OpenFile(filepath.Join(dir, "a"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if swept := l.Sweep(nil, time.Now().Add(8*24*time.Hour)); len(swept) != 1 {
		t.Fatalf("swept = %v", swept)
	}
	if _, err := f.WriteAt([]byte("new!"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	path := keptOrTrashed(t, dir, home, "a")
	if got, err := os.ReadFile(path); err != nil || string(got) != "new!" {
		t.Fatalf("%q = %q, %v, want the write kept", path, got, err)
	}
}

// A source whose copy lost its executable bit is not lost with its mode.
func TestAnExecutableSourceKeepsItsMode(t *testing.T) {
	home := useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "1:1")
	dir := ownedOrder(t, l, root, dst, map[string]string{"tool.sh": "echo hi\n"})
	if err := os.Chmod(filepath.Join(dir, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dst, "tool.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	var order RemovalOrder
	readJSON(l.orderPath(dir), &order)
	order.At = time.Now()
	writeJSON(l.orderPath(dir), order)
	l.Sweep(nil, time.Now().Add(8*24*time.Hour))
	path := keptOrTrashed(t, dir, home, "tool.sh")
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("%q mode = %v, %v, want 0755 kept", path, info, err)
	}
}

// A project edit between the last check and the move leaves no hidden
// folder: the source is under its own name or in the trash, unchanged.
func TestAnEditDuringTheLastCheckHidesNothing(t *testing.T) {
	home := useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "1:1")
	big := strings.Repeat("x", 16<<20)
	dir := ownedOrder(t, l, root, dst, map[string]string{"a.bin": big, "z.txt": "original"})
	var once sync.Once
	edit := func() { once.Do(func() { writeFile(t, filepath.Join(dst, "z.txt"), "changed") }) }
	saved := beforeTrash
	beforeTrash = func(string) { edit() }
	t.Cleanup(func() { beforeTrash = saved })
	stop, polled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(polled)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if found, _ := filepath.Glob(filepath.Join(root, ".removal-*")); len(found) > 0 {
				edit()
				return
			}
		}
	}()
	l.Sweep(nil, time.Now().Add(8*24*time.Hour))
	close(stop)
	<-polled
	if found, _ := filepath.Glob(filepath.Join(root, ".removal-*")); len(found) > 0 {
		t.Errorf("a folder is hidden as %v", found)
	}
	path := keptOrTrashed(t, dir, home, "z.txt")
	if got, err := os.ReadFile(path); err != nil || string(got) != "original" {
		t.Fatalf("%q = %q, %v, want the source kept", path, got, err)
	}
}

// With no trash on the folder's volume the folder stays, and its order with
// it for a later try.
func TestAFolderWithNoTrashStays(t *testing.T) {
	saved := TrashHome
	unavailable := filepath.Join(t.TempDir(), "not-a-directory")
	writeFile(t, unavailable, "keep")
	TrashHome = func() string { return unavailable }
	t.Cleanup(func() { TrashHome = saved })
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "1:1")
	dir := ownedOrder(t, l, root, dst, map[string]string{"a": "x"})
	if trash, err := l.Remove(dir, time.Now().Add(8*24*time.Hour)); err == nil {
		t.Fatalf("moved to %s with no trash", trash)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "a")); err != nil || string(got) != "x" {
		t.Fatalf("source = %q, %v", got, err)
	}
	if _, err := os.Stat(l.orderPath(dir)); err != nil {
		t.Errorf("the order was dropped: %v", err)
	}
}

// otherVolume is a folder this account owns on another device than the
// temporary folder, or the test is skipped.
func otherVolume(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "linux":
		dir, err := os.MkdirTemp("/dev/shm", "quecto-")
		if err != nil {
			t.Skipf("No other volume: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		return dir
	case "darwin":
		out, err := exec.Command("hdiutil", "attach", "-nomount", "ram://16384").Output()
		if err != nil {
			t.Skipf("No RAM disk: %v", err)
		}
		disk := strings.Fields(string(out))[0]
		t.Cleanup(func() { exec.Command("hdiutil", "detach", "-force", disk).Run() })
		if out, err := exec.Command("newfs_hfs", "-v", "quecto", disk).CombinedOutput(); err != nil {
			t.Skipf("No file system on the RAM disk: %s", out)
		}
		mnt := t.TempDir()
		if out, err := exec.Command("mount", "-t", "hfs", disk, mnt).CombinedOutput(); err != nil {
			t.Skipf("The RAM disk did not mount: %s", out)
		}
		t.Cleanup(func() { exec.Command("umount", mnt).Run() })
		return mnt
	}
	t.Skip("No other volume on this system")
	return ""
}

// A folder on another volume than the home trash goes only to that volume's
// own trash, and stays when the volume has none.
func TestAFolderOnAnotherVolumeUsesOnlyItsOwnTrash(t *testing.T) {
	useTrash(t)
	vol := otherVolume(t)
	a, _ := os.Lstat(vol)
	b, _ := os.Lstat(os.TempDir())
	if da, _ := device(a); func() bool { db, _ := device(b); return da == db }() {
		t.Skip("The other folder is on the same device")
	}
	root := filepath.Join(vol, "scratch")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "1:1")
	dir := ownedOrder(t, l, root, dst, map[string]string{"a": "x"})
	later := time.Now().Add(8 * 24 * time.Hour)
	if trash, err := l.Remove(dir, later); err == nil {
		t.Fatalf("moved across volumes to %s", trash)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "a")); err != nil || string(got) != "x" {
		t.Fatalf("source = %q, %v", got, err)
	}

	savedTop := TrashTop
	TrashTop = func(string) string { return vol }
	t.Cleanup(func() { TrashTop = savedTop })
	if runtime.GOOS == "darwin" {
		if err := os.MkdirAll(filepath.Join(vol, ".Trashes", strconv.Itoa(os.Getuid())), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	trash, err := l.Remove(dir, later)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(trash, vol+string(filepath.Separator)) {
		t.Errorf("trashed to %s, outside the volume %s", trash, vol)
	}
	if got, err := os.ReadFile(filepath.Join(trash, "a")); err != nil || string(got) != "x" {
		t.Errorf("trashed copy = %q, %v", got, err)
	}
	if l.Trash(dir) != trash {
		t.Errorf("recorded trash = %q, want %q", l.Trash(dir), trash)
	}
}

// A freedesktop trash records each item before it is moved, under the first
// free name, with its path percent-encoded and the time it was trashed.
func TestAFreedesktopTrashRecordsEachItem(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	home := xdgHomeTrash()
	if home != filepath.Join(data, "Trash") {
		t.Fatalf("home trash = %s", home)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "notes a%b")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "x"), "x")
	info, _ := os.Lstat(dir)
	dev, _ := device(info)
	can, ok := xdgCan(home, dev, "")
	if !ok {
		t.Fatal("the home trash was not made")
	}
	writeFile(t, filepath.Join(home, "info", "notes a%b.trashinfo"), "taken")
	before := time.Now().Add(-time.Second)
	dest, err := can.put(dir, FileID(info), dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if dest != filepath.Join(home, "files", "notes a%b 2") {
		t.Errorf("trashed as %s", dest)
	}
	record, err := os.ReadFile(filepath.Join(home, "info", "notes a%b 2.trashinfo"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(record), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "[Trash Info]" || lines[1] != "Path="+escapeTrashPath(dir) ||
		!strings.Contains(lines[1], "notes%20a%25b") || !strings.HasPrefix(lines[2], "DeletionDate=") {
		t.Fatalf("record = %q", record)
	}
	at, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimPrefix(lines[2], "DeletionDate="), time.Local)
	if err != nil || at.Before(before.Truncate(time.Second)) || at.After(time.Now()) {
		t.Errorf("deletion date = %v, %v", at, err)
	}

	// A move that fails takes its record back.
	if _, err := can.put(filepath.Join(root, "gone"), "0:0", filepath.Join(root, "gone"), true); err == nil {
		t.Error("a missing folder was trashed")
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "info")); len(entries) != 2 {
		t.Errorf("records after a failed move = %d, want 2", len(entries))
	}

	// On another volume the path is relative to its top, and a shared .Trash
	// without the sticky bit is passed over.
	top := t.TempDir()
	if err := os.Mkdir(filepath.Join(top, ".Trash"), 0o777); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(top, "work", "scratch")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	sinfo, _ := os.Lstat(sub)
	sdev, _ := device(sinfo)
	tcan, ok := xdgTopCan(top, sdev)
	if !ok || tcan.files != filepath.Join(top, ".Trash-"+strconv.Itoa(os.Getuid()), "files") {
		t.Fatalf("top trash = %+v, %v", tcan, ok)
	}
	if _, err := tcan.put(sub, FileID(sinfo), sub, true); err != nil {
		t.Fatal(err)
	}
	trec, _ := os.ReadFile(filepath.Join(filepath.Dir(tcan.files), "info", "scratch.trashinfo"))
	if !bytes.Contains(trec, []byte("\nPath=work/scratch\n")) {
		t.Errorf("record = %q, want a path relative to the top", trec)
	}
}

// A folder whose identity is not the recorded one, or a link in its place,
// is not moved.
func TestOnlyTheRecordedFolderIsTrashed(t *testing.T) {
	home := useTrash(t)
	root, outside := t.TempDir(), t.TempDir()
	dir := filepath.Join(root, "scratch-2026-11-26-aaaaaa")
	os.Mkdir(dir, 0o700)
	if _, err := moveToTrash(dir, "0:0", dir); err != errNotTheFolder {
		t.Errorf("another identity = %v", err)
	}
	info, _ := os.Lstat(outside)
	link := filepath.Join(root, "scratch-2026-11-26-bbbbbb")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := moveToTrash(link, FileID(info), link); err != errNotTheFolder {
		t.Errorf("a link = %v", err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("the trash holds %d entries", len(entries))
	}
}

// A folder a removal cut short left under its private name goes back under
// its own name and stays there. When the name is taken it goes to the trash.
// A folder set aside that no order names is kept.
func TestAFolderSetAsideIsNeverLeftHidden(t *testing.T) {
	for _, taken := range []bool{false, true} {
		t.Run(strconv.FormatBool(taken), func(t *testing.T) {
			home := useTrash(t)
			root, dst := t.TempDir(), t.TempDir()
			l := NewScratchLedger(t.TempDir(), root, "1:1")
			dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
			var order RemovalOrder
			readJSON(l.orderPath(dir), &order)
			order.Aside = ".removal-aaaaaaaaaaaa"
			writeJSON(l.orderPath(dir), order)
			aside := filepath.Join(root, order.Aside)
			if err := os.Rename(dir, aside); err != nil {
				t.Fatal(err)
			}
			unbound := filepath.Join(root, ".removal-bbbbbbbbbbbb")
			os.Mkdir(unbound, 0o700)
			if taken {
				os.Mkdir(dir, 0o700)
			}
			_, err := l.Remove(dir, time.Now().Add(8*24*time.Hour))
			if _, serr := os.Lstat(aside); serr == nil {
				t.Fatalf("the folder is still hidden: %v", err)
			}
			if _, serr := os.Stat(unbound); serr != nil {
				t.Errorf("a folder set aside with no order was moved: %v", serr)
			}
			if _, serr := os.Stat(l.orderPath(dir)); serr == nil {
				t.Error("the order outlived the folder's return")
			}
			want := filepath.Join(dir, "note")
			if taken {
				if err != nil {
					t.Fatal(err)
				}
				want = filepath.Join(l.Trash(dir), "note")
				files := home
				if runtime.GOOS == "linux" {
					files = filepath.Join(home, "files")
				}
				if filepath.Dir(filepath.Dir(want)) != files || !strings.HasPrefix(filepath.Base(filepath.Dir(want)), filepath.Base(dir)) {
					t.Errorf("trashed to %s", want)
				}
			} else if err == nil {
				t.Error("a folder put back was reported moved")
			}
			if got, err := os.ReadFile(want); err != nil || string(got) != "original" {
				t.Errorf("%s = %q, %v", want, got, err)
			}
		})
	}
}

func TestOutsideScratchRootStaysWithItsOrder(t *testing.T) {
	useTrash(t)
	l := NewScratchLedger(t.TempDir(), t.TempDir(), "1:1")
	dir := ownedOrder(t, l, t.TempDir(), t.TempDir(), map[string]string{"note": "work"})
	if trash, err := l.Remove(dir, time.Now().Add(8*24*time.Hour)); err == nil || trash != "" {
		t.Fatalf("Moved outside folder to %s: %v", trash, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "note")); err != nil {
		t.Fatal(err)
	}
}

func TestFreedesktopTrashRequiresPrivateFolders(t *testing.T) {
	for _, sub := range []string{".", "files", "info"} {
		t.Run(sub, func(t *testing.T) {
			dir := t.TempDir()
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			dev, _ := device(info)
			home := filepath.Join(dir, "Trash")
			if _, ok := xdgCan(home, dev+1, ""); ok {
				t.Fatal("Accepted another device")
			}
			if _, err := os.Lstat(home); !os.IsNotExist(err) {
				t.Fatalf("Created trash on another device: %v", err)
			}
			if _, ok := xdgCan(home, dev, ""); !ok {
				t.Fatal("Cannot create private trash")
			}
			if err := os.Chmod(filepath.Join(home, sub), 0777); err != nil {
				t.Fatal(err)
			}
			if _, ok := xdgCan(home, dev, ""); ok {
				t.Fatal("Accepted writable shared folder")
			}
		})
	}
}

func TestRenameNeverReplacesAConcurrentArrival(t *testing.T) {
	root := t.TempDir()
	for round := range 20 {
		dest := filepath.Join(root, strconv.Itoa(round))
		start := make(chan struct{})
		results := make(chan error, 32)
		for i := range cap(results) {
			source := dest + "-" + strconv.Itoa(i)
			writeFile(t, source, "work")
			go func() { <-start; results <- renameNoReplace(source, dest) }()
		}
		close(start)
		winners := 0
		for range cap(results) {
			if err := <-results; err == nil {
				winners++
			} else if !os.IsExist(err) {
				t.Fatal(err)
			}
		}
		if winners != 1 {
			t.Fatalf("%d renames succeeded for the same name", winners)
		}
	}
}

func TestTrashRechecksTheSourceIdentityAfterComparison(t *testing.T) {
	useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
	saved := beforeTrash
	t.Cleanup(func() { beforeTrash = saved })
	beforeTrash = func(path string) {
		if err := os.Rename(path, path+"-saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(path, "note"), "replacement")
	}
	if _, err := l.Remove(dir, time.Now().Add(8*24*time.Hour)); err != errNotTheFolder {
		t.Fatalf("Removal error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "note")); err != nil || string(got) != "replacement" {
		t.Fatalf("Replacement = %q, %v", got, err)
	}
}

func TestConfirmedRetentionSettlesAnOldAside(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(strconv.FormatBool(occupied), func(t *testing.T) {
			useTrash(t)
			root, dst := t.TempDir(), t.TempDir()
			l := NewScratchLedger(t.TempDir(), root, "store")
			dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
			var order RemovalOrder
			if err := readJSON(l.orderPath(dir), &order); err != nil {
				t.Fatal(err)
			}
			order.Aside = ".removal-aaaaaaaaaaaa"
			if err := writeJSON(l.orderPath(dir), order); err != nil {
				t.Fatal(err)
			}
			aside := filepath.Join(filepath.Dir(dir), order.Aside)
			if err := os.Rename(dir, aside); err != nil {
				t.Fatal(err)
			}
			if occupied {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(dir, "note"), "replacement")
			}
			if kept := l.KeptCopy(dir, false); kept != dir {
				t.Errorf("Confirmation keeps %q, want %q", kept, dir)
			}
			if err := l.Keep(dir, "s1"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(aside); !os.IsNotExist(err) {
				t.Fatalf("Private name still present: %v", err)
			}
			visible := dir
			if occupied {
				visible = l.Trash(dir)
				if got, err := os.ReadFile(filepath.Join(dir, "note")); err != nil || string(got) != "replacement" {
					t.Fatalf("Replacement = %q, %v", got, err)
				}
			}
			if got, err := os.ReadFile(filepath.Join(visible, "note")); err != nil || string(got) != "original" {
				t.Fatalf("Recovered file = %q, %v", got, err)
			}
			if _, err := os.Stat(l.orderPath(dir)); !os.IsNotExist(err) {
				t.Fatalf("Order after confirmation = %v", err)
			}
		})
	}
}

// swapIn moves the folder at path to path+"-saved" and makes another folder
// under its name holding a note that says so.
func swapIn(t *testing.T, path string) {
	t.Helper()
	if err := os.Rename(path, path+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(path, "note"), "replacement")
}

func onRename(t *testing.T, hook func(string)) {
	saved := beforeRename
	beforeRename = hook
	t.Cleanup(func() { beforeRename = saved })
}

// A folder put in place of the ordered one between its last check and the
// rename is moved back out of the trash, and the order is not settled.
func TestTrashChecksWhatTheRenameMoved(t *testing.T) {
	home := useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
	onRename(t, func(path string) { swapIn(t, path) })
	if trash, err := l.Remove(dir, time.Now().Add(8*24*time.Hour)); err == nil {
		t.Fatalf("Moved the replacement to %s", trash)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "note")); err != nil || string(got) != "replacement" {
		t.Fatalf("Replacement = %q, %v", got, err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("The trash holds %d entries", len(entries))
	}
	if l.Trash(dir) != "" {
		t.Fatal("The order was settled")
	}
}

// A folder put in place of the ordered one while its trash is looked up is
// caught before any rename.
func TestTrashChecksTheFolderAgainBeforeTheRename(t *testing.T) {
	home := useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
	saved := TrashHome
	TrashHome = func() string { swapIn(t, dir); return home }
	t.Cleanup(func() { TrashHome = saved })
	onRename(t, func(path string) { t.Errorf("Renamed %s after it was replaced", path) })
	if _, err := l.Remove(dir, time.Now().Add(8*24*time.Hour)); err != errNotTheFolder {
		t.Fatalf("Removal error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "note")); err != nil || string(got) != "replacement" {
		t.Fatalf("Replacement = %q, %v", got, err)
	}
}

// A folder put in place of a private one as it is restored goes back under
// the private name rather than stand for the folder that was set aside.
func TestRestoringAnAsideChecksWhatTheRenameMoved(t *testing.T) {
	useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
	var order RemovalOrder
	if err := readJSON(l.orderPath(dir), &order); err != nil {
		t.Fatal(err)
	}
	order.Aside = ".removal-aaaaaaaaaaaa"
	if err := writeJSON(l.orderPath(dir), order); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(root, order.Aside)
	if err := os.Rename(dir, aside); err != nil {
		t.Fatal(err)
	}
	onRename(t, func(path string) { swapIn(t, path) })
	if _, err := l.Remove(dir, time.Now().Add(8*24*time.Hour)); err == nil || errors.Is(err, errFolderRestored) {
		t.Fatalf("Removal error = %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("The replacement took the folder's name: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(aside, "note")); err != nil || string(got) != "replacement" {
		t.Fatalf("Replacement = %q, %v", got, err)
	}
}

func TestKeptFolderRetriesAnUnwrittenNotice(t *testing.T) {
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, t.TempDir(), nil)
	now := time.Now()
	if err := l.Order(dir, "s1", RemoveDeleted, "", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	notice := l.failurePath(dir)
	if err := os.Mkdir(notice, 0700); err != nil {
		t.Fatal(err)
	}
	obstruction := filepath.Join(notice, "occupied")
	writeFile(t, obstruction, "keep")
	l.Sweep(nil, now)
	if _, err := os.Stat(l.orderPath(dir)); err != nil {
		t.Fatalf("Lost the order before saving the notice: %v", err)
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(notice); err != nil {
		t.Fatal(err)
	}
	l.Sweep(nil, now)
	got, err := l.TrashFailures()
	if err != nil || len(got) != 1 || !got[0].Kept || got[0].Path != dir {
		t.Fatalf("Retained folders = %v, %v", got, err)
	}
}

func TestStorageIdentifiesUnreadableRecordFields(t *testing.T) {
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, t.TempDir(), nil)
	_, order, err := l.bound(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.recordTrashFailure(order, dir, errNoTrash)
	for _, path := range []string{l.ownerPath(dir), l.orderPath(dir)} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{`{"session":42}`, "null", "{}"} {
			writeFile(t, path, body)
			got, err := l.TrashFailures()
			if err != nil || len(got) != 1 || got[0].Record != path || got[0].Path != dir {
				t.Errorf("%s holding %s: got %v, %v", path, body, got, err)
			}
		}
		writeFile(t, path, string(raw))
	}
}

func TestSettledFolderNamesItsDamagedNotice(t *testing.T) {
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, t.TempDir(), nil)
	now := time.Now()
	if err := l.Order(dir, "s1", RemoveDeleted, "", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	l.Sweep(nil, now)
	path := l.failurePath(dir)
	for _, data := range []string{"{", "null", "{}"} {
		writeFile(t, path, data)
		got, err := l.TrashFailures()
		if err != nil || len(got) != 1 || !got[0].Kept || got[0].Path != dir || got[0].Record != path {
			t.Fatalf("Retained folders = %v, %v", got, err)
		}
	}
	if err := os.Rename(dir, filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := l.TrashFailures(); err != nil || len(got) != 0 {
		t.Fatalf("Listed a replacement folder: %v, %v", got, err)
	}
}

func onLstatMoved(t *testing.T, hook func(string) (os.FileInfo, error)) {
	saved := lstatMoved
	lstatMoved = hook
	t.Cleanup(func() { lstatMoved = saved })
}

// A folder moved into a trash whose contents cannot be read stays there, its
// order waits with a notice naming both places, and the next removal that can
// read the trash settles it.
func TestAMoveThatCannotBeCheckedWaitsForACheck(t *testing.T) {
	useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
	onLstatMoved(t, func(string) (os.FileInfo, error) { return nil, os.ErrPermission })
	later := time.Now().Add(8 * 24 * time.Hour)
	_, err := l.Remove(dir, later)
	failure := RetainedFolder(err)
	if failure == nil || !failure.Waiting || failure.Path != dir || failure.Trash == "" {
		t.Fatalf("Removal = %v, %+v", err, failure)
	}
	if got, err := os.ReadFile(filepath.Join(failure.Trash, "note")); err != nil || string(got) != "original" {
		t.Fatalf("The move was undone: %q, %v", got, err)
	}
	if listed, err := l.TrashFailures(); err != nil || len(listed) != 1 || listed[0] != *failure {
		t.Fatalf("Storage = %v, %v", listed, err)
	}
	lstatMoved = os.Lstat
	if trash, err := l.Remove(dir, later); err != nil || trash != failure.Trash || l.Trash(dir) != trash {
		t.Fatalf("Second removal = %s, %v", trash, err)
	}
	if listed, _ := l.TrashFailures(); len(listed) != 0 {
		t.Fatalf("Storage after the check = %v", listed)
	}
}

// Another folder moved into the trash in place of the ordered one, which
// cannot go back because its name was taken again, is listed with where it
// is in the trash.
func TestAForeignEntryLeftInTheTrashIsListed(t *testing.T) {
	useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
	onRename(t, func(path string) { swapIn(t, path) })
	onLstatMoved(t, func(path string) (os.FileInfo, error) {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		return os.Lstat(path)
	})
	_, err := l.Remove(dir, time.Now().Add(8*24*time.Hour))
	failure := RetainedFolder(err)
	if failure == nil || !failure.Foreign || failure.Trash == "" {
		t.Fatalf("Removal = %v, %+v", err, failure)
	}
	if got, err := os.ReadFile(filepath.Join(failure.Trash, "note")); err != nil || string(got) != "replacement" {
		t.Fatalf("The trash holds %q, %v", got, err)
	}
	if listed, err := l.TrashFailures(); err != nil || len(listed) != 1 || listed[0] != *failure {
		t.Fatalf("Storage = %v, %v", listed, err)
	}
	if err := os.Rename(failure.Trash, failure.Trash+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(failure.Trash, 0700); err != nil {
		t.Fatal(err)
	}
	if listed, _ := l.TrashFailures(); len(listed) != 0 {
		t.Fatalf("Storage after the entry left the trash = %v", listed)
	}
}

func TestUncheckedTrashSurvivesMissingSourceAndReportsReplacement(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, t.TempDir(), map[string]string{"note": "original"})
	onLstatMoved(t, func(string) (os.FileInfo, error) { return nil, os.ErrPermission })
	later := time.Now().Add(8 * 24 * time.Hour)
	_, err := l.Remove(dir, later)
	f := RetainedFolder(err)
	if f == nil || f.Trash == "" {
		t.Fatal(err)
	}
	if l.Missing(dir) {
		t.Error("Unverified move reported as missing")
	}
	l.ForgetMissing(dir, "s1")
	if err := os.Rename(f.Trash, f.Trash+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.Trash, 0700); err != nil {
		t.Fatal(err)
	}
	lstatMoved = os.Lstat
	l.Sweep(nil, later)
	got, err := l.TrashFailures()
	if err != nil || len(got) != 1 || !got[0].Waiting || got[0].Trash != f.Trash || !strings.Contains(got[0].Reason, "another entry") {
		t.Fatalf("Storage after replacement = %+v, %v", got, err)
	}
}

func TestRemovalNeedsARecordOfItsTrashDestination(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, t.TempDir(), map[string]string{"note": "original"})
	if err := os.Mkdir(l.failurePath(dir), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Remove(dir, time.Now().Add(8*24*time.Hour)); err == nil {
		t.Error("Moved without recording a destination")
	}
	if _, err := os.Stat(filepath.Join(dir, "note")); err != nil {
		t.Fatal(err)
	}
}

func TestTrashConfirmationCanBeSavedOnTheNextSweep(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, t.TempDir(), map[string]string{"note": "original"})
	blocked := l.trashedPath(dir)
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(8 * 24 * time.Hour)
	_, err := l.Remove(dir, later)
	if f := RetainedFolder(err); f == nil || !f.Waiting || f.Trash == "" {
		t.Fatalf("Missing confirmation returned %v", err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	l.Sweep(nil, later)
	if l.Trash(dir) == "" {
		t.Fatal("The confirmed destination was lost")
	}
}
