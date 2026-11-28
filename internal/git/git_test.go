package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jesdga95/quecto-harness/internal/diff"
)

// newRepo makes a repository with one commit, isolated from the machine's own
// git configuration so signing or hooks set there cannot reach the test.
func newRepo(t *testing.T) string {
	t.Helper()
	if !Available() {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.name", "Test")
	gitIn(t, dir, "config", "user.email", "test@example.com")
	return dir
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func put(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, dir string) *Repo {
	t.Helper()
	r, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func status(t *testing.T, r *Repo) Status {
	t.Helper()
	st, err := r.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func find(files []File, path string) (File, bool) {
	for _, f := range files {
		if f.Path == path {
			return f, true
		}
	}
	return File{}, false
}

// twenty is a file long enough that changes at either end are two hunks.
func twenty(edit map[int]string) string {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		if s, ok := edit[i]; ok {
			b.WriteString(s + "\n")
			continue
		}
		b.WriteString("line " + string(rune('a'+i-1)) + "\n")
	}
	return b.String()
}

func TestOpenOutsideARepository(t *testing.T) {
	if !Available() {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	_, err := Open(context.Background(), t.TempDir())
	if !errors.Is(err, ErrNotRepo) {
		t.Fatalf("err = %v, want ErrNotRepo", err)
	}
}

func TestStatusGroupsEachSide(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "a.txt", twenty(nil))
	put(t, dir, "gone.txt", "bye\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "First")

	put(t, dir, "a.txt", twenty(map[int]string{2: "TWO", 19: "NINETEEN"}))
	put(t, dir, "new.txt", "fresh\nfile\n")
	put(t, dir, "added.txt", "staged new\n")
	gitIn(t, dir, "add", "added.txt")
	os.Remove(filepath.Join(dir, "gone.txt"))

	r := open(t, dir)
	st := status(t, r)
	if st.Branch != "main" || st.Head == "" || st.Detached {
		t.Fatalf("branch = %q head = %q detached = %v", st.Branch, st.Head, st.Detached)
	}
	if f, ok := find(st.Staged, "added.txt"); !ok || f.State != StateNew || f.Added != 1 {
		t.Errorf("staged added.txt = %+v %v", f, ok)
	}
	a, ok := find(st.Unstaged, "a.txt")
	if !ok || a.State != StateChanged || a.Added != 2 || a.Removed != 2 || len(a.Hunks) != 2 {
		t.Fatalf("unstaged a.txt = %+v", a)
	}
	if f, ok := find(st.Unstaged, "new.txt"); !ok || f.State != StateUntracked || f.Added != 2 {
		t.Errorf("untracked new.txt = %+v %v", f, ok)
	}
	if f, ok := find(st.Unstaged, "gone.txt"); !ok || f.State != StateDeleted || f.Removed != 1 {
		t.Errorf("deleted gone.txt = %+v %v", f, ok)
	}

	// Staging one hunk splits the file across both sides.
	h := a.Hunks[0]
	ref := HunkRef{Path: "a.txt", OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines}
	if err := r.ApplyHunk(context.Background(), ref, HunkStage); err != nil {
		t.Fatal(err)
	}
	st = status(t, r)
	staged, _ := find(st.Staged, "a.txt")
	unstaged, _ := find(st.Unstaged, "a.txt")
	if len(staged.Hunks) != 1 || len(unstaged.Hunks) != 1 {
		t.Fatalf("after staging one hunk: staged %d hunks, unstaged %d", len(staged.Hunks), len(unstaged.Hunks))
	}
	if text := hunkText(staged.Hunks[0]); !strings.Contains(text, "TWO") || strings.Contains(text, "NINETEEN") {
		t.Errorf("the staged hunk is not the first change: %+v", staged.Hunks[0])
	}

	// Unstaging it puts the file back on one side.
	sh := staged.Hunks[0]
	back := HunkRef{Path: "a.txt", Staged: true, OldStart: sh.OldStart, OldLines: sh.OldLines, NewStart: sh.NewStart, NewLines: sh.NewLines}
	if err := r.ApplyHunk(context.Background(), back, HunkUnstage); err != nil {
		t.Fatal(err)
	}
	st = status(t, r)
	if _, ok := find(st.Staged, "a.txt"); ok {
		t.Error("a.txt is still staged after unstaging its only hunk")
	}

	// Discarding the second hunk drops that change from the file on disk.
	a, _ = find(st.Unstaged, "a.txt")
	h = a.Hunks[1]
	ref = HunkRef{Path: "a.txt", OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines}
	if err := r.ApplyHunk(context.Background(), ref, HunkDiscard); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != twenty(map[int]string{2: "TWO"}) {
		t.Errorf("a.txt after discarding the second hunk:\n%s", data)
	}
}

func hunkText(h Hunk) string {
	var b strings.Builder
	for _, l := range h.Lines {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func TestHunkThatMovedIsRefused(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "a.txt", twenty(nil))
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "First")
	put(t, dir, "a.txt", twenty(map[int]string{5: "FIVE"}))

	r := open(t, dir)
	err := r.ApplyHunk(context.Background(), HunkRef{Path: "a.txt", OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 3}, HunkStage)
	if !errors.Is(err, ErrHunkMoved) {
		t.Fatalf("err = %v, want ErrHunkMoved", err)
	}
}

func TestDiscardDeletesAnUntrackedFile(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "keep.txt", "one\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "First")
	put(t, dir, "keep.txt", "two\n")
	put(t, dir, "scratch.txt", "temp\n")

	r := open(t, dir)
	if err := r.Discard(context.Background(), []string{"keep.txt", "scratch.txt"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "keep.txt")); string(data) != "one\n" {
		t.Errorf("keep.txt = %q, want it restored", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "scratch.txt")); !os.IsNotExist(err) {
		t.Error("the untracked file was not deleted")
	}
	if err := r.Discard(context.Background(), []string{"../outside.txt"}); err == nil {
		t.Error("a path outside the project was accepted")
	}
}

func TestCommitAllThenUndo(t *testing.T) {
	dir := newRepo(t)
	r := open(t, dir)
	ctx := context.Background()

	// A branch with no commits has no history and nothing to undo onto.
	if log, err := r.Log(ctx, 10); err != nil || len(log) != 0 {
		t.Fatalf("log on an empty branch = %v %v", log, err)
	}
	put(t, dir, "a.txt", "one\n")
	if err := r.Unstage(ctx, []string{"a.txt"}); err != nil {
		t.Fatalf("unstaging on an empty branch: %v", err)
	}
	first, err := r.Commit(ctx, CommitRequest{Message: "First commit\n\nWith a body.", All: true})
	if err != nil {
		t.Fatal(err)
	}
	put(t, dir, "a.txt", "two\n")
	if _, err := r.Commit(ctx, CommitRequest{Message: "Second", All: true}); err != nil {
		t.Fatal(err)
	}

	log, err := r.Log(ctx, 10)
	if err != nil || len(log) != 2 {
		t.Fatalf("log = %+v %v", log, err)
	}
	if log[0].Subject != "Second" || log[1].Short != first || log[1].Body != "With a body." {
		t.Errorf("log = %+v", log)
	}
	if log[0].Pushed {
		t.Error("a commit no remote holds reads as pushed")
	}
	files, err := r.Show(ctx, log[0].Hash)
	if err != nil || len(files) != 1 || files[0].Path != "a.txt" || files[0].Added != 1 || files[0].Removed != 1 {
		t.Errorf("show = %+v %v", files, err)
	}

	if err := r.Undo(ctx); err != nil {
		t.Fatal(err)
	}
	st := status(t, r)
	if f, ok := find(st.Staged, "a.txt"); !ok || f.State != StateChanged {
		t.Errorf("after undo the change is not staged: %+v", st.Staged)
	}
	if err := r.Undo(ctx); err != nil {
		t.Fatal(err)
	}
	if r.hasHead(ctx) {
		t.Error("undoing the first commit left a HEAD")
	}
	if _, err := r.Commit(ctx, CommitRequest{Message: "  "}); err == nil {
		t.Error("an empty message was committed")
	}
}

func TestProjectBelowTheTop(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "outside.txt", "one\n")
	put(t, dir, "app/inside.txt", twenty(nil))
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "First")
	put(t, dir, "outside.txt", "two\n")
	put(t, dir, "app/inside.txt", twenty(map[int]string{3: "THREE"}))
	put(t, dir, "app/new.txt", "x\n")

	r := open(t, filepath.Join(dir, "app"))
	if r.Prefix != "app/" {
		t.Fatalf("prefix = %q", r.Prefix)
	}
	st := status(t, r)
	if len(st.Unstaged) != 2 {
		t.Fatalf("unstaged = %+v, want only the project's two files", st.Unstaged)
	}
	f, ok := find(st.Unstaged, "inside.txt")
	if !ok || len(f.Hunks) != 1 {
		t.Fatalf("inside.txt = %+v %v", f, ok)
	}
	h := f.Hunks[0]
	ref := HunkRef{Path: "inside.txt", OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines}
	if err := r.ApplyHunk(context.Background(), ref, HunkStage); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(context.Background(), CommitRequest{Message: "Inside only", All: true}); err != nil {
		t.Fatal(err)
	}
	out := gitIn(t, dir, "status", "--porcelain")
	if !strings.Contains(out, " M outside.txt") || strings.Contains(out, "app/") {
		t.Errorf("committing the project touched the rest of the repository:\n%s", out)
	}
}

func TestBranches(t *testing.T) {
	dir := newRepo(t)
	put(t, dir, "a.txt", "one\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "First")

	r := open(t, dir)
	ctx := context.Background()
	if err := r.Checkout(ctx, CheckoutRequest{Branch: "feat/x", Create: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.Checkout(ctx, CheckoutRequest{Branch: "bad name", Create: true}); err == nil {
		t.Error("an invalid branch name was created")
	}
	put(t, dir, "a.txt", "dirty\n")
	if err := r.Checkout(ctx, CheckoutRequest{Branch: "main", Stash: true}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(data) != "one\n" {
		t.Errorf("a.txt = %q, want the change stashed", data)
	}
	local, remote, err := r.Branches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 2 || len(remote) != 0 {
		t.Fatalf("local = %+v remote = %v", local, remote)
	}
	for _, b := range local {
		if b.Current != (b.Name == "main") {
			t.Errorf("branch %+v", b)
		}
	}
	if err := r.Push(ctx); err == nil || !strings.Contains(err.Error(), "no remote") {
		t.Errorf("push with no remote = %v", err)
	}
}

func TestParseDiffEdges(t *testing.T) {
	out := []byte(`diff --git a/has space.txt b/has space.txt
index 1111111..2222222 100644
--- a/has space.txt
+++ b/has space.txt
@@ -1 +1 @@ func top() {
-old
\ No newline at end of file
+new
\ No newline at end of file
diff --git a/img.png b/img.png
index 3333333..4444444 100644
Binary files a/img.png and b/img.png differ
diff --cc conflicted.txt
index 5555555,6666666..0000000
--- a/conflicted.txt
+++ b/conflicted.txt
@@@ -1,1 -1,1 +1,5 @@@
++<<<<<<< ours
diff --git "a/tab\there.txt" "b/tab\there.txt"
new file mode 100644
--- /dev/null
+++ "b/tab\there.txt"
@@ -0,0 +1,2 @@
+a
+b
`)
	files := parseDiff(out)
	if len(files) != 3 {
		t.Fatalf("got %d files: %+v", len(files), files)
	}
	f := files[0]
	if f.Path != "has space.txt" || f.Added != 1 || f.Removed != 1 || len(f.Hunks) != 1 {
		t.Fatalf("first = %+v", f)
	}
	h := f.Hunks[0]
	if h.OldStart != 1 || h.OldLines != 1 || h.NewLines != 1 || h.Section != "func top() {" || len(h.Lines) != 2 {
		t.Errorf("hunk = %+v", h)
	}
	if !strings.Contains(h.raw, `\ No newline at end of file`) {
		t.Error("the no-newline marker was dropped from the raw hunk, so applying it would add a newline")
	}
	if !files[1].Binary || files[1].Path != "img.png" {
		t.Errorf("binary = %+v", files[1])
	}
	if files[2].Path != "tab\there.txt" || files[2].Added != 2 || files[2].Hunks[0].OldStart != 0 {
		t.Errorf("quoted = %+v", files[2])
	}
}

func TestTrimKeepsCounts(t *testing.T) {
	f := File{Added: 700}
	for i := 0; i < 7; i++ {
		h := Hunk{}
		for j := 0; j < 100; j++ {
			h.Lines = append(h.Lines, diff.Line{Kind: diff.Added, Text: "x"})
		}
		f.Hunks = append(f.Hunks, h)
	}
	got := trim(f)
	carried := 0
	for _, h := range got.Hunks {
		carried += len(h.Lines)
	}
	if carried != fileLines || got.Omitted != 700-fileLines || got.Added != 700 {
		t.Errorf("carried %d, omitted %d, added %d", carried, got.Omitted, got.Added)
	}
}

// An untracked link is shown as a link, and what it points to is never read,
// in the status or in the patch a draft is made from.
func TestAnUntrackedLinkIsNotFollowed(t *testing.T) {
	dir := newRepo(t)
	secret := filepath.Join(t.TempDir(), "id_secret")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "key")); err != nil {
		t.Fatal(err)
	}
	put(t, dir, "plain.txt", "ordinary\n")
	r := open(t, dir)

	st := status(t, r)
	link, ok := find(st.Unstaged, "key")
	if !ok || !link.Link || len(link.Hunks) != 0 {
		t.Errorf("the link = %+v, want it marked and unread", link)
	}
	if plain, ok := find(st.Unstaged, "plain.txt"); !ok || plain.Added != 1 {
		t.Errorf("the ordinary file = %+v", plain)
	}
	patch, err := r.Patch(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(patch, "PRIVATE KEY") {
		t.Errorf("the patch read what the link points to: %q", patch)
	}
	if !strings.Contains(patch, "b/key\nnew file mode 120000") || !strings.Contains(patch, "+"+secret+"\n") {
		t.Errorf("the patch does not tell the link as git stores it: %q", patch)
	}
	if !strings.Contains(patch, "ordinary") {
		t.Errorf("the patch lost the ordinary file: %q", patch)
	}
}

// A new link that is the only change still gives a draft something to read.
func TestALinkAloneMakesAPatch(t *testing.T) {
	dir := newRepo(t)
	if err := os.Symlink("elsewhere/target", filepath.Join(dir, "only")); err != nil {
		t.Fatal(err)
	}
	patch, err := open(t, dir).Patch(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "new file mode 120000") || !strings.Contains(patch, "+elsewhere/target\n") {
		t.Errorf("patch = %q, want the link as git stores it", patch)
	}
}

// A patch of untracked files whose names or link targets hold a newline or a
// quote is read back by git as exactly those files.
func TestAnUntrackedPatchKeepsOddNames(t *testing.T) {
	dir := newRepo(t)
	link, target := "two\nlines", "one\ntwo"
	if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
		t.Skip("the volume refuses that name")
	}
	put(t, dir, `quo"te.txt`, "plain\n")
	patch, err := open(t, dir).Patch(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	other := newRepo(t)
	file := filepath.Join(t.TempDir(), "p.diff")
	if err := os.WriteFile(file, []byte(patch), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "apply", file)
	if got, err := os.Readlink(filepath.Join(other, link)); err != nil || got != target {
		t.Errorf("link read back = %q, %v, want %q", got, err, target)
	}
	if got, err := os.ReadFile(filepath.Join(other, `quo"te.txt`)); err != nil || string(got) != "plain\n" {
		t.Errorf("file read back = %q, %v", got, err)
	}
}

// The patch of a new link matches what git itself writes for it, whether or
// not the target ends in a newline.
func TestALinkPatchMatchesGit(t *testing.T) {
	for _, target := range []string{"elsewhere/target", "one\ntwo", "ends\n", "p\nq\n", "\n"} {
		dir := newRepo(t)
		if err := os.Symlink(target, filepath.Join(dir, "lnk")); err != nil {
			t.Skip("the volume refuses that target")
		}
		patch, err := open(t, dir).Patch(context.Background(), false)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "diff", "--no-index", "/dev/null", "lnk")
		cmd.Dir = dir
		out, _ := cmd.Output()
		var want []string
		for _, l := range strings.SplitAfter(string(out), "\n") {
			if !strings.HasPrefix(l, "index ") {
				want = append(want, l)
			}
		}
		if patch != strings.Join(want, "") {
			t.Errorf("target %q: patch = %q, git writes %q", target, patch, strings.Join(want, ""))
		}
	}
}
