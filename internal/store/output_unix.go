//go:build darwin || linux

package store

import (
	"fmt"
	"os"
	"syscall"
)

func markerIdentity(info os.FileInfo) string {
	st := info.Sys().(*syscall.Stat_t)
	t := statChangeTime(st)
	return fmt.Sprintf("%x-%x-%x", st.Ino, t.Sec, t.Nsec)
}

func removeEmptyOutput(path string) error { return syscall.Rmdir(path) }

func outputInode(info os.FileInfo) uint64 { return info.Sys().(*syscall.Stat_t).Ino }
func openOutputFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
