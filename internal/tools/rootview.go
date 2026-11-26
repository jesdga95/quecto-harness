package tools

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The functions below read a conversation's own folder through an open root,
// for a reader outside a turn. Every name is resolved by the root, so neither
// a link nor a folder swapped in meanwhile leads a reader out of it.

// rootPath turns a reader's path into one under the root: relative, or
// absolute under dir, the path the root was opened at.
func rootPath(dir, p string) (string, error) {
	if strings.ContainsRune(p, 0) {
		return "", ErrOutside
	}
	if p == "" {
		return ".", nil
	}
	if filepath.IsAbs(p) {
		rel, err := filepath.Rel(dir, filepath.Clean(p))
		if err != nil {
			return "", ErrOutside
		}
		p = rel
	}
	p = filepath.Clean(p)
	if p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return "", ErrOutside
	}
	return p, nil
}

// rootErr names why a root refused a name.
func rootErr(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrMissing
	case errors.Is(err, fs.ErrPermission):
		return err
	}
	return ErrOutside
}

// ViewIn is ViewFile for a file under root, opened at dir. The file it read is
// returned open and positioned at its start, for the caller to serve or close.
func ViewIn(root *os.Root, dir, p string, limit int64) (View, *os.File, error) {
	rel, err := rootPath(dir, p)
	if err != nil {
		return View{Path: p}, nil, err
	}
	v := View{Path: filepath.ToSlash(rel)}
	f, err := root.Open(rel)
	if err != nil {
		if errors.Is(rootErr(err), fs.ErrPermission) {
			return v, nil, fmt.Errorf("read %s: check the permissions on the file", v.Path)
		}
		return v, nil, rootErr(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return v, nil, fmt.Errorf("read %s: check the permissions on the file", v.Path)
	}
	if info.IsDir() {
		f.Close()
		return v, nil, ErrFolder
	}
	v.Bytes = info.Size()
	head := make([]byte, 8192)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	v.Mime = strings.SplitN(http.DetectContentType(head), ";", 2)[0]
	switch {
	case viewImages[v.Mime]:
		v.Kind = ViewImage
	case looksBinary(head):
		v.Kind = ViewBinary
	case v.Bytes > limit:
		v.Kind = ViewLarge
	default:
		rest, err := io.ReadAll(io.LimitReader(f, limit))
		if err != nil {
			f.Close()
			return v, nil, fmt.Errorf("read %s: %s", v.Path, err)
		}
		v.Kind, v.Content = ViewText, string(head)+string(rest)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return v, nil, fmt.Errorf("read %s: %s", v.Path, err)
	}
	return v, f, nil
}

// ListIn is ListDir for a folder under root, opened at dir. A link is listed
// as what it leads to, and left out when that is outside the root or nowhere.
func ListIn(root *os.Root, dir, p string) (rel string, entries []Entry, err error) {
	clean, err := rootPath(dir, p)
	if err != nil {
		return p, nil, err
	}
	rel = filepath.ToSlash(clean)
	if rel == "." {
		rel = ""
	}
	d, err := root.Open(clean)
	if err != nil {
		if errors.Is(rootErr(err), fs.ErrPermission) {
			return rel, nil, fmt.Errorf("read %s: check the permissions on the folder", rel)
		}
		return rel, nil, rootErr(err)
	}
	defer d.Close()
	info, err := d.Stat()
	if err != nil {
		return rel, nil, fmt.Errorf("read %s: check the permissions on the folder", rel)
	}
	if !info.IsDir() {
		return rel, nil, ErrNotFolder
	}
	list, err := d.ReadDir(-1)
	if err != nil {
		return rel, nil, fmt.Errorf("read %s: check the permissions on the folder", rel)
	}
	entries = make([]Entry, 0, len(list))
	for _, item := range list {
		e := Entry{Name: item.Name(), Dir: item.IsDir()}
		if item.Type()&fs.ModeSymlink != 0 {
			target, err := root.Stat(filepath.Join(clean, item.Name()))
			if err != nil {
				continue
			}
			e.Dir = target.IsDir()
			if !e.Dir {
				e.Bytes = target.Size()
			}
		} else if !e.Dir {
			if fi, err := item.Info(); err == nil {
				e.Bytes = fi.Size()
			}
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return rel, entries, nil
}

// FindIn is FindFiles under root. Linked folders are not entered, and a link
// to a file is kept only when it leads to one inside the root.
func FindIn(root *os.Root, q string, limit int) ([]string, error) {
	const scanLimit = 50000
	words := strings.Fields(strings.ToLower(q))
	type hit struct {
		path string
		rank int
	}
	var hits []hit
	scanned := 0
	errStop := errors.New("stop")
	walkErr := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != "." && slices.Contains(skipDirs, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := root.Stat(filepath.FromSlash(p))
			if err != nil || target.IsDir() {
				return nil
			}
		}
		scanned++
		if scanned > scanLimit {
			return errStop
		}
		lower := strings.ToLower(p)
		name := strings.ToLower(path.Base(p))
		rank := 0
		for _, w := range words {
			if !strings.Contains(lower, w) {
				return nil
			}
			if !strings.Contains(name, w) {
				rank++
			}
		}
		if len(words) > 0 && strings.HasPrefix(name, words[len(words)-1]) {
			rank--
		}
		hits = append(hits, hit{p, rank})
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStop) {
		return nil, walkErr
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		if len(hits[i].path) != len(hits[j].path) {
			return len(hits[i].path) < len(hits[j].path)
		}
		return hits[i].path < hits[j].path
	})
	out := make([]string, 0, min(limit, len(hits)))
	for _, h := range hits {
		if len(out) == limit {
			break
		}
		out = append(out, h.path)
	}
	return out, nil
}
