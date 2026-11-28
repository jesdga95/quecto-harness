package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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
		Entries: []Entry{
			{Seq: 1, Kind: "user", Role: "user", Content: "list the files here", InContext: false, Visible: true, At: updated},
			{Seq: 2, Kind: "assistant", Role: "assistant", Reasoning: "bash with ls",
				Usage: &CallUsage{Reported: true, Prompt: 4193, Completion: 118, Cached: &cached,
					ToolTokens: 1026, FirstMs: 640, GenMs: 2210, TotalMs: 2900, PerSecond: 53.4},
				ToolCalls: []llm.ToolCall{{ID: "call_0", Type: "function", Function: llm.FunctionCall{Name: "bash", Arguments: `{"command":"ls"}`}}},
				InContext: false, Visible: true, At: updated},
			{Seq: 3, Kind: "tool", Role: "tool", ToolCallID: "call_0", ToolName: "bash",
				Content: "AGENTS.md\n\nexit code: 0", ExitCode: &code,
				TodoNote:   "In hand: Add the handler. If that is finished, mark it with todo_write before moving on.",
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

// The fill the index carries is the last call the server reported on. A
// summary in context ends the search, since its own report and every one
// before it counted the messages it replaced, and a call that streamed no
// report says nothing.
func TestFillIsTheLastMeasuredCall(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess := sample(NewID(), time.Now().UTC())
	if i, u := LastMeasured(sess.Entries); u != nil {
		t.Errorf("last measured call = %d %+v, want none past a summary in context", i, u)
	}

	sess.Entries[3].InContext = false
	if i, u := LastMeasured(sess.Entries); i != 1 || u == nil {
		t.Errorf("last measured call = %d, want the reply again once the summary is dropped", i)
	}
	sess.Entries[3].InContext = true

	sess.Entries = append(sess.Entries,
		Entry{Seq: 7, Kind: "assistant", Role: "assistant", Content: "done", InContext: true, Visible: true,
			Usage: &CallUsage{Reported: true, Prompt: 3200, Completion: 40}},
		Entry{Seq: 8, Kind: "status", Content: "Stopped", Visible: true,
			Usage: &CallUsage{Stopped: true}})
	if err := s.Save(sess); err != nil {
		t.Fatal(err)
	}
	if i, u := LastMeasured(sess.Entries); i != 6 || u == nil || u.Prompt != 3200 {
		t.Errorf("last measured call = %d %+v, want the reply after the summary", i, u)
	}
	if got := s.List("/proj")[0].Fill; got != 3240 {
		t.Errorf("fill = %d, want the prompt and completion of that call", got)
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

// lockedOutput puts a saved output in a session's side folder that cannot be
// removed, and returns the function that lets it go.
func lockedOutput(t *testing.T, dir, id string) (folder string, release func()) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	folder = filepath.Join(dir, id+".files")
	spill := filepath.Join(folder, "spill")
	if err := os.MkdirAll(spill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spill, "out.txt"), []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(spill, 0o500); err != nil {
		t.Fatal(err)
	}
	release = func() { os.Chmod(spill, 0o700) }
	t.Cleanup(release)
	return folder, release
}

// A delete that cannot remove the side folder says so and keeps the session
// listed. A later delete retries it, and once the folder goes so does the
// session.
func TestADeleteThatLeavesTheFolderIsNotDone(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	if err := s.Save(sample(id, time.Now())); err != nil {
		t.Fatal(err)
	}
	folder, release := lockedOutput(t, dir, id)

	if err := s.Delete(id); !errors.Is(err, ErrLeftBehind) {
		t.Fatalf("Delete = %v, want the folder left behind", err)
	}
	if !s.Has(id) {
		t.Error("the session left the index although its folder stayed")
	}
	if err := s.Delete(id); !errors.Is(err, ErrLeftBehind) {
		t.Fatalf("the second delete = %v, want the folder left behind", err)
	}

	release()
	if err := s.Delete(id); err != nil {
		t.Fatalf("the delete after the fix = %v", err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the folder is still there: %v", err)
	}
	if s.Has(id) {
		t.Error("the session is still indexed")
	}
	if err := s.Delete(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("a delete once done = %v, want not found", err)
	}
}

// A folder a delete could not remove is removed at the next start, and by a
// delete of another session.
func TestALeftFolderIsRemovedLater(t *testing.T) {
	for _, by := range []string{"start", "another delete"} {
		t.Run(by, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			id, other := NewID(), NewID()
			for _, v := range []string{id, other} {
				if err := s.Save(sample(v, time.Now())); err != nil {
					t.Fatal(err)
				}
			}
			folder, release := lockedOutput(t, dir, id)
			if err := s.Delete(id); !errors.Is(err, ErrLeftBehind) {
				t.Fatalf("Delete = %v, want the folder left behind", err)
			}
			release()

			if by == "start" {
				if _, err := Open(dir); err != nil {
					t.Fatal(err)
				}
			} else if err := s.Delete(other); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(folder); !os.IsNotExist(err) {
				t.Errorf("the folder is still there: %v", err)
			}
		})
	}
}

// A delete whose cleanup cannot be recorded removes nothing: the session file
// stays, and so does its place in the index.
func TestADeleteThatCannotRecordItsCleanupKeepsTheSession(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	if err := s.Save(sample(id, time.Now())); err != nil {
		t.Fatal(err)
	}
	// A folder in the ledger's place fails its write.
	if err := os.MkdirAll(filepath.Join(dir, ledgerFile, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(id); err == nil {
		t.Fatal("the delete went through without its cleanup recorded")
	}
	if _, err := os.Stat(filepath.Join(dir, id+".json")); err != nil {
		t.Errorf("the session file went: %v", err)
	}
	if !s.Has(id) {
		t.Error("the session left the index")
	}
}

// An id in the ledger that is not a session's is never used on disk.
func TestALedgerIdThatIsNoSessionIsIgnored(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(base, "victim.files")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	led := `{"cleanup":{"../victim":{"parent":"","project":""}},"parents":{"../x":{"parent":"sess_aaaaaaaaaaaaaaaa","project":"/p"}}}`
	if err := os.WriteFile(filepath.Join(dir, ledgerFile), []byte(led), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a folder outside the sessions went: %v", err)
	}
	if got := s.Family("sess_aaaaaaaaaaaaaaaa"); len(got) != 0 {
		t.Errorf("family = %v, want nothing from a bad id", got)
	}
}

// A ledger that cannot be read stops the start and is left as it was, and one
// that does not parse is kept aside rather than written over.
func TestALedgerThatCannotBeReadIsKept(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	parent, child := NewID(), NewID()
	if err := s.Save(sample(parent, time.Now())); err != nil {
		t.Fatal(err)
	}
	run := sample(child, time.Now())
	run.Parent = parent
	if err := s.Save(run); err != nil {
		t.Fatal(err)
	}
	// The run's file says what a rebuilt ledger would hold, so a ledger
	// holding less shows whether it was written over.
	path := filepath.Join(dir, ledgerFile)
	want := []byte(`{"parents":{}}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Error("the start went on without the ledger")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(want) {
		t.Errorf("the unreadable ledger was written over: %s", got)
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path + ".unreadable-1"); err != nil || string(got) != "{not json" {
		t.Errorf("the ledger that did not parse = %q, %v, want it kept aside", got, err)
	}
}

// Each ledger that does not parse is kept aside under a name of its own, so a
// second one does not replace the first.
func TestEachUnparsedLedgerIsKeptAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ledgerFile)
	for _, body := range []string{"{first", "{second"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err != nil {
			t.Fatal(err)
		}
	}
	aside, err := filepath.Glob(path + ".unreadable*")
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, p := range aside {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, string(raw))
	}
	slices.Sort(kept)
	if !slices.Equal(kept, []string{"{first", "{second"}) {
		t.Errorf("kept aside = %q, want both ledgers", kept)
	}
}

// A ledger whose cleanup is the earlier list of ids is read, and the folders
// it names are still removed.
func TestALedgerWithACleanupListIsRead(t *testing.T) {
	dir := t.TempDir()
	id := NewID()
	folder := filepath.Join(dir, id+".files")
	if err := os.MkdirAll(filepath.Join(folder, "spill"), 0o700); err != nil {
		t.Fatal(err)
	}
	led := `{"cleanup":["` + id + `"]}`
	if err := os.WriteFile(filepath.Join(dir, ledgerFile), []byte(led), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("the folder the list named is still there: %v", err)
	}
	if aside, _ := filepath.Glob(filepath.Join(dir, ledgerFile+".unreadable*")); len(aside) != 0 {
		t.Errorf("the ledger was set aside as unreadable: %v", aside)
	}
}

func TestCleanupKeepsOutputWhileATranscriptLinkIsPresent(t *testing.T) {
	dir := t.TempDir()
	id, parent := NewID(), NewID()
	path := filepath.Join(dir, id+".json")
	if err := os.Symlink(filepath.Join(t.TempDir(), "transcript.json"), path); err != nil {
		t.Skipf("Cannot make a link: %v", err)
	}
	out := filepath.Join(dir, id+".files")
	if err := os.Mkdir(out, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"cleanup":{"` + id + `":{"parent":"` + parent + `"}}}`)
	if err := os.WriteFile(filepath.Join(dir, ledgerFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Family(parent); !slices.Contains(got, id) {
		t.Errorf("Family = %v, want %s", got, id)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("Saved output: %v", err)
	}
}

func TestALedgerLinkWithAMissingTargetStopsStartup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ledgerFile)
	if err := os.Symlink(filepath.Join(t.TempDir(), "ledger.json"), path); err != nil {
		t.Skipf("Cannot make a link: %v", err)
	}
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Open = %v, want an error naming the ledger", err)
	}
	if _, err := os.Readlink(path); err != nil {
		t.Fatal(err)
	}
}

func TestDamagedLedgerRecoversOnlyRecordedOutput(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, stray := NewID(), NewID()
	if err := s.Save(sample(id, time.Now())); err != nil {
		t.Fatal(err)
	}
	folder, release := lockedOutput(t, dir, id)
	if err := s.Delete(id); !errors.Is(err, ErrLeftBehind) {
		t.Fatal(err)
	}
	release()
	if err := os.Mkdir(s.filesDir(stray), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ledgerFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("Recorded output remains: %v", err)
	}
	if _, err := os.Stat(s.filesDir(stray)); err != nil {
		t.Errorf("Unrecorded output: %v", err)
	}
}

func TestDeleteKeepsTheSameSessionLock(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	if err := s.Save(sample(id, time.Now())); err != nil {
		t.Fatal(err)
	}
	lock := s.lockFor(id)
	// A caller may already hold a reference to the stripe when deletion finishes.
	checkWaits := func(op func() error) {
		t.Helper()
		lock.Lock()
		started := make(chan struct{})
		done := make(chan error, 1)
		go func() { close(started); done <- op() }()
		<-started
		select {
		case err := <-done:
			lock.Unlock()
			t.Fatalf("Operation completed while the lock was held: %v", err)
		case <-time.After(40 * time.Millisecond):
		}
		lock.Unlock()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	checkWaits(func() error { return s.Delete(id) })
	checkWaits(func() error { return s.Save(sample(id, time.Now())) })
}

func TestUnusableLedgerNamesTheRepair(t *testing.T) {
	for _, kind := range []string{"directory", "link"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ledgerFile)
			if kind == "directory" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
				t.Skip(err)
			}
			_, err := Open(dir)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), "permissions") {
				t.Fatalf("Open: %v", err)
			}
		})
	}
}

