//go:build darwin || linux

package tools

import (
	"errors"
	"os/exec"
	"syscall"
)

// newProcessGroup puts the shell in its own process group, so a command that
// forked children can be killed as a tree rather than orphaning them.
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
