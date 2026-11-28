package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/search"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// scratchHarness is a harness whose engine makes scratch folders.
func scratchHarness(t *testing.T) *harness {
	h := newHarness(t)
	h.engine.SetScratchRoot(h.paths.Scratch)
	return h
}

// reopen builds a second engine over the same state, the way a restart does.
func (h *harness) reopen(t *testing.T) *Engine {
	t.Helper()
	st, err := store.Open(h.paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := store.OpenFiles(h.paths.Files)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := NewEngine(ctx, h.cfg, st, blobs, Clients{Pool: llm.NewPool()}, search.NewProviders(), h.broker, h.rec)
	e.SetScratchRoot(h.paths.Scratch)
	t.Cleanup(func() {
		cancel()
		e.Wait(5 * time.Second)
	})
	return e
}

// moveCall is a change_directory call naming dir.
func moveCall(dir string) llm.ToolCall {
	return llm.ToolCall{ID: "call_0", Type: "function", Function: llm.FunctionCall{
		Name: "change_directory", Arguments: `{"path":` + strconv.Quote(dir) + `}`,
	}}
}

func movedEntries(s *Session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.Entries {
		if e.Kind == "moved" {
			n++
		}
	}
	return n
}

// A conversation started without a project works in a folder of its own,
// which goes when the conversation does.
func TestAScratchConversationGoesWithItsFolder(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, scratch := s.Where()
	if !scratch || !store.IsScratch(h.paths.Scratch, dir) {
		t.Fatalf("project = %s, scratch = %v", dir, scratch)
	}
	if h.cfg.HasProject(dir) {
		t.Error("the scratch folder was added to the project list")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder is still there after the delete: %v", err)
	}
}

// Moving to a registered project copies the new file, keeps the one the
// project already had, and survives a restart with one note and the scratch
// folder still there for a sweep to pass over.
func TestAMoveOutOfScratchSurvivesARestart(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	writeText(t, filepath.Join(dir, "new.txt"), "new")
	writeText(t, filepath.Join(dir, "file.txt"), "from scratch")

	runner, err := tools.NewRunner(dir, h.cfg.Config())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
		t.Fatal(err)
	}
	if runner.Outside(filepath.Join(h.project, "file.txt")) {
		t.Error("the folder is not reachable for the rest of the turn")
	}
	if p, _ := s.Where(); p != dir {
		t.Errorf("the project moved before the turn ended: %s", p)
	}
	h.engine.applyMove(s)

	e := h.reopen(t)
	r, err := e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.applyMove(r)
	if p, scratch := r.Where(); p != h.project || scratch {
		t.Errorf("after a restart project = %s, scratch = %v", p, scratch)
	}
	if n := movedEntries(r); n != 1 {
		t.Errorf("the transcript has %d move notes, want 1", n)
	}
	if b, _ := os.ReadFile(filepath.Join(h.project, "file.txt")); string(b) != "one\ntwo\n" {
		t.Errorf("the project's own file = %q, want it kept", b)
	}
	if b, _ := os.ReadFile(filepath.Join(h.project, "new.txt")); string(b) != "new" {
		t.Errorf("the new file = %q", b)
	}
	if r.Carried == nil || r.Carried.Copied != 1 || r.Carried.There != 1 {
		t.Errorf("carried = %+v", r.Carried)
	}
	// The scratch folder holds a file the move did not bring, so it stays
	// while the conversation names it.
	e.sweepOnce(time.Now().Add(30 * 24 * time.Hour))
	if _, err := os.Stat(filepath.Join(dir, "file.txt")); err != nil {
		t.Errorf("the left folder went in a sweep: %v", err)
	}
}

