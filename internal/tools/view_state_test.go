package tools

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The file panel reaches nothing in the state folder, a link that leads there
// included, and shows an ordinary file as before.
func TestThePanelReachesNothingInTheState(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "project")
	home := filepath.Join(base, "home")
	for _, d := range []string{project, home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "token"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "leak_notes.txt"), []byte("ordinary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "token"), filepath.Join(project, "leak_token")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(project, "leak_state")); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"leak_token", "leak_state/token"} {
		if _, err := ViewFile(project, Keep{State: home}, p, 1<<20); !errors.Is(err, ErrHidden) {
			t.Errorf("ViewFile(%s) = %v, want it hidden", p, err)
		}
	}
	if v, err := ViewFile(project, Keep{State: home}, "leak_notes.txt", 1<<20); err != nil || v.Content != "ordinary\n" {
		t.Errorf("ViewFile(ordinary) = %q, %v", v.Content, err)
	}
	if _, _, err := ListDir(project, Keep{State: home}, "leak_state"); !errors.Is(err, ErrHidden) {
		t.Errorf("ListDir(leak_state) = %v, want it hidden", err)
	}
	_, entries, err := ListDir(project, Keep{State: home}, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"leak_notes.txt"}) {
		t.Errorf("listing = %v, want the ordinary file alone", names)
	}
	found, err := FindFiles(project, Keep{State: home}, "leak", 30)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(found, []string{"leak_notes.txt"}) {
		t.Errorf("FindFiles = %v, want the ordinary file alone", found)
	}
}

// On a volume that ignores case, a link to the state folder in another
// casing is left out of the listing and the search all the same.
func TestThePanelJudgesLinksByFolder(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "project")
	home := filepath.Join(base, "home")
	for _, d := range []string{project, home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	upper := filepath.Join(base, "HOME")
	if _, err := os.Stat(upper); err != nil {
		t.Skip("the volume tells case apart")
	}
	if err := os.Symlink(upper, filepath.Join(project, "other_case")); err != nil {
		t.Fatal(err)
	}
	_, entries, err := ListDir(project, Keep{State: home}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("listing = %+v, want the link left out", entries)
	}
	if found, _ := FindFiles(project, Keep{State: home}, "other", 30); len(found) != 0 {
		t.Errorf("FindFiles = %v, want the link left out", found)
	}
}
