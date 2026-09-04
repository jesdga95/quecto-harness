package service

import (
	"encoding/xml"
	"runtime"
	"strings"
	"testing"
)

func spec() Spec {
	return Spec{
		Binary: "/usr/local/bin/quecto-harness",
		Home:   "/home/jay/.quecto-harness",
		LogDir: "/home/jay/.quecto-harness/log",
		Path:   "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
	}
}

func TestPlistText(t *testing.T) {
	body := plistText(spec())
	for _, want := range []string{
		"<key>Label</key>", "io.quecto.harness",
		"<string>/usr/local/bin/quecto-harness</string>",
		"<string>--no-open</string>",
		"<key>KeepAlive</key>", "<key>RunAtLoad</key>",
		"/home/jay/.quecto-harness/log/harness.log",
		"/home/jay/.quecto-harness/log/harness.err",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plist does not contain %q", want)
		}
	}
	if strings.Contains(body, "--listen") {
		t.Error("plist carries --listen when none was asked for")
	}
	var v any
	if err := xml.Unmarshal([]byte(body), &v); err != nil {
		t.Errorf("plist is not valid XML: %v", err)
	}
}

func TestPlistCarriesListenAndEscapes(t *testing.T) {
	s := spec()
	s.Listen = "0.0.0.0:9000"
	s.Home = "/home/jay & co/.quecto-harness"
	body := plistText(s)
	if !strings.Contains(body, "<string>--listen</string>") || !strings.Contains(body, "<string>0.0.0.0:9000</string>") {
		t.Error("plist does not carry the listen address")
	}
	if strings.Contains(body, "jay & co") {
		t.Error("the ampersand was not escaped")
	}
	if !strings.Contains(body, "jay &amp; co") {
		t.Error("the escaped home directory is missing")
	}
	var v any
	if err := xml.Unmarshal([]byte(body), &v); err != nil {
		t.Errorf("plist with an ampersand is not valid XML: %v", err)
	}
}

func TestSystemdText(t *testing.T) {
	body := systemdText(spec())
	for _, want := range []string{
		"Description=quecto harness",
		"ExecStart=/usr/local/bin/quecto-harness --no-open",
		"WorkingDirectory=/home/jay/.quecto-harness",
		"Environment=PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"Restart=on-failure",
		"StandardOutput=append:/home/jay/.quecto-harness/log/harness.log",
		"WantedBy=default.target",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("unit does not contain %q", want)
		}
	}
}

func TestSystemdQuotesAPathWithASpace(t *testing.T) {
	s := spec()
	s.Binary = "/home/jay/my tools/quecto-harness"
	body := systemdText(s)
	if !strings.Contains(body, `ExecStart="/home/jay/my tools/quecto-harness" --no-open`) {
		t.Errorf("ExecStart was not quoted:\n%s", body)
	}
}

func TestPathWithPrependsTheBinaryDirectory(t *testing.T) {
	if got := pathWith("/home/jay/go/bin"); got != "/home/jay/go/bin:"+DefaultPath {
		t.Errorf("pathWith = %q", got)
	}
	if got := pathWith("/usr/local/bin"); got != DefaultPath {
		t.Errorf("pathWith duplicated a directory already on PATH: %q", got)
	}
}

func TestTemporaryBinaryIsRefused(t *testing.T) {
	for _, p := range []string{"/tmp/go-build123/quecto-harness", "/var/folders/x/T/quecto-harness"} {
		if !isTemporary(p) {
			t.Errorf("%s was not recognised as temporary", p)
		}
	}
	if isTemporary("/usr/local/bin/quecto-harness") {
		t.Error("an installed binary was called temporary")
	}
}

func TestUnitTextMatchesThePlatform(t *testing.T) {
	path, body, err := UnitText(spec())
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "darwin":
		if !strings.HasSuffix(path, "Library/LaunchAgents/io.quecto.harness.plist") {
			t.Errorf("path = %q", path)
		}
		if !strings.HasPrefix(body, "<?xml") {
			t.Error("body is not a plist")
		}
	case "linux":
		if !strings.HasSuffix(path, ".config/systemd/user/quecto-harness.service") {
			t.Errorf("path = %q", path)
		}
		if !strings.HasPrefix(body, "[Unit]") {
			t.Error("body is not a systemd unit")
		}
	}
}

func TestParseLaunchctl(t *testing.T) {
	out := `io.quecto.harness = {
	active count = 1
	state = running
	pid = 4711
	program = /usr/local/bin/quecto-harness
}`
	running, detail := ParseLaunchctl(out)
	if !running || detail != "state = running" {
		t.Errorf("ParseLaunchctl = %v %q", running, detail)
	}
	running, _ = ParseLaunchctl("could not find service")
	if running {
		t.Error("a missing service was reported running")
	}
}
