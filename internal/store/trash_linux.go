package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// homeTrash is the freedesktop home trash.
func homeTrash() string { return xdgHomeTrash() }

// trashOf is the freedesktop trash for a folder on the volume info names:
// the home trash on its own volume, or the trash at the top of another.
func trashOf(path string, info os.FileInfo) (trashCan, error) {
	dev, ok := device(info)
	if !ok {
		return trashCan{}, errNoTrash
	}
	if home := TrashHome(); home != "" && homeTrashDevice(filepath.Dir(home), dev) {
		if err := os.MkdirAll(filepath.Dir(home), 0700); err == nil {
			if c, ok := xdgCan(home, dev, ""); ok {
				return c, nil
			}
		}
		return trashCan{}, errNoTrash
	}
	if top := TrashTop(path); top != "" {
		if c, ok := xdgTopCan(top, dev); ok {
			return c, nil
		}
	}
	return trashCan{}, errNoTrash
}

// homeTrashDevice finds the volume of the data directory even before its
// missing parent directories have been created.
func homeTrashDevice(dir string, dev uint64) bool {
	for {
		info, err := os.Stat(dir)
		if err == nil {
			d, ok := device(info)
			return ok && d == dev
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, os.ErrNotExist) || parent == dir {
			return false
		}
		dir = parent
	}
}

// The renameat2 system call number is specific to the Linux architecture.
func renameNoReplace(from, to string) error {
	return renameNoReplaceAt(-100, from, to)
}

func renameNoReplaceAt(fd int, from, to string) error {
	const renameNoreplace = 1
	var trap uintptr
	switch runtime.GOARCH {
	case "amd64":
		trap = 316
	case "386":
		trap = 353
	case "arm":
		trap = 382
	case "arm64", "riscv64", "loong64":
		trap = 276
	case "ppc64", "ppc64le":
		trap = 357
	case "s390x":
		trap = 347
	case "mips", "mipsle":
		trap = 4351
	case "mips64", "mips64le":
		trap = 5311
	default:
		return errors.ErrUnsupported
	}
	return exclusiveRename(trap, fd, renameNoreplace, from, to)
}
