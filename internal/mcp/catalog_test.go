package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCatalogPrivacyChangesTakeEffectWithoutRestart(t *testing.T) {
	s := fakeServer("local")
	m := newTestManager(t, s)
	seedCache(t, m, s, "private")
	key := filepath.Join(m.cacheDir, ".catalog-key")
	for _, item := range []struct {
		path             string
		exposed, private os.FileMode
	}{{key, 0644, 0600}, {m.cacheDir, 0755, 0700}} {
		if err := os.Chmod(item.path, item.exposed); err != nil {
			t.Fatal(err)
		}
		if got, ok := m.readCache(s, keyFor(s, "")); ok {
			t.Errorf("Read from exposed cache: %v", got)
		}
		if tmp := m.prepareCache(s, keyFor(s, ""), []Tool{}); tmp != "" {
			t.Errorf("Staged in exposed cache: %s", tmp)
		}
		if err := os.Chmod(item.path, item.private); err != nil {
			t.Fatal(err)
		}
		if got, ok := m.readCache(s, keyFor(s, "")); !ok || got[0].Name != "private" {
			t.Fatalf("After repair: %v", got)
		}
	}
}

func TestProjectLimitPreservesUnknownFilesAndTheLastStatusRead(t *testing.T) {
	s := fakeServer("local")
	s.Cwd = "{project}"
	m := newTestManager(t, s)
	for i := range 32 {
		project := fmt.Sprint(i)
		k := keyFor(s, project)
		if err := m.publishList(m.instanceFor(s, project), []Tool{{Name: project}}, 0); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(1500000000+int64(i), 0)
		if err := os.Chtimes(m.catalogPath(k), at, at); err != nil {
			t.Fatal(err)
		}
	}
	bad := m.catalogPath(keyFor(s, "broken"))
	if err := os.WriteFile(bad, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Unix(1200000000, 0)
	if err := os.Chtimes(bad, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.statusPath(s.Name)); err != nil {
		t.Fatal(err)
	}
	m.Close()
	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)
	fresh := NewManager(m.cacheDir, "", "test")
	defer fresh.Close()
	fresh.Configure([]Server{s})
	if got := fresh.Status("0")[0]; !got.Listed || got.Project != "0" {
		t.Errorf("Last catalog = %+v", got)
	}
	if err := fresh.publishList(fresh.instanceFor(s, "32"), []Tool{}, 0); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(bad); err != nil || string(b) != "{" || strings.Count(logged.String(), bad) != 1 {
		t.Errorf("Unknown catalog = %q, %v, diagnostic %q", b, err, logged.String())
	}
	for i := range 33 {
		_, err := os.Stat(m.catalogPath(keyFor(s, fmt.Sprint(i))))
		if (i != 1) != (err == nil) {
			t.Errorf("Project %d: %v", i, err)
		}
	}
}

