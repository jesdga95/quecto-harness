package store

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// Ext filesystems assign a generation to each new inode. It survives remounts
// and distinguishes an inode allocated again after a directory was deleted.
func outputDirectoryID(root *os.Root, info os.FileInfo) (string, bool, error) {
	inode := info.Sys().(*syscall.Stat_t).Ino
	dir, err := root.Open(".")
	if err != nil {
		return "", false, err
	}
	defer dir.Close()
	var fs syscall.Statfs_t
	if err := syscall.Fstatfs(int(dir.Fd()), &fs); err != nil {
		return "", false, err
	}
	const extMagic = 0xef53
	if fs.Type != extMagic {
		return fmt.Sprintf("i%x", inode), false, nil
	}
	// FS_IOC_GETVERSION is _IOR('v', 1, long). MIPS and PowerPC encode
	// the read direction in bit 30, the other Linux targets in bit 31.
	read := uintptr(1 << 31)
	switch runtime.GOARCH {
	case "ppc64", "ppc64le", "mips", "mipsle", "mips64", "mips64le":
		read = 1 << 30
	}
	request := read | unsafe.Sizeof(uintptr(0))<<16 | 'v'<<8 | 1
	var generation uint32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, dir.Fd(), request, uintptr(unsafe.Pointer(&generation)))
	switch errno {
	case syscall.EACCES, syscall.EPERM, syscall.ENOTTY, syscall.ENOSYS, syscall.EOPNOTSUPP:
		return fmt.Sprintf("i%x", inode), false, nil
	}
	if errno != 0 {
		return "", false, fmt.Errorf("read the output directory inode generation: %w", errno)
	}
	return fmt.Sprintf("g%x-%x", inode, generation), generation != 0, nil
}