// A move cut off between the switch and the copy finishes the copy on the
// next start, once.
func TestACutOffCopyFinishesOnce(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	writeText(t, filepath.Join(dir, "new.txt"), "new")
	fromInfo, _ := os.Stat(dir)
	toInfo, _ := os.Stat(h.project)
	s.mu.Lock()
	s.Project, s.Scratch, s.LeftIn, s.CarryTo = h.project, false, dir, h.project
	s.LeftInID, s.CarryToID = store.FileID(fromInfo), store.FileID(toInfo)
	s.mu.Unlock()
	h.engine.save(s)

	e := h.reopen(t)
	r, err := e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.applyMove(r)
	e.applyMove(r)
	if n := movedEntries(r); n != 1 {
		t.Errorf("the transcript has %d move notes, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(h.project, "new.txt")); err != nil {
		t.Errorf("the file was not copied: %v", err)
	}
}

// A run a conversation launched cannot move it, and a folder that changed
// after it was approved is not moved to.
func TestAMoveIsTheLaunchersAndOfTheApprovedFolder(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	runner, _ := tools.NewRunner(dir, h.cfg.Config())

	run := newSession(store.NewID(), dir, h.agent.ID, h.agent.Model, "")
	run.Parent = s.ID
	if _, err := h.engine.changeDirectory(run, runner, moveCall(h.project), ""); err == nil {
		t.Error("a run moved the conversation")
	}

	other := t.TempDir()
	if _, err := h.engine.changeDirectory(s, runner, moveCall(other), filepath.Join(other, "elsewhere")); err == nil {
		t.Error("a folder other than the approved one was taken")
	}
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.paths.Scratch), ""); err == nil {
		t.Error("the scratch root was taken as a project")
	}
	above := filepath.Dir(h.paths.Scratch)
	if _, err := h.engine.changeDirectory(s, runner, moveCall(above), ""); err == nil {
		t.Error("a folder holding the scratch root was taken as a project")
	}
	if _, _, err := h.engine.directoryRequest(s, moveCall(above), config.ModeAsk); err == nil {
		t.Error("a card was offered for a move to a folder holding the scratch root")
	}
	if _, err := h.engine.AdmitFolder(above); err != nil {
		t.Errorf("a folder holding the scratch root cannot be reached: %v", err)
	}
}

// A move approved before a restart is not made after it: the folder could be
// another one by then. The access granted for it goes too.
func TestAMoveCutOffByARestartIsAskedAgain(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	runner, _ := tools.NewRunner(dir, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), h.project); err != nil {
		t.Fatal(err)
	}
	e := h.reopen(t)
	r, err := e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.applyMove(r)
	if p, scratch := r.Where(); p != dir || !scratch {
		t.Errorf("after a restart the conversation moved to %s", p)
	}
	if r.Pending != "" || len(r.Folders) != 0 {
		t.Errorf("pending = %q, folders = %v, want both cleared", r.Pending, r.Folders)
	}
	if n := movedEntries(r); n != 1 {
		t.Errorf("the transcript has %d move notes, want the one that says why", n)
	}
}

// A second move keeps the first copy's destination, which the prompt names.
func TestASecondMoveKeepsWhereTheFilesWent(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	writeText(t, filepath.Join(dir, "a.txt"), "a")
	runner, _ := tools.NewRunner(dir, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), ""); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(s)
	other, _ := filepath.EvalSymlinks(t.TempDir())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(other), ""); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(s)
	if p, _ := s.Where(); p != other {
		t.Fatalf("project = %s, want %s", p, other)
	}
	if s.Carried == nil || s.Carried.To != h.project {
		t.Errorf("carried = %+v, want the files recorded as copied to %s", s.Carried, h.project)
	}
}

// A session file that cannot be read does not stop a delete, but keeps the
// conversation's folder until every record reads.
func TestADeleteKeepsTheFolderWhileARecordIsUnread(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	if err := os.WriteFile(filepath.Join(h.paths.Sessions, "sess_aaaaaaaaaaaaaaaa.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := h.reopen(t)
	if err := e.Delete(s.ID); err != nil {
		t.Errorf("an unread record blocked the delete: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the folder went while a record was unread: %v", err)
	}
	// The order stays, so the folder goes once every record reads.
	os.Remove(filepath.Join(h.paths.Sessions, "sess_aaaaaaaaaaaaaaaa.json"))
	e.sweepOnce(time.Now())
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the folder outlived the unread record: %v", err)
	}
}

// A conversation whose record cannot be written is not handed out, and its
// folder goes again.
func TestAScratchConversationTheDiskRefusesIsNotMade(t *testing.T) {
	h := scratchHarness(t)
	if err := os.Chmod(h.paths.Sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h.paths.Sessions, 0o700) })
	if _, err := h.engine.CreateScratch(); err == nil {
		t.Fatal("a conversation was made with no record on disk")
	}
	entries, _ := os.ReadDir(h.paths.Scratch)
	if len(entries) != 0 {
		t.Errorf("the scratch root holds %d folders, want none", len(entries))
	}
}

