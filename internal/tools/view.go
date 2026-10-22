package tools

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// The kinds of file View reports. Only text carries its content; an image is
// fetched again by the caller as bytes.
const (
	ViewText   = "text"
	ViewImage  = "image"
	ViewBinary = "binary"
	ViewLarge  = "large"
)

// ErrOutside and ErrMissing let a caller outside a turn tell the two refusals
// apart, which a model reading a tool result is not told.
var (
	ErrOutside   = errors.New("outside the project")
	ErrMissing   = errors.New("no such file")
	ErrFolder    = errors.New("a folder")
	ErrNotFolder = errors.New("not a folder")
)

// viewImages are the image types a browser draws without running anything.
// An SVG can carry script, so it is read as text.
var viewImages = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// View is one file in a project, as a reader outside a turn is shown it.
type View struct {
	Abs     string
	Path    string
	Kind    string
	Mime    string
	Bytes   int64
	Content string
}

// ViewFile reads a file under root the way a tool argument is resolved, and
// nothing outside root. Text past limit is reported as large.
func ViewFile(root, p string, limit int64) (View, error) {
	r, err := NewRunner(root, config.Config{})
	if err != nil {
		return View{}, err
	}
	abs, err := r.resolveInside(p)
	if err != nil {
		return View{}, ErrOutside
	}
	v := View{Abs: abs, Path: r.rel(abs)}
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return v, ErrMissing
	}
	if err != nil {
		return v, fmt.Errorf("read %s: check the permissions on the file", v.Path)
	}
	if info.IsDir() {
		return v, ErrFolder
	}
	v.Bytes = info.Size()

	f, err := os.Open(abs)
	if err != nil {
		return v, fmt.Errorf("read %s: check the permissions on the file", v.Path)
	}
	defer f.Close()
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
			return v, fmt.Errorf("read %s: %s", v.Path, err)
		}
		v.Kind, v.Content = ViewText, string(head)+string(rest)
	}
	return v, nil
}

// Entry is one row of a folder listing.
type Entry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Bytes int64  `json:"bytes"`
}

// ListDir lists one folder under root, folders first and then files, each by
// name without regard to case. rel is the folder relative to root, empty for
// root itself.
func ListDir(root, p string) (rel string, entries []Entry, err error) {
	r, err := NewRunner(root, config.Config{})
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(p) == "" {
		p = "."
	}
	abs, err := r.resolveInside(p)
	if err != nil {
		return "", nil, ErrOutside
	}
	rel = r.rel(abs)
	if rel == "." {
		rel = ""
	}
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return rel, nil, ErrMissing
	}
	if err != nil {
		return rel, nil, fmt.Errorf("read %s: check the permissions on the folder", rel)
	}
	if !info.IsDir() {
		return rel, nil, ErrNotFolder
	}
	list, err := os.ReadDir(abs)
	if err != nil {
		return rel, nil, fmt.Errorf("read %s: check the permissions on the folder", rel)
	}
	entries = make([]Entry, 0, len(list))
	for _, d := range list {
		e := Entry{Name: d.Name(), Dir: d.IsDir()}
		// A link is listed as what it points to, so a linked folder opens.
		if d.Type()&fs.ModeSymlink != 0 {
			if target, err := os.Stat(filepath.Join(abs, d.Name())); err == nil {
				e.Dir = target.IsDir()
			}
		}
		if !e.Dir {
			if fi, err := d.Info(); err == nil {
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

// FindFiles lists files under root whose path holds every word of q, ignoring
// case, for a reader picking a file by name. A match in the file name ranks
// above one in its folders, and a shorter path above a longer one. The walk
// passes over the folders grep skips and stops after scanLimit files.
func FindFiles(root, q string, limit int) ([]string, error) {
	r, err := NewRunner(root, config.Config{})
	if err != nil {
		return nil, err
	}
	const scanLimit = 50000
	words := strings.Fields(strings.ToLower(q))
	type hit struct {
		path string
		rank int
	}
	var hits []hit
	scanned := 0
	errStop := errors.New("stop")
	walkErr := filepath.WalkDir(r.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != r.Root && slices.Contains(skipDirs, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		scanned++
		if scanned > scanLimit {
			return errStop
		}
		rel := r.rel(p)
		lower := strings.ToLower(rel)
		name := strings.ToLower(d.Name())
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
		hits = append(hits, hit{rel, rank})
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
