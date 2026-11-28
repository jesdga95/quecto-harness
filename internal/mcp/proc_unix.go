//go:build darwin || linux

package mcp

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// newProcessGroup puts the server in its own process group, so a server that
// started children of its own is stopped as a tree.
func newProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup signals the whole group. A group that has already gone reports
// ESRCH, which is not a failure.
func killGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func catalogProcessGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func lockCatalogKey(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
}

func catalogOwned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}

func catalogLease(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return false, shareCatalogLease(f)
	}
	return err == nil, err
}
func shareCatalogLease(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_SH) }
