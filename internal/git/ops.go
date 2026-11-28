package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Stage adds paths to the index, deletions included.
func (r *Repo) Stage(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := r.write(ctx, nil, append([]string{"add", "-A"}, r.fulls(paths)...)...)
	return err
}

// Unstage takes paths out of the index and leaves the working tree alone. A
// branch with no commits has no HEAD to reset to, so the paths are removed
// from the index instead.
func (r *Repo) Unstage(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	if !r.hasHead(ctx) {
		_, err := r.write(ctx, nil, append([]string{"rm", "--cached", "-r", "-q", "--ignore-unmatch"}, r.fulls(paths)...)...)
		return err
	}
	_, err := r.write(ctx, nil, append([]string{"reset", "-q", "HEAD"}, r.fulls(paths)...)...)
	return err
}

// Discard puts unstaged changes back to the index. A path git does not track
// is deleted, since there is nothing to put it back to.
func (r *Repo) Discard(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	untracked, err := r.untrackedSet(ctx, paths)
	if err != nil {
		return err
	}
	var tracked []string
	for _, p := range paths {
		if !untracked[p] {
			tracked = append(tracked, p)
			continue
		}
		abs, err := r.inside(p)
		if err != nil {
			return err
		}
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", p, err)
		}
	}
	if len(tracked) == 0 {
		return nil
	}
	_, err = r.write(ctx, nil, append([]string{"checkout", "-q"}, r.fulls(tracked)...)...)
	return err
}

func (r *Repo) untrackedSet(ctx context.Context, paths []string) (map[string]bool, error) {
	out, err := r.read(ctx, append([]string{"ls-files", "-z", "--others", "--exclude-standard"}, r.fulls(paths)...)...)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range strings.Split(string(out), "\x00") {
		if local, ok := r.local(p); ok && p != "" {
			set[local] = true
		}
	}
	return set, nil
}

// HunkRef names a hunk by git's own header numbers, which change when the
// file does, so a hunk that moved since it was shown is not applied.
type HunkRef struct {
	Path     string `json:"path"`
	Staged   bool   `json:"staged"`
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
}

// ErrHunkMoved is returned when the hunk asked for is no longer in the diff.
var ErrHunkMoved = errors.New("that hunk has changed since it was shown")

// Hunk actions.
const (
	HunkStage   = "stage"
	HunkUnstage = "unstage"
	HunkDiscard = "discard"
)

// ApplyHunk stages, unstages or discards one hunk. A file git does not track
// has no diff to cut a hunk from, so the whole file is staged or deleted.
func (r *Repo) ApplyHunk(ctx context.Context, ref HunkRef, action string) error {
	if !ref.Staged {
		untracked, err := r.untrackedSet(ctx, []string{ref.Path})
		if err != nil {
			return err
		}
		if untracked[ref.Path] {
			if action == HunkDiscard {
				return r.Discard(ctx, []string{ref.Path})
			}
			return r.Stage(ctx, []string{ref.Path})
		}
	}
	files, err := r.diffFiles(ctx, ref.Staged, []string{ref.Path})
	if err != nil {
		return err
	}
	f, ok := files[ref.Path]
	if !ok {
		return ErrHunkMoved
	}
	var patch string
	for _, h := range f.Hunks {
		if h.OldStart == ref.OldStart && h.OldLines == ref.OldLines && h.NewStart == ref.NewStart && h.NewLines == ref.NewLines {
			patch = f.head + h.raw
			break
		}
	}
	if patch == "" {
		return ErrHunkMoved
	}
	args := []string{"apply", "--whitespace=nowarn"}
	if r.Prefix != "" {
		args = append(args, "--directory="+strings.TrimSuffix(r.Prefix, "/"))
	}
	switch action {
	case HunkStage:
		args = append(args, "--cached")
	case HunkUnstage:
		args = append(args, "--cached", "--reverse")
	case HunkDiscard:
		args = append(args, "--reverse")
	default:
		return fmt.Errorf("unknown hunk action %q", action)
	}
	_, err = r.write(ctx, []byte(patch), append(args, "-")...)
	return err
}

// CommitRequest is what the commit box sends.
type CommitRequest struct {
	Message string
	Amend   bool
	// All stages every change in the project first, for a commit made with
	// nothing staged.
	All bool
}

