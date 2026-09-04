package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MaxAttachment is the largest text file a message may carry. Attachments are
// inlined into the prompt, so the cap is what keeps one file from filling the
// context.
const MaxAttachment = 256 << 10

// ReadAttachment reads a text file the user attached to a message. It is not
// held to a project root: the file was chosen in the host's own dialog, which
// is the user saying which file it is.
func ReadAttachment(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("attach %s: the path is not absolute", path)
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("attach %s: no such file, or it cannot be read", path)
	}
	if info.IsDir() {
		return "", fmt.Errorf("attach %s: that is a folder", path)
	}
	if info.Size() > MaxAttachment {
		return "", fmt.Errorf("attach %s: the file is %d kb, the limit is %d kb",
			path, info.Size()/1024, MaxAttachment/1024)
	}
	raw, err := os.ReadFile(clean)
	if err != nil {
		return "", fmt.Errorf("attach %s: check the permissions on the file", path)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("attach %s: the file is empty", path)
	}
	if looksBinary(raw[:min(len(raw), 8192)]) {
		return "", errors.New("attach " + path + ": that looks like a binary file")
	}
	return string(raw), nil
}
