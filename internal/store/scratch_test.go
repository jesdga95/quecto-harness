package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// A scratch folder is new, empty, private and named by its date.
func TestMintScratchMakesAPrivateFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scratch-workspaces")
	now := time.Date(2026, 11, 26, 23, 30, 0, 0, time.FixedZone("x", -5*3600))
	dir, err := MintScratch(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if !scratchName.MatchString(filepath.Base(dir)) || filepath.Base(dir)[8:18] != "2026-11-27" {
		t.Errorf("name = %s, want scratch-<UTC date>-<hex>", filepath.Base(dir))
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %v, %v, want 0700", info.Mode().Perm(), err)
	}
	if !IsScratch(root, dir) || IsScratch(root, filepath.Join(dir, "sub")) || IsScratch(root, root) {
		t.Error("IsScratch does not tell the folder itself apart")
	}
	if !config.ContainsDirectory(root, filepath.Join(dir, "a")) || config.ContainsDirectory(root, t.TempDir()) {
		t.Error("InScratchRoot does not tell the root's contents apart")
	}
}

// A sweep trashes a folder only on an order bound to its owner record, after
// its wait and while nothing in it changed. A folder with no record or order,
// an unreadable or mismatched one, or that a session claims, is kept.
func TestSweepTakesOnlyOrderedFolders(t *testing.T) {
	root, state, dest := t.TempDir(), t.TempDir(), t.TempDir()
	l := NewScratchLedger(state, root, "1:1")
	writeFile(t, filepath.Join(dest, "a.txt"), "x")
	mk := func(name string) string {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "a.txt"), "x")
		return dir
	}
	owned := func(name, session string) string {
		dir := mk(name)
		info, _ := os.Lstat(dir)
		if err := l.Own(dir, session, info); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	order := func(dir, session, reason string, at time.Time) {
		if err := l.Order(dir, session, reason, dest, at); err != nil {
			t.Fatal(err)
		}
	}
	bare := mk("scratch-2026-12-01-aaaaaa")
	unordered := owned("scratch-2026-12-01-bbbbbb", "s1")
	deleted := owned("scratch-2026-12-01-cccccc", "s1")
	waiting := owned("scratch-2026-12-01-dddddd", "s1")
	done := owned("scratch-2026-12-01-eeeeee", "s1")
	changed := owned("scratch-2026-12-01-ffffff", "s1")
	claimed := owned("scratch-2026-12-01-111111", "s1")
	unreadable := owned("scratch-2026-12-01-222222", "s1")
	replaced := owned("scratch-2026-12-01-333333", "s1")
	otherStore := mk("scratch-2026-12-01-444444")

	copied := time.Now()
	now := copied.Add(8 * 24 * time.Hour)
	order(deleted, "s1", RemoveDeleted, copied)
	order(waiting, "s1", RemoveCopied, now.Add(-time.Hour))
	order(done, "s1", RemoveCopied, copied)
	order(changed, "s1", RemoveCopied, copied)
	order(claimed, "s1", RemoveDeleted, copied)
	order(unreadable, "s1", RemoveDeleted, copied)
	writeFile(t, l.orderPath(unreadable), "{")
	order(replaced, "s1", RemoveDeleted, copied)
	os.Rename(replaced, replaced+"-old")
	mk(filepath.Base(replaced))
	other := NewScratchLedger(state, root, "2:2")
	oinfo, _ := os.Lstat(otherStore)
	if other.Own(otherStore, "s1", oinfo) != nil || other.Order(otherStore, "s1", RemoveDeleted, "", copied) != nil {
		t.Fatal("the other store's records were not written")
	}
	if err := l.Order(unordered, "s2", RemoveDeleted, "", copied); err == nil {
		t.Error("an order was given for a session that does not own the folder")
	}
	time.Sleep(10 * time.Millisecond)
	writeFile(t, filepath.Join(changed, "a.txt"), "x")

	removed := l.Sweep([]string{claimed}, now)
	gone := map[string]bool{}
	for _, d := range removed {
		gone[filepath.Base(d)] = true
	}
	for dir, want := range map[string]bool{
		bare: false, unordered: false, deleted: true, waiting: false, done: true,
		changed: false, claimed: false, unreadable: false, replaced: false, otherStore: false,
	} {
		if gone[filepath.Base(dir)] != want {
			t.Errorf("%s removed = %v, want %v", filepath.Base(dir), !want, want)
		}
		if _, err := os.Stat(dir); (err == nil) == want {
			t.Errorf("%s on disk after the sweep = %v", filepath.Base(dir), err == nil)
		}
	}
	if _, err := os.Stat(l.ownerPath(done)); !os.IsNotExist(err) {
		t.Errorf("the records of a removed folder were kept: %v", err)
	}
}

