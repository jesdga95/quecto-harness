package store

import (
	"os"
	"syscall"
	"time"
)

// changeTime is when the file's data or attributes last changed, which no
// call can set back.
func changeTime(info os.FileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(st.Ctimespec.Unix()), true
}
