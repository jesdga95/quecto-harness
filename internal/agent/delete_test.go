package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/search"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// subagentOf files an idle subagent run under a session, live and on disk,
// the way a finished delegation leaves one.
func subagentOf(t *testing.T, h *harness, parent *Session) *Session {
	t.Helper()
	child := newSession(store.NewID(), h.project, h.agent.ID, "Alpha-12B", config.ToolsetFull)
	child.Parent = parent.ID
	child.Entries = []store.Entry{{Kind: "user", Role: "user", Content: "do it", InContext: true, Visible: true}}
	h.engine.mu.Lock()
	h.engine.live[child.ID] = child
	h.engine.mu.Unlock()
	h.engine.save(child)
	return child
}

func onDisk(h *harness, id string) bool {
	_, err := os.Stat(filepath.Join(h.paths.Sessions, id+".json"))
	return err == nil
}

// evict drops a session from the live set, as a restart or another delete
// leaves it.
func evict(h *harness, id string) {
	h.engine.mu.Lock()
	delete(h.engine.live, id)
	h.engine.mu.Unlock()
}

// blockRemoval puts a folder where the file at path was, which cannot be
// removed as a file.
func blockRemoval(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func reserved(h *harness) int {
	h.engine.mu.Lock()
	defer h.engine.mu.Unlock()
	return len(h.engine.deleting)
}

// A session a delete has reserved starts no work by any way in, and is
// usable again once the reservation is released.
func TestAReservedSessionTakesNoWork(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	group, _, err := h.engine.reserve(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Send(s.ID, "hello", nil); !errors.Is(err, ErrBusy) {
		t.Errorf("Send on a reserved session = %v, want busy", err)
	}
	if _, err := h.engine.Resend(s.ID, 1, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("Resend on a reserved session = %v, want busy", err)
	}
	if err := h.engine.Compact(s.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("Compact on a reserved session = %v, want busy", err)
	}
	if err := h.engine.Delete(s.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("a second delete = %v, want busy", err)
	}
	if s.State() != StateIdle {
		t.Errorf("state = %s, want idle", s.State())
	}
	h.engine.unreserve(group)
	if err := h.engine.lockForWork(s); err != nil {
		t.Fatalf("released session refused work: %v", err)
	}
	s.mu.Unlock()
}

// A copy read from disk before a delete is not kept once the delete holds the
// id or has removed it, so a message sent after cannot write the file back.
func TestALoadThatEndsAfterADeleteIsNotKept(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	// The load stands in for a Get under way across the delete.
	h.engine.beginRead()
	defer h.engine.endRead()
	doc, err := h.store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}

	group, _, err := h.engine.reserve(s)
	if err != nil {
		t.Fatal(err)
	}
	evict(h, s.ID)
	if _, err := h.engine.keep(fromDoc(doc)); !errors.Is(err, ErrNotFound) {
		t.Errorf("keep during the delete = %v, want not found", err)
	}
	h.engine.mu.Lock()
	h.engine.live[s.ID] = s
	h.engine.mu.Unlock()
	h.engine.unreserve(group)

	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	late := fromDoc(doc)
	if _, err := h.engine.keep(late); !errors.Is(err, ErrNotFound) {
		t.Fatalf("keep after the delete = %v, want not found", err)
	}
	if _, err := h.engine.deliver(late, "hello", nil); err == nil {
		t.Error("a late copy took a message")
	}
	if onDisk(h, s.ID) {
		t.Error("the file came back")
	}
}

// A subagent run that is on disk but cannot be read keeps its launcher: the
// delete fails rather than leaving the run without one.
func TestAnUnreadableSubagentKeepsItsLauncher(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	evict(h, child.ID)
	if err := os.WriteFile(filepath.Join(h.paths.Sessions, child.ID+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := h.engine.Delete(parent.ID)
	if !errors.Is(err, store.ErrUnreadable) || !strings.Contains(err.Error(), child.ID) {
		t.Fatalf("Delete = %v, want the run named as unreadable", err)
	}
	if !onDisk(h, parent.ID) || !onDisk(h, child.ID) {
		t.Error("a file went although a run could not be read")
	}
	if n := reserved(h); n != 0 {
		t.Errorf("%d ids left reserved", n)
	}
}

// A queued message keeps the session from being deleted. Flushed, it starts
// its turn once, with no other send, and its stored copy goes when the
// session is deleted after.
func TestAQueuedMessageIsDeliveredOnceAndGoesWithTheSession(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_title.txt")
	s := h.session(t)
	h.engine.save(s)
	id, _, err := h.engine.files.PutBytes(onePixelPNG())
	if err != nil {
		t.Fatal(err)
	}
	held := &Steer{Text: "later", Attachments: []store.Attachment{{Kind: "image", File: id}}}
	s.mu.Lock()
	s.steer = held
	s.mu.Unlock()
	if err := h.engine.Delete(s.ID); !errors.Is(err, ErrBusy) {
		t.Errorf("Delete with a queued message = %v, want busy", err)
	}

	h.engine.flushSteer(s)
	waitIdle(t, s)
	h.engine.wg.Wait()
	doc, _, _, _ := s.Snapshot()
	n := 0
	for _, e := range doc.Entries {
		if e.Kind == "user" && e.Content == "later" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the queued message landed %d times, want once", n)
	}
	h.engine.flushSteer(s)
	if after, _, _, _ := s.Snapshot(); len(after.Entries) != len(doc.Entries) {
		t.Error("a second flush changed the transcript")
	}

	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	path, err := h.engine.files.Path(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the queued message's stored copy outlived the session")
	}
}

// A file that names another session is not taken for it: deleting it fails
// and leaves both sessions as they were.
func TestAFileNamingAnotherSessionDeletesNothing(t *testing.T) {
	h := newHarness(t)
	b := h.session(t)
	h.engine.save(b)
	raw, err := os.ReadFile(filepath.Join(h.paths.Sessions, b.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	a := store.NewID()
	if err := os.WriteFile(filepath.Join(h.paths.Sessions, a+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := h.engine.Delete(a); err == nil {
		t.Fatal("deleting a file that names another session succeeded")
	}
	if !onDisk(h, a) || !onDisk(h, b.ID) {
		t.Error("a file went")
	}
	if got, err := h.engine.Get(b.ID); err != nil || got != b {
		t.Errorf("the named session = %v, %v, want it live as it was", got, err)
	}
}

// A run another delete removed after it was read is left out of the group, so
// its launcher can still be reserved.
func TestClaimLeavesOutARunAlreadyRemoved(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	if err := h.engine.Delete(child.ID); err != nil {
		t.Fatal(err)
	}

	group, err := h.engine.claim(parent, []*Session{child})
	if err != nil {
		t.Fatal(err)
	}
	defer h.engine.unreserve(group)
	if len(group) != 1 || group[0] != parent {
		t.Errorf("group = %d sessions, want the launcher alone", len(group))
	}
}

// A run another delete holds keeps its launcher from being reserved.
func TestClaimRefusesARunAnotherDeleteHolds(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	held, err := h.engine.claim(child, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.engine.unreserve(held)

	if _, err := h.engine.claim(parent, []*Session{child}); !errors.Is(err, ErrBusy) {
		t.Fatalf("claim = %v, want busy", err)
	}
	h.engine.mu.Lock()
	free := !h.engine.deleting[parent.ID]
	h.engine.mu.Unlock()
	if !free {
		t.Error("the launcher was left reserved")
	}
}

// An instance that is no longer the live one for its id takes no work.
func TestAStaleInstanceTakesNoWork(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	evict(h, s.ID)
	if _, err := h.engine.deliver(s, "hello", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("deliver to a stale instance = %v, want not found", err)
	}
}

// A subagent that is still working keeps its launcher, itself and every file
// in place, and leaves nothing reserved.
func TestDeleteWithAWorkingSubagentChangesNothing(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	child.mu.Lock()
	child.state = StatePreflight
	child.mu.Unlock()
	defer func() {
		child.mu.Lock()
		child.state = StateIdle
		child.mu.Unlock()
	}()

	if err := h.engine.Delete(parent.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("Delete = %v, want busy", err)
	}
	if !onDisk(h, parent.ID) || !onDisk(h, child.ID) {
		t.Error("a refused delete removed a file")
	}
	if n := reserved(h); n != 0 {
		t.Errorf("%d ids left reserved", n)
	}
	if err := h.engine.lockForWork(parent); err != nil {
		t.Fatalf("the launcher refused work after a refused delete: %v", err)
	}
	parent.mu.Unlock()
}

// A subagent run that is only on disk, as after a restart, goes with its
// launcher.
func TestDeleteRemovesASubagentOnlyOnDisk(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	evict(h, child.ID)

	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	if onDisk(h, parent.ID) || onDisk(h, child.ID) {
		t.Error("a file outlived the delete")
	}
	if h.store.Has(child.ID) {
		t.Error("the subagent run is still indexed")
	}
}

// A delete that fails on a subagent run leaves the launcher in place, usable,
// with a change made in the meantime saved, and the delete can be tried again.
func TestAFailedDeleteCanBeTriedAgain(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	path := filepath.Join(h.paths.Sessions, child.ID+".json")
	blockRemoval(t, path)

	err := h.engine.Delete(parent.ID)
	if err == nil || errors.Is(err, ErrBusy) || errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete = %v, want the disk error", err)
	}
	if !onDisk(h, parent.ID) {
		t.Fatal("the launcher went although a run under it could not be deleted")
	}
	if n := reserved(h); n != 0 {
		t.Errorf("%d ids left reserved", n)
	}
	if err := h.engine.Rename(parent.ID, "Kept"); err != nil {
		t.Fatal(err)
	}
	doc, err := h.store.Load(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Title != "Kept" {
		t.Errorf("title on disk = %q, want the rename saved", doc.Title)
	}

	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatalf("the second delete = %v", err)
	}
	if onDisk(h, parent.ID) {
		t.Error("the launcher outlived the second delete")
	}
}

// A session whose own file cannot be removed comes back usable: it is not
// marked deleted, keeps its place, and takes work again.
func TestASessionThatCouldNotBeRemovedComesBack(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	blockRemoval(t, filepath.Join(h.paths.Sessions, s.ID+".json"))

	err := h.engine.Delete(s.ID)
	if err == nil || errors.Is(err, ErrBusy) || errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete = %v, want the disk error", err)
	}
	s.mu.Lock()
	gone := s.gone
	s.mu.Unlock()
	if gone {
		t.Error("the session is still marked deleted")
	}
	h.engine.mu.Lock()
	live, removed := h.engine.live[s.ID] == s, h.engine.removed[s.ID]
	h.engine.mu.Unlock()
	if !live || removed {
		t.Errorf("live = %v, removed = %v, want it live and not removed", live, removed)
	}
	if err := h.engine.lockForWork(s); err != nil {
		t.Fatalf("the session refused work: %v", err)
	}
	s.mu.Unlock()
}

// A command that outlasts the wait keeps the session, its file and its folder,
// and the delete says so.
func TestADeleteThatCannotStopACommandRemovesNothing(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	runner, err := tools.NewRunner(h.project, h.cfg.Config())
	if err != nil {
		t.Fatal(err)
	}
	runner.SpillTo(h.store.SpillDir(s.ID))
	// The engine's context is over before the command's kill comes.
	stubborn(t, h, runner, s.ID)
	h.cancel()

	if err := h.engine.Delete(s.ID); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("Delete = %v, want the command still running", err)
	}
	if !onDisk(h, s.ID) {
		t.Error("the file went while a command was still running")
	}
	if n := reserved(h); n != 0 {
		t.Errorf("%d ids left reserved", n)
	}
	waitEnded(t, h, s.ID)
}

// stubborn starts a background command that ignores the polite stop and
// returns once it is ignoring it, so it lives until the kill that follows.
func stubborn(t *testing.T, h *harness, runner *tools.Runner, session string) {
	t.Helper()
	args := tools.RunShellArgs{Command: `trap "" TERM; echo ready; sleep 30`, Background: true}
	if _, err := h.engine.procs.Start(context.Background(), runner, session, "call_1", args); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if procs := h.engine.Processes(session); len(procs) == 1 && slices.Contains(procs[0].Tail, "ready") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the command never said it was ready")
}

// Drain reports a command that is still running when its wait ends.
func TestDrainReportsACommandStillRunning(t *testing.T) {
	h := newHarness(t)
	runner, err := tools.NewRunner(h.project, h.cfg.Config())
	if err != nil {
		t.Fatal(err)
	}
	stubborn(t, h, runner, "sess_drain")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := h.engine.procs.Drain(ctx, []string{"sess_drain"}); err == nil {
		t.Error("Drain returned nil with a command still running")
	}
	if err := h.engine.procs.Drain(context.Background(), []string{"sess_drain"}); err != nil {
		t.Errorf("Drain after the kill = %v", err)
	}
}

// A command still running in the background is stopped and waited on before
// the folder its output goes to is removed, and the session hears nothing.
func TestDeleteWaitsForABackgroundCommand(t *testing.T) {
	h := newHarness(t, "stream_background_long.txt", "stream_plain.txt")
	if _, err := h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist }); err != nil {
		t.Fatal(err)
	}
	if err := h.cfg.AddAllow(h.project, "sleep"); err != nil {
		t.Fatal(err)
	}
	s := h.session(t)
	h.engine.Send(s.ID, "start it", nil)
	waitIdle(t, s)
	h.engine.wg.Wait()
	if procs := h.engine.Processes(s.ID); len(procs) != 1 || procs[0].Status != "running" {
		t.Fatalf("processes = %v", procs)
	}

	start := time.Now()
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took >= drainTimeout {
		t.Errorf("the delete took %s, as long as the drain allows", took)
	}
	for _, p := range h.engine.Processes(s.ID) {
		if p.Status == "running" {
			t.Errorf("process %s still running after the delete", p.ID)
		}
	}
	h.engine.wg.Wait()
	if _, err := os.Stat(filepath.Join(h.paths.Sessions, s.ID+".files")); err == nil {
		t.Error("the session's folder is back after the delete")
	}
	if onDisk(h, s.ID) {
		t.Error("the session's file is back after the delete")
	}
}

// Two deletes of one session at once: one removes it, the other is refused,
// and nothing is left reserved.
func TestConcurrentDeletes(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	subagentOf(t, h, s)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = h.engine.Delete(s.ID)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrBusy), errors.Is(err, ErrNotFound):
		default:
			t.Errorf("Delete = %v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d deletes succeeded, want one (%v)", ok, errs)
	}
	if onDisk(h, s.ID) {
		t.Error("the session survived")
	}
	if n := reserved(h); n != 0 {
		t.Errorf("%d ids left reserved", n)
	}
}

// storedFiles counts the copies in the attachment store.
func storedFiles(t *testing.T, h *harness) int {
	t.Helper()
	entries, err := os.ReadDir(h.paths.Files)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// A message refused after its pictures were copied, by a delete holding the
// session or by a second file that cannot be read, leaves no copy behind and
// the picked file as it was.
func TestARefusedMessageLeavesNoStoredCopy(t *testing.T) {
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, onePixelPNG(), 0o644); err != nil {
		t.Fatal(err)
	}
	before := storedFiles(t, h)

	group, _, err := h.engine.reserve(s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Send(s.ID, "look", []string{png}); !errors.Is(err, ErrBusy) {
		t.Errorf("Send = %v, want busy", err)
	}
	h.engine.unreserve(group)
	if n := storedFiles(t, h); n != before {
		t.Errorf("stored copies = %d after a refused message, want %d", n, before)
	}

	missing := filepath.Join(t.TempDir(), "gone.png")
	if _, err := h.engine.Send(s.ID, "look", []string{png, missing}); err == nil {
		t.Fatal("a message with a file that cannot be read was sent")
	}
	if n := storedFiles(t, h); n != before {
		t.Errorf("stored copies = %d after a partly read message, want %d", n, before)
	}
	if raw, err := os.ReadFile(png); err != nil || len(raw) != len(onePixelPNG()) {
		t.Errorf("the picked file changed: %v", err)
	}
}

// A message and a delete racing each other end one of two ways: the turn
// starts and the delete is refused with the file in place, or the delete wins
// and nothing writes the file back.
func TestSendRacingADelete(t *testing.T) {
	const rounds = 20
	fixtures := make([]string, 0, 2*rounds)
	for range 2 * rounds {
		fixtures = append(fixtures, "stream_plain.txt")
	}
	h := newHarness(t, fixtures...)
	for range rounds {
		s := h.session(t)
		h.engine.save(s)
		var wg sync.WaitGroup
		var sendErr, delErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, sendErr = h.engine.Send(s.ID, "hello", nil)
		}()
		go func() {
			defer wg.Done()
			delErr = h.engine.Delete(s.ID)
		}()
		wg.Wait()
		h.engine.wg.Wait()

		switch {
		case delErr == nil:
			if onDisk(h, s.ID) {
				t.Fatalf("the delete won (send: %v) and the file came back", sendErr)
			}
		case errors.Is(delErr, ErrBusy):
			if sendErr != nil {
				t.Fatalf("the delete was refused as busy and the send failed too: %v", sendErr)
			}
			if !onDisk(h, s.ID) {
				t.Fatal("the delete was refused and the file is gone")
			}
		default:
			t.Fatalf("Delete = %v", delErr)
		}
		if n := reserved(h); n != 0 {
			t.Fatalf("%d ids left reserved", n)
		}
	}
}

// Each session a delete removes lets go of its stream, and only those: a
// delete refused before it starts drops nothing, and one that fails part way
// drops the runs it did remove and keeps the launcher and the run that stays.
func TestADeleteDropsTheStreamsOfWhatItRemoved(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	subagentOf(t, h, parent)
	subagentOf(t, h, parent)
	order := h.store.Children(parent.ID)
	if len(order) != 2 {
		t.Fatalf("children = %d", len(order))
	}
	other, err := h.engine.Get(order[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	stays, err := h.engine.Get(order[1].ID)
	if err != nil {
		t.Fatal(err)
	}

	stays.mu.Lock()
	stays.state = StatePreflight
	stays.mu.Unlock()
	if err := h.engine.Delete(parent.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("Delete = %v, want busy", err)
	}
	if d := h.rec.droppedIDs(); len(d) != 0 {
		t.Errorf("a refused delete dropped %v", d)
	}
	stays.mu.Lock()
	stays.state = StateIdle
	stays.mu.Unlock()

	path := filepath.Join(h.paths.Sessions, stays.ID+".json")
	blockRemoval(t, path)
	if err := h.engine.Delete(parent.ID); err == nil {
		t.Fatal("the delete went through a run it could not remove")
	}
	// The runs go in an order of their own; what is dropped is exactly what
	// went, whichever it was.
	dropped := map[string]bool{}
	for _, id := range h.rec.droppedIDs() {
		dropped[id] = true
	}
	for _, id := range []string{parent.ID, other.ID, stays.ID} {
		if dropped[id] == onDisk(h, id) {
			t.Errorf("%s: dropped %v, on disk %v", id, dropped[id], onDisk(h, id))
		}
	}

	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, id := range h.rec.droppedIDs() {
		got[id] = true
	}
	for _, id := range []string{parent.ID, other.ID, stays.ID} {
		if !got[id] {
			t.Errorf("%s was not dropped", id)
		}
	}
}

// A delete that cannot remove a session's saved output is not reported done,
// and a later delete of the same session finishes it once the folder can go.
func TestADeleteThatLeavesTheOutputCanBeFinished(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	h := newHarness(t)
	s := h.session(t)
	h.engine.save(s)
	spill := h.store.SpillDir(s.ID)
	if err := os.MkdirAll(spill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spill, "out.txt"), []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(spill, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(spill, 0o700) })

	if err := h.engine.Delete(s.ID); !errors.Is(err, store.ErrLeftBehind) {
		t.Fatalf("Delete = %v, want the output left behind", err)
	}
	if onDisk(h, s.ID) || h.engine.IsLive(s.ID) {
		t.Error("the session came back after its file was removed")
	}
	if err := h.engine.Delete(s.ID); !errors.Is(err, store.ErrLeftBehind) {
		t.Fatalf("the second delete = %v, want the output left behind", err)
	}
	if err := os.Chmod(spill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatalf("the delete after the fix = %v", err)
	}
	if _, err := os.Stat(spill); !os.IsNotExist(err) {
		t.Errorf("the output is still there: %v", err)
	}
	if h.store.Has(s.ID) {
		t.Error("the session is still indexed")
	}
}

// restart opens the sessions again with a new engine, as the next start does.
func restart(t *testing.T, h *harness) {
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
	t.Cleanup(func() {
		cancel()
		e.Wait(5 * time.Second)
	})
	h.engine, h.store = e, st
}

// A run whose file was already unreadable at start is still known to belong to
// its launcher: deleting the launcher names the run and removes nothing.
func TestARunUnreadableAtStartKeepsItsLauncher(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	path := filepath.Join(h.paths.Sessions, child.ID+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	restart(t, h)

	err = h.engine.Delete(parent.ID)
	if !errors.Is(err, store.ErrUnreadable) || !strings.Contains(err.Error(), child.ID) {
		t.Fatalf("Delete = %v, want the run named as unreadable", err)
	}
	if !onDisk(h, parent.ID) || !onDisk(h, child.ID) {
		t.Error("a file went although a run could not be read")
	}
	if n := reserved(h); n != 0 {
		t.Errorf("%d ids left reserved", n)
	}

	// Removing the unreadable file is the way out the error names.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatalf("the delete after the file went = %v", err)
	}
}

// A file that cannot be read and was never known as anyone's run blocks no
// delete.
func TestAnUnknownUnreadableFileBlocksNoDelete(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	subagentOf(t, h, parent)
	stray := store.NewID()
	if err := os.WriteFile(filepath.Join(h.paths.Sessions, stray+".json"), []byte(`{"id":"`+stray+`","parent":"`), 0o600); err != nil {
		t.Fatal(err)
	}
	restart(t, h)

	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatalf("Delete = %v, want the family deleted", err)
	}
	if !onDisk(h, stray) {
		t.Error("the unknown file went")
	}
}

// fromRun counts the messages a launcher holds from one run.
func fromRun(s *Session, run string) int {
	doc, _, _, _ := s.Snapshot()
	n := 0
	for _, e := range doc.Entries {
		if e.Kind == "user" && e.FromSession == run {
			n++
		}
	}
	return n
}

// A run's report that arrives while a delete holds its launcher waits for the
// delete. Rolled back, the launcher gets it once; and nothing says busy.
func TestAReportDuringADeleteThatFailsArrivesOnce(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_title.txt")
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	child.mu.Lock()
	child.Entries = append(child.Entries, store.Entry{Kind: "assistant", Role: "assistant",
		Content: "done", InContext: true, Visible: true})
	child.mu.Unlock()

	group, _, err := h.engine.reserve(parent)
	if err != nil {
		t.Fatal(err)
	}
	h.engine.report(child)
	if n := len(h.rec.ofType("error")); n != 0 {
		t.Errorf("%d errors published while the delete held the launcher", n)
	}
	if n := fromRun(parent, child.ID); n != 0 {
		t.Fatalf("the report landed %d times during the delete", n)
	}

	h.engine.unreserve(group)
	waitIdle(t, parent)
	h.engine.wg.Wait()
	if n := fromRun(parent, child.ID); n != 1 {
		t.Fatalf("the report landed %d times after the rollback, want once", n)
	}
	h.engine.unreserve(group)
	if n := fromRun(parent, child.ID); n != 1 {
		t.Errorf("a second release delivered it again: %d", n)
	}
	if n := len(h.rec.ofType("error")); n != 0 {
		t.Errorf("%d errors published", n)
	}
}

// A report held for a launcher the delete then removes goes with it: nothing
// is delivered, nothing is published and nothing stays held.
func TestAReportHeldForADeletedLauncherGoesWithIt(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)

	group, _, err := h.engine.reserve(parent)
	if err != nil {
		t.Fatal(err)
	}
	h.engine.report(child)
	for _, m := range slices.Backward(group) {
		if err := h.engine.remove(m); err != nil {
			t.Fatal(err)
		}
	}
	h.engine.unreserve(group)
	h.engine.mu.Lock()
	held := len(h.engine.held)
	h.engine.mu.Unlock()
	if held != 0 {
		t.Errorf("%d sessions still hold reports", held)
	}
	if n := len(h.rec.ofType("error")); n != 0 {
		t.Errorf("%d errors published", n)
	}
	if onDisk(h, parent.ID) {
		t.Error("the report wrote the launcher back")
	}
}

