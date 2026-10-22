package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/diff"
)

// baselineLimit is the largest file kept as a baseline. Past it the write
// still happens and the change is still listed, with nothing to compare.
const baselineLimit = 2 << 20

// The state of a file before a write, as the snapshot reports it. Content is
// carried only for text.
const (
	SnapshotNew    = "new"
	SnapshotText   = "text"
	SnapshotBinary = "binary"
	SnapshotLarge  = "large"
)

// Snapshot is one file as it stood before a write, handed to whatever wants
// to keep it. Path is relative to the project root.
type Snapshot struct {
	Path    string
	State   string
	Content []byte
}

// WriteFile replaces a file whole. The result given to the model is the
// summary line, not the content, so the session file does not carry it twice.
func (r *Runner) WriteFile(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args WriteFileArgs
	if err := DecodeArgs("write", raw, &args); err != nil {
		return Result{}, err
	}
	if args.Path == "" {
		return Result{}, errors.New("write needs a path")
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return Result{}, err
	}

	verb, perm := "wrote", os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return Result{}, fmt.Errorf("%s is a directory", args.Path)
		}
		if err := r.checkSeen(args.Path, path, info); err != nil {
			return Result{}, err
		}
		verb, perm = "replaced", info.Mode().Perm()
	}
	// Read before writing, since after the write the old content is gone.
	before := r.snapshot(path)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{}, fmt.Errorf("create the folder for %s: check the permissions on the project", args.Path)
	}
	data := []byte(args.Content)
	if err := config.WriteAtomicNoSync(path, data, perm); err != nil {
		return Result{}, fmt.Errorf("write %s: %s", args.Path, err)
	}
	// The change list covers the project, so a file outside keeps no copy.
	if r.Baseline != nil && within(r.Root, path) {
		r.Baseline(before)
	}
	r.noteFile(path)

	lines := strings.Count(args.Content, "\n")
	if len(args.Content) > 0 && !strings.HasSuffix(args.Content, "\n") {
		lines++
	}
	return Result{
		Output: fmt.Sprintf("%s %s (%d bytes, %d lines)", verb, r.rel(path), len(data), lines),
		Bytes:  len(data),
		Meta:   lineCounts(before, args.Content),
	}, nil
}

// checkSeen refuses to change a file the session has not read, or one that
// changed since, so a write from a stale read cannot overwrite someone
// else's work.
func (r *Runner) checkSeen(arg, path string, info os.FileInfo) error {
	if r.Seen == nil {
		return nil
	}
	read, same := r.Seen.check(path, info)
	switch {
	case !read:
		return fmt.Errorf("%s has not been read in this session: read it first, then retry", arg)
	case !same:
		return fmt.Errorf("%s changed since it was last read: read it again, then retry", arg)
	}
	return nil
}

// lineCounts is how many lines a write added and removed, for the step row.
// A file that was binary or too large before has nothing to count against.
func lineCounts(before Snapshot, after string) map[string]any {
	old := ""
	switch before.State {
	case SnapshotText:
		old = string(before.Content)
	case SnapshotNew:
	default:
		return nil
	}
	d := diff.Compute(old, after, 1)
	return map[string]any{"added": d.Added, "removed": d.Removed}
}

// Current describes a file in the project as it stands now. A missing file
// comes back as new, which for a file that was written earlier means gone.
func (r *Runner) Current(rel string) (Snapshot, error) {
	path, err := r.resolve(rel)
	if err != nil {
		return Snapshot{}, err
	}
	return r.snapshot(path), nil
}

// snapshot describes a file as it is now. A file that cannot be read is
// reported as new, since the comparison it would feed has nothing either way.
func (r *Runner) snapshot(path string) Snapshot {
	s := Snapshot{Path: r.rel(path), State: SnapshotNew}
	info, err := os.Stat(path)
	if err != nil {
		return s
	}
	if info.Size() > baselineLimit {
		s.State = SnapshotLarge
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	sniff := data
	if len(sniff) > 8192 {
		sniff = sniff[:8192]
	}
	if looksBinary(sniff) {
		s.State = SnapshotBinary
		return s
	}
	s.State, s.Content = SnapshotText, data
	return s
}
