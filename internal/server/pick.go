package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// errNoDialog is returned when the host has no folder chooser installed.
var errNoDialog = errors.New("No folder dialog on this system: install zenity, or type the path")

// folderPicker is a variable so the route can be exercised without opening a
// dialog on the machine running the tests.
var folderPicker = pickFolder

// postPick opens the host's own folder chooser and returns what the user
// picked. A dismissed dialog is a null path, not an error. The timeout is
// generous because a person is choosing.
func (s *Server) postPick(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), pickTimeout)
	defer cancel()

	path, err := folderPicker(ctx)
	switch {
	case errors.Is(err, errNoDialog):
		writeError(w, http.StatusNotImplemented, errNoDialog.Error())
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"path": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

// pickFolder runs the platform's dialog. An empty path means the user
// dismissed it.
func pickFolder(ctx context.Context) (string, error) {
	if runtime.GOOS == "darwin" {
		return runDialog(ctx, "osascript", "-e",
			`POSIX path of (choose folder with prompt "Choose a project")`)
	}
	if _, err := exec.LookPath("zenity"); err == nil {
		return runDialog(ctx, "zenity", "--file-selection", "--directory")
	}
	if _, err := exec.LookPath("kdialog"); err == nil {
		home := homeDir()
		return runDialog(ctx, "kdialog", "--getexistingdirectory", home)
	}
	return "", errNoDialog
}

// runDialog treats a non-zero exit as a dismissal, which is what both zenity
// and osascript report when the user cancels.
func runDialog(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if noGUISession(string(exit.Stderr)) {
				return "", errors.New("The folder dialog cannot open: this process has no desktop session, type the path instead")
			}
			return "", nil
		}
		if errors.Is(err, exec.ErrNotFound) {
			return "", errNoDialog
		}
		if ctx.Err() != nil {
			return "", errors.New("The folder dialog did not answer in time: type the path instead")
		}
		return "", errors.New("Open the folder dialog: " + err.Error())
	}
	// osascript reports a folder with a trailing slash. The path is a key
	// everywhere else, so it is cleaned before it leaves this package.
	path := strings.TrimRight(string(out), "\r\n")
	if path == "" {
		return "", nil
	}
	return filepath.Clean(path), nil
}

// noGUISession recognises the refusals a headless session produces, so the
// message says that rather than reporting a cancelled dialog.
func noGUISession(stderr string) bool {
	s := strings.ToLower(stderr)
	for _, marker := range []string{
		"cannot open display",
		"no display",
		"not allowed to send apple events",
		"application isn't running",
		"no protocol specified",
		"gtk initialization failed",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "/"
}