// A delete with no read from disk under way keeps nothing for the ids it
// removed, and one under way keeps them only until it ends.
func TestRemovedIdsAreLetGo(t *testing.T) {
	h := newHarness(t)
	for range 3 {
		s := h.session(t)
		h.engine.save(s)
		if err := h.engine.Delete(s.ID); err != nil {
			t.Fatal(err)
		}
	}
	h.engine.mu.Lock()
	n := len(h.engine.removed)
	h.engine.mu.Unlock()
	if n != 0 {
		t.Errorf("%d removed ids kept with no read under way", n)
	}

	s := h.session(t)
	h.engine.save(s)
	h.engine.beginRead()
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	h.engine.mu.Lock()
	during := h.engine.removed[s.ID]
	h.engine.mu.Unlock()
	h.engine.endRead()
	h.engine.mu.Lock()
	n = len(h.engine.removed)
	h.engine.mu.Unlock()
	if !during || n != 0 {
		t.Errorf("kept during the read = %v, kept after = %d, want true and 0", during, n)
	}
}

// A run whose file was emptied to {} is still known to its launcher after a
// restart: the delete names the file and keeps the launcher.
func TestARunEmptiedToNothingKeepsItsLauncher(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	path := filepath.Join(h.paths.Sessions, child.ID+".json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	restart(t, h)

	err := h.engine.Delete(parent.ID)
	if !errors.Is(err, store.ErrUnreadable) || !strings.Contains(err.Error(), path) {
		t.Fatalf("Delete = %v, want the run's file named", err)
	}
	if !onDisk(h, parent.ID) || !onDisk(h, child.ID) {
		t.Error("a file went although a run could not be read")
	}
}

// A run whose saved output a delete could not remove still keeps its launcher
// after a restart, until the output is gone.
func TestARunWithOutputLeftKeepsItsLauncherAcrossARestart(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permissions do not bind root")
	}
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	spill := h.store.SpillDir(child.ID)
	if err := os.MkdirAll(spill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spill, "out.txt"), []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(spill, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(spill, 0o700) })

	if err := h.engine.Delete(parent.ID); !errors.Is(err, store.ErrLeftBehind) {
		t.Fatalf("Delete = %v, want the output left behind", err)
	}
	restart(t, h)
	if err := h.engine.Delete(parent.ID); !errors.Is(err, store.ErrLeftBehind) {
		t.Fatalf("the delete after a restart = %v, want the output left behind", err)
	}
	if !onDisk(h, parent.ID) {
		t.Fatal("the launcher went while its run's output stayed")
	}

	if err := os.Chmod(spill, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatalf("the delete after the fix = %v", err)
	}
	if onDisk(h, parent.ID) {
		t.Error("the launcher outlived the delete")
	}
}

