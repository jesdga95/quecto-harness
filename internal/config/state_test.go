package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stateStore(t *testing.T) (*Store, string) {
	t.Helper()
	base, _ := filepath.EvalSymlinks(t.TempDir())
	p := DiscoverPathsIn(filepath.Join(base, "home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return s, base
}

// A project that holds the state folder, lies inside it, is it, or reaches it
// through a link is refused; one beside it is taken.
func TestAProjectMayNotOverlapTheStateFolder(t *testing.T) {
	s, base := stateStore(t)
	home := s.Paths().Home
	beside := filepath.Join(base, "beside")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(beside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{base, home, s.Paths().Sessions, link} {
		if _, err := s.AddProject(dir); !errors.Is(err, ErrStateOverlap) {
			t.Errorf("AddProject(%s) = %v, want the overlap refused", dir, err)
		}
	}
	if _, err := s.AddProject(beside); err != nil {
		t.Errorf("AddProject(beside) = %v", err)
	}
	// A volume that ignores case reaches the state folder by another casing.
	upper := strings.ToUpper(home)
	if _, err := os.Stat(upper); err == nil && upper != home {
		if err := s.CheckProject(upper); !errors.Is(err, ErrStateOverlap) {
			t.Errorf("CheckProject(%s) = %v, want the overlap found", upper, err)
		}
	}
}

// A folder that cannot be resolved is an error, not a clean bill.
func TestAProjectThatCannotBeCheckedIsAnError(t *testing.T) {
	s, base := stateStore(t)
	err := s.CheckProject(filepath.Join(base, "gone"))
	if err == nil || errors.Is(err, ErrStateOverlap) {
		t.Errorf("CheckProject(missing) = %v, want an error of its own", err)
	}
}

// InState follows links.
func TestInStateFollowsLinks(t *testing.T) {
	s, base := stateStore(t)
	token := filepath.Join(s.Paths().Home, "token")
	if err := os.WriteFile(token, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "token-link")
	if err := os.Symlink(token, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(base, "other.txt")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{token: true, link: true, other: false} {
		if got, err := s.InState(path); err != nil || got != want {
			t.Errorf("InState(%s) = %v, %v, want %v", path, got, err, want)
		}
	}
}

// A project that holds a link on the path the harness was given to its state
// is refused, though the state itself lies elsewhere.
func TestAProjectHoldingThePathToTheStateIsRefused(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "project")
	elsewhere := filepath.Join(base, "elsewhere")
	for _, d := range []string{project, elsewhere} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(elsewhere, filepath.Join(project, "state")); err != nil {
		t.Fatal(err)
	}
	p := DiscoverPathsIn(filepath.Join(project, "state", "home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckProject(project); !errors.Is(err, ErrStateOverlap) {
		t.Errorf("CheckProject = %v, want the overlap refused", err)
	}
}

// The same, with the project named by an alias the configured path uses: the
// folders are compared, not their names.
func TestAProjectHoldingAnAliasedPathToTheStateIsRefused(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "project")
	elsewhere := filepath.Join(base, "elsewhere")
	for _, d := range []string{project, elsewhere} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(project, filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(project, "state")); err != nil {
		t.Fatal(err)
	}
	p := DiscoverPathsIn(filepath.Join(base, "alias", "state", "home"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckProject(project); !errors.Is(err, ErrStateOverlap) {
		t.Errorf("CheckProject = %v, want the overlap refused", err)
	}
	if err := s.CheckProject(elsewhere); !errors.Is(err, ErrStateOverlap) {
		t.Errorf("CheckProject(elsewhere) = %v, want it refused as the state's own folder", err)
	}
}

// A link whose target climbs out of another link with .. still counts: the
// walk takes .. from the folder the inner link reached, so the project that
// holds the inner link is found, as the system would go through it.
func TestALinkCrossedBeforeADotDotStillCounts(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "project")
	for _, d := range []string{project, filepath.Join(base, "elsewhere", "sub"), filepath.Join(base, "elsewhere", "h")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "elsewhere", "sub"), filepath.Join(project, "state")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("project/state/../h", filepath.Join(base, "jump")); err != nil {
		t.Fatal(err)
	}
	p := DiscoverPathsIn(filepath.Join(base, "jump"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckProject(project); !errors.Is(err, ErrStateOverlap) {
		t.Errorf("CheckProject = %v, want the overlap refused", err)
	}
	if err := s.CheckProject(filepath.Join(base, "elsewhere", "sub")); err != nil {
		t.Errorf("CheckProject(a folder the path only passes) = %v", err)
	}
}

// A plain folder of the project that the configured path passes through, on
// its way out with .., counts as much as a link does.
func TestAFolderThePathPassesThroughCounts(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project := filepath.Join(base, "project")
	for _, d := range []string{filepath.Join(project, "sub"), filepath.Join(base, "h")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("project/sub/../../h", filepath.Join(base, "jump")); err != nil {
		t.Fatal(err)
	}
	p := DiscoverPathsIn(filepath.Join(base, "jump"))
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckProject(project); !errors.Is(err, ErrStateOverlap) {
		t.Errorf("CheckProject = %v, want the overlap refused", err)
	}
}