func TestLegacyCatalogFollowsItsLaunchAcrossCaseVariants(t *testing.T) {
	upper, lower := fakeServer("Local"), fakeServer("local")
	lower.Command += "other"
	m := newTestManager(t, upper, lower)
	for _, name := range []string{"Local.json", ".last-Local.json"} {
		tmp := m.prepareCache(lower, keyFor(lower, ""), []Tool{{Name: "lower"}})
		if err := os.Rename(tmp, filepath.Join(m.cacheDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	m.Close()
	fresh := NewManager(m.cacheDir, "", "test")
	defer fresh.Close()
	fresh.Configure([]Server{upper, lower})
	if tools, ok := fresh.readCache(lower, keyFor(lower, "")); !ok || len(tools) != 1 || tools[0].Name != "lower" {
		t.Errorf("Migrated tools = %v", tools)
	}
	if !stateOf(fresh, "local").Listed || stateOf(fresh, "Local").Listed {
		t.Fatal("Listing belongs to the wrong server")
	}
}

func TestOverlappingManagersPreserveEachOthersLaunch(t *testing.T) {
	s := fakeServer("local")
	if dir := os.Getenv("QUECTO_TEST_PEER_CATALOG"); dir != "" {
		s.Command += "other"
		n := NewManager(dir, "", "test")
		defer n.Close()
		n.Configure([]Server{s})
		if stateOf(n, "local").Listed {
			t.Fatal("Listed another launch")
		}
		if err := n.publishList(n.instanceFor(s, ""), []Tool{{Name: "second"}}, 0); err != nil {
			t.Fatal(err)
		}
		if got := stateOf(n, "local"); !got.Listed || got.Tools[0] != "second" {
			t.Fatal(got)
		}
		return
	}
	m := newTestManager(t, s)
	if err := m.publishList(m.instanceFor(s, ""), []Tool{{Name: "first"}}, 0); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestOverlappingManagersPreserveEachOthersLaunch$", "-test.timeout=10s")
	cmd.Env = append(os.Environ(), "QUECTO_TEST_PEER_CATALOG="+m.cacheDir)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Peer: %v, %s", err, data)
	}
	if got, ok := m.readCache(s, keyFor(s, "")); !ok || got[0].Name != "first" {
		t.Fatalf("Peer lost its catalog: %v", got)
	}
}

func TestLegacyBindingCannotReplaceANewPublication(t *testing.T) {
	s := fakeServer("local")
	s.Cwd = "{project}"
	m := newTestManager(t, s)
	k := keyFor(s, "work")
	raw, err := json.Marshal(diskCatalog{Launch: m.launchDigest(s, k), catalogView: catalogView{Tools: []Tool{{Name: "old"}}}})
	if err != nil {
		t.Fatal(err)
	}
	path := m.catalogPath(k)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	n := NewManager(m.cacheDir, "", "test")
	defer n.Close()
	read := n.readFile
	started, resume := make(chan struct{}), make(chan struct{})
	first := true
	n.readFile = func(p string) ([]byte, error) {
		data, err := read(p)
		if first && p == path {
			first = false
			close(started)
			<-resume
		}
		return data, err
	}
	configured := make(chan struct{})
	go func() { n.Configure([]Server{s}); close(configured) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(resume)
		t.Fatal("Maintenance did not read the catalog")
	}
	published := make(chan error, 1)
	go func() { published <- m.publishList(m.instanceFor(s, "work"), []Tool{{Name: "new"}}, 0) }()
	select {
	case err := <-published:
		close(resume)
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(150 * time.Millisecond):
		close(resume)
		if err := <-published; err != nil {
			t.Fatal(err)
		}
	}
	<-configured
	if got, ok := m.readCache(s, k); !ok || got[0].Name != "new" {
		t.Fatalf("Last published listing = %v", got)
	}
}

func TestStartupWaitsForAMissingCatalogKey(t *testing.T) {
	s := fakeServer("local")
	m := newTestManager(t, s)
	seedCache(t, m, s, "saved")
	m.Close()
	key := filepath.Join(m.cacheDir, ".catalog-key")
	if err := os.Rename(key, key+".aside"); err != nil {
		t.Fatal(err)
	}
	fresh := NewManager(m.cacheDir, "", "test")
	defer fresh.Close()
	fresh.Configure([]Server{s})
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Fatalf("Maintenance replaced the key: %v", err)
	}
	if err := os.Rename(key+".aside", key); err != nil {
		t.Fatal(err)
	}
	if got, ok := fresh.readCache(s, keyFor(s, "")); !ok || len(got) != 1 || got[0].Name != "saved" {
		t.Fatal(got)
	}
}

func TestStartupMigratesOverAnObsoleteCatalog(t *testing.T) {
	s := fakeServer("local")
	m := newTestManager(t, s)
	seedCache(t, m, s, "obsolete")
	s.Command += "new"
	tmp := m.prepareCache(s, keyFor(s, ""), []Tool{{Name: "current"}})
	if err := os.Rename(tmp, filepath.Join(m.cacheDir, "local.json")); err != nil {
		t.Fatal(err)
	}
	m.Close()
	fresh := NewManager(m.cacheDir, "", "test")
	defer fresh.Close()
	fresh.Configure([]Server{s})
	if got, ok := fresh.readCache(s, keyFor(s, "")); !ok || len(got) != 1 || got[0].Name != "current" {
		t.Fatal(got)
	}
}

func TestCatalogWriteLockFailureNamesTheCacheOnce(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".catalog-write.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir, "", "test")
	defer m.Close()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	for range 2 {
		m.Configure([]Server{fakeServer("local")})
	}
	if strings.Count(buf.String(), "MCP disk cache unavailable") != 1 || !strings.Contains(buf.String(), dir) {
		t.Fatalf("Lock diagnostic = %q", buf.String())
	}
}