func TestOpenRepairsOnlyTheLedgerCopyThatNeedsIt(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	child := sample(NewID(), time.Now())
	child.Parent = NewID()
	if err := st.Save(child); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ledgerFile)
	old := time.Unix(1600000000, 0)
	for _, name := range []string{path, path + ".backup"} {
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for _, corrupt := range []bool{false, true} {
		if corrupt {
			if err := os.WriteFile(path+".backup", []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Open(dir); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{path, path + ".backup"} {
			fi, err := os.Stat(name)
			if err != nil {
				t.Fatal(err)
			}
			wantUnchanged := name == path || !corrupt
			if fi.ModTime().Equal(old) != wantUnchanged {
				t.Errorf("Unexpected timestamp for %s (backup changed: %t)", name, corrupt)
			}
		}
		a, _ := os.ReadFile(path)
		b, _ := os.ReadFile(path + ".backup")
		if string(a) != string(b) {
			t.Error("Copies differ after open")
		}
	}
	if err := os.Remove(st.path(child.ID)); err != nil {
		t.Fatal(err)
	}
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Family(child.Parent); len(got) != 0 {
		t.Errorf("Removed family remains: %v", got)
	}
}

func TestArchiveCancelsPendingOutputRemoval(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	if err := st.Save(sample(id, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(st.SpillDir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := st.markPending(id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.path(id), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArchiveUnreadable(id); err != nil {
		t.Fatal(err)
	}
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Pending(id) {
		t.Error("Archived transcript still has a cleanup order")
	}
	if _, err := os.Stat(st.SpillDir(id)); err != nil {
		t.Fatal(err)
	}
}

func TestOpenDistinguishesAnArchiveFromAPendingDeletion(t *testing.T) {
	for _, tc := range []struct {
		name         string
		version      int
		pending      bool
		archive      string
		keepOutput   bool
		keepRelation bool
	}{
		{name: "Earlier archive with a recorded launcher", keepOutput: true},
		{name: "Interrupted archive with pending cleanup", version: 1, pending: true, archive: ".unreadable-1", keepOutput: true},
		{name: "Interrupted archive with only a launcher", version: 1, archive: ".unreadable-1", keepOutput: true},
		{name: "Restored run deleted after an earlier archive", version: 1, pending: true},
		{name: "Earlier archive with a different pending name", version: 1, pending: true, archive: ".unreadable-2"},
		{name: "Missing restored run with a recorded launcher", version: 1, keepOutput: true, keepRelation: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			id, parent := NewID(), NewID()
			file := id + ".json"
			if err := os.WriteFile(filepath.Join(dir, file+".unreadable-1"), []byte("{broken"), 0600); err != nil {
				t.Fatal(err)
			}
			spill := filepath.Join(dir, id+".files", "spill")
			if err := os.MkdirAll(spill, 0700); err != nil {
				t.Fatal(err)
			}
			relations := map[string]any{id: map[string]string{"parent": parent, "project": "/project"}}
			record := map[string]any{"parents": relations}
			if tc.version != 0 {
				record["version"] = tc.version
			}
			if tc.archive != "" {
				record["archiving"] = file + tc.archive
			}
			if tc.pending {
				record["cleanup"] = relations
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ledgerFile), raw, 0600); err != nil {
				t.Fatal(err)
			}
			st, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(st.Family(parent), id) != tc.keepRelation || st.Pending(id) {
				t.Errorf("Family = %v, cleanup pending = %t", st.Family(parent), st.Pending(id))
			}
			if _, err := os.Stat(spill); (tc.keepOutput && err != nil) || (!tc.keepOutput && !os.IsNotExist(err)) {
				t.Errorf("Saved output after restart: %v, want kept %t", err, tc.keepOutput)
			}
		})
	}
}

func TestCleanupWithAnEmptyOrNullPrimaryLedger(t *testing.T) {
	for _, primary := range []string{"null", "{}", `{"version":1}`} {
		t.Run(primary, func(t *testing.T) {
			dir := t.TempDir()
			st, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			id := NewID()
			if err := st.Save(sample(id, time.Now())); err != nil {
				t.Fatal(err)
			}
			folder, release := lockedOutput(t, dir, id)
			if err := st.Delete(id); !errors.Is(err, ErrLeftBehind) {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ledgerFile)
			if primary == "null" {
				if err := st.clearFailedRemoval(id); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte(primary), 0600); err != nil {
				t.Fatal(err)
			}
			st, err = Open(dir)
			if err != nil || !st.Pending(id) {
				t.Fatalf("Lost pending cleanup: %v", err)
			}
			backup, err := readLedgerCopy(path + ".backup")
			if _, ok := backup.Cleanup[id]; err != nil || !ok {
				t.Fatalf("Lost backup order: %v", err)
			}
			release()
			st, err = Open(dir)
			if err != nil || st.Pending(id) {
				t.Fatalf("Cleanup did not settle: %v", err)
			}
			if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Output remains: %v", err)
			}
		})
	}
}

func TestEmptyLedgerKeepsUnconfirmedBackupOrdersForRecovery(t *testing.T) {
	dir := t.TempDir()
	id := NewID()
	path := filepath.Join(dir, ledgerFile)
	raw := []byte(`{"version":1,"cleanup":["` + id + `"]}`)
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".backup", raw, 0600); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, id+".files")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil || st.Pending(id) {
		t.Fatalf("Unconfirmed cleanup: %v", err)
	}
	if _, err := os.Stat(folder); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path + ".backup.divergent-1")
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("Backup evidence = %s, %v", saved, err)
	}
}

