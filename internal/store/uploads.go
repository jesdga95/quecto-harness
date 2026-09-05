package store

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Uploads holds files that arrived as bytes rather than as a path, which is
// what a paste into the composer is. Each one gets a directory of its own so
// it can keep a name a person can read.
type Uploads struct {
	dir string
}

// uploadLife is how long a pasted file is kept. It is read when the message
// is sent, moments after it is written, so anything older was abandoned.
const uploadLife = 24 * time.Hour

// maxUploadName keeps a name inside what a filesystem takes. A longer one is
// cut down, extension kept, since that is what the composer draws.
const maxUploadName = 64

func OpenUploads(dir string) (*Uploads, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("Create %s: check the permissions on its parent", dir)
	}
	return &Uploads{dir: dir}, nil
}

// Save writes data under name and returns the path to it. The name comes from
// a browser, so it is reduced to its last element before it is joined.
func (u *Uploads) Save(name string, data []byte) (string, error) {
	name = filepath.Base(filepath.Clean("/" + name))
	if name == "." || name == string(filepath.Separator) {
		name = "Pasted file"
	}
	name = trimName(name)
	dir := filepath.Join(u.dir, NewFileID())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("Store the pasted file: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("Store the pasted file: %w", err)
	}
	return path, nil
}

// trimName cuts a long name from the middle of its stem, so both the start of
// the name and the extension survive.
func trimName(name string) string {
	if len(name) <= maxUploadName {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 8 {
		ext = ""
	}
	return name[:maxUploadName-len(ext)] + ext
}

// Prune drops what earlier pastes left behind. A directory that cannot be
// read is left where it is, since nothing depends on it going away.
func (u *Uploads) Prune() {
	entries, err := os.ReadDir(u.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < uploadLife {
			continue
		}
		os.RemoveAll(filepath.Join(u.dir, e.Name()))
	}
}
