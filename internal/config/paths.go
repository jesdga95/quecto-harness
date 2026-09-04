// Package config owns the state directory: config.json, allowlist.json and
// the atomic write used by everything that persists. Nothing is written into
// a project folder.
package config

import (
	"errors"
	"os"
	"path/filepath"
)

// Paths are the locations under the state directory.
type Paths struct {
	Home      string
	Config    string
	Allowlist string
	Sessions  string
	Log       string
	Token     string
	URL       string
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
	return DiscoverPathsIn(filepath.Join(home, ".quecto-harness")), nil
}

func DiscoverPathsIn(home string) Paths {
	return Paths{
		Home:      home,
		Config:    filepath.Join(home, "config.json"),
		Allowlist: filepath.Join(home, "allowlist.json"),
		Sessions:  filepath.Join(home, "sessions"),
		Log:       filepath.Join(home, "log"),
		Token:     filepath.Join(home, "token"),
		URL:       filepath.Join(home, "url"),
	}
}

// EnsureDirs creates the state directories at 0700. An existing directory is
// left alone, mode included.
func (p Paths) EnsureDirs() error {
	for _, d := range []string{p.Home, p.Sessions, p.Log} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return errors.New("create " + d + ": check the permissions on its parent")
		}
	}
	return nil
}
