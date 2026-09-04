package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestDefaultsWhenFileIsAbsent(t *testing.T) {
	home := t.TempDir()
	p := DiscoverPathsIn(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Config(); got.Listen != "127.0.0.1:7331" || got.Effort != EffortMedium || got.CompactAt != 0.8 {
		t.Errorf("defaults not applied: %+v", got)
	}
	for _, f := range []string{p.Config, p.Allowlist} {
		info, err := os.Stat(f)
		if err != nil {
			t.Fatalf("%s was not written: %v", f, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", f, info.Mode().Perm())
		}
	}
}

func TestResolveClamps(t *testing.T) {
	d := Defaults()
	cases := []struct {
		name string
		file File
		want func(Config) bool
		says string
	}{
		{"listen", File{Listen: ptr("nonsense")}, func(c Config) bool { return c.Listen == d.Listen }, "listen"},
		{"port", File{Listen: ptr("127.0.0.1:0")}, func(c Config) bool { return c.Listen == d.Listen }, "listen"},
		{"url", File{Server: &Server{URL: ptr("ftp://x")}}, func(c Config) bool { return c.ServerURL == d.ServerURL }, "server.url"},
		{"effort", File{Effort: ptr("extreme")}, func(c Config) bool { return c.Effort == EffortMedium }, "effort"},
		{"compact", File{CompactAt: ptr(1.5)}, func(c Config) bool { return c.CompactAt == 0.8 }, "compact_at"},
		{"shell", File{Shell: ptr("sh")}, func(c Config) bool { return c.Shell == "/bin/sh" }, "shell"},
		{"timeout", File{CommandTimeoutS: ptr(99999)}, func(c Config) bool { return c.CommandTimeout == 120*time.Second }, "command_timeout_s"},
		{"output", File{OutputLimitBytes: ptr(10)}, func(c Config) bool { return c.OutputLimitBytes == 16384 }, "output_limit_bytes"},
		{"project", File{Projects: []string{"relative/path"}}, func(c Config) bool { return len(c.Projects) == 0 }, "project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, notes := tc.file.Resolve(d)
			if !tc.want(c) {
				t.Errorf("value was not clamped: %+v", c)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], tc.says) {
				t.Errorf("notes = %v, want one mentioning %q", notes, tc.says)
			}
		})
	}
}

func TestResolveAcceptsEveryEffort(t *testing.T) {
	for _, e := range []string{"auto", "off", "low", "medium", "high"} {
		c, notes := File{Effort: ptr(e)}.Resolve(Defaults())
		if len(notes) != 0 {
			t.Errorf("%s: notes = %v", e, notes)
		}
		if string(c.Effort) != e {
			t.Errorf("effort = %q, want %q", c.Effort, e)
		}
	}
}

func TestResolveDropsDuplicateProjects(t *testing.T) {
	c, notes := File{Projects: []string{"/a", "/a/", "/b"}}.Resolve(Defaults())
	if len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}
	if len(c.Projects) != 2 || c.Projects[0] != "/a" || c.Projects[1] != "/b" {
		t.Errorf("projects = %v", c.Projects)
	}
}

