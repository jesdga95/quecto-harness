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
		return Result{}, fmt.Errorf("edit: %s", err)
	}
	if args.Path == "" {
		return Result{}, errors.New("edit needs a path")
	}
	if args.OldString == "" {
		return Result{}, errors.New("edit needs old_string: to create a file, use write")
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
	content := before.Content
	if before.State == SnapshotLarge {
		// A file past the baseline limit is still edited; only the copy kept
		// for the change panel is skipped.
		content, err = os.ReadFile(path)
		if err == nil && looksBinary(content[:min(len(content), 8192)]) {
			return Result{}, fmt.Errorf("%s looks like a binary file, not edited", args.Path)
		}
	}
	if before.State == SnapshotNew || err != nil {
		return Result{}, fmt.Errorf("read %s: check the permissions on the file", args.Path)
	}

	// read shows a CRLF file with LF endings, so text copied from it is matched
	// against the file with its endings folded the same way.
	old, oldString, newString := string(content), args.OldString, args.NewString
	crlf := allCRLF(old) && !strings.Contains(oldString, "\r")
	if crlf {
		old = strings.ReplaceAll(old, "\r\n", "\n")
		newString = strings.ReplaceAll(newString, "\r\n", "\n")
	}

	switch n := strings.Count(old, oldString); {
	case n == 0:
		return Result{}, fmt.Errorf("old_string is not in %s: read the file and copy the text exactly, whitespace included", args.Path)
	case n > 1:
		return Result{}, fmt.Errorf("old_string appears %d times in %s: include more of the surrounding lines so it matches once", n, args.Path)
	}

	updated := strings.Replace(old, oldString, newString, 1)
	line := lineOf(old, oldString)
	if crlf {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	if err := config.WriteAtomic(path, []byte(updated), info.Mode().Perm()); err != nil {
		return Result{}, fmt.Errorf("write %s: %s", args.Path, err)
	}
	if r.Baseline != nil {
		r.Baseline(before)
	}

	return Result{
		Output: fmt.Sprintf("edited %s at line %d (-%d +%d)",
			r.rel(path), line, countLines(oldString), countLines(newString)),
		Bytes: len(updated),
		Meta:  lineCounts(before, updated),
	}, nil
}

// allCRLF reports whether every line in s ends in CRLF. A file that mixes the
// two is matched as it stands, since folding it could not be undone.
func allCRLF(s string) bool {
	n := strings.Count(s, "\r\n")
	return n > 0 && n == strings.Count(s, "\n")
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
