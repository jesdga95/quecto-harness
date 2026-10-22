package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// ReplaceInFile makes a list of exact replacements in one file, in order. A
// match must be unique unless every occurrence was asked for, since a model
// that gave too little context would otherwise change whichever came first.
// Each replacement that matches is applied and the rest are reported, so one
// wrong line does not cost the others.
func (r *Runner) ReplaceInFile(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args ReplaceInFileArgs
	if err := DecodeArgs("edit", raw, &args); err != nil {
		return Result{}, err
	}
	if args.Path == "" {
		return Result{}, errors.New("edit needs a path")
	}
	edits := args.Edits
	if len(edits) == 0 && (args.OldString != "" || args.NewString != "") {
		edits = []Replacement{{OldString: args.OldString, NewString: args.NewString, ReplaceAll: args.ReplaceAll}}
	}
	if len(edits) == 0 {
		return Result{}, errors.New("edit needs edits, a list of replacements: to create a file, use write")
	}
	if len(edits) == 1 {
		if edits[0].OldString == "" {
			return Result{}, errors.New("edit needs old_string: to create a file, use write")
		}
		if edits[0].OldString == edits[0].NewString {
			return Result{}, fmt.Errorf("old_string and new_string are the same, so %s is already what you asked for", args.Path)
		}
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
	if err := r.checkSeen(args.Path, path, info); err != nil {
		return Result{}, err
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
	text := string(content)
	crlf := allCRLF(text)
	if crlf {
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}

	var done, failed []string
	for i, e := range edits {
		oldString, newString := e.OldString, e.NewString
		if crlf && !strings.Contains(oldString, "\r") {
			newString = strings.ReplaceAll(newString, "\r\n", "\n")
		} else if crlf {
			oldString = strings.ReplaceAll(oldString, "\r\n", "\n")
			newString = strings.ReplaceAll(newString, "\r\n", "\n")
		}
		next, summary, err := replaceOnce(text, args.Path, oldString, newString, e.ReplaceAll)
		if err != nil {
			if len(edits) == 1 {
				return Result{}, err
			}
			failed = append(failed, fmt.Sprintf("%d: %s", i+1, err))
			continue
		}
		if summary == "" {
			continue
		}
		text = next
		done = append(done, summary)
	}
	if len(done) == 0 {
		if len(failed) == 0 {
			return Result{}, fmt.Errorf("every old_string matches its new_string, so %s is already what you asked for", args.Path)
		}
		return Result{}, fmt.Errorf("no replacement was applied to %s:\n%s", args.Path, strings.Join(failed, "\n"))
	}

	updated := text
	if crlf {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	if err := config.WriteAtomicNoSync(path, []byte(updated), info.Mode().Perm()); err != nil {
		return Result{}, fmt.Errorf("write %s: %s", args.Path, err)
	}
	// The change list covers the project, so a file outside keeps no copy.
	if r.Baseline != nil && within(r.Root, path) {
		r.Baseline(before)
	}
	r.noteFile(path)

	var out string
	if len(edits) == 1 {
		out = fmt.Sprintf("edited %s %s", r.rel(path), done[0])
	} else {
		out = fmt.Sprintf("edited %s: %d of %d replacements applied, %s", r.rel(path), len(done), len(edits), strings.Join(done, "; "))
	}
	if len(failed) > 0 {
		out += "\nnot applied:\n" + strings.Join(failed, "\n")
	}
	return Result{
		Output: out,
		Bytes:  len(updated),
		Meta:   lineCounts(before, updated),
	}, nil
}

// replaceOnce makes one replacement in text and describes where it landed. A
// replacement whose old and new text are the same changes nothing and is
// described as nothing.
func replaceOnce(text, name, oldString, newString string, all bool) (string, string, error) {
	if oldString == "" {
		return "", "", errors.New("old_string is empty")
	}
	if oldString == newString {
		return text, "", nil
	}
	n := strings.Count(text, oldString)
	switch {
	case n == 0:
		return "", "", fmt.Errorf("old_string is not in %s: read the file and copy the text exactly, whitespace included", name)
	case n > 1 && !all:
		return "", "", fmt.Errorf("old_string appears %d times in %s, at lines %s: include more of the surrounding lines so it matches once, or set replace_all to change every one",
			n, name, joinLines(linesOf(text, oldString)))
	}
	if n > 1 {
		return strings.ReplaceAll(text, oldString, newString), fmt.Sprintf("in %d places, at lines %s", n, joinLines(linesOf(text, oldString))), nil
	}
	return strings.Replace(text, oldString, newString, 1),
		fmt.Sprintf("at line %d (-%d +%d)", lineOf(text, oldString), countLines(oldString), countLines(newString)), nil
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

// linesOf is the 1-based line each match starts on, at most ten of them.
func linesOf(content, match string) []int {
	var out []int
	at, line := 0, 1
	for len(out) < 10 {
		i := strings.Index(content[at:], match)
		if i < 0 {
			break
		}
		line += strings.Count(content[at:at+i], "\n")
		out = append(out, line)
		at += i + len(match)
		line += strings.Count(match, "\n")
	}
	return out
}

// joinLines writes line numbers as a list a sentence can carry.
func joinLines(lines []int) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = strconv.Itoa(l)
	}
	if len(parts) < 2 {
		return strings.Join(parts, "")
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// lineOf is the 1-based line the match starts on.
func lineOf(content, match string) int {
	return strings.Count(content[:strings.Index(content, match)], "\n") + 1
}
