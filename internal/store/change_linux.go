package store

import (
	"os"
	"syscall"
)

func sameChangeTime(a, b os.FileInfo) bool {
	return a.Sys().(*syscall.Stat_t).Ctim == b.Sys().(*syscall.Stat_t).Ctim
}

func statChangeTime(st *syscall.Stat_t) syscall.Timespec { return st.Ctim }
