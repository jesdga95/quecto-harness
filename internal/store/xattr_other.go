//go:build !darwin && !linux

package store

import "os"

// hasAttrs cannot list attributes here. No folder is swept here either, since
// its change times are not read.
func hasAttrs(*os.File) bool { return false }
