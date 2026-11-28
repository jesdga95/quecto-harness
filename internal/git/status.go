package git

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jesdga95/quecto-harness/internal/diff"
)

// listLimit bounds the files one group lists, for a tree with a large folder
// nobody ignored. untrackedRead bounds the size of an untracked file read to
// show it, and fileLines the diff lines carried for one file.
const (
	listLimit     = 1000
	untrackedRead = 1 << 20
	fileLines     = 600
)

// What happened to a file on one side of the index.
const (
	StateChanged   = "changed"
	StateNew       = "new"
	StateDeleted   = "deleted"
	StateUntracked = "untracked"
	StateConflict  = "conflict"
)

// Hunk is a run of changed lines. The counts are git's own, so a hunk whose
// lines were cut still names the hunk in the file.
type Hunk struct {
	OldStart int         `json:"old_start"`
	OldLines int         `json:"old_lines"`
	NewStart int         `json:"new_start"`
	NewLines int         `json:"new_lines"`
	Section  string      `json:"section,omitempty"`
	Lines    []diff.Line `json:"lines"`
	// raw is the hunk as git printed it, header included, for applying it on
	// its own.
	raw string
}

// File is one path on one side of the index: staged, against HEAD, or
// unstaged, against the index.
type File struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Binary bool   `json:"binary,omitempty"`
	// Link is an untracked symbolic link, shown without reading what it
	// points to.
	Link    bool `json:"link,omitempty"`
	Added   int  `json:"added"`
	Removed int  `json:"removed"`
	// Omitted counts the diff lines past the limit that were not carried.
	Omitted int    `json:"omitted"`
	Hunks   []Hunk `json:"hunks"`
	// head is the file header git printed before the hunks.
	head string
}

