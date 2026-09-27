package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"quecto-harness/internal/config"
)

// ReplaceInFile swaps one exact run of text for another. The match must be
// unique, since a model that gave too little context would otherwise change
// whichever occurrence came first.
func (r *Runner) ReplaceInFile(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args ReplaceInFileArgs
	if err := decodeArgs(raw, &args); err != nil {
		return Result{}, fmt.Errorf("replace_in_file: %s", err)
	}
	if args.Path == "" {
		return Result{}, errors.New("replace_in_file needs a path")
	}
	if args.OldString == "" {
		return Result{}, errors.New("replace_in_file needs old_string: to create a file, use write_file")
	}
	if args.OldString == args.NewString {
		return Result{}, fmt.Errorf("old_string and new_string are the same, so %s is already what you asked for", args.Path)
	}
	path, err := r.resolve(args.Path)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, fmt.Errorf("%s: no such file", args.Path)
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("%s is a directory", args.Path)
	}
	if info.Size() > maxReadableFile {
		return Result{}, fmt.Errorf("%s is %d MB, too large to edit", args.Path, info.Size()/(1<<20))
	}

	before := r.snapshot(path)
	if before.State == SnapshotBinary {
		return Result{}, fmt.Errorf("%s looks like a binary file, not edited", args.Path)
	}
	if before.State != SnapshotText {
		return Result{}, fmt.Errorf("read %s: check the permissions on the file", args.Path)
	}
	old := string(before.Content)

	switch n := strings.Count(old, args.OldString); {
	case n == 0:
		return Result{}, fmt.Errorf("old_string is not in %s: read the file and copy the text exactly, whitespace included", args.Path)
	case n > 1:
		return Result{}, fmt.Errorf("old_string appears %d times in %s: include more of the surrounding lines so it matches once", n, args.Path)
	}

	updated := strings.Replace(old, args.OldString, args.NewString, 1)
	if err := config.WriteAtomic(path, []byte(updated), info.Mode().Perm()); err != nil {
		return Result{}, fmt.Errorf("write %s: %s", args.Path, err)
	}
	if r.Baseline != nil {
		r.Baseline(before)
	}

	return Result{
		Output: fmt.Sprintf("edited %s at line %d (-%d +%d)",
			r.rel(path), lineOf(old, args.OldString),
			countLines(args.OldString), countLines(args.NewString)),
		Bytes: len(updated),
		Meta:  lineCounts(before, updated),
	}, nil
}

// countLines counts the lines a run of text spans. A trailing newline ends
// the last line rather than starting another, and empty text spans none.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// lineOf is the 1-based line the match starts on.
func lineOf(content, match string) int {
	return strings.Count(content[:strings.Index(content, match)], "\n") + 1
}