// Outside full mode a folder that cannot be used is turned down before any
// card, so the tool does not resolve it again unseen.
func TestAFolderThatCannotBeUsedIsTurnedDown(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	_, ask, refused := h.engine.directoryRequest(s, moveCall("/no/such/folder"), "ask")
	if ask || refused == nil {
		t.Errorf("ask = %v, refused = %v, want it refused without a card", ask, refused)
	}
	_, ask, refused = h.engine.directoryRequest(s, moveCall(h.project), "ask")
	if !ask || refused != nil {
		t.Errorf("a project folder: ask = %v, refused = %v", ask, refused)
	}
}

// A copy that could not run when the conversation moved to A still goes to A
// after a second move to B.
func TestAPendingCopyGoesWhereTheFirstMoveWent(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	writeText(t, filepath.Join(dir, "a.txt"), "a")
	runner, _ := tools.NewRunner(dir, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), ""); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0)
	h.engine.applyMove(s)
	os.Chmod(dir, 0o700)
	if s.Carried != nil {
		t.Fatalf("a copy that could not read its folder was recorded: %+v", s.Carried)
	}
	other, _ := filepath.EvalSymlinks(t.TempDir())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(other), ""); err != nil {
		t.Fatal(err)
	}
	h.engine.applyMove(s)
	if _, err := os.Stat(filepath.Join(h.project, "a.txt")); err != nil {
		t.Errorf("the file did not reach the first folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "a.txt")); err == nil {
		t.Error("the file went to the second folder")
	}
}

// A move the disk refuses is undone in memory, so it is made once the disk
// takes it again.
func TestAMoveTheDiskRefusesIsKept(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	runner, _ := tools.NewRunner(dir, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), ""); err != nil {
		t.Fatal(err)
	}
	os.Chmod(h.paths.Sessions, 0o500)
	h.engine.applyMove(s)
	os.Chmod(h.paths.Sessions, 0o700)
	if p, _ := s.Where(); p != dir || s.Pending != h.project || s.pendingInfo == nil {
		t.Fatalf("after a refused save project = %s, pending = %q", p, s.Pending)
	}
	h.engine.applyMove(s)
	if p, _ := s.Where(); p != h.project {
		t.Errorf("the move was not made once the disk took it: %s", p)
	}
}

// A folder granted on its own stays when a move to it is not made.
func TestACancelledMoveKeepsAnEarlierGrant(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	runner, _ := tools.NewRunner(dir, h.cfg.Config())
	ask := llm.ToolCall{ID: "call_1", Type: "function", Function: llm.FunctionCall{
		Name: "request_directory", Arguments: `{"path":` + strconv.Quote(h.project) + `}`,
	}}
	if _, err := h.engine.requestDirectory(s, runner, ask, perms.Decision{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), ""); err != nil {
		t.Fatal(err)
	}
	e := h.reopen(t)
	r, err := e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.applyMove(r)
	if r.Pending != "" || len(r.Folders) != 1 || r.Folders[0] != h.project {
		t.Errorf("pending = %q, folders = %v, want the earlier grant kept", r.Pending, r.Folders)
	}
}

