//go:build !darwin && !linux

package store

import (
	"errors"
	"os"
)

// homeTrash is not known here.
func homeTrash() string { return "" }

// trashOf finds no trash here, so every folder stays where it is.
func trashOf(string, os.FileInfo) (trashCan, error) { return trashCan{}, errNoTrash }

func renameNoReplace(from, to string) error { return errors.ErrUnsupported }

func renameNoReplaceAt(int, string, string) error    { return errors.ErrUnsupported }
func removeIdle(*os.File, string, os.FileInfo) error { return errors.ErrUnsupported }

func renameInto(*os.File, string, *os.File, string) error { return errors.ErrUnsupported }
func removeEmptyAt(*os.File, string) error                { return errors.ErrUnsupported }
