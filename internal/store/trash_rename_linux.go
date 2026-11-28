package store

import (
	"os"
	"syscall"
	"unsafe"
)

// exclusiveRename asks the kernel to test the destination and move the source
// in one operation. An unsupported flag or filesystem leaves both names alone.
func exclusiveRename(trap uintptr, cwd int, flags uintptr, from, to string) error {
	a, err := syscall.BytePtrFromString(from)
	if err != nil {
		return err
	}
	b, err := syscall.BytePtrFromString(to)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(trap, uintptr(cwd), uintptr(unsafe.Pointer(a)), uintptr(cwd), uintptr(unsafe.Pointer(b)), flags, 0)
	if errno != 0 {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: errno}
	}
	return nil
}

// A read lease excludes existing writers and blocks new ones until the
// descriptor closes. A filesystem that refuses the lease keeps the file.
func removeIdle(dir *os.File, name string, want os.FileInfo) error {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if _, _, err := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETLEASE, syscall.F_RDLCK); err != 0 {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !unchanged(want, info) {
		return errNotHeld
	}
	return syscall.Unlinkat(int(dir.Fd()), name)
}

func renameInto(parent *os.File, from string, dest *os.File, to string) error {
	return syscall.Renameat(int(parent.Fd()), from, int(dest.Fd()), to)
}

func removeEmptyAt(dir *os.File, name string) error {
	const atRemoveDir = 0x200
	path, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, dir.Fd(), uintptr(unsafe.Pointer(path)), atRemoveDir)
	if errno != 0 {
		return errno
	}
	return nil
}
