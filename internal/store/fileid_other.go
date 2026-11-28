//go:build !unix

package store

import "os"

// FileID is empty where the system gives no stable identity for a file, and
// a check by identity then rests on the path alone.
func FileID(os.FileInfo) string { return "" }

// device is not known here.
func device(os.FileInfo) (uint64, bool) { return 0, false }

// ownedByMe cannot tell the owner here.
func ownedByMe(os.FileInfo) bool { return false }

// KnowsFileID reports whether FileID names files here. It does not, so no
// scratch folder is removed.
const KnowsFileID = false

func hasOtherLinks(info os.FileInfo) bool {
	return false
}
