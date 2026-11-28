package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Each digest covers one path, its type and inode, and the bytes of a file or link.
// Directory contents are recorded separately so an interrupted deletion can resume.
type outputSnapshot struct {
	identity string
	manifest string
}

func (s outputSnapshot) generation() string {
	if s.identity == "" {
		return ""
	}
	return fmt.Sprintf("%s.%x", s.identity, sha256.Sum256([]byte(s.manifest)))
}

func (s outputSnapshot) approved() map[string]bool {
	approved := make(map[string]bool)
	for digest := range strings.FieldsSeq(s.manifest) {
		approved[digest] = true
	}
	return approved
}

func (r relation) outputSnapshot() (outputSnapshot, bool) {
	last := strings.LastIndexByte(r.OutputGeneration, '.')
	if last < 0 {
		return outputSnapshot{}, false
	}
	s := outputSnapshot{r.OutputGeneration[:last], r.OutputManifest}
	return s, s.generation() == r.OutputGeneration
}

func snapshotIn(root *os.Root, info os.FileInfo, entries []fs.DirEntry, create bool) (outputSnapshot, error) {
	id, err := outputMarkerID(root, info, entries, create)
	if err != nil || id == "" {
		return outputSnapshot{}, err
	}
	marker, _, _ := strings.Cut(id, ".")
	var hashes []string
	err = walkOutputEntries(root, entries, ".recovery-"+marker, func(digest string) error {
		hashes = append(hashes, digest)
		return nil
	}, false)
	if err != nil {
		return outputSnapshot{}, err
	}
	slices.Sort(hashes)
	return outputSnapshot{id, strings.Join(hashes, " ")}, nil
}

func walkOutputEntries(root *os.Root, entries []fs.DirEntry, marker string, check func(string) error, remove bool) error {
	for _, e := range entries {
		if e.Name() == marker {
			continue
		}
		if err := walkOutput(root, e.Name(), e.Name(), check, remove); err != nil {
			return err
		}
	}
	return nil
}

// Open each child relative to its parent. A directory that cannot be read can
// only be removed empty. A failed removal leaves the marker and order in place.
func walkOutput(parent *os.Root, name, path string, check func(string) error, remove bool) error {
	info, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	digest, err := outputEntryDigest(parent, name, path, info)
	if err != nil {
		return err
	}
	if err := check(digest); err != nil {
		return err
	}
	if info.IsDir() {
		dir, err := parent.OpenRoot(name)
		if err == nil {
			defer dir.Close()
			opened, err := dir.Stat(".")
			if err != nil {
				return err
			}
			if !os.SameFile(info, opened) {
				return errOutputChanged
			}
			entries, err := fs.ReadDir(dir.FS(), ".")
			if err != nil && !errors.Is(err, os.ErrPermission) {
				return err
			}
			for _, child := range entries {
				if err := walkOutput(dir, child.Name(), filepath.Join(path, child.Name()), check, remove); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, os.ErrPermission) {
			return err
		}
	}
	if !remove {
		return nil
	}
	now, err := parent.Lstat(name)
	if err != nil {
		return err
	}
	if !os.SameFile(info, now) || (!info.IsDir() && !sameChangeTime(info, now)) {
		return errOutputChanged
	}
	return parent.Remove(name)
}

func outputEntryDigest(parent *os.Root, name, path string, info os.FileInfo) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%d\x00", path, info.Mode().Type(), outputInode(info))
	switch {
	case info.Mode().IsRegular():
		f, err := openOutputFile(parent, name)
		if err != nil {
			return "", err
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil {
			return "", err
		}
		if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			return "", errOutputChanged
		}
		fmt.Fprintf(h, "%d\x00%d\x00", info.Size(), info.ModTime().UnixNano())
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		after, err := f.Stat()
		if err != nil {
			return "", err
		}
		if !sameChangeTime(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
			return "", errOutputChanged
		}
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(filepath.Join(parent.Name(), name))
		if err != nil {
			return "", err
		}
		io.WriteString(h, target)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