// A run whose file was replaced by a folder is still known to its launcher
// after a restart: the delete names it and keeps the launcher.
func TestARunWhoseFileIsAFolderKeepsItsLauncher(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	path := filepath.Join(h.paths.Sessions, child.ID+".json")
	blockRemoval(t, path)
	restart(t, h)

	err := h.engine.Delete(parent.ID)
	if !errors.Is(err, store.ErrUnreadable) || !strings.Contains(err.Error(), path) {
		t.Fatalf("Delete = %v, want the run's file named", err)
	}
	if !onDisk(h, parent.ID) {
		t.Error("the launcher went although its run could not be read")
	}
}

func TestALinkedRunKeepsItsLauncherUntilItsFileReturns(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	path := filepath.Join(h.paths.Sessions, child.ID+".json")
	backup := filepath.Join(t.TempDir(), "transcript.json")
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	target := backup + ".restored"
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("Cannot make a link: %v", err)
	}
	restart(t, h)
	if err := h.engine.Delete(parent.ID); !errors.Is(err, store.ErrUnreadable) || !strings.Contains(err.Error(), path) {
		t.Fatalf("Delete = %v, want the unreadable run's path", err)
	}
	if !onDisk(h, parent.ID) {
		t.Fatal("The launcher was removed")
	}
	if err := os.Rename(backup, target); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("Run entry after deletion: %v", err)
	}
}