func TestFailedOutputOrderSurvivesLossOfBothLedgers(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, stray := NewID(), NewID()
	if err := s.Save(sample(id, time.Now())); err != nil {
		t.Fatal(err)
	}
	folder, release := lockedOutput(t, dir, id)
	if err := s.Delete(id); !errors.Is(err, ErrLeftBehind) {
		t.Fatal(err)
	}
	release()
	if err := os.Mkdir(s.filesDir(stray), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ledgerFile, ledgerFile + ".backup"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{folder, filepath.Join(dir, id+".cleanup")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Remaining %s: %v", path, err)
		}
	}
	if _, err := os.Stat(s.filesDir(stray)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckTranscriptsFindsDamageAndRepairedRuns(t *testing.T) {
	dir := t.TempDir()
	id, parent := NewID(), NewID()
	doc := sample(id, time.Now())
	doc.Parent = parent
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	damaged := slices.Clone(raw)
	damaged[0] = '!'
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.UnreadableSessions(); err != nil || !slices.Contains(ids, id) {
		t.Fatalf("Damaged = %v, %v", ids, err)
	}
	if !s.Has(id) {
		t.Fatal("Damaged transcript disappeared from the index")
	}
	items, err := s.RecoveryItems()
	if err != nil || len(items) != 0 {
		t.Fatalf("Unreadable run listed as readable: %v, %v", items, err)
	}
	// An unreadable run can predate the first ledger.
	for _, name := range []string{ledgerFile, ledgerFile + ".backup"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if ids, err := s.UnreadableSessions(); err != nil || len(ids) != 0 {
			t.Fatalf("Repaired = %v, %v", ids, err)
		}
		items, err := s.RecoveryItems()
		if err != nil || !slices.Contains(items, RecoveryItem{Name: id + ".json", ID: id, Kind: "run"}) {
			t.Fatalf("Recovery = %v, %v", items, err)
		}
		s, err = Open(dir)
		if err != nil {
			t.Fatal(err)
		}
	}
	doc.Parent = ""
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnreadableSessions(); err != nil {
		t.Fatal(err)
	}
	if got := s.Family(parent); slices.Contains(got, id) {
		t.Fatalf("Former launcher still owns the repaired session: %v", got)
	}

}

func BenchmarkCheckTranscripts(b *testing.B) {
	dir := b.TempDir()
	const count = 512
	payload := strings.Repeat("x", 256<<10)
	for range count {
		id := NewID()
		doc := sample(id, time.Now())
		doc.Entries[0].Content = payload
		raw, err := json.Marshal(doc)
		if err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".json"), raw, 0600); err != nil {
			b.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	for _, bench := range []string{"Changed", "Unchanged"} {
		b.Run(bench, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if bench == "Changed" {
					b.StopTimer()
					for id, v := range s.index {
						v.checked = nil
						s.index[id] = v
					}
					b.StartTimer()
				}
				if _, err := s.UnreadableSessions(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestDeleteReportsBothRemovalFailuresOnOneLine(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	marker := filepath.Join(s.dir, id+".cleanup")
	for _, p := range []string{s.path(id), marker} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "kept"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	err = s.Delete(id)
	if err == nil {
		t.Fatal("Deletion succeeded with nonempty directories")
	}
	message := err.Error()
	if strings.ContainsAny(message, "\r\n") || !strings.Contains(message, s.path(id)) || !strings.Contains(message, marker) {
		t.Fatalf("Deletion diagnostic = %q", message)
	}
	if !s.Pending(id) {
		t.Fatal("Cleanup order was lost")
	}
}

func TestRestoredSessionReportsWhyCleanupCannotSettle(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	if err := st.Save(sample(id, time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := st.markPending(id); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(st.dir, ledgerFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = st.FinishCleanup(id)
	if err == nil || !strings.HasPrefix(err.Error(), "Finish cleanup for session "+id) || !strings.Contains(err.Error(), ledgerFile) {
		t.Fatalf("Cleanup error = %v", err)
	}
}

func TestArchiveKeepsItsMarkerUntilTheRenameCommits(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	if err := st.markPending(id); err != nil {
		t.Fatal(err)
	}
	if err := st.recordFailedRemoval(id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.path(id), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := st.archive(id, filepath.Join(st.dir, "absent", "archive")); err == nil {
		t.Fatal("Rename succeeded")
	}
	marker := filepath.Join(st.dir, id+".cleanup")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	aside := st.path(id) + ".unreadable-1"
	st.led.Archiving = filepath.Base(aside)
	if err := st.writeLedgerLocked(false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(st.path(id), aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(st.filesDir(id), 0700); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st, err = Open(st.dir)
		if err != nil || st.Pending(id) {
			t.Fatalf("Pending after archive: %v", err)
		}
		if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stale marker: %v", err)
		}
		if _, err := os.Stat(st.filesDir(id)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLostLedgersDoNotApplyAnAmbiguousMarkerToArchivedOutput(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	if err := st.markPending(id); err != nil {
		t.Fatal(err)
	}
	if err := st.recordFailedRemoval(id); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(st.filesDir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.path(id)+".unreadable-1", []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ledgerFile, ledgerFile + ".backup"} {
		if err := os.WriteFile(filepath.Join(st.dir, name), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	st, err = Open(st.dir)
	if err != nil || st.Pending(id) {
		t.Fatalf("Ambiguous cleanup: %v", err)
	}
	if err := st.DeleteRetained(id+".json.unreadable-1", ""); err != nil {
		t.Fatal(err)
	}
	st, err = Open(st.dir)
	if err != nil || st.Pending(id) {
		t.Fatalf("Archive removal rearmed cleanup: %v", err)
	}
	if _, err := os.Stat(st.filesDir(id)); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRetriesThePreviousMarker(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, second := NewID(), NewID()
	for _, id := range []string{first, second} {
		if err := os.WriteFile(s.path(id), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := s.markPending(id); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(s.dir, first+".cleanup")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(marker, "keep")
	if err := os.WriteFile(blocker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	archive, err := s.ArchiveUnreadable(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRetained(filepath.Base(archive), ""); err == nil || !strings.Contains(err.Error(), marker) {
		t.Fatalf("Delete with unresolved marker = %v", err)
	}
	if _, err := Open(s.dir); err == nil || !strings.Contains(err.Error(), "move "+marker+" aside") {
		t.Fatalf("Startup with unresolved marker = %v", err)
	}
	if _, err := s.ArchiveUnreadable(second); err == nil || !strings.Contains(err.Error(), first) {
		t.Fatalf("Second archive = %v", err)
	}
	if s.led.Archiving != filepath.Base(archive) {
		t.Fatal("Lost the first archive")
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveUnreadable(second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stale marker = %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatal(err)
	}
}

func TestNewCleanupSupersedesArchiveOfTheSameSession(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	archive := id + ".json.unreadable-1"
	if err := os.WriteFile(filepath.Join(s.dir, archive), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	s.led.Archiving = archive
	folder, release := lockedOutput(t, s.dir, id)
	if err := s.markPending(id); err != nil {
		t.Fatal(err)
	}
	if err := s.recordFailedRemoval(id); err != nil {
		t.Fatal(err)
	}
	s, err = Open(s.dir)
	if err != nil || !s.Pending(id) {
		t.Fatalf("New cleanup was released: %v", err)
	}
	release()
	if _, err := s.FinishCleanup(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(folder); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Output remains", err)
	}
}

func TestEmptyLedgerQuarantinesAContradictedMarker(t *testing.T) {
	dir := t.TempDir()
	id, parent := NewID(), NewID()
	path := filepath.Join(dir, ledgerFile)
	put := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(path, "{}")
	put(path+".backup", fmt.Sprintf(`{"parents":{"%s":{"parent":"%s"}},"cleanup":{"%s":{"parent":"%s"}}}`, id, parent, id, parent))
	put(filepath.Join(dir, id+".cleanup"), fmt.Sprintf(`{"id":"%s"}`, id))
	put(filepath.Join(dir, id+".json"), "{")
	if err := os.Mkdir(filepath.Join(dir, id+".files"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.RecoveryItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{id + ".cleanup.ambiguous-1", ledgerFile + ".backup.divergent-1"} {
		if !slices.Contains(items, RecoveryItem{Name: name, Kind: "record"}) {
			t.Fatalf("Missing %s in %v", name, items)
		}
	}
	for _, p := range []string{path, path + ".backup", filepath.Join(dir, id+".json")} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, id+".files")); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveDeletionWaitsForBothLedgerCopies(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	if err := os.WriteFile(s.path(id), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.filesDir(id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.markPending(id); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.dir, id+".cleanup")
	if err := os.Mkdir(marker, 0700); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(marker, "occupied")
	if err := os.WriteFile(block, nil, 0600); err != nil {
		t.Fatal(err)
	}
	path, err := s.ArchiveUnreadable(id)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(path)
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	primary := filepath.Join(s.dir, ledgerFile)
	if err := os.Rename(primary, primary+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(primary, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRetained(name, ""); err == nil {
		t.Fatal("Deleted archive before saving the ledger")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(primary); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(primary+".saved", primary); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRetained(name, ""); err != nil {
		t.Fatal(err)
	}
	s, err = Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Pending(id) {
		t.Fatal("Archive deletion rearmed cleanup")
	}
	if _, err := os.Stat(s.filesDir(id)); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedOutputGenerationPersistsAcrossOpen(t *testing.T) {
	dir := t.TempDir()
	id := NewID()
	if err := os.Mkdir(filepath.Join(dir, id+".files"), 0700); err != nil {
		t.Fatal(err)
	}
	var generation string
	for range 2 {
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		items, err := s.RecoveryItems()
		if err != nil || len(items) != 1 || items[0].Kind != "output" || items[0].Generation == "" {
			t.Fatalf("Items = %v, %v", items, err)
		}
		if generation != "" && generation != items[0].Generation {
			t.Fatal("Output generation changed on reopen")
		}
		generation = items[0].Generation
	}
}

func TestDivergentLedgerIsPreservedOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ledgerFile)
	raw := []byte(fmt.Sprintf(`{"cleanup":["%s"]}`, NewID()))
	for range 2 {
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".backup", raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := os.ReadFile(path + ".backup.divergent-1"); err != nil || !bytes.Equal(raw, data) {
		t.Fatalf("Recovery copy = %s, %v", data, err)
	}
	if _, err := os.Lstat(path + ".backup.divergent-2"); !os.IsNotExist(err) {
		t.Fatalf("Duplicate recovery copy: %v", err)
	}
}

func TestNumberedRecordNamesThePathToRepair(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(file, "record")
	if _, err := numberedName(prefix); err == nil || !strings.Contains(err.Error(), prefix+"-1") || !strings.Contains(err.Error(), "check access") {
		t.Fatalf("Repair instructions = %v", err)
	}
}

func TestPendingOutputConfirmationCannotRemoveAReplacement(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewID()
	path := s.filesDir(id)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	items, err := s.RecoveryItems()
	if err != nil || len(items) != 1 {
		t.Fatalf("Recovery = %v, %v", items, err)
	}
	if err := s.orderOutput(id, items[0].Generation); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	s, err = Open(s.dir)
	if err != nil || s.Pending(id) {
		t.Fatalf("Pending after replacement: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestRestoredCopyRequiresANewOutputConfirmation(t *testing.T) {
	for _, action := range []string{"confirmation", "restart", "transcript"} {
		t.Run(action, func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id := NewID()
			path := s.filesDir(id)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			items, err := s.RecoveryItems()
			if err != nil || len(items) != 1 {
				t.Fatal(items, err)
			}
			gen := items[0].Generation
			if action != "confirmation" {
				if err := s.orderOutput(id, gen); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Rename(path, path+".original"); err != nil {
				t.Fatal(err)
			}
			if err := os.CopyFS(path, os.DirFS(path+".original")); err != nil {
				t.Fatal(err)
			}
			extra := filepath.Join(path, "new")
			if err := os.WriteFile(extra, []byte("saved"), 0600); err != nil {
				t.Fatal(err)
			}
			switch action {
			case "confirmation":
				if err := s.DeleteRetained(id+".files", gen); !errors.Is(err, errOutputChanged) {
					t.Errorf("Stale confirmation = %v", err)
				}
			case "restart":
				if s, err = Open(s.dir); err != nil || s.Pending(id) {
					t.Fatal("Stale order", err)
				}
			case "transcript":
				if err := s.Save(sample(id, time.Now())); err != nil {
					t.Fatal(err)
				}
				if err := s.Delete(id); !errors.Is(err, errOutputChanged) {
					t.Errorf("Stale order = %v", err)
				}
				if _, err := os.Stat(s.path(id)); err != nil {
					t.Error("Lost restored transcript", err)
				}
			}
			if data, err := os.ReadFile(extra); err != nil || string(data) != "saved" {
				t.Fatal("Lost restored output", err)
			}
			if action == "transcript" {
				return
			}
			items, err = s.RecoveryItems()
			if err != nil || len(items) != 1 || items[0].Generation == gen {
				t.Fatal("No fresh confirmation", items, err)
			}
			if err := s.DeleteRetained(id+".files", items[0].Generation); err != nil {
				t.Fatal(err)
			}
		})
	}
}
