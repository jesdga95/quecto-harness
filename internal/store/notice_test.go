package store

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// deletedOrder makes a scratch folder in root holding a note and orders it
// removed as if its conversation was deleted at at.
func deletedOrder(t *testing.T, l *ScratchLedger, root string, at time.Time) string {
	t.Helper()
	dir, err := MintScratch(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "note"), "work")
	info, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Own(dir, "s1", info); err != nil {
		t.Fatal(err)
	}
	if err := l.Order(dir, "s1", RemoveDeleted, "", at); err != nil {
		t.Fatal(err)
	}
	return dir
}

func onlyNotice(t *testing.T, l *ScratchLedger) TrashFailure {
	t.Helper()
	listed, err := l.TrashFailures()
	if err != nil || len(listed) != 1 {
		t.Fatalf("Storage = %v, %v", listed, err)
	}
	return listed[0]
}

// A folder whose copy in the project was edited after a full copy stays, and
// is named with the reason for as long as its order waits.
func TestAFolderWhoseCopyWasEditedIsNamed(t *testing.T) {
	useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
	writeFile(t, filepath.Join(dst, "note"), "edited in the project")
	for _, years := range []int{1, 2} {
		if moved := l.Sweep(nil, time.Now().AddDate(years, 0, 0)); len(moved) != 0 {
			t.Fatalf("Moved %v", moved)
		}
		notice := onlyNotice(t, l)
		if notice.Path != dir || !notice.Waiting || notice.Kept || !strings.Contains(notice.Reason, "no longer matches") {
			t.Fatalf("Notice = %+v", notice)
		}
	}
	if _, err := os.Stat(l.orderPath(dir)); err != nil {
		t.Fatalf("The order did not wait: %v", err)
	}
}

// A folder kept for a change is named with what the change came after, and
// once it is deleted by hand the sweep drops its records.
func TestAFolderKeptForAChangeIsForgottenOnceDeleted(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(-time.Minute))
	writeFile(t, filepath.Join(dir, "later"), "written after the delete")
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	if notice := onlyNotice(t, l); !notice.Kept || notice.Reason != "it changed after its conversation was deleted" {
		t.Fatalf("Notice = %+v", notice)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	for _, record := range []string{l.ownerPath(dir), l.orderPath(dir), l.failurePath(dir)} {
		if _, err := os.Lstat(record); !os.IsNotExist(err) {
			t.Errorf("%s outlived the folder: %v", record, err)
		}
	}
}

// A copy order takes the time its copy began, so a change is said to come
// after that.
func TestAChangeAfterACopyNamesTheCopy(t *testing.T) {
	useTrash(t)
	root, dst := t.TempDir(), t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := ownedOrder(t, l, root, dst, map[string]string{"note": "original"})
	writeFile(t, filepath.Join(dir, "later"), "written after the copy")
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	if notice := onlyNotice(t, l); !notice.Kept || notice.Reason != "it changed after the copy of its files began" {
		t.Fatalf("Notice = %+v", notice)
	}
}

// A retained folder whose owner record or order is removed by hand is no
// longer moved, and stays named as kept.
func TestARetainedFolderWithARecordRemovedStaysNamed(t *testing.T) {
	for _, removed := range []string{"owner", "order"} {
		t.Run(removed, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, "x"), "")
			saved := TrashHome
			TrashHome = func() string { return filepath.Join(home, "x") }
			t.Cleanup(func() { TrashHome = saved })
			root := t.TempDir()
			l := NewScratchLedger(t.TempDir(), root, "store")
			dir := deletedOrder(t, l, root, time.Now().Add(time.Minute))
			if _, err := l.Remove(dir, time.Now()); RetainedFolder(err) == nil || RetainedFolder(err).Kept {
				t.Fatalf("Removal = %v", err)
			}
			record := l.ownerPath(dir)
			if removed == "order" {
				record = l.orderPath(dir)
			}
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
			if notice := onlyNotice(t, l); notice.Path != dir || !notice.Kept || !strings.Contains(notice.Reason, removed) {
				t.Fatalf("Notice = %+v", notice)
			}
		})
	}
}

// A settled notice whose order cannot be withdrawn is listed while that order
// stands, naming it as the record to remove.
func TestAKeptNoticeShowsTheOrderThatHoldsIt(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Needs chflags to lock the order")
	}
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(-time.Minute))
	if out, err := exec.Command("chflags", "uchg", l.orderPath(dir)).CombinedOutput(); err != nil {
		t.Skipf("Cannot lock the order: %s", out)
	}
	t.Cleanup(func() { exec.Command("chflags", "nouchg", l.orderPath(dir)).Run() })
	writeFile(t, filepath.Join(dir, "later"), "written after the delete")
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	notice := onlyNotice(t, l)
	if !notice.Kept || notice.Record != l.orderPath(dir) || !strings.Contains(notice.Reason, "could not be withdrawn") {
		t.Fatalf("Notice = %+v", notice)
	}
}

