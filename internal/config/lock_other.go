//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package config

func lockDirectoryEntry(dir, name string) (release func(), err error) { return func() {}, nil }
