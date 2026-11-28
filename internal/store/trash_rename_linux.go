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