// Commit records the index, and answers with the new commit's short hash.
func (r *Repo) Commit(ctx context.Context, req CommitRequest) (string, error) {
	if req.All {
		if _, err := r.write(ctx, nil, append([]string{"add", "-A"}, r.allScope()...)...); err != nil {
			return "", err
		}
	}
	args := []string{"commit", "-q"}
	if req.Amend {
		args = append(args, "--amend")
	}
	var stdin []byte
	if strings.TrimSpace(req.Message) == "" {
		if !req.Amend {
			return "", errors.New("a commit needs a message")
		}
		args = append(args, "--no-edit")
	} else {
		args = append(args, "-F", "-")
		stdin = []byte(req.Message)
	}
	if _, err := r.write(ctx, stdin, args...); err != nil {
		return "", err
	}
	out, err := r.read(ctx, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *Repo) allScope() []string {
	if r.Prefix == "" {
		return []string{"--", "."}
	}
	return r.scope()
}

// Undo moves the branch back one commit and keeps that commit's changes
// staged. The first commit on a branch has no parent, so the branch is
// emptied instead.
func (r *Repo) Undo(ctx context.Context) error {
	if _, err := r.read(ctx, "rev-parse", "--verify", "-q", "HEAD~1"); err != nil {
		_, err := r.write(ctx, nil, "update-ref", "-d", "HEAD")
		return err
	}
	_, err := r.write(ctx, nil, "reset", "--soft", "-q", "HEAD~1")
	return err
}

func (r *Repo) hasHead(ctx context.Context) bool {
	_, err := r.read(ctx, "rev-parse", "--verify", "-q", "HEAD")
	return err == nil
}

// Fetch reads every remote.
func (r *Repo) Fetch(ctx context.Context) error {
	_, err := r.remote(ctx, "fetch", "--all", "--prune", "-q")
	return err
}

// Pull brings the upstream in when it fast forwards. A pull that would merge
// is left for the user, since a merge needs decisions this cannot make.
func (r *Repo) Pull(ctx context.Context) error {
	_, err := r.remote(ctx, "pull", "--ff-only", "-q")
	return err
}

// Push sends the branch to its upstream, or to the one remote with an
// upstream set when the branch has none yet.
func (r *Repo) Push(ctx context.Context) error {
	if _, err := r.read(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}"); err == nil {
		_, err := r.remote(ctx, "push", "-q")
		return err
	}
	out, err := r.read(ctx, "remote")
	if err != nil {
		return err
	}
	remotes := strings.Fields(string(out))
	name := ""
	for _, rm := range remotes {
		if rm == "origin" {
			name = rm
		}
	}
	if name == "" && len(remotes) == 1 {
		name = remotes[0]
	}
	if name == "" {
		if len(remotes) == 0 {
			return errors.New("this repository has no remote to push to")
		}
		return errors.New("this branch has no upstream and there is more than one remote to choose from")
	}
	_, err = r.remote(ctx, "push", "-q", "-u", name, "HEAD")
	return err
}

// Stash puts every change, untracked files included, on the stash.
func (r *Repo) Stash(ctx context.Context, message string) error {
	args := []string{"stash", "push", "-q", "--include-untracked"}
	if message != "" {
		args = append(args, "-m", message)
	}
	_, err := r.write(ctx, nil, args...)
	return err
}

// CheckoutRequest is a branch switch. Create makes the branch, from Start when
// given. Stash puts uncommitted changes away first rather than carrying them.
type CheckoutRequest struct {
	Branch string
	Create bool
	Start  string
	Stash  bool
}

// Checkout switches branch. A remote branch with no local one of that name is
// checked out as a new local branch tracking it.
func (r *Repo) Checkout(ctx context.Context, req CheckoutRequest) error {
	name := strings.TrimSpace(req.Branch)
	if name == "" {
		return errors.New("a branch needs a name")
	}
	if req.Stash {
		if err := r.Stash(ctx, "Before switching to "+name); err != nil {
			return err
		}
	}
	if req.Create {
		if _, err := r.read(ctx, "check-ref-format", "--branch", name); err != nil {
			return fmt.Errorf("%q is not a valid branch name", name)
		}
		args := []string{"switch", "-q", "-c", name}
		if req.Start != "" {
			args = append(args, req.Start)
		}
		_, err := r.write(ctx, nil, args...)
		return err
	}
	if _, err := r.read(ctx, "rev-parse", "--verify", "-q", "refs/heads/"+name); err == nil {
		_, err := r.write(ctx, nil, "switch", "-q", name)
		return err
	}
	if _, err := r.read(ctx, "rev-parse", "--verify", "-q", "refs/remotes/"+name); err == nil {
		_, local, _ := strings.Cut(name, "/")
		if _, err := r.read(ctx, "rev-parse", "--verify", "-q", "refs/heads/"+local); err == nil {
			_, err := r.write(ctx, nil, "switch", "-q", local)
			return err
		}
		_, err := r.write(ctx, nil, "switch", "-q", "--track", name)
		return err
	}
	return fmt.Errorf("there is no branch named %s", name)
}

