package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/llm"
)

func ids(list []Summary) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, v.ID)
	}
	return out
}

func sample(id string, updated time.Time) *Session {
	code, cached := 0, 3968
	return &Session{
		ID: id, Project: "/proj", Agent: "agt_alpha", Model: "Alpha-12B", Title: "list the files here",
		ContextBase: &ContextBase{System: 154, Rules: 287, Tools: 1026},
		Created:     updated.Add(-time.Minute), Updated: updated,
		NCtx: 131072, NCtxSource: "props", Compactions: 1,
		LastUsage: &llm.Usage{PromptTokens: 4193, CompletionTokens: 118, TotalTokens: 4311, CachedTokens: &cached},
		Entries: []Entry{
			{Seq: 1, Kind: "user", Role: "user", Content: "list the files here", InContext: false, Visible: true, At: updated},
			{Seq: 2, Kind: "assistant", Role: "assistant", Reasoning: "bash with ls",
				ToolCalls: []llm.ToolCall{{ID: "call_0", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}}},
				InContext: false, Visible: true, At: updated},
			{Seq: 3, Kind: "tool", Role: "tool", ToolCallID: "call_0", ToolName: "bash",
				Content: "AGENTS.md\n\nexit code: 0", ExitCode: &code,
				Permission: &PermissionRecord{ID: "perm_1", Tool: "bash", Command: "ls", Decision: "always", Prefix: "ls"},
				InContext:  false, Visible: true, At: updated},
			{Seq: 4, Kind: "compaction", Role: "user", Content: "Summary of earlier work:\nx\n\nContinue.",
				InContext: true, Visible: true, At: updated, Meta: map[string]any{"before": 104857.0, "after": 3120.0, "n": 1.0}},
			{Seq: 5, Kind: "error", Content: "turn limit reached", InContext: false, Visible: true, At: updated},
			{Seq: 6, Kind: "status", Content: "Stopped", InContext: false, Visible: true, At: updated},
		},
	}
}

func TestRoundTripEveryEntryKind(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := sample(NewID(), time.Now().UTC().Truncate(time.Millisecond))
	if err := s.Save(in); err != nil {
		t.Fatal(err)
	}
	out, err := s.Load(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(in)
	b, _ := json.Marshal(out)
	if string(a) != string(b) {
		t.Errorf("round trip differs:\n%s\n%s", a, b)
	}
	if !strings.Contains(string(a), `"in_context"`) || !strings.Contains(string(a), `"visible"`) {
		t.Error("in_context and visible must be explicit in the file")
	}
	info, err := os.Stat(filepath.Join(dir, in.ID+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v %v, want 0600", info, err)
	}
}

func TestIndexBuiltAtOpen(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	now := time.Now().UTC()
	s.Save(sample(NewID(), now.Add(-time.Hour)))
	s.Save(sample(NewID(), now))
	other := sample(NewID(), now)
	other.Project = "/other"
	s.Save(other)

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.List(""); len(got) != 3 {
		t.Fatalf("List(\"\") = %d entries", len(got))
	}
	got := reopened.List("/proj")
	if len(got) != 2 {
		t.Fatalf("List(/proj) = %d entries", len(got))
	}
	if !got[0].Updated.After(got[1].Updated) {
		t.Error("List is not newest first")
	}
	if got[0].Messages != 6 || got[0].Bytes == 0 {
		t.Errorf("summary = %+v", got[0])
	}
}

func TestCorruptFileIsSkipped(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	good := sample(NewID(), time.Now())
	s.Save(good)
	bad := "sess_" + strings.Repeat("a", 16) + ".json"
	os.WriteFile(filepath.Join(dir, bad), []byte("{not json"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o600)

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("a corrupt file must not stop Open: %v", err)
	}
	if got := reopened.List(""); len(got) != 1 || got[0].ID != good.ID {
		t.Errorf("List = %+v", got)
	}
}

func TestLoadAndDeleteUnknown(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.Load("sess_aaaaaaaaaaaaaaaa"); err != ErrNotFound {
		t.Errorf("Load unknown = %v", err)
	}
	if _, err := s.Load("../../etc/passwd"); err != ErrNotFound {
		t.Errorf("Load with a path = %v, want ErrNotFound", err)
	}
	if err := s.Delete("sess_aaaaaaaaaaaaaaaa"); err != ErrNotFound {
		t.Errorf("Delete unknown = %v", err)
	}
}

func TestDeleteRemovesFileAndIndex(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	sess := sample(NewID(), time.Now())
	s.Save(sess)
	if err := s.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, sess.ID+".json")); err == nil {
		t.Error("the file is still there")
	}
	if len(s.List("")) != 0 {
		t.Error("the index still holds the session")
	}
}

