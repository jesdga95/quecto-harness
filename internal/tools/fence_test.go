package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// In a project that holds the scratch folders, grep and read reach the
// conversation's own scratch folder and none of the others.
func TestToolsStayOutOfOtherScratchFolders(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "scratch-workspaces")
	own := filepath.Join(root, "scratch-2026-11-27-aaaaaa")
	other := filepath.Join(root, "scratch-2026-11-27-bbbbbb")
	for _, d := range []string{own, other} {
		os.MkdirAll(d, 0o700)
	}
	os.WriteFile(filepath.Join(own, "mine.txt"), []byte("needle mine\n"), 0o600)
	os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("needle theirs\n"), 0o600)
	r := testRoot(t, base)
	r.FenceOff(Fence{Root: root, Own: []string{own}})

	res, err := r.Find(context.Background(), json.RawMessage(`{"text":"needle"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "mine.txt") || strings.Contains(res.Output, "theirs") {
		t.Errorf("grep = %q, want the own folder only", res.Output)
	}
	// bash still reaches the folder, so the refusal names the file tools only.
	if _, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"scratch-workspaces/scratch-2026-11-27-bbbbbb/theirs.txt"}`)); err == nil || !strings.Contains(err.Error(), "the file tools do not reach") {
		t.Errorf("a read of another conversation's folder = %v, want it refused", err)
	}
	if _, err := r.ReadFile(context.Background(), json.RawMessage(`{"path":"scratch-workspaces/scratch-2026-11-27-aaaaaa/mine.txt"}`)); err != nil {
		t.Errorf("a read of the own folder = %v", err)
	}
}

func TestMentionsSkipLooseFilesInTheScratchRoot(t *testing.T) {
	project := t.TempDir()
	scratch := filepath.Join(project, "workspaces")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "note-project"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "note-service"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	found, err := FindFiles(project, "", "note", 10, Fence{Root: scratch})
	if err != nil || len(found) != 1 || found[0] != "note-project" {
		t.Fatalf("Suggestions = %v, %v", found, err)
	}
}

func TestFenceRecognizesTheOwnedFolderByIdentity(t *testing.T) {
	root := t.TempDir()
	own := filepath.Join(root, "Workspace")
	if err := os.Mkdir(own, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "WORKSPACE")
	info, err := os.Stat(alias)
	if os.IsNotExist(err) {
		t.Skip("Volume distinguishes letter case")
	}
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(own)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(original, info) {
		t.Skip("Names refer to different folders")
	}
	r := testRoot(t, root)
	r.FenceOff(Fence{Root: root, Own: []string{alias}})
	if r.fenced(filepath.Join(r.Root, "Workspace", "note")) {
		t.Fatal("The owned folder was excluded")
	}
}

// A walk that decides once for each folder at the root's depth still knows
// the root by identity under another spelling, and still reaches the folders
// beside it.
func TestAWalkKnowsTheFenceRootByIdentity(t *testing.T) {
	project := t.TempDir()
	for _, d := range []string{"Scratch/other", "src/deep/er"} {
		if err := os.MkdirAll(filepath.Join(project, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(project, "Scratch", "other", "note-theirs"), nil, 0o600)
	os.WriteFile(filepath.Join(project, "src", "deep", "er", "note-mine"), nil, 0o600)
	root := filepath.Join(project, "SCRATCH")
	if _, err := os.Stat(root); err != nil {
		t.Skip("Volume distinguishes letter case")
	}
	found, err := FindFiles(project, "", "note", 10, Fence{Root: root})
	if err != nil || len(found) != 1 || filepath.Base(found[0]) != "note-mine" {
		t.Fatalf("FindFiles = %v, %v", found, err)
	}
}

// A fence root named through a link is still known by identity, so the
// check runs on volumes that tell letter case apart.
func TestAWalkKnowsTheFenceRootThroughALink(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(base, "project")
	for _, d := range []string{"Scratch/other", "src"} {
		if err := os.MkdirAll(filepath.Join(project, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(project, filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	r := &Runner{Root: project, Fence: Fence{Root: filepath.Join(base, "alias", "Scratch")}}
	walk := fenceWalk{}
	if !r.fencedIn(filepath.Join(project, "Scratch", "other", "note"), walk) {
		t.Error("a file in another conversation's folder was reached")
	}
	if r.fencedIn(filepath.Join(project, "src", "note"), walk) {
		t.Error("a file beside the fence root was fenced")
	}
}

// A walk compares each folder at the fence root's depth once, however many
// files are below it.
func TestAWalkComparesEachFolderOnce(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const folders, files = 20, 30
	for i := range folders {
		d := filepath.Join(project, fmt.Sprintf("a%02d", i), "b")
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		for j := range files {
			os.WriteFile(filepath.Join(d, fmt.Sprintf("f%02d.txt", j)), nil, 0o600)
		}
	}
	root := filepath.Join(project, "s")
	os.Mkdir(root, 0o700)
	saved := sameFenceDir
	t.Cleanup(func() { sameFenceDir = saved })
	calls := 0
	sameFenceDir = func(a, b string) bool {
		calls++
		return saved(a, b)
	}
	if _, err := FindFiles(project, "", "zzz", 30, Fence{Root: root}); err != nil {
		t.Fatal(err)
	}
	if calls > folders+1 {
		t.Errorf("the walk compared %d folders, want at most %d", calls, folders+1)
	}
}
