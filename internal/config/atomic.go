package config

import (
	"os"
	"path/filepath"
)

// WriteAtomic replaces path with data. The temp file is created in the same
// directory so the rename cannot cross a filesystem boundary, and a crash
// never leaves a half-written file.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return wrapWrite(path, err)
	}
	tmp := f.Name()
	cleanup := func(e error) error {
		f.Close()
		os.Remove(tmp)
		return wrapWrite(path, e)
	}
	if err := f.Chmod(perm); err != nil {
		return cleanup(err)
	}
	if _, err := f.Write(data); err != nil {
		return cleanup(err)
	}
	if err := f.Sync(); err != nil {
		return cleanup(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return wrapWrite(path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return wrapWrite(path, err)
	}
	syncDir(dir)
	return nil
}

// syncDir flushes the directory entry the rename changed, so a crash cannot
// bring back the old file. The data is already written, and a filesystem that
// refuses to sync a directory has nothing more to offer, so a failure is ignored.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	d.Sync()
	d.Close()
}

func wrapWrite(path string, err error) error {
	return &writeError{path: path, err: err}
}

type writeError struct {
	path string
	err  error
}

func (e *writeError) Error() string {
	return "Write " + e.path + ": " + e.err.Error() + ": check the permissions on the state directory"
}

func (e *writeError) Unwrap() error { return e.err }
