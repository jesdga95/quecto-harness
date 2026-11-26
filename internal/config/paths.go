// Package config owns the state directory: config.json, allowlist.json,
// mcp.json and the atomic write used by everything that persists. Nothing is written into
// a project folder.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Paths are the locations under the state directory.
type Paths struct {
	Home      string
	Config    string
	Allowlist string
	MCP       string
	Sessions  string
	Files     string
	Uploads   string
	Log       string
	Token     string
	URL       string
	// Scratch holds the working folder of each conversation. It is apart
	// from the state, in the platform's folder for application data, and
	// SettleScratch fixes it for the installation.
	Scratch string
	// ScratchRecord is the file in the state that names Scratch once chosen.
	ScratchRecord string
}

// DiscoverPaths honours $QUECTO_HARNESS_HOME and otherwise puts the state
// directory beside the user's home.
func DiscoverPaths() (Paths, error) {
	if h := os.Getenv("QUECTO_HARNESS_HOME"); h != "" {
		abs, err := filepath.Abs(h)
		if err != nil {
			return Paths{}, errors.New("Resolve $QUECTO_HARNESS_HOME " + h + ": give it an absolute path")
		}
		return DiscoverPathsIn(abs), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, errors.New("Find your home directory: set $QUECTO_HARNESS_HOME to the state directory you want")
	}
	p := DiscoverPathsIn(filepath.Join(home, ".quecto-harness"))
	p.Scratch = platformScratch(home)
	return p, nil
}

// platformScratch is the scratch folder in the platform's folder for
// application data under the user's home.
func platformScratch(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Quecto Harness", "scratch")
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Quecto Harness", "scratch")
		}
		return filepath.Join(home, "AppData", "Local", "Quecto Harness", "scratch")
	}
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return filepath.Join(d, "quecto-harness", "scratch")
	}
	return filepath.Join(home, ".local", "share", "quecto-harness", "scratch")
}

func DiscoverPathsIn(home string) Paths {
	return Paths{
		Home:      home,
		Config:    filepath.Join(home, "config.json"),
		Allowlist: filepath.Join(home, "allowlist.json"),
		MCP:       filepath.Join(home, "mcp.json"),
		Sessions:  filepath.Join(home, "sessions"),
		Files:     filepath.Join(home, "files"),
		Uploads:   filepath.Join(home, "uploads"),
		Log:       filepath.Join(home, "log"),
		Token:     filepath.Join(home, "token"),
		URL:       filepath.Join(home, "url"),
		// A state folder of its own keeps its scratch beside it, so two
		// installations never share one.
		Scratch:       filepath.Clean(home) + ".scratch",
		ScratchRecord: filepath.Join(home, "scratch-root"),
	}
}

// SettleScratch fixes the scratch folder for the installation. The first
// one used is recorded in the state and stays: a later start asking for
// another, by override or by $QUECTO_HARNESS_SCRATCH, is refused, since the
// conversations' folders and the deletes still owed are in the first. The
// folder is made at 0700 and must not overlap the state folder.
func (p *Paths) SettleScratch(override string) error {
	want := override
	if want == "" {
		want = os.Getenv("QUECTO_HARNESS_SCRATCH")
	}
	asked := want != ""
	if !asked {
		want = p.Scratch
	}
	abs, err := filepath.Abs(want)
	if err != nil {
		return fmt.Errorf("Resolve the scratch folder %s: give it an absolute path", want)
	}
	want = filepath.Clean(abs)
	recorded, err := readRecord(p.ScratchRecord)
	if err != nil {
		return err
	}
	if recorded != "" {
		if asked && recorded != want {
			return fmt.Errorf("The scratch folder is %s, recorded in %s: move that folder to %s and write the new path there, or start without the override", recorded, p.ScratchRecord, want)
		}
		want = recorded
	}
	if err := checkScratch(want, p.Home); err != nil {
		return err
	}
	if recorded == "" {
		// Published only if no other start published one first, which then
		// stands, and is compared like any recorded one.
		won, err := publishRecord(p.ScratchRecord, want)
		if err != nil {
			return err
		}
		if won != want {
			if asked {
				return fmt.Errorf("The scratch folder is %s, recorded in %s by another start: start without the override", won, p.ScratchRecord)
			}
			if err := checkScratch(won, p.Home); err != nil {
				return err
			}
			want = won
		}
	}
	p.Scratch = want
	return nil
}

// checkScratch makes the scratch folder at 0700 and refuses one that
// overlaps the state folder, by name before anything is made and by identity
// after.
func checkScratch(scratch, home string) error {
	overlap := fmt.Errorf("The scratch folder %s and the state folder %s overlap: choose a scratch folder apart from it", scratch, home)
	if nested(scratch, home) || nested(home, scratch) {
		return overlap
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return fmt.Errorf("Create the scratch folder %s: check the permissions on its parent", scratch)
	}
	if err := private(scratch); err != nil {
		return err
	}
	for _, pair := range [][2]string{{scratch, home}, {home, scratch}} {
		in, err := Under(pair[0], pair[1])
		if err != nil {
			return fmt.Errorf("Check the scratch folder %s against the state folder: %w", scratch, err)
		}
		if in {
			return overlap
		}
	}
	return nil
}

// readRecord reads the recorded scratch folder, empty when none is. The path
// is the file's content less the one line ending it was written with.
func readRecord(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("Read %s: check the permissions on the file", path)
	}
	recorded := strings.TrimSuffix(string(raw), "\n")
	if !filepath.IsAbs(recorded) || filepath.Clean(recorded) != recorded {
		return "", fmt.Errorf("%s does not hold an absolute folder path: write the scratch folder there, or remove the file", path)
	}
	return recorded, nil
}

// publishRecord writes the record unless one is already there, and answers
// the one that stands.
func publishRecord(path, want string) (string, error) {
	tmp := path + ".new-" + fmt.Sprint(os.Getpid())
	if err := WriteAtomic(tmp, []byte(want+"\n"), 0o600); err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	if err := os.Link(tmp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("Record the scratch folder in %s: %w", path, err)
	}
	if err := syncDirOf(path); err != nil {
		return "", fmt.Errorf("Record the scratch folder in %s: %w", path, err)
	}
	won, err := readRecord(path)
	if err != nil {
		return "", err
	}
	return won, nil
}

// private refuses a folder others than the user can read or enter, which a
// folder that was already there may be.
func private(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("Read %s: check the permissions on its parent", dir)
	}
	if info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o700 != 0o700 {
		return fmt.Errorf("%s must be the user's alone, to read, write and enter: set it with chmod 700 %q", dir, dir)
	}
	return nil
}

// syncDirOf flushes the folder holding path, so a name made in it survives a
// crash.
func syncDirOf(path string) error {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// nested reports whether the clean absolute path a is b or lies under it.
func nested(a, b string) bool {
	rel, err := filepath.Rel(filepath.Clean(b), a)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// EnsureDirs creates the state directories at 0700. An existing directory is
// left alone, mode included.
func (p Paths) EnsureDirs() error {
	for _, d := range []string{p.Home, p.Sessions, p.Files, p.Uploads, p.Log} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return errors.New("Create " + d + ": check the permissions on its parent")
		}
	}
	return nil
}