// A move copies new files and leaves a file the new folder already has, a
// dependency folder, repository metadata and links where they are.
func TestCarryKeepsWhatIsThere(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(src, "a.txt"), "new")
	write(filepath.Join(src, "sub", "b.txt"), "b")
	write(filepath.Join(src, "node_modules", "x", "i.js"), "x")
	write(filepath.Join(src, ".git", "HEAD"), "ref")
	if err := os.Symlink("/etc/hosts", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dst, "a.txt"), "mine")

	c, err := CarryScratch(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	want := Carried{Copied: 1, There: 1, Held: 2, Dependencies: 1}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("carried = %+v, want %+v", c, want)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "a.txt")); string(b) != "mine" {
		t.Errorf("the file already there = %q, want it kept", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); string(b) != "b" {
		t.Errorf("the new file = %q", b)
	}
	for _, name := range []string{"node_modules", ".git", "link"} {
		if _, err := os.Lstat(filepath.Join(dst, name)); err == nil {
			t.Errorf("%s was copied", name)
		}
	}
	// A second run finds everything there and copies nothing.
	if again, _ := CarryScratch(src, dst); again.Copied != 0 || again.There != 2 {
		t.Errorf("the second copy = %+v", again)
	}
}

// A link in the destination does not lead a copy out of it, a worktree's
// .git file stays behind, and a source that is gone is an error rather than
// an empty result.
func TestCarryStaysInTheDestination(t *testing.T) {
	src, dst, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "b")
	writeFile(t, filepath.Join(src, ".git"), "gitdir: /x")
	if err := os.Symlink(elsewhere, filepath.Join(dst, "sub")); err != nil {
		t.Fatal(err)
	}
	c, err := CarryScratch(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "b.txt")); err == nil {
		t.Error("the copy followed a link out of the destination")
	}
	if c.Held != 1 || c.Failed != 1 || c.Copied != 0 {
		t.Errorf("carried = %+v", c)
	}
	if _, err := os.Lstat(filepath.Join(dst, ".git")); err == nil {
		t.Error("the .git file was copied")
	}
	entries, _ := os.ReadDir(dst)
	for _, e := range entries {
		if e.Name() != "sub" {
			t.Errorf("the destination holds %s", e.Name())
		}
	}
	if _, err := CarryScratch(filepath.Join(src, "gone"), dst); err == nil {
		t.Error("a missing source was taken as an empty one")
	}
}

// A copied folder keeps its source's permissions, and a file whose name is
// near the longest the volume takes is still copied.
func TestCarryKeepsPermissionsAndLongNames(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "private"), 0o700); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("n", 246)
	if err := os.WriteFile(filepath.Join(src, long), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := CarryScratch(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if c.Copied != 1 || c.Failed != 0 {
		t.Errorf("carried = %+v", c)
	}
	if info, err := os.Stat(filepath.Join(dst, "private")); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the folder's mode = %v, %v, want 0700", info.Mode().Perm(), err)
	}
}

