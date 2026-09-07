package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"quecto-harness/internal/llm"
)

func sample(id string, updated time.Time) *Session {
	code := 0
	return &Session{
		ID: id, Project: "/proj", Model: "Alpha-12B", Title: "list the files here",
		Created: updated.Add(-time.Minute), Updated: updated,
		NCtx: 131072, NCtxSource: "props", Compactions: 1,
		LastUsage: &llm.Usage{PromptTokens: 4193, CompletionTokens: 118, TotalTokens: 4311, CachedTokens: 3968},
		Entries: []Entry{
			{Seq: 1, Kind: "user", Role: "user", Content: "list the files here", InContext: false, Visible: true, At: updated},
			{Seq: 2, Kind: "assistant", Role: "assistant", Reasoning: "run_shell with ls",
				ToolCalls: []llm.ToolCall{{ID: "call_0", Type: "function", Function: llm.FunctionCall{Name: "run_shell", Arguments: `{"command":"ls"}`}}},
				InContext: false, Visible: true, At: updated},
			{Seq: 3, Kind: "tool", Role: "tool", ToolCallID: "call_0", ToolName: "run_shell",
				Content: "AGENTS.md\n\nexit code: 0", ExitCode: &code,
				Permission: &PermissionRecord{ID: "perm_1", Tool: "run_shell", Command: "ls", Decision: "always", Prefix: "ls"},
				InContext:  false, Visible: true, At: updated},
			{Seq: 4, Kind: "compaction", Role: "user", Content: "Summary of earlier work:\nx\n\nContinue.",
				InContext: true, Visible: true, At: updated, Meta: map[string]any{"before": 104857.0, "after": 3120.0, "n": 1.0}},
			{Seq: 5, Kind: "error", Content: "turn limit reached", InContext: false, Visible: true, At: updated},
			{Seq: 6, Kind: "status", Content: "model changed to X", InContext: false, Visible: true, At: updated},
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
