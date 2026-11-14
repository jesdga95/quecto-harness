package git

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"

	"github.com/jesdga95/quecto-harness/internal/diff"
)

// parseDiff reads the output of git diff into files with every hunk whole.
// Combined diffs, which git prints for a path in conflict, are skipped: the
// path is listed from the status instead.
func parseDiff(out []byte) []File {
	var files []File
	var cur *File
	var hunk *Hunk
	var head strings.Builder
	var raw strings.Builder
	skip := false

	endHunk := func() {
		if cur != nil && hunk != nil {
			hunk.raw = raw.String()
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
		raw.Reset()
	}
	endFile := func() {
		endHunk()
		if cur != nil {
			cur.head = head.String()
			files = append(files, *cur)
		}
		cur = nil
		head.Reset()
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			endFile()
			skip = false
			cur = &File{Path: gitPath(strings.TrimPrefix(line, "diff --git ")), Hunks: []Hunk{}}
			head.WriteString(line + "\n")
			continue
		case strings.HasPrefix(line, "diff --cc ") || strings.HasPrefix(line, "diff --combined "):
			endFile()
			skip = true
			continue
		}
		if skip || cur == nil {
			continue
		}
		if hunk == nil && !strings.HasPrefix(line, "@@ ") {
			switch {
			case strings.HasPrefix(line, "+++ "):
				if p := strings.TrimPrefix(line, "+++ "); p != "/dev/null" {
					cur.Path = strip(unquote(p))
				}
			case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
				cur.Binary = true
			}
			head.WriteString(line + "\n")
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			endHunk()
			h, ok := parseHunkHeader(line)
			if !ok {
				continue
			}
			hunk = &h
			raw.WriteString(line + "\n")
			continue
		}
		raw.WriteString(line + "\n")
		if line == "" {
			// A blank context line loses its leading space in some pipelines.
			hunk.Lines = append(hunk.Lines, diff.Line{Kind: diff.Context})
			continue
		}
		switch line[0] {
		case '+':
			hunk.Lines = append(hunk.Lines, diff.Line{Kind: diff.Added, Text: line[1:]})
			cur.Added++
		case '-':
			hunk.Lines = append(hunk.Lines, diff.Line{Kind: diff.Removed, Text: line[1:]})
			cur.Removed++
		case ' ':
			hunk.Lines = append(hunk.Lines, diff.Line{Kind: diff.Context, Text: line[1:]})
		}
	}
	endFile()
	return files
}

// parseHunkHeader reads "@@ -a,b +c,d @@ section". A count left out is one.
func parseHunkHeader(line string) (Hunk, bool) {
	rest := strings.TrimPrefix(line, "@@ ")
	end := strings.Index(rest, " @@")
	if end < 0 {
		return Hunk{}, false
	}
	ranges := strings.Fields(rest[:end])
	if len(ranges) != 2 || ranges[0][0] != '-' || ranges[1][0] != '+' {
		return Hunk{}, false
	}
	h := Hunk{Lines: []diff.Line{}}
	var ok1, ok2 bool
	h.OldStart, h.OldLines, ok1 = parseRange(ranges[0][1:])
	h.NewStart, h.NewLines, ok2 = parseRange(ranges[1][1:])
	if !ok1 || !ok2 {
		return Hunk{}, false
	}
	h.Section = strings.TrimSpace(rest[end+3:])
	return h, true
}

func parseRange(s string) (start, count int, ok bool) {
	a, b, has := strings.Cut(s, ",")
	start, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, false
	}
	count = 1
	if has {
		if count, err = strconv.Atoi(b); err != nil {
			return 0, 0, false
		}
	}
	return start, count, true
}

// gitPath takes the path out of "a/x b/x". Renames are off, so both halves
// name the same path and the line splits in the middle.
func gitPath(s string) string {
	if strings.HasPrefix(s, `"`) {
		if i := strings.Index(s, `" `); i > 0 {
			return strip(unquote(s[:i+1]))
		}
	}
	if n := len(s); n >= 5 && (n-5)%2 == 0 {
		half := (n - 5) / 2
		if s[:2] == "a/" && s[2+half:2+half+3] == " b/" {
			return s[2 : 2+half]
		}
	}
	return strip(s)
}

// unquote reads a path git wrote in C-style quotes because it holds a
// character a line cannot.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return strings.Trim(s, `"`)
}

func strip(p string) string {
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

// trim cuts a file's hunks to the line budget for display. The counts stay
// true and Omitted says how many lines were left off.
func trim(f File) File {
	budget := fileLines
	kept := make([]Hunk, 0, len(f.Hunks))
	for _, h := range f.Hunks {
		if budget <= 0 {
			f.Omitted += len(h.Lines)
			continue
		}
		if len(h.Lines) > budget {
			f.Omitted += len(h.Lines) - budget
			h.Lines = h.Lines[:budget]
		}
		budget -= len(h.Lines)
		kept = append(kept, h)
	}
	f.Hunks = kept
	return f
}
