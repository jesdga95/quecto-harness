package store

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// Files holds the attachments that are not text: one file per attachment,
// named by an id the session entry carries. Bytes stay out of the session
// document, which keeps a session file something a person can read.
type Files struct {
	dir string
}

var filePattern = regexp.MustCompile(`^file_[a-z2-7]{16}$`)

func OpenFiles(dir string) (*Files, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("Create %s: check the permissions on its parent", dir)
	}
	return &Files{dir: dir}, nil
}

// Put copies src into the store and returns its id and size.
func (f *Files) Put(src string) (string, int, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, fmt.Errorf("Read %s: check the permissions on the file", src)
	}
	defer in.Close()

	id := NewFileID()
	out, err := os.OpenFile(filepath.Join(f.dir, id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("Store %s: %w", src, err)
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(filepath.Join(f.dir, id))
		return "", 0, fmt.Errorf("Store %s: %w", src, err)
	}
	return id, int(n), nil
}

// Read returns the whole file. Attachments are capped when they are taken in,
// so nothing here is large enough to stream.
func (f *Files) Read(id string) ([]byte, error) {
	path, err := f.Path(id)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// Path resolves an id to a file. An id that is not one this store minted is
// refused rather than joined onto the directory.
func (f *Files) Path(id string) (string, error) {
	if !filePattern.MatchString(id) {
		return "", errors.New("No file with that id")
	}
	return filepath.Join(f.dir, id), nil
}

// Remove deletes one file. A file that is already gone is not an error, since
// the caller is cleaning up after a session either way.
func (f *Files) Remove(id string) error {
	path, err := f.Path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func NewFileID() string {
	b := make([]byte, 10)
	rand.Read(b)
	return "file_" + idEncoding.EncodeToString(b)[:16]
}
