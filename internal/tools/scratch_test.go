package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// scratchLayout is a state folder with a token in it, a project with a file in
// it, and a runner whose scratch folder is a session's, not made yet.
type scratchLayout struct {
	r       *Runner
	home    string
	project string
	scratch string
}

func newScratchLayout(t *testing.T) scratchLayout {
	t.Helper()
	home, _ := filepath.EvalSymlinks(t.TempDir())
	project, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "token"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.CommandTimeout = 5 * time.Second
	r, err := NewRunner(project, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.Hide(home)
	scratch := filepath.Join(home, "sessions", "sess_aaaaaaaaaaaaaaaa.files", "scratch")
	r.Scratch = scratch
	return scratchLayout{r: r, home: home, project: project, scratch: scratch}
}

func pathArgs(path string, extra ...string) json.RawMessage {
	fields := map[string]string{"path": path}
	for i := 0; i+1 < len(extra); i += 2 {
		fields[extra[i]] = extra[i+1]
	}
	raw, _ := json.Marshal(fields)
	return raw
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// Reading creates nothing; the first write makes the folder, its session
// folder and the folders under it, each readable by the user alone.
func TestScratchIsMadeByTheFirstWrite(t *testing.T) {
	l := newScratchLayout(t)
	ctx := context.Background()
	target := filepath.Join(l.scratch, "notes", "a.txt")
	if !l.r.InScratch(target) || l.r.Outside(target) {
		t.Fatalf("InScratch = %v, Outside = %v, want a scratch path", l.r.InScratch(target), l.r.Outside(target))
	}
	if _, err := l.r.ReadFile(ctx, pathArgs(target)); err == nil {
		t.Error("a file that does not exist was read")
	}
	if _, err := os.Stat(filepath.Dir(l.scratch)); err == nil {
		t.Fatal("a read made the session folder")
	}

	res, err := l.r.WriteFile(ctx, pathArgs(target, "content", "hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, target) {
		t.Errorf("the result = %q, want the absolute path", res.Output)
	}
	for _, dir := range []string{filepath.Dir(l.scratch), l.scratch, filepath.Dir(target)} {
		if m := mode(t, dir); m != 0o700 {
			t.Errorf("%s has mode %o, want 700", dir, m)
		}
	}
	out, err := l.r.ReadFile(ctx, pathArgs(target))
	if err != nil || !strings.Contains(out.Output, "hello") {
		t.Errorf("read back = %q, %v", out.Output, err)
	}
}

// A path under the scratch folder that leads out of it is refused, wherever
// it leads, and what it leads to is left as it was.
func TestScratchRefusesEveryWayOut(t *testing.T) {
	l := newScratchLayout(t)
	ctx := context.Background()
	if err := l.r.ensureScratch(); err != nil {
		t.Fatal(err)
	}
	nowhere := filepath.Join(l.project, "made-through-a-link.txt")
	links := map[string]string{
		"to_project": filepath.Join(l.project, "file.txt"),
		"to_token":   filepath.Join(l.home, "token"),
		"to_outside": "/etc/hosts",
		"to_nowhere": nowhere,
		"to_folder":  l.project,
	}
	for name, dest := range links {
		if err := os.Symlink(dest, filepath.Join(l.scratch, name)); err != nil {
			t.Fatal(err)
		}
	}
	for name := range links {
		p := filepath.Join(l.scratch, name)
		if name == "to_folder" {
			p = filepath.Join(p, "file.txt")
		}
		if _, err := l.r.WriteFile(ctx, pathArgs(p, "content", "overwritten\n")); err == nil || !strings.Contains(err.Error(), "scratch folder") {
			t.Errorf("write through %s = %v, want it refused", name, err)
		}
		if _, err := l.r.ReadFile(ctx, pathArgs(p)); err == nil {
			t.Errorf("read through %s was allowed", name)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(l.project, "file.txt")); string(raw) != "one\n" {
		t.Errorf("the project file changed: %q", raw)
	}
	if raw, _ := os.ReadFile(filepath.Join(l.home, "token")); string(raw) != "secret\n" {
		t.Errorf("the token changed: %q", raw)
	}
	if _, err := os.Stat(nowhere); err == nil {
		t.Error("a write through a dangling link made its target")
	}
}

// A link put in place of the session folder or of the scratch folder makes
// every path under it lead out, and the folder is not made through it.
func TestScratchRefusesAFolderReplacedByALink(t *testing.T) {
	for _, which := range []string{"session", "scratch"} {
		t.Run(which, func(t *testing.T) {
			l := newScratchLayout(t)
			elsewhere := t.TempDir()
			at := filepath.Dir(l.scratch)
			if which == "scratch" {
				if err := os.MkdirAll(at, 0o700); err != nil {
					t.Fatal(err)
				}
				at = l.scratch
			}
			if err := os.Symlink(elsewhere, at); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(l.scratch, "a.txt")
			if _, err := l.r.WriteFile(context.Background(), pathArgs(target, "content", "x")); err == nil {
				t.Error("a write went through the link")
			}
			if err := l.r.ensureScratch(); err == nil {
				t.Error("the folder was taken through the link")
			}
			if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
				t.Errorf("the link's target holds %d entries, want none", len(entries))
			}
		})
	}
}

// The scratch folder opens nothing else in the state folder: another
// session's, a path that climbs out with .., and one that reaches it through a
// link in the project are all refused as the state folder.
func TestScratchOpensNothingElseInTheState(t *testing.T) {
	l := newScratchLayout(t)
	ctx := context.Background()
	other := filepath.Join(l.home, "sessions", "sess_bbbbbbbbbbbbbbbb.files", "scratch", "x.txt")
	climb := filepath.Join(l.scratch, "..", "..", "..", "token")
	if err := os.Symlink(l.home, filepath.Join(l.project, "state")); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(l.project, "state", "sessions", "sess_aaaaaaaaaaaaaaaa.files", "scratch", "x.txt")
	for _, p := range []string{other, climb, via} {
		if l.r.InScratch(p) {
			t.Errorf("%s counted as the scratch folder", p)
		}
		if _, err := l.r.WriteFile(ctx, pathArgs(p, "content", "x")); err == nil || !strings.Contains(err.Error(), "state folder") {
			t.Errorf("write %s = %v, want the state folder refused", p, err)
		}
	}
}

// Without a scratch folder the runner behaves as it did: nothing is scratch,
// and the state folder stays closed.
func TestNoScratchChangesNothing(t *testing.T) {
	l := newScratchLayout(t)
	target := filepath.Join(l.scratch, "a.txt")
	l.r.Scratch = ""
	if l.r.InScratch(target) {
		t.Error("a runner with no scratch folder counted one")
	}
	if _, err := l.r.WriteFile(context.Background(), pathArgs(target, "content", "x")); err == nil || !strings.Contains(err.Error(), "state folder") {
		t.Errorf("write = %v, want the state folder refused", err)
	}
	if err := l.r.ensureScratch(); err != nil {
		t.Errorf("ensureScratch with none = %v", err)
	}
}

// A search of the scratch folder finds what is in it, leaves out a link that
// leads out, and names what it found by its absolute path.
func TestGrepInTheScratchFolder(t *testing.T) {
	l := newScratchLayout(t)
	if err := l.r.ensureScratch(); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"found.txt": "needle here\n", "found.md": "other\n"} {
		if err := os.WriteFile(filepath.Join(l.scratch, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(l.home, "token"), []byte("needle secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Same stem as the hit, so it would be suggested if links were not judged.
	if err := os.Symlink(filepath.Join(l.home, "token"), filepath.Join(l.scratch, "found.secret")); err != nil {
		t.Fatal(err)
	}

	res, err := l.r.Find(context.Background(), json.RawMessage(`{"text":"needle","path":`+mustJSON(l.scratch)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, filepath.Join(l.scratch, "found.txt")) {
		t.Errorf("grep = %q, want the file by its absolute path", res.Output)
	}
	if strings.Contains(res.Output, "secret") {
		t.Errorf("grep = %q, followed or suggested a link out", res.Output)
	}
	if !strings.Contains(res.Output, "found.md") {
		t.Errorf("grep = %q, want the related file named", res.Output)
	}
}

// bash gets the folder in QUECTO_SCRATCH, made before the command starts, and
// keeps the TMPDIR it was given.
func TestShellIsToldTheScratchFolder(t *testing.T) {
	l := newScratchLayout(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	res, err := l.r.RunShell(context.Background(), json.RawMessage(`{"command":"printf '%s|%s|' \"$QUECTO_SCRATCH\" \"$TMPDIR\"; test -d \"$QUECTO_SCRATCH\" && echo made"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := l.scratch + "|" + tmp + "|made"
	if !strings.Contains(res.Output, want) {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
	if m := mode(t, l.scratch); m != 0o700 {
		t.Errorf("scratch mode = %o, want 700", m)
	}
}

// A scratch folder that cannot be made keeps the command from starting.
func TestShellDoesNotStartWithoutTheFolder(t *testing.T) {
	l := newScratchLayout(t)
	if err := os.MkdirAll(filepath.Dir(l.scratch), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.scratch, []byte("not a folder"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(l.project, "ran")
	if _, err := l.r.RunShell(context.Background(), json.RawMessage(`{"command":"touch ran"}`)); err == nil {
		t.Error("the command started without its scratch folder")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran")
	}
}

// A file a printing command showed by its literal path counts as read, so it
// can be edited; one named through the variable does not, and asks for a read.
func TestShellReadsInTheScratchFolder(t *testing.T) {
	l := newScratchLayout(t)
	l.r.Seen = NewSeen()
	if err := l.r.ensureScratch(); err != nil {
		t.Fatal(err)
	}
	shown := filepath.Join(l.scratch, "shown.txt")
	named := filepath.Join(l.scratch, "named.txt")
	os.WriteFile(shown, []byte("old\n"), 0o600)
	os.WriteFile(named, []byte("old\n"), 0o600)
	l.r.NoteShellReads("cat " + shown)
	l.r.NoteShellReads(`cat "$QUECTO_SCRATCH/named.txt"`)
	ctx := context.Background()

	edit := func(p string) error {
		raw, _ := json.Marshal(map[string]string{"path": p, "old_string": "old", "new_string": "new"})
		_, err := l.r.ReplaceInFile(ctx, raw)
		return err
	}
	if err := edit(shown); err != nil {
		t.Errorf("edit after cat by path = %v", err)
	}
	if err := edit(named); err == nil {
		t.Error("a file named through the variable was edited without a read")
	}
}

// A picture made in the scratch folder can be looked at.
func TestReadImageInTheScratchFolder(t *testing.T) {
	l := newScratchLayout(t)
	if err := l.r.ensureScratch(); err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(l.scratch, "shot.png")
	os.WriteFile(png, onePixelPNG(), 0o600)
	if _, err := l.r.ReadImage(context.Background(), pathArgs(png)); err != nil {
		t.Errorf("read_image = %v", err)
	}
}

// A search of one file outside the project suggests nothing from the folder
// it sits in, which the search was not allowed to look at.
func TestGrepOfOneOutsideFileSuggestsNothingBesideIt(t *testing.T) {
	l := newScratchLayout(t)
	outside := t.TempDir()
	for name, content := range map[string]string{"notes.txt": "needle\n", "notes.private": "x\n"} {
		if err := os.WriteFile(filepath.Join(outside, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	res, err := l.r.Find(context.Background(), json.RawMessage(`{"text":"needle","path":`+mustJSON(filepath.Join(outside, "notes.txt"))+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "notes.private") {
		t.Errorf("grep = %q, named a file beside the one searched", res.Output)
	}
}

// A scratch folder that was due and could not be named keeps bash from
// starting, and a QUECTO_SCRATCH the harness itself inherited never reaches
// a command.
func TestShellWithoutItsScratchFolder(t *testing.T) {
	l := newScratchLayout(t)
	t.Setenv("QUECTO_SCRATCH", "/somewhere/else")
	l.r.Scratch = ""
	res, err := l.r.RunShell(context.Background(), json.RawMessage(`{"command":"printf '[%s]' \"$QUECTO_SCRATCH\""}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "[]") {
		t.Errorf("output = %q, want no inherited QUECTO_SCRATCH", res.Output)
	}
	l.r.ScratchErr = errors.New("not a session id")
	if _, err := l.r.RunShell(context.Background(), json.RawMessage(`{"command":"true"}`)); err == nil {
		t.Error("bash started although its scratch folder could not be named")
	}
}

// A pinned scratch folder whose name comes to hold another folder takes no
// more writes.
func TestAPinnedScratchRefusesAnotherFolder(t *testing.T) {
	root := t.TempDir()
	r, err := NewRunner(root, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r.Scratch = r.Root
	if err := r.PinScratch(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve("a.txt"); err != nil {
		t.Fatalf("a path in the pinned folder = %v", err)
	}
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(moved) })
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve("a.txt"); err == nil {
		t.Error("a path under a folder put in the pinned one's place resolved")
	}
}

// grep and bash check the pinned folder before they start, so neither works
// in a folder put in its place.
func TestEveryToolChecksThePinnedFolder(t *testing.T) {
	root := t.TempDir()
	r, err := NewRunner(root, config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	r.Scratch = r.Root
	if err := r.PinScratch(); err != nil {
		t.Fatal(err)
	}
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(moved) })
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Find(context.Background(), json.RawMessage(`{"name":"*"}`)); err == nil {
		t.Error("grep walked a folder put in the pinned one's place")
	}
	if _, err := r.RunShell(context.Background(), json.RawMessage(`{"command":"true"}`)); err == nil {
		t.Error("bash started in a folder put in the pinned one's place")
	}
}
