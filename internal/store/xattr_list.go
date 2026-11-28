//go:build darwin || linux

package store

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// hasAttrs reports whether f carries an extended attribute, a resource fork
// among them, other than those the system puts on any file it writes, or an
// access control list. A list that cannot be read counts as one.
func hasAttrs(f *os.File) bool {
	if extendedACL(f) {
		return true
	}
	for range 3 {
		n, err := flistxattr(f.Fd(), nil)
		if errors.Is(err, syscall.ENOTSUP) {
			return false
		}
		if err != nil {
			return true
		}
		if n == 0 {
			return false
		}
		buf := make([]byte, n)
		n, err = flistxattr(f.Fd(), buf)
		if errors.Is(err, syscall.ERANGE) {
			continue
		}
		if err != nil {
			return true
		}
		for name := range strings.SplitSeq(strings.TrimRight(string(buf[:n]), "\x00"), "\x00") {
			if !systemAttr(name) {
				return true
			}
		}
		return false
	}
	return true
}
