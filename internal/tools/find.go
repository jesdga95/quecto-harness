package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	// Caps on one search. A result that reaches one of them says so.
	findMaxMatches = 40
	findMaxFiles   = 60
	findMaxPerFile = 5
	// Matching sees the first 4 KB of a line. The result shows 200 bytes of it.
	findScanClip = 4 << 10
	findLineClip = 200
	findRelated  = 3
)

// skipDirs are passed over when the walk reaches them on its own, under the
// project or under a named path. A path that is one, or lies inside one,
// searches it whole.
var skipDirs = []string{".git", "node_modules", "dist", "build", "vendor", ".venv", "target"}

// Find lists files whose name matches and the lines in them that contain the
// text. Matching ignores case.
func (r *Runner) Find(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args FindArgs
	if err := decodeArgs(raw, &args); err != nil {
		return Result{}, fmt.Errorf("grep: %s", err)
	}
	if args.Name == "" && args.Text == "" {
		return Result{}, errors.New("grep needs name or text")
	}

	base := r.Root
	if strings.TrimSpace(args.Path) != "" {
		p, err := r.resolveRead(args.Path)
		if err != nil {
			return Result{}, err
		}
		if _, err := os.Stat(p); err != nil {
			return Result{}, fmt.Errorf("%s: no such file or folder", args.Path)
		}
		base = p
	}

	if args.Name != "" {
		if _, err := filepath.Match(args.Name, "probe"); err != nil {
			return Result{Output: fmt.Sprintf("%s is not a valid name pattern: %s\nuse * for any run of characters", args.Name, err)}, nil
		}
	}

	var re *regexp.Regexp
	if args.Text != "" {
		re = textMatcher(args.Text)
	}

	f := finder{runner: r, re: re, name: args.Name, skipping: !inSkipDir(r.rel(base))}
	if err := f.walk(ctx, base); err != nil {
		if ctx.Err() != nil {
			return Result{Output: "stopped"}, nil
		}
		return Result{}, fmt.Errorf("grep in %s: %s", r.rel(base), err)
	}
	out, truncated := Truncate(f.render(args), r.OutputLimit)
	return Result{Output: out, Truncated: truncated, Bytes: len(out)}, nil
}

// textMatcher matches a line that contains text as written or, when text
// compiles as a regular expression, matches it, so punctuation in plain text
// never causes a miss.
func textMatcher(text string) *regexp.Regexp {
	expr := regexp.QuoteMeta(text)
	if _, err := regexp.Compile(text); err == nil {
		expr = "(?:" + expr + ")|(?:" + text + ")"
	}
	return regexp.MustCompile("(?i)" + expr)
}

// inSkipDir reports whether a project-relative path is or lies inside a
// skipped folder.
func inSkipDir(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if slices.Contains(skipDirs, part) {
			return true
		}
	}
	return false
}

// finder holds the state of one walk.
type finder struct {
	runner   *Runner
	re       *regexp.Regexp
	name     string
	skipping bool

	lines   []string
	files   []string
	matches int
	capped  bool
	skipped []string
}

func (f *finder) walk(ctx context.Context, base string) error {
	return filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// An unreadable entry is skipped, not an error.
			return nil
		}
		if d.IsDir() {
			if !f.runner.reachable(p) {
				return fs.SkipDir
			}
			if p != base && f.skipping && slices.Contains(skipDirs, d.Name()) {
				if !slices.Contains(f.skipped, d.Name()) {
					f.skipped = append(f.skipped, d.Name())
				}
				return fs.SkipDir
			}
			return nil
		}
		// The walk does not follow a link, but opening one does, so a link is
		// taken only when what it points at is inside the project.
		if d.Type()&fs.ModeSymlink != 0 {
			if _, err := f.runner.resolve(p); err != nil {
				return nil
			}
		}
		if f.name != "" {
			if ok, _ := filepath.Match(f.name, d.Name()); !ok {
				return nil
			}
		}
		if f.re == nil {
			f.files = append(f.files, f.runner.rel(p))
			if len(f.files) >= findMaxFiles {
				f.capped = true
				return fs.SkipAll
			}
			return nil
		}
		return f.scan(p)
	})
}