func TestARestoredRunIsCheckedBeforeRetryingItsCleanup(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("Permissions do not bind root")
	}
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	child := subagentOf(t, h, parent)
	path := filepath.Join(h.paths.Sessions, child.ID+".json")
	transcript, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := h.store.SpillDir(child.ID)
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "note"), []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(out, 0o700) })
	if err := h.engine.Delete(parent.ID); !errors.Is(err, store.ErrLeftBehind) {
		t.Fatalf("First delete = %v", err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(out, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(parent.ID); !errors.Is(err, store.ErrUnreadable) {
		t.Fatalf("Retry = %v, want the recovered entry checked", err)
	}
	if !onDisk(h, parent.ID) || !onDisk(h, child.ID) {
		t.Fatal("The retry removed a transcript")
	}
	if _, err := os.Stat(filepath.Join(out, "note")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, transcript, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(parent.ID); err != nil {
		t.Fatal(err)
	}
	if onDisk(h, parent.ID) || onDisk(h, child.ID) {
		t.Fatal("A restored transcript remained after deletion")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("Saved output after deletion: %v", err)
	}
}

func TestRemovingAKnownUnreadableRunCleansItsOutput(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		h := newHarness(t)
		parent := h.session(t)
		h.engine.save(parent)
		child := subagentOf(t, h, parent)
		out := h.store.SpillDir(child.ID)
		if err := os.MkdirAll(out, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(h.paths.Sessions, child.ID+".json")
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		restart(t, h)
		if err := h.engine.Delete(parent.ID); !errors.Is(err, store.ErrUnreadable) {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if reopen {
			restart(t, h)
		}
		if err := h.engine.Delete(parent.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Errorf("Output remains after removal (reopen %t): %v", reopen, err)
		}
		if got := h.store.Family(parent.ID); len(got) != 0 {
			t.Errorf("Family after removal: %v", got)
		}
	}
}