// Branch is one local branch.
type Branch struct {
	Name     string `json:"name"`
	Current  bool   `json:"current"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead"`
	Behind   int    `json:"behind"`
}

// Branches lists local branches, most recently committed first, and the
// remote branches by name.
func (r *Repo) Branches(ctx context.Context) ([]Branch, []string, error) {
	out, err := r.read(ctx, "for-each-ref", "--sort=-committerdate",
		"--format=%(refname:short)%00%(HEAD)%00%(upstream:short)%00%(upstream:track,nobracket)", "refs/heads")
	if err != nil {
		return nil, nil, err
	}
	local := []Branch{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) < 4 || f[0] == "" {
			continue
		}
		b := Branch{Name: f[0], Current: f[1] == "*", Upstream: f[2]}
		for _, part := range strings.Split(f[3], ",") {
			part = strings.TrimSpace(part)
			if n, ok := strings.CutPrefix(part, "ahead "); ok {
				b.Ahead, _ = strconv.Atoi(n)
			} else if n, ok := strings.CutPrefix(part, "behind "); ok {
				b.Behind, _ = strconv.Atoi(n)
			}
		}
		local = append(local, b)
	}
	out, err = r.read(ctx, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)%00%(symref)", "refs/remotes")
	if err != nil {
		return nil, nil, err
	}
	remote := []string{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		name, symref, _ := strings.Cut(line, "\x00")
		// origin/HEAD points at another remote branch and is not one itself.
		if name == "" || symref != "" || !strings.Contains(name, "/") {
			continue
		}
		remote = append(remote, name)
	}
	return local, remote, nil
}

// Commit is one entry in the history.
type Commit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
	Body    string `json:"body,omitempty"`
	Author  string `json:"author"`
	// When is seconds since the epoch.
	When   int64 `json:"when"`
	Pushed bool  `json:"pushed"`
}

// Log lists the newest commits on HEAD. Pushed means a remote branch holds the
// commit, so moving the branch past it rewrites what others may have.
func (r *Repo) Log(ctx context.Context, limit int) ([]Commit, error) {
	if !r.hasHead(ctx) {
		return []Commit{}, nil
	}
	out, err := r.read(ctx, "log", "-n", strconv.Itoa(limit), "--format=%H%x00%h%x00%s%x00%b%x00%an%x00%at%x1e")
	if err != nil {
		return nil, err
	}
	unpushed := map[string]bool{}
	if pending, err := r.read(ctx, "rev-list", "-n", strconv.Itoa(limit), "HEAD", "--not", "--remotes"); err == nil {
		for _, h := range strings.Fields(string(pending)) {
			unpushed[h] = true
		}
	}
	commits := []Commit{}
	for _, rec := range strings.Split(string(out), "\x1e") {
		f := strings.Split(strings.TrimLeft(rec, "\n"), "\x00")
		if len(f) < 6 || f[0] == "" {
			continue
		}
		when, _ := strconv.ParseInt(strings.TrimSpace(f[5]), 10, 64)
		commits = append(commits, Commit{
			Hash: f[0], Short: f[1], Subject: f[2], Body: strings.TrimSpace(f[3]),
			Author: f[4], When: when, Pushed: !unpushed[f[0]],
		})
	}
	return commits, nil
}