// scan reads one file and keeps the lines that match, up to the per-file cap.
func (f *finder) scan(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	sniff := make([]byte, 8192)
	n, err := io.ReadFull(file, sniff)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil
	}
	if looksBinary(sniff[:n]) {
		return nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil
	}

	rd := bufio.NewReaderSize(file, 64<<10)
	rel := f.runner.rel(path)
	lineNo, inFile := 0, 0
	for {
		line, _, ok, err := readLine(rd, findScanClip)
		if err != nil || !ok {
			break
		}
		lineNo++
		if !f.re.MatchString(line) {
			continue
		}
		if inFile == 0 {
			f.files = append(f.files, rel)
		}
		inFile++
		f.matches++
		f.lines = append(f.lines, fmt.Sprintf("%s:%d: %s", rel, lineNo, clipLine(line)))
		if f.matches >= findMaxMatches {
			f.capped = true
			return fs.SkipAll
		}
		if inFile >= findMaxPerFile {
			f.lines = append(f.lines, fmt.Sprintf("%s: more matches in this file", rel))
			return nil
		}
	}
	return nil
}

// render writes the matches, the counts, the folders passed over and the
// related files.
func (f *finder) render(args FindArgs) string {
	var b strings.Builder
	body := f.lines
	if f.re == nil {
		body = f.files
	}
	for _, l := range body {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if len(body) > 0 {
		b.WriteString("\n")
	}

	switch {
	case f.re == nil && len(f.files) == 0:
		fmt.Fprintf(&b, "no file named %s\n", args.Name)
	case f.re == nil:
		fmt.Fprintf(&b, "%s\n", count(len(f.files), "file", "files"))
	case f.matches == 0 && f.name != "":
		fmt.Fprintf(&b, "no line matching %s in a file named %s\n", args.Text, args.Name)
	case f.matches == 0:
		fmt.Fprintf(&b, "no match for %s\n", args.Text)
	default:
		fmt.Fprintf(&b, "%s in %s\n", count(f.matches, "match", "matches"), count(len(f.files), "file", "files"))
	}
	if f.capped {
		b.WriteString("stopped at the limit, narrow the search with name or path\n")
	}
	if len(f.skipped) > 0 {
		fmt.Fprintf(&b, "passed over %s: give one as path to search inside it\n", strings.Join(f.skipped, ", "))
	}
	if rel := f.related(); len(rel) > 0 {
		fmt.Fprintf(&b, "related files: %s\n", strings.Join(rel, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// related names existing files in the same folder whose stem matches one of
// the files that matched. It runs only for a search that fell in one or two
// files.
func (f *finder) related() []string {
	if f.re == nil || len(f.files) == 0 || len(f.files) > 2 {
		return nil
	}
	var out []string
	for _, hit := range f.files {
		dir := filepath.Dir(filepath.Join(f.runner.Root, hit))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		stem := stemOf(filepath.Base(hit))
		for _, e := range entries {
			if e.IsDir() || stemOf(e.Name()) != stem {
				continue
			}
			rel := f.runner.rel(filepath.Join(dir, e.Name()))
			if rel == hit || slices.Contains(f.files, rel) || slices.Contains(out, rel) {
				continue
			}
			if out = append(out, rel); len(out) >= findRelated {
				return out
			}
		}
	}
	return out
}

// stemOf is the name up to the first dot, so Chat.module.css stems to Chat.
func stemOf(name string) string {
	if i := strings.Index(name, "."); i > 0 {
		return name[:i]
	}
	return name
}

// clipLine trims indentation and keeps the head of a long line.
func clipLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= findLineClip {
		return s
	}
	cut := findLineClip
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " ..."
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
