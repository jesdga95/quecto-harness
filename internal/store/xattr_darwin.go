package store

import (
	"encoding/binary"
	"os"
	"runtime"
	"strings"
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
	n, _, errno := syscall.Syscall6(syscall.SYS_FLISTXATTR, fd, uintptr(p), uintptr(len(buf)), 0, 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(n), nil
}

// systemAttr reports whether name is the tag naming the app that wrote a
// file, which the system puts on the copy as well.
func systemAttr(name string) bool { return strings.EqualFold(name, "com.apple.provenance") }

// extendedACL reports whether f carries an access control list, which macOS
// keeps apart from the extended attributes. A list that cannot be checked
// counts as one, except on a volume that has none.
func extendedACL(f *os.File) bool {
	const (
		returnedAttrs    = 0x80000000
		extendedSecurity = 0x00400000
	)
	req := struct {
		bitmapCount, reserved             uint16
		common, volume, dir, file, forked uint32
	}{bitmapCount: 5, common: returnedAttrs | extendedSecurity}
	// The reply starts with its length and the set of attributes returned.
	// A longer list is cut off, which leaves that set whole.
	var buf [64]byte
	_, _, errno := syscall.Syscall6(syscall.SYS_FGETATTRLIST, f.Fd(), uintptr(unsafe.Pointer(&req)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 0)
	runtime.KeepAlive(f)
	if errno == syscall.ENOTSUP {
		return false
	}
	if errno != 0 {
		return true
	}
	return binary.LittleEndian.Uint32(buf[4:8])&extendedSecurity != 0
}
