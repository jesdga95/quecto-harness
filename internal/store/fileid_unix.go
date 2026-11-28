//go:build unix

package store

import (
	"fmt"
	"os"
	"syscall"
)

// FileID names a file by its device and inode, which survive a restart and
// change when another file is put under the same name. Empty when the system
// does not say.
func FileID(info os.FileInfo) string {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}

// device is the device a file is on.
func device(info os.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true
}

// ownedByMe reports whether the file belongs to the account the harness runs as.
func ownedByMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// KnowsFileID reports whether FileID names files here, which removing a
// scratch folder depends on.
const KnowsFileID = true

func hasOtherLinks(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink > 1
}
