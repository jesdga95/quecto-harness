//go:build !darwin && !linux

package store

import "os"

func sameChangeTime(a, b os.FileInfo) bool { return false }

func outputDirectoryID(*os.Root, os.FileInfo) (string, bool, error) { return "", false, os.ErrInvalid }
func markerIdentity(os.FileInfo) string                             { return "" }
func removeEmptyOutput(string) error                                { return os.ErrInvalid }

func outputInode(os.FileInfo) uint64                    { return 0 }
func openOutputFile(*os.Root, string) (*os.File, error) { return nil, os.ErrInvalid }
