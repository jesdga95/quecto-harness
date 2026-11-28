package store

import (
	"os"
	"syscall"
	"unsafe"
)

// flistxattr writes the names of the extended attributes of fd into buf, or
// with an empty buf reports the room they need.
func flistxattr(fd uintptr, buf []byte) (int, error) {
	var p unsafe.Pointer
	if len(buf) > 0 {
		p = unsafe.Pointer(&buf[0])
	}
	n, _, errno := syscall.Syscall(syscall.SYS_FLISTXATTR, fd, uintptr(p), uintptr(len(buf)))
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// The destination receives its SELinux label from local policy.
func systemAttr(name string) bool { return name == "security.selinux" }

// extendedACL is false: Linux keeps an access control list among the extended
// attributes, which hasAttrs already counts.
func extendedACL(*os.File) bool { return false }
