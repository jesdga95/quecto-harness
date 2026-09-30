// Package diff turns two versions of a file into hunks a browser can draw.
// The edit script comes from go-udiff, a port of the diff the Go tools use;
// what is here is the shaping: a cap on how much is sent and honest counts
// for what the cap left off.
package diff

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// contextLines is how many unchanged lines surround a change. Three is what
// every unified diff uses, so a hunk reads the way a patch reads.
const contextLines = 3

// sectionWidth bounds the heading a hunk carries, which is one line of code.
const sectionWidth = 80

// Kind is what happened to one line.
type Kind string

const (
	Context Kind = "context"
	Added   Kind = "added"
	Removed Kind = "removed"
)

// Line is one row of a hunk. Text carries no sign; the sign is the Kind.
type Line struct {
	Kind Kind   `json:"kind"`
	Text string `json:"text"`
}

// Hunk is a run of changed lines with their context, at a position in each
// version of the file. Section is the nearest line above it that starts at
// the left margin, as git's hunk headers show, and is empty when none is.
type Hunk struct {
	OldStart int    `json:"old_start"`
	NewStart int    `json:"new_start"`
	Section  string `json:"section,omitempty"`
	Lines    []Line `json:"lines"`
}

// Result is the whole comparison. Added and Removed count the entire change;
// Omitted says how many lines past maxLines were not included, so the counts
// stay true when the body is cut.
type Result struct {
	Hunks     []Hunk `json:"hunks"`
	Added     int    `json:"added"`
	Removed   int    `json:"removed"`
	Omitted   int    `json:"omitted"`
	Identical bool   `json:"identical"`
}

// Compute compares two versions of a file. maxLines bounds the lines carried
// in the hunks; zero or less means no bound.
func Compute(old, new string, maxLines int) Result {
	if old == new {
		return Result{Hunks: []Hunk{}, Identical: true}
	}
	edits := udiff.Lines(old, new)
	u, err := udiff.ToUnifiedDiff("old", "new", old, edits, contextLines)
	if err != nil {
		// The edits came from this same content, so a failure here means the
		// two versions disagree about their own bytes. Report the change
		// without a body rather than losing the file from the list.
		return Result{Hunks: []Hunk{}}
	}

	res := Result{Hunks: []Hunk{}}
	emitted := 0
	oldLines := strings.Split(old, "\n")
	for _, h := range u.Hunks {
		hunk := Hunk{OldStart: h.FromLine, NewStart: h.ToLine, Section: section(oldLines, h.FromLine), Lines: []Line{}}
		for _, l := range h.Lines {
			kind := Context
			switch l.Kind {
			case udiff.Insert:
				kind = Added
				res.Added++
			case udiff.Delete:
				kind = Removed
				res.Removed++
			}
			if maxLines > 0 && emitted >= maxLines {
				res.Omitted++
				continue
			}
			hunk.Lines = append(hunk.Lines, Line{Kind: kind, Text: trimEOL(l.Content)})
			emitted++
		}
		if len(hunk.Lines) > 0 {
			res.Hunks = append(res.Hunks, hunk)
		}
	}
	return res
}

// section finds the heading for a hunk starting at line from, 1-based: the
// closest line before it that begins with a letter, an underscore or a dollar
// sign, the rule git uses when a language gives it no better one.
func section(lines []string, from int) string {
	for i := min(from-2, len(lines)-1); i >= 0; i-- {
		line := trimEOL(lines[i])
		if line == "" {
			continue
		}
		c := line[0]
		if c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			line = strings.TrimRight(line, " \t")
			if len(line) > sectionWidth {
				line = line[:sectionWidth]
			}
			return line
		}
	}
	return ""
}

// trimEOL drops the line terminator go-udiff keeps on each line, since the
// rows are drawn one per element rather than joined back into a patch.
func trimEOL(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\n' {
		s = s[:n-1]
	}
	if n := len(s); n > 0 && s[n-1] == '\r' {
		s = s[:n-1]
	}
	return s
}
