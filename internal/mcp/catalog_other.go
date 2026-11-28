//go:build !darwin && !linux

package mcp

import (
	"errors"
	"os"
)

// Keep staged catalogs when the writer's status is unknown.
func catalogProcessGone(pid int) bool { return false }

func lockCatalogKey(file *os.File) error { return errors.ErrUnsupported }

func checkCatalogVolume(string) error { return errors.ErrUnsupported }
func catalogOwned(os.FileInfo) bool   { return false }

func catalogLease(*os.File) (bool, error) { return false, errors.ErrUnsupported }
func shareCatalogLease(*os.File) error    { return errors.ErrUnsupported }