// A file read again after it was saved or deleted meanwhile does not come
// back into the index.
func TestRereadDoesNotBringBackASettledFile(t *testing.T) {
	dir := t.TempDir()
	id := NewID()
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Unread()) != 1 {
		t.Fatalf("unread = %v", s.Unread())
	}
	if err := s.Save(&Session{ID: id, Project: "/p"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(id); err != nil {
		t.Fatal(err)
	}
	s.Reread()
	if s.Has(id) || len(s.Unread()) != 0 {
		t.Errorf("has = %v, unread = %v, want the deleted session gone", s.Has(id), s.Unread())
	}
}

// A folder that cannot be checked for now stays in place with its removal
// order, and the sweep moves it once it can be.
func TestARemovalThatCannotBeCheckedWaits(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	l := NewScratchLedger(state, root, "1:1")
	dir := filepath.Join(root, "scratch-2026-11-26-ffffff")
	os.MkdirAll(filepath.Join(dir, "sub"), 0o700)
	writeFile(t, filepath.Join(dir, "sub", "a.txt"), "x")
	info, _ := os.Lstat(dir)
	if err := l.Own(dir, "s1", info); err != nil {
		t.Fatal(err)
	}
	if err := l.Order(dir, "s1", RemoveDeleted, "", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	os.Chmod(filepath.Join(dir, "sub"), 0o000)
	t.Cleanup(func() {
		matches, _ := filepath.Glob(filepath.Join(root, "*", "sub"))
		for _, m := range matches {
			os.Chmod(m, 0o700)
		}
	})
	if _, err := l.Remove(dir, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("a folder that could not be opened was reported removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); err != nil {
		t.Fatalf("the folder left its place: %v", err)
	}
	if _, err := os.Stat(l.orderPath(dir)); err != nil {
		t.Fatalf("the order was dropped: %v", err)
	}
	os.Chmod(filepath.Join(dir, "sub"), 0o700)
	var order RemovalOrder
	readJSON(l.orderPath(dir), &order)
	order.At = time.Now()
	writeJSON(l.orderPath(dir), order)
	l.Sweep(nil, time.Now().Add(time.Minute))
	if left, _ := os.ReadDir(root); len(left) != 0 {
		t.Errorf("the root still holds %d entries after the sweep", len(left))
	}
}

// writeFile writes s to path, and fails the test when it cannot.
func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A removal to finish whose records name a folder outside the root is
// refused, and nothing outside the root is emptied.
func TestARemovalOutsideTheRootIsNotFinished(t *testing.T) {
	root, state, outside := t.TempDir(), t.TempDir(), t.TempDir()
	l := NewScratchLedger(state, root, "1:1")
	dir := filepath.Join(outside, "scratch-2026-11-26-aaaaaa")
	aside := filepath.Join(outside, ".removal-aaaaaaaaaaaa")
	os.Mkdir(aside, 0o700)
	writeFile(t, filepath.Join(aside, "keep.txt"), "x")
	info, _ := os.Lstat(aside)
	owner := ScratchOwner{Folder: dir, Session: "s1", ID: FileID(info), Store: "1:1"}
	order := RemovalOrder{Folder: dir, Session: "s1", ID: owner.ID, Store: "1:1",
		Reason: RemoveDeleted, At: time.Now(), Aside: filepath.Base(aside)}
	if writeJSON(l.ownerPath(dir), owner) != nil || writeJSON(l.orderPath(dir), order) != nil {
		t.Fatal("the records were not written")
	}
	if _, err := l.Remove(dir, time.Now()); err == nil {
		t.Error("a removal outside the root was finished")
	}
	if _, err := os.Stat(filepath.Join(aside, "keep.txt")); err != nil {
		t.Errorf("a folder outside the root was emptied: %v", err)
	}
}

// A removal waits for one of the same ledger already under way.
func TestRemovalsOfOneLedgerTakeTurns(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	l := NewScratchLedger(state, root, "1:1")
	dir := filepath.Join(root, "scratch-2026-11-26-aaaaaa")
	os.Mkdir(dir, 0o700)
	info, _ := os.Lstat(dir)
	if err := l.Own(dir, "s1", info); err != nil {
		t.Fatal(err)
	}
	if err := l.Order(dir, "s1", RemoveDeleted, "", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := l.Remove(dir, time.Now().Add(time.Minute))
		done <- err
	}()
	select {
	case <-done:
		l.mu.Unlock()
		t.Fatal("a removal ran while another held the ledger")
	case <-time.After(200 * time.Millisecond):
	}
	l.mu.Unlock()
	if err := <-done; err != nil {
		t.Errorf("the removal that waited failed: %v", err)
	}
}

// Taking back an order and forgetting a folder wait for a removal under
// way, so neither changes the records while it reads and writes them.
func TestWithdrawAndForgetWaitForARemoval(t *testing.T) {
	for _, name := range []string{"withdraw", "forget"} {
		t.Run(name, func(t *testing.T) {
			root, state := t.TempDir(), t.TempDir()
			l := NewScratchLedger(state, root, "1:1")
			dir := filepath.Join(root, "scratch-2026-11-26-aaaaaa")
			os.Mkdir(dir, 0o700)
			info, _ := os.Lstat(dir)
			if err := l.Own(dir, "s1", info); err != nil {
				t.Fatal(err)
			}
			if err := l.Order(dir, "s1", RemoveDeleted, "", time.Now()); err != nil {
				t.Fatal(err)
			}
			l.mu.Lock()
			done := make(chan struct{})
			go func() {
				if name == "withdraw" {
					l.Withdraw(dir)
				} else {
					l.Forget(dir)
				}
				close(done)
			}()
			select {
			case <-done:
				l.mu.Unlock()
				t.Fatal("the records changed while a removal held the ledger")
			case <-time.After(200 * time.Millisecond):
			}
			l.mu.Unlock()
			<-done
			if _, err := os.Stat(l.orderPath(dir)); !os.IsNotExist(err) {
				t.Errorf("the order after %s = %v, want it gone", name, err)
			}
		})
	}
}

func TestCopyRequiresItsEmptyDirectories(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(src, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := sameCopy(src, dst); err == nil {
		t.Fatal("A missing empty directory matched")
	}
}

func TestKeptCopyReadsWithoutReservingRemovals(t *testing.T) {
	root, dst := t.TempDir(), t.TempDir()
	ledger := NewScratchLedger(t.TempDir(), root, "store")
	dir, err := MintScratch(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "note"), "same")
	writeFile(t, filepath.Join(dst, "note"), "same")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Own(dir, "session", info); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Order(dir, "session", RemoveCopied, dst, time.Now()); err != nil {
		t.Fatal(err)
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	done := make(chan string, 1)
	go func() { done <- ledger.KeptCopy(dir, false) }()
	select {
	case kept := <-done:
		if kept != "" {
			t.Fatalf("Matching copy kept in %q", kept)
		}
	case <-time.After(time.Second):
		t.Fatal("Checking a copy waited for the removal lock")
	}
}

func TestCopyPreviewDetectsChangesDuringComparison(t *testing.T) {
	for _, side := range []string{"source", "destination", "same contents"} {
		t.Run(side, func(t *testing.T) {
			src, dst := t.TempDir(), t.TempDir()
			for _, dir := range []string{src, dst} {
				writeFile(t, filepath.Join(dir, "note"), "original")
			}
			// A renamed source can have changed since the order was written.
			order := RemovalOrder{Folder: src + ".original", To: dst}
			calls := 0
			compare := func(a, b string) error {
				err := sameCopy(a, b)
				calls++
				if calls == 1 && err == nil {
					dir, contents := dst, "modified"
					if side == "source" {
						dir = src
					}
					if side == "same contents" {
						contents = "original"
					}
					path := filepath.Join(dir, "note")
					saved, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					writeFile(t, path, contents)
					if err := os.Chtimes(path, saved.ModTime(), saved.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
				return err
			}
			if got := order.stableCopy(src, compare); got != (side == "same contents") || calls != 2 {
				t.Fatalf("Match = %t after %d comparisons", got, calls)
			}
		})
	}
}