// A folder moved to the trash is not listed as waiting for a check when its
// order cannot be withdrawn afterwards.
func TestAConfirmedMoveIsNotListedWhenItsOrderStays(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Needs chflags to lock the order")
	}
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(time.Minute))
	if out, err := exec.Command("chflags", "uchg", l.orderPath(dir)).CombinedOutput(); err != nil {
		t.Skipf("Cannot lock the order: %s", out)
	}
	t.Cleanup(func() { exec.Command("chflags", "nouchg", l.orderPath(dir)).Run() })
	trash, err := l.Remove(dir, time.Now())
	if err != nil || trash == "" || l.Trash(dir) != trash {
		t.Fatalf("Removal = %s, %v", trash, err)
	}
	if listed, err := l.TrashFailures(); err != nil || len(listed) != 0 {
		t.Fatalf("Storage = %+v, %v", listed, err)
	}
}

// pendingMove orders a folder removed and moves it into a trash whose entry
// cannot be read back, which leaves its order waiting with a notice.
func pendingMove(t *testing.T, l *ScratchLedger, root string) string {
	t.Helper()
	dir := deletedOrder(t, l, root, time.Now().Add(time.Minute))
	onLstatMoved(t, func(string) (os.FileInfo, error) { return nil, os.ErrPermission })
	if _, err := l.Remove(dir, time.Now()); RetainedFolder(err) == nil || !RetainedFolder(err).Waiting {
		t.Fatalf("Removal = %v", err)
	}
	return dir
}

func unreadable(t *testing.T, path string) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("Requires an unprivileged account")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
}

// A move waiting for its check keeps its records, and stays listed, while its
// notice cannot be read.
func TestAnUnreadableNoticeKeepsAPendingMove(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := pendingMove(t, l, root)
	unreadable(t, l.failurePath(dir))
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	for _, record := range []string{l.ownerPath(dir), l.orderPath(dir), l.failurePath(dir)} {
		if _, err := os.Lstat(record); err != nil {
			t.Fatalf("%s is gone: %v", record, err)
		}
	}
	if notice := onlyNotice(t, l); notice.Path != dir || notice.Record != l.failurePath(dir) || !notice.Waiting {
		t.Fatalf("Notice = %+v", notice)
	}
}

// A move waiting for its check stays listed when its owner record or order
// is removed by hand, since its notice still says where the folder went.
func TestAPendingMoveWithARecordRemovedStaysListed(t *testing.T) {
	for _, removed := range []string{"owner", "order"} {
		t.Run(removed, func(t *testing.T) {
			useTrash(t)
			root := t.TempDir()
			l := NewScratchLedger(t.TempDir(), root, "store")
			dir := pendingMove(t, l, root)
			record := l.ownerPath(dir)
			if removed == "order" {
				record = l.orderPath(dir)
			}
			if err := os.Remove(record); err != nil {
				t.Fatal(err)
			}
			notice := onlyNotice(t, l)
			if notice.Trash == "" || !strings.Contains(notice.Reason, removed) || notice.Record != l.failurePath(dir) {
				t.Fatalf("Notice = %+v", notice)
			}
		})
	}
}

// A notice of a file a copy moved to the trash that cannot be read is listed
// with its path, and the sweep keeps it.
func TestAnUnreadableFileNoticeIsListed(t *testing.T) {
	useTrash(t)
	state := t.TempDir()
	l := NewScratchLedger(state, t.TempDir(), "store")
	notice := filepath.Join(state, "scratch-2026-11-27-abcdef.0123456789abcdef01234567"+copyTrashSuffix)
	writeFile(t, notice, `{"order":{"folder":"x"}}`)
	unreadable(t, notice)
	if got := onlyNotice(t, l); got.Record != notice || !got.File {
		t.Fatalf("Notice = %+v", got)
	}
	l.Sweep(nil, time.Now())
	if _, err := os.Lstat(notice); err != nil {
		t.Fatalf("The notice is gone: %v", err)
	}
}

// A folder kept for a change whose notice is damaged gets its notice back
// from its owner record, stays listed, and its records go once it is gone.
func TestADamagedKeptNoticeIsWrittenAgain(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(-time.Minute))
	writeFile(t, filepath.Join(dir, "later"), "written after the delete")
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	writeFile(t, l.failurePath(dir), "{")
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	if notice := onlyNotice(t, l); notice.Path != dir || !notice.Kept || notice.Record != "" {
		t.Fatalf("Notice = %+v", notice)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	if _, err := os.Lstat(l.ownerPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("The owner record outlived the folder: %v", err)
	}
}

// A folder whose owner record and notice are both damaged is still listed,
// by its name under the root, with the notice to repair.
func TestAFolderWithItsOwnerAndNoticeDamagedIsListed(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(-time.Minute))
	writeFile(t, filepath.Join(dir, "later"), "written after the delete")
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	writeFile(t, l.failurePath(dir), "{")
	writeFile(t, l.ownerPath(dir), "{")
	if notice := onlyNotice(t, l); notice.Path != filepath.Join(root, filepath.Base(dir)) || notice.Record != l.failurePath(dir) {
		t.Fatalf("Notice = %+v", notice)
	}
}