// A record that left a scratch folder without naming where its files go
// copies nothing, rather than guess the folder it is in now.
func TestACopyWithNoRecordedDestinationCopiesNothing(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	writeText(t, filepath.Join(dir, "a.txt"), "a")
	s.mu.Lock()
	s.Project, s.Scratch, s.LeftIn = h.project, false, dir
	s.mu.Unlock()
	h.engine.applyMove(s)
	if _, err := os.Stat(filepath.Join(h.project, "a.txt")); err == nil {
		t.Error("the file was copied to a folder the move did not name")
	}
	if s.Carried != nil || movedEntries(s) != 0 {
		t.Errorf("carried = %+v with %d notes, want the copy left due and nothing said", s.Carried, movedEntries(s))
	}
}

// A scratch folder replaced before the move is not copied from, since it is
// checked against the folder that was made, not the one found at the move.
func TestAReplacedScratchFolderIsNotCopied(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	runner, _ := tools.NewRunner(dir, h.cfg.Config())
	if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir + "-old") })
	os.Mkdir(dir, 0o700)
	writeText(t, filepath.Join(dir, "planted.txt"), "x")
	h.engine.applyMove(s)
	if _, err := os.Stat(filepath.Join(h.project, "planted.txt")); err == nil {
		t.Error("a file from a folder put in the scratch folder's place was copied")
	}
	if s.Carried == nil || s.Carried.Reason == "" {
		t.Errorf("carried = %+v, want a reason", s.Carried)
	}
}

// A run whose file could not be read at start, and reads now, goes with its
// conversation, record and all, though nothing opened it.
func TestADeleteTakesARunTheIndexMissed(t *testing.T) {
	h := scratchHarness(t)
	p := h.session(t)
	run := newSession(store.NewID(), h.project, h.agent.ID, h.agent.Model, "")
	run.Parent = p.ID
	h.engine.mu.Lock()
	h.engine.live[run.ID] = run
	h.engine.mu.Unlock()
	h.engine.save(run)
	h.engine.save(p)
	file := filepath.Join(h.paths.Sessions, run.ID+".json")
	os.Chmod(file, 0)
	e := h.reopen(t)
	os.Chmod(file, 0o600)
	// Nothing opens the run: the delete finds it.
	if err := e.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the run's record outlived its conversation: %v", err)
	}
}

// Asking twice to move to the same folder keeps the access provisional, so a
// move that is not made takes it back.
func TestAskingTwiceKeepsTheGrantProvisional(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	runner, _ := tools.NewRunner(dir, h.cfg.Config())
	for range 2 {
		if _, err := h.engine.changeDirectory(s, runner, moveCall(h.project), ""); err != nil {
			t.Fatal(err)
		}
	}
	e := h.reopen(t)
	r, err := e.Get(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.applyMove(r)
	if len(r.Folders) != 0 {
		t.Errorf("folders = %v, want the provisional grant taken back", r.Folders)
	}
}

// A run held only in memory goes with its conversation, and a run whose
// record cannot be read keeps the conversation from being deleted.
func TestADeleteTakesEveryRunOrNone(t *testing.T) {
	h := scratchHarness(t)
	s := h.session(t)
	run := newSession(store.NewID(), h.project, h.agent.ID, h.agent.Model, "")
	run.Parent = s.ID
	h.engine.mu.Lock()
	h.engine.live[run.ID] = run
	h.engine.mu.Unlock()
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatalf("delete with a run only in memory = %v", err)
	}
	if _, err := h.engine.Get(run.ID); err == nil {
		t.Error("the run outlived its conversation")
	}

	p := h.session(t)
	child := newSession(store.NewID(), h.project, h.agent.ID, h.agent.Model, "")
	child.Parent = p.ID
	h.engine.mu.Lock()
	h.engine.live[child.ID] = child
	h.engine.mu.Unlock()
	h.engine.save(child)
	h.engine.save(p)
	// The index knows the run; its file is spoiled afterwards.
	e := h.reopen(t)
	if err := os.WriteFile(filepath.Join(h.paths.Sessions, child.ID+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := e.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(r.ID); err == nil {
		t.Error("the conversation was deleted with a run that could not be read")
	}
	if _, err := os.Stat(filepath.Join(h.paths.Sessions, p.ID+".json")); err != nil {
		t.Errorf("the conversation's record went: %v", err)
	}
}

// Deleting a conversation leaves a folder that was put in place of the one
// made for it.
func TestADeleteLeavesAFolderPutInPlaceOfTheScratch(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.Where()
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir + "-old") })
	os.Mkdir(dir, 0o700)
	writeText(t, filepath.Join(dir, "theirs.txt"), "x")
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	// Its identity is not the one recorded when the folder was made, so it is
	// not moved or touched.
	if _, err := os.Stat(filepath.Join(dir, "theirs.txt")); err != nil {
		t.Errorf("the folder put in its place was not kept whole: %v", err)
	}
}