func TestConcurrentSave(t *testing.T) {
	s, _ := Open(t.TempDir())
	a, b := sample(NewID(), time.Now()), sample(NewID(), time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sess := a
			if i%2 == 0 {
				sess = b
			}
			if err := s.Save(sess); err != nil {
				t.Error(err)
			}
			s.List("/proj")
		}(i)
	}
	wg.Wait()
	if len(s.List("")) != 2 {
		t.Errorf("index = %d entries", len(s.List("")))
	}
}

func TestNewIDAndTitle(t *testing.T) {
	id := NewID()
	if !namePattern.MatchString(id + ".json") {
		t.Errorf("id %q does not match the file name pattern", id)
	}
	if got := Title("first line\nsecond line"); got != "first line second line" {
		t.Errorf("Title = %q", got)
	}
	long := strings.Repeat("x", 100)
	if got := Title(long); len([]rune(got)) != 60 {
		t.Errorf("Title kept %d runes, want 60", len([]rune(got)))
	}
}

// TestUploadsPruneAndTrim asserts that a pasted file nobody sent is cleared
// out a day later, that a fresh one is left alone, and that a name too long
// for a filesystem is cut down with its extension kept.
func TestUploadsPruneAndTrim(t *testing.T) {
	uploads, err := OpenUploads(filepath.Join(t.TempDir(), "uploads"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := uploads.Save("Pasted text.txt", []byte("stale"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := uploads.Save("Pasted image.png", []byte("recent"))
	if err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-uploadLife - time.Hour)
	if err := os.Chtimes(filepath.Dir(old), stale, stale); err != nil {
		t.Fatal(err)
	}

	uploads.Prune()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a day-old paste is still there: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a paste just written was pruned: %v", err)
	}

	long, err := uploads.Save(strings.Repeat("n", 300)+".txt", []byte("long name"))
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(long)
	if len(name) != maxUploadName || !strings.HasSuffix(name, ".txt") {
		t.Errorf("trimmed name = %q (%d bytes)", name, len(name))
	}
}

func TestFilesRoundTripAndGuardTheID(t *testing.T) {
	dir := t.TempDir()
	files, err := OpenFiles(filepath.Join(dir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(src, []byte("not really a png"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, size, err := files.Put(src)
	if err != nil {
		t.Fatal(err)
	}
	if size != 16 {
		t.Errorf("size = %d, want 16", size)
	}
	raw, err := files.Read(id)
	if err != nil || string(raw) != "not really a png" {
		t.Errorf("read back %q, err %v", raw, err)
	}

	// Nothing but an id this store minted can name a file.
	for _, bad := range []string{"", "..", "../config.json", "file_x", id + "/../../config.json"} {
		if _, err := files.Path(bad); err == nil {
			t.Errorf("Path(%q) resolved", bad)
		}
	}

	if err := files.Remove(id); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Read(id); err == nil {
		t.Error("the file survived the remove")
	}
	if err := files.Remove(id); err != nil {
		t.Errorf("removing a file that is already gone: %v", err)
	}
}

func TestSessionCarriesItsAgentAndBase(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(sample("sess_aaaaaaaaaaaaaaaa", time.Now())); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load("sess_aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "agt_alpha" {
		t.Errorf("agent = %q, want agt_alpha", got.Agent)
	}
	if got.ContextBase == nil {
		t.Fatalf("context base = nil, want the estimate that was saved")
	}
	if got.ContextBase.Rules != 287 {
		t.Errorf("context base rules = %d, want 287", got.ContextBase.Rules)
	}
	if list := reopened.List("/proj"); len(list) != 1 || list[0].Agent != "agt_alpha" {
		t.Errorf("summary agent = %+v", list)
	}
}

// A subagent run is a session with a launcher. It is not a conversation
// anybody started, so it is not in the list, and it is reachable only by
// asking its launcher for it.
func TestChildSessionsAreListedOnlyUnderTheirLauncher(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	parent := sample("sess_aaaaaaaaaaaaaaaa", now)
	child := sample("sess_bbbbbbbbbbbbbbbb", now.Add(time.Second))
	child.Parent = parent.ID
	child.Entries = append(child.Entries, Entry{Seq: 7, Kind: "user", Role: "user",
		Content: "review it", From: "Reviewer", InContext: true, Visible: true, At: now})
	other := sample("sess_cccccccccccccccc", now.Add(2*time.Second))
	for _, doc := range []*Session{parent, child, other} {
		if err := s.Save(doc); err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []*Sessions{s, reopened} {
		list := st.List("/proj")
		if len(list) != 2 {
			t.Fatalf("list = %d sessions, want the two nobody launched", len(list))
		}
		for _, v := range list {
			if v.ID == child.ID {
				t.Errorf("the sidebar listed a subagent run: %+v", v)
			}
		}
		kids := st.Children(parent.ID)
		if len(kids) != 1 || kids[0].ID != child.ID || kids[0].Parent != parent.ID {
			t.Errorf("children = %+v, want the one child", kids)
		}
		if kids := st.Children(other.ID); len(kids) != 0 {
			t.Errorf("a session that launched nothing has children: %+v", kids)
		}
		if kids := st.Children(""); kids != nil {
			t.Errorf("an empty id matched every top-level session: %+v", kids)
		}
	}

	back, err := reopened.Load(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Parent != parent.ID {
		t.Errorf("parent = %q, want %q", back.Parent, parent.ID)
	}
	last := back.Entries[len(back.Entries)-1]
	if last.From != "Reviewer" {
		t.Errorf("from = %q, want the agent that sent it", last.From)
	}
}

// TestListIsNewestFirst asserts that a project's conversations come out of
// List newest first, that a subagent run is not one of them, and that the
// order survives a reopen.
func TestListIsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := sample(NewID(), now.Add(-2*time.Hour))
	b := sample(NewID(), now.Add(-time.Hour))
	c := sample(NewID(), now)
	for _, doc := range []*Session{a, b, c} {
		if err := s.Save(doc); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := ids(s.List("/proj")), []string{c.ID, b.ID, a.ID}; !slices.Equal(got, want) {
		t.Fatalf("list = %v, want newest first %v", got, want)
	}

	// A conversation created later leads the list.
	d := sample(NewID(), now.Add(time.Hour))
	if err := s.Save(d); err != nil {
		t.Fatal(err)
	}
	if got, want := ids(s.List("/proj")), []string{d.ID, c.ID, b.ID, a.ID}; !slices.Equal(got, want) {
		t.Errorf("list with a new conversation = %v, want %v", got, want)
	}

	// A subagent run is not a conversation, so it is not listed.
	child := sample(NewID(), now.Add(2*time.Hour))
	child.Parent = a.ID
	if err := s.Save(child); err != nil {
		t.Fatal(err)
	}
	if got, want := ids(s.List("/proj")), []string{d.ID, c.ID, b.ID, a.ID}; !slices.Equal(got, want) {
		t.Errorf("the list moved for a subagent run: %v", got)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(reopened.List("/proj")), []string{d.ID, c.ID, b.ID, a.ID}; !slices.Equal(got, want) {
		t.Errorf("the reopened store lists %v, want the newest-first order %v", got, want)
	}
}
