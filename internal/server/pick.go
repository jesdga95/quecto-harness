package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// errNoDialog is returned when the host has no chooser installed.
var errNoDialog = errors.New("No file dialog on this system: install zenity, or type the path")

// folderPicker and filePicker are variables so the route can be exercised
// without opening a dialog on the machine running the tests.
var (
	folderPicker = pickFolder
	filePicker   = pickFiles
)

// postPick opens the host's own chooser and returns what the user picked, a
// folder by default and a file when the body asks for one. A dismissed dialog
// is a null path, not an error. The timeout is generous because a person is
// choosing.
func (s *Server) postPick(w http.ResponseWriter, r *http.Request) {
	// The body is optional and names the kind of chooser. Anything else, an
	// empty body included, opens the folder dialog.
	var body struct {
		Kind string `json:"kind"`
	}
	defer r.Body.Close()
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)

	ctx, cancel := context.WithTimeout(r.Context(), pickTimeout)
	defer cancel()

	// A file dialog takes several at once and answers with a list; the folder
	// dialog takes one and answers with it.
	if body.Kind == "file" {
		paths, err := filePicker(ctx)
		if !pickOK(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"paths": paths})
		return
	}
	path, err := folderPicker(ctx)
	if !pickOK(w, err) {
		return
	}
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{"path": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

// pickOK writes the failure a dialog reported, and reports whether the
// handler should carry on.
func pickOK(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errNoDialog):
		writeError(w, http.StatusNotImplemented, errNoDialog.Error())
		return false
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return false
	}
	return true
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

// chooseFiles asks for as many files as the user cares to select and prints
// one path per line, which is the only shape that survives a path with a
// space in it.
const chooseFiles = `set picked to choose file with prompt "Choose files to attach" with multiple selections allowed
set out to ""
repeat with item_ in picked
	set out to out & POSIX path of item_ & linefeed
end repeat
return out`

// pickFiles runs the platform's file chooser, which takes several files at
// once. An empty list means the user dismissed it.
func pickFiles(ctx context.Context) ([]string, error) {
	var raw string
	var err error
	switch {
	case runtime.GOOS == "darwin":
		raw, err = runDialogRaw(ctx, "osascript", "-e", chooseFiles)
	case hasBinary("zenity"):
		raw, err = runDialogRaw(ctx, "zenity", "--file-selection", "--multiple", "--separator=\n")
	case hasBinary("kdialog"):
		// kdialog separates several files with spaces, which a path can
		// contain, so it is asked for one.
		raw, err = runDialogRaw(ctx, "kdialog", "--getopenfilename", homeDir())
	default:
		return nil, errNoDialog
	}
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, line := range strings.Split(raw, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			out = append(out, filepath.Clean(p))
		}
	}
	return out, nil
}

func hasBinary(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// runDialog is one path, cleaned. osascript reports a folder with a trailing
// slash, and the path is a key everywhere else.
func runDialog(ctx context.Context, name string, args ...string) (string, error) {
	out, err := runDialogRaw(ctx, name, args...)
	if err != nil || out == "" {
		return "", err
	}
	return filepath.Clean(out), nil
}

// runDialogRaw treats a non-zero exit as a dismissal, which is what both
// zenity and osascript report when the user cancels.
func runDialogRaw(ctx context.Context, name string, args ...string) (string, error) {
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
	return strings.TrimRight(string(out), "\r\n"), nil
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