// writeText writes s to path, and fails the test when it cannot.
func writeText(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A no-folder conversation is told only of projects a conversation ran in,
// most recent first and at most recentLimit, never every configured one.
func TestRecentProjectsAreOnlyThoseUsed(t *testing.T) {
	h := newHarness(t)
	var used []string
	for i := 0; i < recentLimit+2; i++ {
		dir, _ := filepath.EvalSymlinks(t.TempDir())
		if _, err := h.cfg.AddProject(dir); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			continue // configured, never used
		}
		if _, err := h.engine.Create(dir); err != nil {
			t.Fatal(err)
		}
		used = append(used, dir)
		time.Sleep(2 * time.Millisecond)
	}
	got := h.engine.recentProjects(h.cfg.Config())
	if len(got) != recentLimit {
		t.Fatalf("recent = %d folders, want %d: %v", len(got), recentLimit, got)
	}
	if got[0] != used[len(used)-1] {
		t.Errorf("most recent first: got %s, want %s", got[0], used[len(used)-1])
	}
	for _, p := range got {
		if !slices.Contains(used, p) {
			t.Errorf("%s is listed but no conversation ran in it", p)
		}
	}
}

func TestArchivedTranscriptDefersScratchRemovalAfterRestart(t *testing.T) {
	h := scratchHarness(t)
	s, err := h.engine.CreateScratch()
	if err != nil {
		t.Fatal(err)
	}
	folder, _ := s.Where()
	note := filepath.Join(folder, "note")
	writeText(t, note, "scratch content")
	id := store.NewID()
	transcript := filepath.Join(h.paths.Sessions, id+".json")
	writeText(t, transcript, "{")
	spill := h.engine.store.SpillDir(id)
	if err := os.MkdirAll(spill, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(spill, "result")
	writeText(t, output, "saved output")
	preview, err := h.engine.PreviewDelete(s.ID)
	if err != nil || !preview.Deferred || preview.Kept != folder {
		t.Fatalf("Preview = %+v, %v", preview, err)
	}
	if err := h.engine.DeleteConfirmed(s.ID, preview); err != nil {
		t.Fatal(err)
	}
	archive, err := h.engine.store.ArchiveUnreadable(id)
	if err != nil {
		t.Fatal(err)
	}
	e := h.reopen(t)
	later := time.Now().AddDate(1, 0, 0)
	e.sweepOnce(later)
	if e.LauncherGone(id) {
		t.Fatal("Archived launcher reported gone")
	}
	assertScratchNote(t, note, "scratch content")
	assertScratchNote(t, output, "saved output")
	if err := e.store.DeleteRetained(filepath.Base(archive), ""); err != nil {
		t.Fatal(err)
	}
	e = h.reopen(t)
	if !e.LauncherGone(id) {
		t.Fatal("Removed archive still holds its launcher")
	}
	e.sweepOnce(later)
	if _, err := os.Lstat(folder); !os.IsNotExist(err) {
		t.Fatalf("Deferred scratch removal: %v", err)
	}
	assertScratchNote(t, output, "saved output")
	items, err := e.store.RecoveryItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Name == id+".files" && item.Generation != "" {
			if err := e.store.DeleteRetained(item.Name, item.Generation); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatalf("Confirmed output removal: %v", err)
			}
			return
		}
	}
	t.Fatal("Retained output missing from recovery")
}