func TestUnknownKeysIgnored(t *testing.T) {
	home := t.TempDir()
	p := DiscoverPathsIn(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Config, []byte(`{"effort":"high","future_key":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Config().Effort != EffortHigh {
		t.Errorf("effort = %q", s.Config().Effort)
	}
}

func TestBadJSONStopsStartup(t *testing.T) {
	home := t.TempDir()
	p := DiscoverPathsIn(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.Config, []byte(`{"effort":`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(p)
	if err == nil {
		t.Fatal("a config that does not parse must stop startup")
	}
	if !strings.Contains(err.Error(), "delete the file") {
		t.Errorf("message = %q, it must say what to do", err.Error())
	}
}

func TestEmptyProjectsIsStillWritten(t *testing.T) {
	raw, err := json.Marshal(Defaults().File())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"projects":[]`) {
		t.Errorf("an empty project list was dropped from the file: %s", raw)
	}
}

func TestConfigFileRoundTrip(t *testing.T) {
	in := Defaults()
	in.Effort = EffortAuto
	in.Model = "Alpha-12B"
	in.Projects = []string{"/tmp/x"}
	raw, err := json.Marshal(in.File())
	if err != nil {
		t.Fatal(err)
	}
	var back File
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	out, notes := back.Resolve(Defaults())
	if len(notes) != 0 {
		t.Fatalf("round trip produced notes: %v", notes)
	}
	if out.Effort != in.Effort || out.Model != in.Model || out.CompactAt != in.CompactAt {
		t.Errorf("got %+v, want %+v", out, in)
	}
	for _, key := range []string{"listen", "server", "model", "effort", "auto", "compact_at", "shell", "command_timeout_s", "output_limit_bytes", "projects"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("saved file omits %q, a reader would have to guess the default", key)
		}
	}
}

func TestAllowMatchTable(t *testing.T) {
	home := t.TempDir()
	p := DiscoverPathsIn(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"git status", "go test", "ls", "rm"} {
		if err := s.AddAllow("/proj", prefix); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		cmd    string
		prefix string
		ok     bool
	}{
		{"git status", "git status", true},
		{"git status --short", "git status", true},
		{"git statusfoo", "", false},
		{"go test ./...", "go test", true},
		{"ls", "ls", true},
		{"ls  -la", "ls", true},
		{"rm -rf /", "rm", true},
		{"rmdir x", "", false},
		{"", "", false},
		{"git", "", false},
	}
	for _, tc := range cases {
		got, ok := s.AllowMatch("/proj", tc.cmd)
		if ok != tc.ok || got != tc.prefix {
			t.Errorf("AllowMatch(%q) = %q,%v, want %q,%v", tc.cmd, got, ok, tc.prefix, tc.ok)
		}
	}
	if _, ok := s.AllowMatch("/other", "ls"); ok {
		t.Error("a rule leaked into another project")
	}
}

func TestAllowlistLongestFirstAndPersisted(t *testing.T) {
	home := t.TempDir()
	p := DiscoverPathsIn(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"go", "go test", "  ", "go test ./..."} {
		s.AddAllow("/proj", prefix)
	}
	got := s.Allowlist("/proj")
	if len(got) != 3 || got[0] != "go test ./..." || got[2] != "go" {
		t.Errorf("order = %v, want longest first with the empty rule dropped", got)
	}
	reopened, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if match, _ := reopened.AllowMatch("/proj", "go test ./... -run X"); match != "go test ./..." {
		t.Errorf("after reopen the match was %q", match)
	}
	if err := s.RemoveAllow("/proj", "go test"); err != nil {
		t.Fatal(err)
	}
	if len(s.Allowlist("/proj")) != 2 {
		t.Errorf("remove left %v", s.Allowlist("/proj"))
	}
}

func TestWriteAtomicLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	if err := WriteAtomic(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("content = %q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, a temp file was left behind", len(entries))
	}
}

func TestWriteAtomicKeepsOldContentOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "x.json")
	if err := WriteAtomic(path, []byte("data"), 0o600); err == nil {
		t.Fatal("writing into a missing directory must fail")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("a failed write created the file")
	}
}

func TestConcurrentUpdate(t *testing.T) {
	home := t.TempDir()
	p := DiscoverPathsIn(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Update(func(c *Config) { c.Auto = i%2 == 0 })
			s.AddAllow("/proj", "cmd")
			_ = s.Config()
			_, _ = s.AllowMatch("/proj", "cmd x")
		}(i)
	}
	wg.Wait()
	if len(s.Allowlist("/proj")) != 1 {
		t.Errorf("allowlist = %v", s.Allowlist("/proj"))
	}
}

func TestAddProject(t *testing.T) {
	home := t.TempDir()
	p := DiscoverPathsIn(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	cfg, err := s.AddProject(proj)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(proj)
	if len(cfg.Projects) != 1 || cfg.Projects[0] != real {
		t.Errorf("projects = %v, want the symlink-resolved path %q", cfg.Projects, real)
	}
	if _, err := s.AddProject(proj); err == nil {
		t.Error("adding the same project twice must fail")
	}
	if _, err := s.AddProject(filepath.Join(proj, "missing")); err == nil {
		t.Error("adding a missing directory must fail")
	}
	cfg, err = s.RemoveProject(proj)
	if err != nil || len(cfg.Projects) != 0 {
		t.Errorf("remove left %v (%v)", cfg.Projects, err)
	}
}