func TestForgetMissingPreservesAnUnverifiedDestinationWithoutItsOrder(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := pendingMove(t, l, root)
	if err := os.Remove(l.orderPath(dir)); err != nil {
		t.Fatal(err)
	}
	l.ForgetMissing(dir, "s1")
	if l.Missing(dir) || onlyNotice(t, l).Trash == "" {
		t.Fatal("Lost the pending move")
	}
}

func TestKeptFolderOutlivesItsRemovedNotice(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(-time.Minute))
	writeFile(t, filepath.Join(dir, "later"), "kept")
	l.Sweep(nil, time.Now())
	if err := os.Remove(l.failurePath(dir)); err != nil {
		t.Fatal(err)
	}
	l = NewScratchLedger(l.dir, root, "store")
	if f := onlyNotice(t, l); !f.Kept || f.Path != dir {
		t.Fatal(f)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	l.Sweep(nil, time.Now())
	if _, err := os.Lstat(l.ownerPath(dir)); !os.IsNotExist(err) {
		t.Fatal("Stale owner", err)
	}
}

func TestPendingTrashLocationSurvivesAnIncompleteRecord(t *testing.T) {
	for _, broken := range []string{"owner", "order"} {
		t.Run(broken, func(t *testing.T) {
			useTrash(t)
			root := t.TempDir()
			l := NewScratchLedger(t.TempDir(), root, "store")
			dir := pendingMove(t, l, root)
			before := onlyNotice(t, l)
			record := l.orderPath(dir)
			if broken == "owner" {
				record = l.ownerPath(dir)
			}
			writeFile(t, record, "{}")
			l.ForgetMissing(dir, "s1")
			l.Sweep(nil, time.Now().Add(time.Hour))
			f := onlyNotice(t, l)
			if !f.Waiting || f.Trash != before.Trash || f.Record != record {
				t.Fatalf("Pending move = %+v", f)
			}
		})
	}
}

func TestMissingAsideKeepsItsUnreadableTrashNotice(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(-time.Minute))
	var order RemovalOrder
	if err := readJSON(l.orderPath(dir), &order); err != nil {
		t.Fatal(err)
	}
	order.Aside = ".removal-aaaaaaaaaaaa"
	if err := writeJSON(l.orderPath(dir), order); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, filepath.Join(root, order.Aside)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "occupied")
	onLstatMoved(t, func(string) (os.FileInfo, error) { return nil, os.ErrPermission })
	if _, err := l.Remove(dir, time.Now()); err == nil {
		t.Fatal("Expected an unverified move")
	}
	if f := onlyNotice(t, l); !f.Waiting || f.Trash == "" {
		t.Fatal(f)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	unreadable(t, l.failurePath(dir))
	if err := l.Keep(dir, "s1"); err == nil {
		t.Fatal("Cleared an unverified move")
	}
	l.Sweep(nil, time.Now().Add(time.Hour))
	if f := onlyNotice(t, l); !f.Waiting || f.Record != l.failurePath(dir) {
		t.Fatal(f)
	}
}

// A move whose removal order and notice are both damaged is still listed by
// the folder its owner record names.
func TestAMoveWithItsOrderAndNoticeDamagedIsListed(t *testing.T) {
	for _, damaged := range []string{"{", "{}"} {
		t.Run(damaged, func(t *testing.T) {
			useTrash(t)
			root := t.TempDir()
			l := NewScratchLedger(t.TempDir(), root, "store")
			dir := pendingMove(t, l, root)
			writeFile(t, l.orderPath(dir), damaged)
			writeFile(t, l.failurePath(dir), "{")
			if notice := onlyNotice(t, l); notice.Path != dir || notice.Record != l.failurePath(dir) || !strings.Contains(notice.Reason, "removal order") {
				t.Fatalf("Notice = %+v", notice)
			}
		})
	}
}

// Keeping a folder already kept for a change replaces that reason, so it is
// no longer listed as kept for the change.
func TestKeepingAFolderKeptForAChangeClearsTheReason(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(-time.Minute))
	writeFile(t, filepath.Join(dir, "later"), "written after the delete")
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	onlyNotice(t, l)
	if err := l.Keep(dir, "s1"); err != nil {
		t.Fatal(err)
	}
	if owner, err := l.owner(dir); err != nil || owner.Kept != "" {
		t.Fatalf("Owner = %+v, %v", owner, err)
	}
	if listed, _ := l.TrashFailures(); len(listed) != 0 {
		t.Fatalf("Storage = %+v", listed)
	}
}

func TestKeepWithoutAnOrderPreservesAnotherStoresNotice(t *testing.T) {
	useTrash(t)
	root := t.TempDir()
	l := NewScratchLedger(t.TempDir(), root, "store")
	dir := deletedOrder(t, l, root, time.Now().Add(-time.Minute))
	l.Sweep(nil, time.Now().AddDate(1, 0, 0))
	onlyNotice(t, l)
	record := l.failurePath(dir)
	other := NewScratchLedger(l.dir, root, "another-store")
	if err := other.Keep(dir, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(record); err != nil {
		t.Fatal("Lost the owning store's notice", err)
	}
}