// Status is the working tree as the panel shows it.
type Status struct {
	Branch   string `json:"branch"`
	Detached bool   `json:"detached"`
	// Head is the short hash of HEAD, empty on a branch with no commits.
	Head     string `json:"head"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
	Remotes  bool   `json:"remotes"`
	Staged   []File `json:"staged"`
	Unstaged []File `json:"unstaged"`
	// Hidden counts files past the list limit.
	Hidden int `json:"hidden"`
}

type entry struct {
	path string
	x, y byte
}

// Status reads the branch, the index and the working tree.
func (r *Repo) Status(ctx context.Context) (Status, error) {
	args := append([]string{"status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all", "--no-renames"}, r.scope()...)
	out, err := r.read(ctx, args...)
	if err != nil {
		return Status{}, err
	}
	st, entries := parseStatus(out)
	if remotes, err := r.read(ctx, "remote"); err == nil {
		st.Remotes = strings.TrimSpace(string(remotes)) != ""
	}

	staged, err := r.diffFiles(ctx, true, nil)
	if err != nil {
		return Status{}, err
	}
	unstaged, err := r.diffFiles(ctx, false, nil)
	if err != nil {
		return Status{}, err
	}

	st.Staged, st.Unstaged = []File{}, []File{}
	for _, e := range entries {
		path, ok := r.local(e.path)
		if !ok {
			continue
		}
		if e.x == 'u' {
			st.Unstaged = r.add(st.Unstaged, File{Path: path, State: StateConflict, Hunks: []Hunk{}}, &st.Hidden)
			continue
		}
		if e.x == '?' {
			st.Unstaged = r.add(st.Unstaged, r.untracked(path), &st.Hidden)
			continue
		}
		if e.x != '.' {
			st.Staged = r.add(st.Staged, pick(staged, path, e.x), &st.Hidden)
		}
		if e.y != '.' {
			st.Unstaged = r.add(st.Unstaged, pick(unstaged, path, e.y), &st.Hidden)
		}
	}
	return st, nil
}

func (r *Repo) add(list []File, f File, hidden *int) []File {
	if len(list) >= listLimit {
		*hidden++
		return list
	}
	return append(list, f)
}

// pick takes a path's diff and names its state from the status letter, which
// is the account git keeps of the path rather than what its diff looks like.
func pick(diffs map[string]File, path string, letter byte) File {
	f, ok := diffs[path]
	if ok {
		f = trim(f)
	} else {
		f = File{Path: path, Hunks: []Hunk{}}
	}
	switch letter {
	case 'A':
		f.State = StateNew
	case 'D':
		f.State = StateDeleted
	default:
		f.State = StateChanged
	}
	return f
}

// parseStatus reads porcelain v2 with -z. Renames are off, so every entry is
// one path.
func parseStatus(out []byte) (Status, []entry) {
	var st Status
	var entries []entry
	for _, rec := range bytes.Split(out, []byte{0}) {
		line := string(rec)
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			oid := strings.TrimPrefix(line, "# branch.oid ")
			if oid != "(initial)" && len(oid) >= 7 {
				st.Head = oid[:7]
			}
		case strings.HasPrefix(line, "# branch.head "):
			head := strings.TrimPrefix(line, "# branch.head ")
			if head == "(detached)" {
				st.Detached = true
			} else {
				st.Branch = head
			}
		case strings.HasPrefix(line, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			for _, f := range strings.Fields(strings.TrimPrefix(line, "# branch.ab ")) {
				n, _ := strconv.Atoi(f[1:])
				if f[0] == '+' {
					st.Ahead = n
				} else {
					st.Behind = n
				}
			}
		case strings.HasPrefix(line, "1 "):
			// 1 XY sub mH mI mW hH hI path
			if f := strings.SplitN(line, " ", 9); len(f) == 9 && len(f[1]) == 2 {
				entries = append(entries, entry{path: f[8], x: f[1][0], y: f[1][1]})
			}
		case strings.HasPrefix(line, "u "):
			// u XY sub m1 m2 m3 mW h1 h2 h3 path
			if f := strings.SplitN(line, " ", 11); len(f) == 11 {
				entries = append(entries, entry{path: f[10], x: 'u', y: 'u'})
			}
		case strings.HasPrefix(line, "? "):
			entries = append(entries, entry{path: strings.TrimPrefix(line, "? "), x: '?', y: '?'})
		}
	}
	return st, entries
}

// diffFiles runs git diff for one side of the index and parses it by path.
func (r *Repo) diffFiles(ctx context.Context, staged bool, paths []string) (map[string]File, error) {
	out, err := r.read(ctx, r.diffArgs(staged, paths)...)
	if err != nil {
		return nil, err
	}
	files := map[string]File{}
	for _, f := range parseDiff(out) {
		files[f.Path] = f
	}
	return files, nil
}

func (r *Repo) diffArgs(staged bool, paths []string) []string {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-renames", "-U3"}
	if staged {
		args = append(args, "--cached")
	}
	if r.Prefix != "" {
		args = append(args, "--relative="+r.Prefix)
	}
	if len(paths) > 0 {
		return append(args, r.fulls(paths)...)
	}
	return append(args, r.scope()...)
}

// untracked shows a file git does not know as wholly added.
func (r *Repo) untracked(path string) File {
	f := File{Path: path, State: StateUntracked, Hunks: []Hunk{}}
	abs, err := r.inside(path)
	if err != nil {
		return f
	}
	// Lstat, so a link is not followed: what it points to may be any file
	// on the machine, and its content would reach the panel and a draft.
	info, err := os.Lstat(abs)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		f.Link = true
		return f
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > untrackedRead {
		f.Binary = err == nil && info.Mode().IsRegular()
		return f
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return f
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		f.Binary = true
		return f
	}
	res := diff.Compute("", string(data), fileLines)
	f.Added, f.Omitted = res.Added, res.Omitted
	for _, h := range res.Hunks {
		f.Hunks = append(f.Hunks, Hunk{OldStart: 0, NewStart: h.NewStart, NewLines: len(h.Lines), Lines: h.Lines})
	}
	if len(f.Hunks) == 1 {
		f.Hunks[0].NewLines = res.Added
	}
	return f
}
