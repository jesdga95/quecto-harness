// Package config owns the state directory: config.json, allowlist.json,
// mcp.json and the atomic write used by everything that persists. Nothing is written into
// a project folder.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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
	// Scratch holds the folders made for conversations started without a
	// project. It lies outside Home, which the file tools never reach.
	Scratch string
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
	if dir := platformData(home); dir != "" {
		p.Scratch = filepath.Join(dir, "scratch-workspaces")
	}
	return p, nil
}

// platformData is the folder the platform gives an application for its own
// data, empty where there is none.
func platformData(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Quecto Harness")
	case "windows":
		if d := os.Getenv("LocalAppData"); d != "" {
			return filepath.Join(d, "Quecto Harness")
		}
		return ""
	default:
		if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
			return filepath.Join(d, "quecto-harness")
		}
		return filepath.Join(home, ".local", "share", "quecto-harness")
	}
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
		Scratch:   home + ".scratch-workspaces",
	}
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

// LockState claims the state directory for the life of the server.
func LockState(home string) (func(), error) { return lockDirectoryEntry(home, "lock") }

const sessionsLockName = ".sessions.lock"

// LockSessions also covers a sessions directory shared by separate state homes.
func LockSessions(dir string) (func(), error) { return lockDirectoryEntry(dir, sessionsLockName) }
