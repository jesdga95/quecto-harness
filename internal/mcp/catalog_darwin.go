package mcp

import (
	"errors"
	"syscall"
)

var catalogStatfs = syscall.Statfs

func checkCatalogVolume(path string) error {
	var fs syscall.Statfs_t
	if err := catalogStatfs(path, &fs); err != nil {
		return err
	}
	const ignoreOwnership = 0x00200000 // MNT_IGNORE_OWNERSHIP
	if fs.Flags&ignoreOwnership != 0 {
		return errors.New("the catalog volume ignores file ownership")
	}
	return nil
}
