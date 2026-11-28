//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func lockDirectoryEntry(dir, name string) (release func(), err error) {
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("Open %s: check the permissions on %s: %w", path, dir, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if name == sessionsLockName {
				return nil, fmt.Errorf("Another quecto-harness is using the sessions directory %s: stop it or choose a separate sessions directory", dir)
			}
			return nil, fmt.Errorf("Another quecto-harness is already running on the state in %s: stop it first, or pass --home to use another state directory", dir)
		}
		return nil, fmt.Errorf("Lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