// CommitFile is one path a commit changed, with its line counts. Binary files
// carry no counts.
type CommitFile struct {
	Path    string `json:"path"`
	State   string `json:"state"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
}

// Show lists what one commit changed inside the project.
func (r *Repo) Show(ctx context.Context, hash string) ([]CommitFile, error) {
	base := []string{"diff-tree", "--no-commit-id", "-r", "--root", "--no-renames", "-z"}
	if r.Prefix != "" {
		base = append(base, "--relative="+r.Prefix)
	}
	states, err := r.read(ctx, append(append(base, "--name-status"), hash)...)
	if err != nil {
		return nil, err
	}
	counts, err := r.read(ctx, append(append(base, "--numstat"), hash)...)
	if err != nil {
		return nil, err
	}
	files := []CommitFile{}
	index := map[string]int{}
	f := strings.Split(strings.TrimRight(string(states), "\x00"), "\x00")
	for i := 0; i+1 < len(f); i += 2 {
		state := StateChanged
		switch f[i] {
		case "A":
			state = StateNew
		case "D":
			state = StateDeleted
		}
		index[f[i+1]] = len(files)
		files = append(files, CommitFile{Path: f[i+1], State: state})
	}
	for _, rec := range strings.Split(strings.TrimRight(string(counts), "\x00"), "\x00") {
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		i, ok := index[parts[2]]
		if !ok {
			continue
		}
		if parts[0] == "-" {
			files[i].Binary = true
			continue
		}
		files[i].Added, _ = strconv.Atoi(parts[0])
		files[i].Removed, _ = strconv.Atoi(parts[1])
	}
	return files, nil
}

// Patch is the project's uncommitted changes as one unified diff: what is
// staged, then what is not, then untracked files as new.
func (r *Repo) Patch(ctx context.Context, stagedOnly bool) (string, error) {
	var b strings.Builder
	staged, err := r.read(ctx, r.diffArgs(true, nil)...)
	if err != nil {
		return "", err
	}
	b.Write(staged)
	if stagedOnly {
		return b.String(), nil
	}
	unstaged, err := r.read(ctx, r.diffArgs(false, nil)...)
	if err != nil {
		return "", err
	}
	b.Write(unstaged)
	others, err := r.read(ctx, append([]string{"ls-files", "-z", "--others", "--exclude-standard"}, r.scope()...)...)
	if err != nil {
		return "", err
	}
	for _, p := range strings.Split(string(others), "\x00") {
		local, ok := r.local(p)
		if !ok || p == "" {
			continue
		}
		f := r.untracked(local)
		a, bb := quotePath("a/"+local), quotePath("b/"+local)
		// A link is told as git stores it, its target as the content, and
		// what it points to is never read.
		if f.Link {
			abs, err := r.inside(local)
			if err != nil {
				continue
			}
			target, err := os.Readlink(abs)
			if err != nil {
				continue
			}
			// A final newline ends the last line rather than starting another,
			// and git leaves a count of one out of the hunk header.
			body, ended := strings.CutSuffix(target, "\n")
			lines := strings.Split(body, "\n")
			count := "1"
			if len(lines) > 1 {
				count += "," + strconv.Itoa(len(lines))
			}
			fmt.Fprintf(&b, "diff --git %s %s\nnew file mode 120000\n--- /dev/null\n+++ %s\n@@ -0,0 +%s @@\n", a, bb, bb, count)
			for _, l := range lines {
				b.WriteString("+" + l + "\n")
			}
			if !ended {
				b.WriteString("\\ No newline at end of file\n")
			}
			continue
		}
		if f.Binary {
			fmt.Fprintf(&b, "diff --git %s %s\nnew file mode 100644\nBinary files /dev/null and %s differ\n", a, bb, bb)
			continue
		}
		fmt.Fprintf(&b, "diff --git %s %s\nnew file mode 100644\n--- /dev/null\n+++ %s\n@@ -0,0 +1,%d @@\n", a, bb, bb, f.Added)
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				b.WriteString("+" + l.Text + "\n")
			}
		}
	}
	return b.String(), nil
}

// quotePath writes a path in a patch header the way git does: as is, or in
// double quotes with C escapes when it holds a quote, a backslash or a control
// character.
func quotePath(p string) string {
	if !strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '"' || r == '\\' }) {
		return p
	}
	var q strings.Builder
	q.WriteByte('"')
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '"' || c == '\\':
			q.WriteByte('\\')
			q.WriteByte(c)
		case c == '\n':
			q.WriteString(`\n`)
		case c == '\t':
			q.WriteString(`\t`)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&q, `\%03o`, c)
		default:
			q.WriteByte(c)
		}
	}
	q.WriteByte('"')
	return q.String()
}

// Subjects lists the subjects of the newest commits, for matching a drafted
// message to the repository's own style.
func (r *Repo) Subjects(ctx context.Context, n int) []string {
	if !r.hasHead(ctx) {
		return nil
	}
	out, err := r.read(ctx, "log", "-n", strconv.Itoa(n), "--format=%s")
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

// HasStaged reports whether anything in the project is staged.
func (r *Repo) HasStaged(ctx context.Context) bool {
	args := append([]string{"diff", "--cached", "--quiet"}, r.scope()...)
	_, err := r.read(ctx, args...)
	return err != nil
}
