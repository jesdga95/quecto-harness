package config

import (
	"encoding/json"
	"errors"
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
	if got := s.Config(); got.Listen != "127.0.0.1:7331" || got.Mode != ModeAsk || got.CompactAt != 0.8 {
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

// openStore is a store over a fresh state directory.
func openStore(t *testing.T) *Store {
	t.Helper()
	p := DiscoverPathsIn(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
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
		{"url", oneAgent(FileAgent{URL: ptr("ftp://x")}), func(c Config) bool { return c.Agents[0].URL == "" }, "address"},
		{"effort", oneAgent(FileAgent{Effort: ptr("extreme")}), func(c Config) bool { return c.Agents[0].Effort == EffortMedium }, "effort"},
		{"protocol", oneAgent(FileAgent{Protocol: ptr("grpc")}), func(c Config) bool { return c.Agents[0].Protocol == ProtocolLlama }, "protocol"},
		{"compact", File{CompactAt: ptr(1.5)}, func(c Config) bool { return c.CompactAt == 0.8 }, "compact_at"},
		{"shell", File{Shell: ptr("sh")}, func(c Config) bool { return c.Shell == "/bin/sh" }, "shell"},
		{"timeout", File{CommandTimeoutS: ptr(99999)}, func(c Config) bool { return c.CommandTimeout == 120*time.Second }, "command_timeout_s"},
		{"output", File{OutputLimitBytes: ptr(10)}, func(c Config) bool { return c.OutputLimitBytes == 16384 }, "output_limit_bytes"},
		{"temperature", oneAgent(FileAgent{Temperature: ptr(2.5)}), func(c Config) bool { return c.Agents[0].Temperature == nil }, "temperature"},
		{"top_p", oneAgent(FileAgent{TopP: ptr(-0.1)}), func(c Config) bool { return c.Agents[0].TopP == nil }, "top_p"},
		{"concurrency", oneAgent(FileAgent{Concurrency: ptr(0)}), func(c Config) bool { return c.Agents[0].Concurrency == DefaultConcurrency }, "concurrency"},
		{"max_context", oneAgent(FileAgent{MaxContext: ptr(64)}), func(c Config) bool { return c.Agents[0].MaxContext == nil }, "max_context"},
		{"selection", File{Agent: ptr("agt_gone")}, func(c Config) bool { return c.AgentID == "" }, "agt_gone"},
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

// oneAgent is a file holding a single named agent, so a clamp case names only
// the field it is about.
func oneAgent(a FileAgent) File {
	if a.ID == nil {
		a.ID = ptr("agt_aaaaaaaaaaaaaaaa")
	}
	if a.Name == nil {
		a.Name = ptr("Local")
	}
	return File{Agents: []FileAgent{a}}
}

// Sampling is the one setting with no default of its own: what is not set is
// written as no field, and reads back unset.
func TestSamplingRoundTrip(t *testing.T) {
	d := Defaults()
	c, notes := oneAgent(FileAgent{Temperature: ptr(0.35), TopP: ptr(0.0)}).Resolve(d)
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	if c.Agents[0].Temperature == nil || *c.Agents[0].Temperature != 0.35 {
		t.Errorf("temperature = %v, want 0.35", c.Agents[0].Temperature)
	}
	if c.Agents[0].TopP == nil || *c.Agents[0].TopP != 0 {
		t.Errorf("top_p = %v, want 0", c.Agents[0].TopP)
	}

	b, err := json.Marshal(c.File())
	if err != nil {
		t.Fatal(err)
	}
	var written struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(b, &written); err != nil {
		t.Fatal(err)
	}
	if len(written.Agents) != 1 || written.Agents[0]["temperature"] != 0.35 || written.Agents[0]["top_p"] != 0.0 {
		t.Errorf("written = %v", written.Agents)
	}

	c.Agents[0].Temperature, c.Agents[0].TopP = nil, nil
	b, err = json.Marshal(c.File())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "temperature") || strings.Contains(string(b), "top_p") {
		t.Errorf("an unset value was written anyway: %s", b)
	}
	back, notes := oneAgent(FileAgent{}).Resolve(d)
	if len(notes) != 0 || back.Agents[0].Temperature != nil || back.Agents[0].TopP != nil {
		t.Errorf("read back as %v, %v with notes %v", back.Agents[0].Temperature, back.Agents[0].TopP, notes)
	}
}

// The window a session is measured against comes from one of three places,
// and the override is the one that wins.
func TestAgentWindow(t *testing.T) {
	var a Agent
	if n, src := a.Window(131072, "props"); n != 131072 || src != "props" {
		t.Errorf("window = %d/%s, want what the server reported", n, src)
	}
	if n, src := a.Window(0, ""); n != 0 || src != "" {
		t.Errorf("window = %d/%s, want unknown", n, src)
	}
	a.MaxContext = ptr(200000)
	if n, src := a.Window(131072, "props"); n != 200000 || src != "agent" {
		t.Errorf("window = %d/%s, want the override", n, src)
	}
}

func TestResolveAcceptsEveryEffort(t *testing.T) {
	for _, e := range []string{"auto", "off", "low", "medium", "high"} {
		c, notes := oneAgent(FileAgent{Effort: ptr(e)}).Resolve(Defaults())
		if len(notes) != 0 {
			t.Errorf("%s: notes = %v", e, notes)
		}
		if string(c.Agents[0].Effort) != e {
			t.Errorf("effort = %q, want %q", c.Agents[0].Effort, e)
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
	if err := os.WriteFile(p.Config, []byte(`{"shell":"/bin/zsh","future_key":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Config().Shell != "/bin/zsh" {
		t.Errorf("shell = %q", s.Config().Shell)
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
	in.Mode = ModeFull
	in.Projects = []string{"/tmp/x"}
	in.Agents = []Agent{{
		ID: "agt_aaaaaaaaaaaaaaaa", Name: "Local", Enabled: true, URL: "http://127.0.0.1:8080",
		Key: "sk-secret", Protocol: ProtocolResponses, Model: "Alpha-12B", Effort: EffortAuto,
		PreserveReasoning: false, Concurrency: 4, Instructions: "Review Go, do not write it.",
	}}
	in.AgentID = "agt_aaaaaaaaaaaaaaaa"
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
	if out.Mode != in.Mode || out.CompactAt != in.CompactAt || out.AgentID != in.AgentID {
		t.Errorf("got %+v, want %+v", out, in)
	}
	got, ok := out.Agent()
	if !ok || got != in.Agents[0] {
		t.Errorf("agent = %+v, %v, want %+v", got, ok, in.Agents[0])
	}
	for _, key := range []string{"listen", "agents", "agent", "permissions", "compact_at", "shell",
		"command_timeout_s", "output_limit_bytes", "projects",
		"id", "name", "enabled", "url", "key", "protocol", "model", "effort", "preserve_reasoning",
		"concurrency", "description", "instructions"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("saved file omits %q, a reader would have to guess the default", key)
		}
	}
}

// The description is one line in a list every delegating agent reads, so what
// is stored is a line whatever was typed.
func TestDescriptionIsOneBoundedLine(t *testing.T) {
	long := strings.Repeat("x", MaxDescription+50)
	for _, tc := range []struct{ in, want string }{
		{"  Used for design work.  ", "Used for design work."},
		{"Used for design work.\nAnd nothing else.", "Used for design work."},
		{long, long[:MaxDescription]},
	} {
		c, _ := oneAgent(FileAgent{Description: ptr(tc.in)}).Resolve(Defaults())
		if got := c.Agents[0].Description; got != tc.want {
			t.Errorf("description = %q, want %q", got, tc.want)
		}
	}
}

func TestAgentWithoutConcurrencyWorksAlone(t *testing.T) {
	c, notes := oneAgent(FileAgent{}).Resolve(Defaults())
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	if c.Agents[0].Concurrency != 1 {
		t.Errorf("concurrency = %d, want 1", c.Agents[0].Concurrency)
	}
}

// main_only answers only as the selected agent. Absent takes false, and a
// true one is written out and read back.
func TestMainOnlyIsWrittenAndReadBack(t *testing.T) {
	c, notes := oneAgent(FileAgent{}).Resolve(Defaults())
	if len(notes) != 0 || c.Agents[0].MainOnly {
		t.Fatalf("an absent main_only = %+v, notes %v, want false and none", c.Agents[0], notes)
	}
	c.Agents[0].MainOnly = true
	raw, err := json.Marshal(c.File())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"main_only":true`) {
		t.Errorf("saved file omits a true main_only: %s", raw)
	}
	var back File
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	out, notes := back.Resolve(Defaults())
	if len(notes) != 0 || !out.Agents[0].MainOnly {
		t.Errorf("second resolve = %+v, notes %v, want the flag kept", out.Agents[0], notes)
	}
}

func TestAgentIDIsMinted(t *testing.T) {
	c, notes := File{Agents: []FileAgent{{Name: ptr("Local")}}}.Resolve(Defaults())
	if len(c.Agents) != 1 || !strings.HasPrefix(c.Agents[0].ID, "agt_") {
		t.Fatalf("agents = %+v", c.Agents)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], c.Agents[0].ID) {
		t.Errorf("notes = %v, want one naming the minted id", notes)
	}
	if NewAgentID() == NewAgentID() {
		t.Error("two minted ids were the same")
	}
}

func TestResolveDropsDuplicateAgentIDs(t *testing.T) {
	c, notes := File{Agents: []FileAgent{
		{ID: ptr("agt_one"), Name: ptr("First")},
		{ID: ptr("agt_one"), Name: ptr("Second")},
	}}.Resolve(Defaults())
	if len(c.Agents) != 1 || c.Agents[0].Name != "First" {
		t.Fatalf("agents = %+v", c.Agents)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "Second") {
		t.Errorf("notes = %v", notes)
	}
}

func TestAnAgentBeingFilledInIsNotAnError(t *testing.T) {
	c, notes := File{Agents: []FileAgent{{ID: ptr("agt_one"), Name: ptr("Local"), URL: ptr("")}},
		Agent: ptr("agt_one")}.Resolve(Defaults())
	if len(notes) != 0 {
		t.Errorf("notes = %v, an address not yet typed is not a bad value", notes)
	}
	if a, ok := c.Agent(); !ok || a.URL != "" {
		t.Errorf("agent = %+v, %v", a, ok)
	}
}

func TestUpdateDoesNotAliasAgents(t *testing.T) {
	s := openStore(t)
	temp := 0.5
	if _, _, err := s.AddAgent(Agent{Name: "Local", Enabled: true, Temperature: &temp}); err != nil {
		t.Fatal(err)
	}
	before := s.Config()
	if _, _, err := s.UpdateAgent(before.Agents[0].ID, func(a *Agent) {
		v := 1.5
		a.Name, a.Temperature = "Renamed", &v
	}); err != nil {
		t.Fatal(err)
	}
	if before.Agents[0].Name != "Local" || *before.Agents[0].Temperature != 0.5 {
		t.Errorf("a config taken before the write changed with it: %+v", before.Agents[0])
	}
}

func TestAgentStoreLifecycle(t *testing.T) {
	s := openStore(t)
	cfg, first, err := s.AddAgent(Agent{Name: "Local", Enabled: true, URL: "http://127.0.0.1:8080", Model: "Alpha-12B"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentID != first.ID {
		t.Errorf("the first agent was not selected: %q", cfg.AgentID)
	}
	if _, _, err := s.AddAgent(Agent{Name: "local"}); err == nil {
		t.Error("a name that differs only in case was accepted")
	}
	if _, _, err := s.AddAgent(Agent{Name: "  "}); err == nil {
		t.Error("a blank name was accepted")
	}

	_, second, err := s.AddAgent(Agent{Name: "Studio", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Config().AgentID != first.ID {
		t.Error("adding a second agent moved the selection")
	}
	if _, err := s.SelectAgent("agt_nothing"); !errors.Is(err, ErrNoSuchAgent) {
		t.Errorf("err = %v, want ErrNoSuchAgent", err)
	}

	cfg, err = s.RemoveAgent(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentID != second.ID {
		t.Errorf("removing the active agent left %q selected, want the one remaining", cfg.AgentID)
	}
	cfg, err = s.RemoveAgent(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Agents) != 0 || cfg.AgentID != "" {
		t.Errorf("removing the last agent left %+v", cfg)
	}
}

// A disabled agent keeps everything it was configured with, but stops being
// the one that answers.
func TestDisablingTheActiveAgentMovesTheSelection(t *testing.T) {
	s := openStore(t)
	_, first, err := s.AddAgent(Agent{Name: "Local", Enabled: true, URL: "http://127.0.0.1:8080", Model: "Alpha-12B"})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := s.AddAgent(Agent{Name: "Studio", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	cfg, stored, err := s.UpdateAgent(first.ID, func(a *Agent) { a.Enabled = false })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentID != second.ID {
		t.Errorf("disabling the active agent left %q selected, want the one still enabled", cfg.AgentID)
	}
	if stored.URL != first.URL || stored.Model != first.Model {
		t.Errorf("disabling an agent changed what it was configured with: %+v", stored)
	}
	if _, err := s.SelectAgent(first.ID); err == nil {
		t.Error("a disabled agent was selected")
	}

	// Nothing is left to hand over to, so the harness has no agent answering.
	cfg, _, err = s.UpdateAgent(second.ID, func(a *Agent) { a.Enabled = false })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentID != "" {
		t.Errorf("disabling the last agent left %q selected", cfg.AgentID)
	}
	if len(cfg.Agents) != 2 {
		t.Errorf("agents = %d, want both kept", len(cfg.Agents))
	}

	cfg, _, err = s.UpdateAgent(second.ID, func(a *Agent) { a.Enabled = true })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentID != second.ID {
		t.Errorf("enabling an agent again left %q selected", cfg.AgentID)
	}
}

func TestAgentKeyIsStoredAndInstructionsTrimmed(t *testing.T) {
	s := openStore(t)
	if _, _, err := s.AddAgent(Agent{Name: "Local", Key: "sk-secret", Instructions: "  Be brief.\n"}); err != nil {
		t.Fatal(err)
	}
	a := s.Config().Agents[0]
	if a.Key != "sk-secret" {
		t.Errorf("key = %q", a.Key)
	}
	if a.Instructions != "Be brief." {
		t.Errorf("instructions = %q, want them trimmed", a.Instructions)
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

func TestBadPermissionsFallsBackToAsk(t *testing.T) {
	bad := "yolo"
	got, notes := File{Permissions: &bad}.Resolve(Defaults())
	if got.Mode != ModeAsk {
		t.Errorf("mode = %q, want ask: an unreadable policy must be the safe one", got.Mode)
	}
	if len(notes) != 1 {
		t.Errorf("notes = %v, want one", notes)
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
			s.Update(func(c *Config) {
				if i%2 == 0 {
					c.Mode = ModeFull
				} else {
					c.Mode = ModeAsk
				}
			})
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

func TestSearchDefaults(t *testing.T) {
	d := Defaults()
	if d.Search.Provider != "" || d.Search.MaxHits != DefaultSearchMaxHits ||
		d.Search.MaxChars != DefaultSearchMaxChars || d.Search.Timeout != DefaultSearchTimeout {
		t.Errorf("search defaults = %+v", d.Search)
	}
	if len(d.Search.Accounts) != 1 || d.Search.Accounts[0].Name != "exa" ||
		d.Search.Accounts[0].URL != "https://api.exa.ai" || d.Search.Accounts[0].Key != "" {
		t.Errorf("search accounts = %+v", d.Search.Accounts)
	}
	if d.Search.Enabled() {
		t.Error("search is on before a provider is named")
	}
}

func TestResolveSearchClamps(t *testing.T) {
	d := Defaults()
	cases := []struct {
		name string
		file *FileSearch
		want func(Config) bool
		says string
	}{
		{"provider", &FileSearch{Provider: ptr("nope")},
			func(c Config) bool { return c.Search.Provider == "" }, "provider"},
		{"max_hits", &FileSearch{MaxHits: ptr(26)},
			func(c Config) bool { return c.Search.MaxHits == DefaultSearchMaxHits }, "max_hits"},
		{"max_hits_low", &FileSearch{MaxHits: ptr(0)},
			func(c Config) bool { return c.Search.MaxHits == DefaultSearchMaxHits }, "max_hits"},
		{"max_chars", &FileSearch{MaxChars: ptr(100)},
			func(c Config) bool { return c.Search.MaxChars == DefaultSearchMaxChars }, "max_chars"},
		{"timeout", &FileSearch{TimeoutS: ptr(121)},
			func(c Config) bool { return c.Search.Timeout == DefaultSearchTimeout }, "timeout_s"},
		{"url", &FileSearch{Providers: []FileSearchAccount{{Name: ptr("exa"), URL: ptr("ftp://x")}}},
			func(c Config) bool { return c.Search.Accounts[0].URL == "https://api.exa.ai" }, "address"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, notes := File{Search: tc.file}.Resolve(d)
			if !tc.want(c) {
				t.Errorf("value was not clamped: %+v", c.Search)
			}
			if len(notes) != 1 || !strings.Contains(notes[0], tc.says) {
				t.Errorf("notes = %v, want one mentioning %q", notes, tc.says)
			}
		})
	}
}

// A provider a build never wrote gets its account added on the next write,
// which is how a second provider arrives without a migration.
func TestResolveSearchBackfillsEveryProvider(t *testing.T) {
	d := Defaults()
	c, notes := File{Search: &FileSearch{
		Providers: []FileSearchAccount{{Name: ptr("nope"), URL: ptr("https://example.org")}},
	}}.Resolve(d)
	if len(notes) != 1 || !strings.Contains(notes[0], "dropped") {
		t.Errorf("notes = %v, want the unknown account dropped once", notes)
	}
	if len(c.Search.Accounts) != 1 || c.Search.Accounts[0].Name != "exa" ||
		c.Search.Accounts[0].URL != "https://api.exa.ai" {
		t.Errorf("accounts = %+v, want exa backfilled at its default", c.Search.Accounts)
	}
}

// A named provider keeps its key through a round trip, and naming it is what
// turns the tool on: no provider is off, a provider with no key is off, a key
// is on.
func TestSearchRoundTripKeepsTheKey(t *testing.T) {
	d := Defaults()
	c, notes := File{Search: &FileSearch{
		Provider:  ptr("exa"),
		MaxHits:   ptr(5),
		MaxChars:  ptr(8000),
		TimeoutS:  ptr(30),
		Providers: []FileSearchAccount{{Name: ptr("exa"), URL: ptr("https://api.exa.ai"), Key: ptr("sk-exa")}},
	}}.Resolve(d)
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	if !c.Search.Enabled() {
		t.Errorf("search is not on with a named provider and a key: %+v", c.Search)
	}
	a, ok := c.Search.Account()
	if !ok || a.Key != "sk-exa" || a.URL != "https://api.exa.ai" {
		t.Errorf("account = %+v (%v)", a, ok)
	}

	raw, err := json.Marshal(c.File())
	if err != nil {
		t.Fatal(err)
	}
	var written struct {
		Search struct {
			Provider  string `json:"provider"`
			MaxHits   int    `json:"max_hits"`
			MaxChars  int    `json:"max_chars"`
			TimeoutS  int    `json:"timeout_s"`
			Providers []struct {
				Name string `json:"name"`
				URL  string `json:"url"`
				Key  string `json:"key"`
			} `json:"providers"`
		} `json:"search"`
	}
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatal(err)
	}
	if written.Search.Provider != "exa" || written.Search.MaxHits != 5 ||
		written.Search.MaxChars != 8000 || written.Search.TimeoutS != 30 {
		t.Errorf("written = %+v", written.Search)
	}
	if len(written.Search.Providers) != 1 || written.Search.Providers[0].Key != "sk-exa" {
		t.Errorf("written providers = %+v, want the key kept", written.Search.Providers)
	}

	// Reading the written block back keeps everything, key included.
	back, notes := c.File().Resolve(d)
	acc, ok := back.Search.Account()
	if len(notes) != 0 || !back.Search.Enabled() || !ok || acc.Key != "sk-exa" {
		t.Errorf("read back %+v with notes %v", back.Search, notes)
	}
}

func TestSearchEnabledNeedsAKey(t *testing.T) {
	d := Defaults()
	c, _ := File{Search: &FileSearch{Provider: ptr("exa")}}.Resolve(d)
	if c.Search.Enabled() {
		t.Error("a named provider with an empty key is not on")
	}
	c2, _ := File{Search: &FileSearch{
		Provider:  ptr("exa"),
		Providers: []FileSearchAccount{{Name: ptr("exa"), Key: ptr("k")}},
	}}.Resolve(d)
	if !c2.Search.Enabled() {
		t.Error("a named provider with a key is on")
	}
}
