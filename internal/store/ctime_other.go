//go:build !darwin && !linux

package store

import (
	"os"
	"time"
)

// changeTime is not read here, so no folder is shown unchanged and none is
// swept.
func changeTime(os.FileInfo) (time.Time, bool) { return time.Time{}, false }
