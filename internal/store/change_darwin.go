package store

import (
	"fmt"
	"os"
	"syscall"
)

func sameChangeTime(a, b os.FileInfo) bool {
	return a.Sys().(*syscall.Stat_t).Ctimespec == b.Sys().(*syscall.Stat_t).Ctimespec
}

func statChangeTime(st *syscall.Stat_t) syscall.Timespec { return st.Ctimespec }

func outputDirectoryID(_ *os.Root, info os.FileInfo) (string, bool, error) {
	st := info.Sys().(*syscall.Stat_t)
	birth := st.Birthtimespec
	return fmt.Sprintf("b%x-%x-%x", st.Ino, birth.Sec, birth.Nsec), birth != (syscall.Timespec{}), nil
}
