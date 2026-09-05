package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"quecto-harness/internal/config"
)

// The state of a file the first time a session wrote to it. Only Text has a
// copy kept; the rest say why there is nothing to compare against.
const (
	BaselineNew    = "new"
	BaselineText   = "text"
	BaselineBinary = "binary"
	BaselineLarge  = "large"
)

// Baseline is one file as it stood before this session first wrote to it.
// Path is relative to the project. Blob names the copy in the session's
// sidecar folder, and is empty for every state but text.
type Baseline struct {
	Path  string    `json:"path"`
	State string    `json:"state"`
	Blob  string    `json:"blob,omitempty"`
	At    time.Time `json:"at"`
}

// BlobName is the sidecar file a path's copy is kept under. Hashing keeps the
// name flat and free of separators, so a deep path is still one file.
func BlobName(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:32]
}

// filesDir is the sidecar folder holding one session's baseline copies.
func (s *Sessions) filesDir(id string) string {
	return filepath.Join(s.dir, id+".files")
}

// WriteBlob stores one baseline copy. The name comes from BlobName, so it can
// never reach outside the folder.
func (s *Sessions) WriteBlob(id, name string, data []byte) error {
	if !namePattern.MatchString(id+".json") || !blobPattern.MatchString(name) {
		return errors.New("Store a baseline: the session or the name is not one of ours")
	}
	dir := s.filesDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("Create %s: check the permissions on the sessions folder", dir)
	}
	return config.WriteAtomic(filepath.Join(dir, name), data, 0o600)
}

// Blob reads one baseline copy back.
func (s *Sessions) Blob(id, name string) ([]byte, error) {
	if !namePattern.MatchString(id+".json") || !blobPattern.MatchString(name) {
		return nil, ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.filesDir(id), name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("Read a baseline of session %s: %w", id, err)
	}
	return data, nil
}
