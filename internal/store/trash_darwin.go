package store

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"unsafe"
)

// homeTrash is the trash in the home folder, which macOS uses for the boot volume.
func homeTrash() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".Trash")
}

// trashOf is the trash for a folder on the volume info names: the home trash
// on the home volume, or .Trashes/<uid> at the top of another. Neither is
// made here; one that is missing leaves the folder where it is.
func trashOf(path string, info os.FileInfo) (trashCan, error) {
	dev, ok := device(info)
	if !ok {
		return trashCan{}, errNoTrash
	}
	if home := TrashHome(); home != "" && ownFolder(home, dev) {
		return trashCan{files: home}, nil
	}
	if top := TrashTop(path); top != "" {
		shared := filepath.Join(top, ".Trashes")
		dir := filepath.Join(shared, strconv.Itoa(os.Getuid()))
		if info, err := os.Lstat(shared); err == nil && info.IsDir() && ownFolder(dir, dev) {
			return trashCan{files: dir}, nil
		}
	}
	return trashCan{}, errNoTrash
}

// renameatx_np is called through libSystem, whose symbols macOS keeps stable,
// rather than by system call number, which it does not promise.
//
//go:cgo_import_dynamic libc_renameatx_np renameatx_np "/usr/lib/libSystem.B.dylib"

func libc_renameatx_np_trampoline()

// libc_renameatx_np_trampoline_addr is set in assembly to the trampoline's address.
var libc_renameatx_np_trampoline_addr uintptr

//go:linkname syscall_syscall6 syscall.syscall6
func syscall_syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

// renameNoReplace renames from to to in one step that fails with fs.ErrExist
// when to is taken. An unsupported flag or filesystem leaves both names alone.
func renameNoReplace(from, to string) error {
	return renameNoReplaceAt(-2, from, to)
}

func renameNoReplaceAt(fd int, from, to string) error {
	const renameExcl = 0x4
	return renameAt(fd, from, fd, to, renameExcl)
}

func renameInto(parent *os.File, from string, dest *os.File, to string) error {
	return renameAt(int(parent.Fd()), from, int(dest.Fd()), to, 0)
}

func renameAt(oldFD int, from string, newFD int, to string, flags uintptr) error {
	a, err := syscall.BytePtrFromString(from)
	if err != nil {
		return err
	}
	b, err := syscall.BytePtrFromString(to)
	if err != nil {
		return err
	}
	_, _, errno := syscall_syscall6(libc_renameatx_np_trampoline_addr, uintptr(uint32(oldFD)), uintptr(unsafe.Pointer(a)), uintptr(uint32(newFD)), uintptr(unsafe.Pointer(b)), flags, 0)
	if errno != 0 {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: errno}
	}
	return nil
}

//go:cgo_import_dynamic libc_unlinkat unlinkat "/usr/lib/libSystem.B.dylib"

func libc_unlinkat_trampoline()

var libc_unlinkat_trampoline_addr uintptr

// AT_NODELETEBUSY leaves a file with open descriptors recoverable.
func removeIdle(dir *os.File, name string, _ os.FileInfo) error {
	const atNoDeleteBusy = 0x4000
	return unlinkAt(dir, name, atNoDeleteBusy)
}

func removeEmptyAt(dir *os.File, name string) error {
	const atRemoveDir = 0x80
	return unlinkAt(dir, name, atRemoveDir)
}

func unlinkAt(dir *os.File, name string, flags uintptr) error {
	path, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall_syscall6(libc_unlinkat_trampoline_addr, dir.Fd(), uintptr(unsafe.Pointer(path)), flags, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
