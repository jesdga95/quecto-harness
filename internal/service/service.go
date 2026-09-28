// Package service writes and loads the OS unit. The unit text is a pure
// function of a few paths, so both formats can be asserted on either
// platform.
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"quecto-harness/internal/config"
)

const label = "io.quecto.harness"

// Spec is everything the unit text needs.
type Spec struct {
	Binary string
	Home   string
	LogDir string
	Path   string
	Listen string
}

type Status struct {
	Installed bool
	Running   bool
	Unit      string
	Detail    string
	URL       string
}

// DefaultPath is what a service-managed process gets, since it inherits
// almost no environment and bash needs a usable PATH.
const DefaultPath = "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// NewSpec resolves the running binary and the paths the unit refers to.
func NewSpec(home, listen string) (Spec, error) {
	exe, err := os.Executable()
	if err != nil {
		return Spec{}, errors.New("Find the running binary: install quecto-harness somewhere permanent and run it from there")
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if isTemporary(exe) {
		return Spec{}, fmt.Errorf("The binary is at %s: install it somewhere permanent first, for example /usr/local/bin", exe)
	}
	return Spec{
		Binary: exe,
		Home:   home,
		LogDir: filepath.Join(home, "log"),
		Path:   pathWith(filepath.Dir(exe)),
		Listen: listen,
	}, nil
}

func isTemporary(p string) bool {
	for _, prefix := range []string{"/tmp/", "/var/folders/", "/private/var/folders/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func pathWith(dir string) string {
	for _, part := range strings.Split(DefaultPath, ":") {
		if part == dir {
			return DefaultPath
		}
	}
	return dir + ":" + DefaultPath
}

// UnitPath is where the unit for this platform belongs.
func UnitPath(home string) (string, error) {
	base, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("Find your home directory: the unit has nowhere to live")
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(base, "Library", "LaunchAgents", label+".plist"), nil
	case "linux":
		return filepath.Join(base, ".config", "systemd", "user", "quecto-harness.service"), nil
	}
	return "", fmt.Errorf("No service support on %s: run quecto-harness yourself", runtime.GOOS)
}

// UnitText renders the unit without touching the filesystem.
func UnitText(s Spec) (string, string, error) {
	path, err := UnitPath(s.Home)
	if err != nil {
		return "", "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return path, plistText(s), nil
	case "linux":
		return path, systemdText(s), nil
	}
	return "", "", fmt.Errorf("No service support on %s: run quecto-harness yourself", runtime.GOOS)
}

func args(s Spec) []string {
	out := []string{s.Binary, "--no-open"}
	if s.Listen != "" {
		out = append(out, "--listen", s.Listen)
	}
	return out
}

// plistText renders the launchd agent. Every string is XML-escaped, so a home
// directory containing an ampersand still produces a valid plist.
func plistText(s Spec) string {
	home, _ := os.UserHomeDir()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	b.WriteString("\t<key>Label</key>\n\t<string>" + xmlEscape(label) + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, a := range args(s) {
		b.WriteString("\t\t<string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n\t<true/>\n")
	b.WriteString("\t<key>ProcessType</key>\n\t<string>Background</string>\n")
	b.WriteString("\t<key>WorkingDirectory</key>\n\t<string>" + xmlEscape(s.Home) + "</string>\n")
	b.WriteString("\t<key>StandardOutPath</key>\n\t<string>" + xmlEscape(filepath.Join(s.LogDir, "harness.log")) + "</string>\n")
	b.WriteString("\t<key>StandardErrorPath</key>\n\t<string>" + xmlEscape(filepath.Join(s.LogDir, "harness.err")) + "</string>\n")
	b.WriteString("\t<key>EnvironmentVariables</key>\n\t<dict>\n")
	b.WriteString("\t\t<key>HOME</key>\n\t\t<string>" + xmlEscape(home) + "</string>\n")
	b.WriteString("\t\t<key>PATH</key>\n\t\t<string>" + xmlEscape(s.Path) + "</string>\n")
	b.WriteString("\t</dict>\n</dict>\n</plist>\n")
	return b.String()
}

var xmlEscaper = strings.NewReplacer(
	"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;",
)

func xmlEscape(s string) string { return xmlEscaper.Replace(s) }

// systemdText renders the user unit. ExecStart is quoted as one value so a
// path containing a space survives.
func systemdText(s Spec) string {
	var exec strings.Builder
	for i, a := range args(s) {
		if i > 0 {
			exec.WriteString(" ")
		}
		exec.WriteString(quoteUnit(a))
	}
	return "[Unit]\n" +
		"Description=quecto harness\n" +
		"After=network-online.target\n" +
		"\n[Service]\n" +
		"Type=simple\n" +
		"ExecStart=" + exec.String() + "\n" +
		"WorkingDirectory=" + s.Home + "\n" +
		"Environment=PATH=" + s.Path + "\n" +
		"Restart=on-failure\n" +
		"RestartSec=3\n" +
		"StandardOutput=append:" + filepath.Join(s.LogDir, "harness.log") + "\n" +
		"StandardError=append:" + filepath.Join(s.LogDir, "harness.err") + "\n" +
		"\n[Install]\n" +
		"WantedBy=default.target\n"
}

func quoteUnit(s string) string {
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// Install writes the unit and hands it to the init system.
func Install(s Spec) error {
	path, body, err := UnitText(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("Create %s: check the permissions on its parent", filepath.Dir(path))
	}
	if err := config.WriteAtomic(path, []byte(body), 0o644); err != nil {
		return err
	}
	uid := strconv.Itoa(os.Getuid())
	switch runtime.GOOS {
	case "darwin":
		target := "gui/" + uid + "/" + label
		// bootout first so a reinstall over a loaded agent works, and enable
		// before bootstrap because enable acts on the service identifier.
		run("launchctl", "bootout", target)
		run("launchctl", "enable", target)
		if out, err := runOut("launchctl", "bootstrap", "gui/"+uid, path); err != nil {
			return fmt.Errorf("Load the launch agent: %s", strings.TrimSpace(out))
		}
		run("launchctl", "kickstart", target)
	case "linux":
		if out, err := runOut("systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("Reload the user units: %s", strings.TrimSpace(out))
		}
		if out, err := runOut("systemctl", "--user", "enable", "--now", "quecto-harness"); err != nil {
			return fmt.Errorf("Enable the unit: %s", strings.TrimSpace(out))
		}
	}
	return nil
}

// Uninstall unloads the unit and removes it. Configuration, the token and the
// sessions are left alone.
func Uninstall(home string) error {
	path, err := UnitPath(home)
	if err != nil {
		return err
	}
	uid := strconv.Itoa(os.Getuid())
	switch runtime.GOOS {
	case "darwin":
		run("launchctl", "bootout", "gui/"+uid+"/"+label)
	case "linux":
		run("systemctl", "--user", "disable", "--now", "quecto-harness")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Remove %s: check the permissions on the folder", path)
	}
	if runtime.GOOS == "linux" {
		run("systemctl", "--user", "daemon-reload")
	}
	os.Remove(filepath.Join(home, "url"))
	return nil
}

// Report says whether the unit is present and running.
func Report(home string) (Status, error) {
	path, err := UnitPath(home)
	if err != nil {
		return Status{}, err
	}
	st := Status{Unit: path}
	if _, err := os.Stat(path); err == nil {
		st.Installed = true
	}
	uid := strconv.Itoa(os.Getuid())
	switch runtime.GOOS {
	case "darwin":
		out, err := runOut("launchctl", "print", "gui/"+uid+"/"+label)
		if err == nil {
			st.Running, st.Detail = ParseLaunchctl(out)
		}
	case "linux":
		out, _ := runOut("systemctl", "--user", "is-active", "quecto-harness")
		st.Detail = strings.TrimSpace(out)
		st.Running = st.Detail == "active"
	}
	if raw, err := os.ReadFile(filepath.Join(home, "url")); err == nil {
		st.URL = strings.TrimSpace(string(raw))
	}
	return st, nil
}

// ParseLaunchctl reads a launchctl print block: a pid line means running, and
// the state line is the detail worth showing.
func ParseLaunchctl(out string) (bool, string) {
	running, detail := false, ""
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "pid = ") {
			running = true
			if detail == "" {
				detail = t
			}
		}
		if strings.HasPrefix(t, "state = ") {
			detail = t
		}
	}
	return running, detail
}

func run(name string, args ...string) {
	_, _ = runOut(name, args...)
}

func runOut(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
