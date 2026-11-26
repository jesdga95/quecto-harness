package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/search"
	"github.com/jesdga95/quecto-harness/internal/store"
)

// loose starts a conversation without a folder.
func loose(t *testing.T, h *harness) *Session {
	t.Helper()
	s, err := h.engine.CreateLoose()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// looseChildOf files an idle run under a conversation without a folder, with
// one stored attachment, live and on disk.
func looseChildOf(t *testing.T, h *harness, parent *Session) *Session {
	t.Helper()
	parent.mu.Lock()
	h.engine.waitSavedLocked(parent)
	parent.mu.Unlock()
	child := newSession(store.NewLooseID(), "", h.agent.ID, "Alpha-12B", config.ToolsetFull)
	child.Loose = true
	child.Parent = parent.ID
	child.Entries = []store.Entry{withFile(t, h, "do it")}
	h.engine.mu.Lock()
	h.engine.live[child.ID] = child
	h.engine.mu.Unlock()
	h.engine.save(child)
	return child
}

// withFile is a user entry holding one attachment in the file store.
func withFile(t *testing.T, h *harness, text string) store.Entry {
	t.Helper()
	blobs, err := store.OpenFiles(h.paths.Files)
	if err != nil {
		t.Fatal(err)
	}
	id, n, err := blobs.PutBytes(onePixelPNG())
	if err != nil {
		t.Fatal(err)
	}
	return store.Entry{Kind: "user", Role: "user", Content: text, InContext: true, Visible: true,
		Attachments: []store.Attachment{{Path: "shot.png", Kind: "image", Mime: "image/png", Bytes: n, File: id}}}
}

func fileGone(h *harness, id string) bool {
	_, err := os.Stat(filepath.Join(h.paths.Files, id))
	return errors.Is(err, os.ErrNotExist)
}

// A conversation without a folder is made by its id, names no project and
// is read back as it was written.
func TestALooseConversationIsKeptAsOne(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	if !store.IsLoose(s.ID) || !s.Loose || s.Project != "" {
		t.Fatalf("made %s loose=%v project=%q", s.ID, s.Loose, s.Project)
	}
	h.engine.save(s)
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	s.mu.Unlock()
	doc, err := h.store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !doc.Loose || doc.Project != "" {
		t.Errorf("read back loose=%v project=%q", doc.Loose, doc.Project)
	}
	if got := h.store.ListLoose(); len(got) != 1 || got[0].ID != s.ID {
		t.Errorf("ListLoose = %+v", got)
	}
	if got := h.store.List(h.project); len(got) != 0 {
		t.Errorf("the project lists %d conversations, want none", len(got))
	}
}

// A turn works in the conversation's own folder: a relative write lands there
// without a card, nothing is written to any project, and a rules file there
// is never read into the prompt.
func TestALooseTurnWorksInItsFolder(t *testing.T) {
	h := newHarness(t, "stream_write.txt", "stream_plain.txt")
	s := loose(t, h)
	dir, err := h.store.EnsureScratch(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("OBEY THE FILE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.engine.Send(s.ID, "write it", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)

	doc, _, _, _ := s.Snapshot()
	result := toolResult(t, doc, "write")
	if result.Permission == nil || result.Permission.Decision != "auto" {
		t.Errorf("the write was decided %+v, want auto", result.Permission)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "out.txt")); err != nil || string(raw) != "written by the model\n" {
		t.Errorf("out.txt in the folder = %q, %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(h.project, "out.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the write reached the project: %v", err)
	}
	system := systemOf(h.replay.Body(0))
	if !strings.Contains(system, "in a folder of your own, "+dir+", which is deleted with this conversation") {
		t.Errorf("the prompt does not name the folder: %q", system[:min(len(system), 300)])
	}
	if strings.Contains(system, "OBEY THE FILE") || strings.Contains(system, "QUECTO_SCRATCH") {
		t.Errorf("the prompt carries the rules file or the scratch line: %q", system)
	}
}

// A card in a conversation without a folder keeps no rule: allow always is
// refused without taking the card, which allow once still answers.
func TestALooseCardKeepsNoRule(t *testing.T) {
	h := newHarness(t, "stream_tool.txt", "stream_plain.txt")
	if _, err := h.cfg.Update(func(c *config.Config) { c.Mode = config.ModeAllowlist }); err != nil {
		t.Fatal(err)
	}
	if err := h.cfg.AddAllow("", "ls"); err != nil {
		t.Fatal(err)
	}
	s := loose(t, h)
	if _, err := h.engine.Send(s.ID, "list it", nil); err != nil {
		t.Fatal(err)
	}
	id := waitForCard(t, h, s.ID)
	req, _ := h.broker.Lookup(id)
	if !req.OnceOnly || req.Project != "" {
		t.Errorf("the card is once_only=%v project=%q", req.OnceOnly, req.Project)
	}
	if err := h.broker.Resolve(id, perms.Decision{Kind: perms.DecideAlways, Prefix: "ls"}); !errors.Is(err, perms.ErrOnceOnly) {
		t.Fatalf("allow always = %v, want refused", err)
	}
	if err := h.broker.Resolve(id, perms.Decision{Kind: perms.DecideOnce}); err != nil {
		t.Fatalf("allow once after the refusal = %v", err)
	}
	waitIdle(t, s)
	if got := h.cfg.Allowlist(""); len(got) != 1 {
		t.Errorf("the allowlist changed: %v", got)
	}
}

// A run a conversation without a folder launches has no folder either, and
// works in its launcher's.
func TestALooseRunWorksInItsLaunchersFolder(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")
	s := loose(t, h)
	if _, err := h.engine.Send(s.ID, "delegate it", nil); err != nil {
		t.Fatal(err)
	}
	child := childOf(t, h, s)
	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, s)
	if !child.Loose || child.Project != "" || !store.IsLoose(child.ID) {
		t.Errorf("the run is %s loose=%v project=%q", child.ID, child.Loose, child.Project)
	}
	b := h.engine.briefFor(context.Background(), child, h.cfg.Config())
	want, _ := h.store.ScratchDir(s.ID)
	if b.folder != want || b.rules != "" {
		t.Errorf("the run works in %q with rules %q, want %q", b.folder, b.rules, want)
	}
}

// Deleting a conversation without a folder takes its folder, its runs and
// every attachment they hold, and leaves another conversation's alone.
func TestDeletingALooseConversationTakesAllOfIt(t *testing.T) {
	h := newHarness(t)
	other := loose(t, h)
	other.Entries = []store.Entry{withFile(t, h, "keep me")}
	h.engine.save(other)

	s := loose(t, h)
	s.Entries = []store.Entry{withFile(t, h, "look")}
	h.engine.save(s)
	child := looseChildOf(t, h, s)
	dir, err := h.store.EnsureScratch(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := []string{s.Entries[0].Attachments[0].File, child.Entries[0].Attachments[0].File}

	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{s.ID, child.ID} {
		if onDisk(h, id) {
			t.Errorf("%s is still on disk", id)
		}
		if _, err := os.Stat(filepath.Join(h.paths.Sessions, id+".files")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s.files is still there: %v", id, err)
		}
		if _, err := h.engine.Get(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%s) = %v, want not found", id, err)
		}
	}
	for _, f := range files {
		if !fileGone(h, f) {
			t.Errorf("attachment %s outlived its conversation", f)
		}
	}
	if fileGone(h, other.Entries[0].Attachments[0].File) {
		t.Error("another conversation's attachment went with it")
	}
	if _, err := os.Stat(filepath.Join(h.paths.Sessions, s.ID+".json.deleting")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the mark outlived the delete: %v", err)
	}
	if got := h.rec.droppedIDs(); len(got) != 2 {
		t.Errorf("streams dropped = %v, want the conversation and its run", got)
	}
}

// A rename the folder refuses leaves the conversation whole and able to work.
func TestALooseDeleteThatCannotMarkChangesNothing(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	h.engine.save(s)
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	s.mu.Unlock()
	if err := os.Chmod(h.paths.Sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	err := h.engine.Delete(s.ID)
	if err := os.Chmod(h.paths.Sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err == nil {
		t.Fatal("the delete went through a folder that refuses renames")
	}
	if !onDisk(h, s.ID) || !h.engine.IsLive(s.ID) || !h.store.Has(s.ID) {
		t.Error("the conversation did not survive the failed delete")
	}
	if err := h.engine.Rename(s.ID, "still here"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	s.mu.Unlock()
	doc, err := h.store.Load(s.ID)
	if err != nil || doc.Title != "still here" {
		t.Errorf("a write after the failed delete = %+v, %v", doc, err)
	}
}

// A delete stopped after the mark is hidden from a fresh store and finished
// by a fresh engine, and a link put in place of a run's folder is removed as
// a link.
func TestAnInterruptedLooseDeleteIsFinishedAtStart(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	s.Entries = []store.Entry{withFile(t, h, "look")}
	h.engine.save(s)
	child := looseChildOf(t, h, s)
	for _, m := range []*Session{s, child} {
		m.mu.Lock()
		h.engine.waitSavedLocked(m)
		m.mu.Unlock()
	}
	decoy := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoy, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, filepath.Join(h.paths.Sessions, child.ID+".files")); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	doc := s.doc()
	s.mu.Unlock()
	if err := h.store.MarkDeleting(doc, []string{child.ID}); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(h.paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	if st.Has(s.ID) || st.Has(child.ID) || len(st.List("")) != 0 || len(st.Children(s.ID)) != 0 {
		t.Error("a fresh store lists the family being deleted")
	}
	if _, err := st.Load(child.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("loading the run = %v, want not found", err)
	}
	blobs, err := store.OpenFiles(h.paths.Files)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewEngine(ctx, h.cfg, st, blobs, Clients{Pool: llm.NewPool()}, search.NewProviders(), h.broker, &recorder{})
	e.FinishDeletes()

	entries, err := os.ReadDir(h.paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, d := range entries {
			names = append(names, d.Name())
		}
		t.Errorf("left in the sessions folder: %v", names)
	}
	if _, err := os.Stat(filepath.Join(decoy, "keep.txt")); err != nil {
		t.Errorf("the link's target lost its file: %v", err)
	}
	for _, f := range []string{s.Entries[0].Attachments[0].File, child.Entries[0].Attachments[0].File} {
		if !fileGone(h, f) {
			t.Errorf("attachment %s survived", f)
		}
	}
	if len(st.Marked()) != 0 {
		t.Errorf("marks left = %v", st.Marked())
	}
}

// Deleting a run of a conversation without a folder keeps the folder it
// shares with its launcher.
func TestDeletingALooseRunKeepsTheFolder(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	h.engine.save(s)
	child := looseChildOf(t, h, s)
	dir, err := h.store.EnsureScratch(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.engine.Delete(child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("deleting the run took the shared folder: %v", err)
	}
	if onDisk(h, child.ID) || !onDisk(h, s.ID) {
		t.Error("the run or its launcher is in the wrong place after the delete")
	}
}

// An attachment only the session in memory holds is released with it: every
// run is written before the mark, and the mark holds the conversation.
func TestALooseDeleteReleasesWhatWasNeverWritten(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	h.engine.save(s)
	child := looseChildOf(t, h, s)
	var files []string
	for _, m := range []*Session{s, child} {
		m.mu.Lock()
		h.engine.waitSavedLocked(m)
		entry := withFile(t, h, "only in memory")
		m.Entries = append(m.Entries, entry)
		files = append(files, entry.Attachments[0].File)
		m.mu.Unlock()
	}
	if err := h.engine.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !fileGone(h, f) {
			t.Errorf("attachment %s outlived its conversation", f)
		}
	}
}

// restartWith opens the sessions folder again and finishes its deletes, as a
// harness starting up does.
func restartWith(t *testing.T, h *harness) *store.Sessions {
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
	t.Cleanup(cancel)
	NewEngine(ctx, h.cfg, st, blobs, Clients{Pool: llm.NewPool()}, search.NewProviders(), h.broker, &recorder{}).FinishDeletes()
	return st
}

// A run that cannot be read while a delete is finished keeps the mark, stays
// out of sight, and is removed once it can be read.
func TestARecoveryWaitsForARunItCannotRead(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	h.engine.save(s)
	child := looseChildOf(t, h, s)
	for _, m := range []*Session{s, child} {
		m.mu.Lock()
		h.engine.waitSavedLocked(m)
		m.mu.Unlock()
	}
	s.mu.Lock()
	doc := s.doc()
	s.mu.Unlock()
	if err := h.store.MarkDeleting(doc, []string{child.ID}); err != nil {
		t.Fatal(err)
	}
	kid := filepath.Join(h.paths.Sessions, child.ID+".json")
	if err := os.Chmod(kid, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(kid, 0o600) })
	st := restartWith(t, h)
	if len(st.Marked()) != 1 {
		t.Fatalf("marks = %v, want the unfinished delete", st.Marked())
	}
	if _, err := st.Load(child.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the unreadable run loads as %v, want not found", err)
	}
	if err := os.Chmod(kid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(child.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the run loads as %v once it can be read, want not found", err)
	}
	st = restartWith(t, h)
	if len(st.Marked()) != 0 || onDisk(h, child.ID) {
		t.Errorf("the second start left marks %v, run on disk %v", st.Marked(), onDisk(h, child.ID))
	}
}

// A delete whose cleanup stops says so, keeps the family out of sight, and
// the next start finishes it.
func TestALooseDeleteThatCannotCleanUpIsPending(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	h.engine.save(s)
	child := looseChildOf(t, h, s)
	locked := filepath.Join(h.paths.Sessions, child.ID+".files", "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })

	if err := h.engine.Delete(s.ID); !errors.Is(err, ErrDeletePending) {
		t.Fatalf("Delete = %v, want pending", err)
	}
	if h.store.Has(s.ID) || h.store.Has(child.ID) || h.engine.IsLive(s.ID) {
		t.Error("the family is still in sight")
	}
	if _, err := h.engine.Get(child.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(run) = %v, want not found", err)
	}
	if err := os.Chmod(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	restartWith(t, h)
	if entries, _ := os.ReadDir(h.paths.Sessions); len(entries) != 0 {
		t.Errorf("%d names left in the sessions folder", len(entries))
	}
}

// A run without a folder whose launcher is not a conversation without one is
// neither saved, loaded nor listed.
func TestALooseRunNeedsALooseLauncher(t *testing.T) {
	h := newHarness(t)
	parent := h.session(t)
	h.engine.save(parent)
	parent.mu.Lock()
	h.engine.waitSavedLocked(parent)
	parent.mu.Unlock()
	orphan := &store.Session{ID: store.NewLooseID(), Loose: true, Parent: parent.ID, Agent: h.agent.ID}
	if err := h.store.Save(orphan); err == nil {
		t.Fatal("saved a run without a folder under a project's conversation")
	}
	stray := &store.Session{ID: store.NewLooseID(), Loose: true, Parent: store.NewLooseID(), Agent: h.agent.ID}
	if err := h.store.Save(stray); err == nil {
		t.Fatal("saved a run whose launcher does not exist")
	}
	raw, _ := json.Marshal(stray)
	if err := os.WriteFile(filepath.Join(h.paths.Sessions, stray.ID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Load(stray.ID); err == nil {
		t.Error("loaded a run whose launcher does not exist")
	}
	st, err := store.Open(h.paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	if st.Has(stray.ID) {
		t.Error("a fresh index holds a run whose launcher does not exist")
	}
}

// The card a stream draws keeps no rule, as the one a reload restores does.
func TestTheLiveCardSaysItKeepsNoRule(t *testing.T) {
	r := perms.Request{ID: "perm_x", Tool: "bash", Command: "ls", OnceOnly: true}
	if got := PermissionEvent(r).Fields["once_only"]; got != true {
		t.Errorf("the event carries once_only = %v", got)
	}
}

// A run the store could not read when it opened leaves the family unknown:
// the delete is refused with everything in place, and goes through once the
// run can be read, taking it too.
func TestALooseDeleteWaitsForEveryRunToBeKnown(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	h.engine.save(s)
	child := looseChildOf(t, h, s)
	child.mu.Lock()
	h.engine.waitSavedLocked(child)
	child.mu.Unlock()
	kid := filepath.Join(h.paths.Sessions, child.ID+".json")
	if err := os.Chmod(kid, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(kid, 0o600) })

	st, err := store.Open(h.paths.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	blobs, _ := store.OpenFiles(h.paths.Files)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewEngine(ctx, h.cfg, st, blobs, Clients{Pool: llm.NewPool()}, search.NewProviders(), h.broker, &recorder{})
	if err := e.Delete(s.ID); err == nil || errors.Is(err, ErrDeletePending) {
		t.Fatalf("Delete with a run nobody can read = %v, want refused", err)
	}
	if !onDisk(h, s.ID) || !st.Has(s.ID) {
		t.Fatal("the refused delete took the conversation")
	}
	if err := os.Chmod(kid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if onDisk(h, s.ID) || onDisk(h, child.ID) {
		t.Error("the delete left the conversation or its run")
	}
}

// A run that cannot be written to disk is not started, and the call says so.
func TestARunThatCannotBeKeptIsNotStarted(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	s := loose(t, h)
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	s.mu.Unlock()
	if _, err := h.store.EnsureScratch(s.ID); err != nil {
		t.Fatal(err)
	}
	// The folder stops taking writes once the message is in and the call to
	// launch the run is under way.
	var once sync.Once
	h.rec.mu.Lock()
	h.rec.onPublish = func(e Event) {
		if e.Type == "tool_call" {
			once.Do(func() { os.Chmod(h.paths.Sessions, 0o500) })
		}
	}
	h.rec.mu.Unlock()
	t.Cleanup(func() { os.Chmod(h.paths.Sessions, 0o700) })
	if _, err := h.engine.Send(s.ID, "delegate it", nil); err != nil {
		t.Fatal(err)
	}
	out := toolOutput(t, s, "subagent")
	waitIdle(t, s)
	if err := os.Chmod(h.paths.Sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "subagent:") {
		t.Errorf("the call answered %q", out)
	}
	for _, m := range h.engine.liveSessions() {
		if m.Parent == s.ID {
			t.Errorf("run %s started without a file", m.ID)
		}
	}
}

// A launched run is one instance from the start: a reader that finds it in
// the index the moment it is written gets the live one, and a rename made
// then is what its file holds after its first turn and what its launcher is
// told.
func TestALaunchedRunIsOneInstance(t *testing.T) {
	h, _ := launcher(t)
	h.replay.forModel("Alpha-12B", "stream_subagent.txt", "stream_plain.txt", "stream_plain.txt")
	h.replay.forModel("Beta-30B", "stream_plain.txt")
	var renameErr error
	var reader *Session
	h.engine.launched = func(id string) {
		reader, renameErr = h.engine.Get(id)
		if renameErr == nil {
			renameErr = h.engine.Rename(id, "renamed while launching")
		}
	}
	s := loose(t, h)
	if _, err := h.engine.Send(s.ID, "delegate it", nil); err != nil {
		t.Fatal(err)
	}
	child := childOf(t, h, s)
	waitForEntryFrom(t, s, "Reviewer")
	waitIdle(t, child)
	waitIdle(t, s)
	if renameErr != nil || reader != child {
		t.Fatalf("the reader got %p, %v, want the live %p", reader, renameErr, child)
	}
	child.mu.Lock()
	h.engine.waitSavedLocked(child)
	child.mu.Unlock()
	doc, err := h.store.Load(child.ID)
	if err != nil || doc.Title != "renamed while launching" {
		t.Errorf("the run's file holds %q, %v", doc.Title, err)
	}
	var told string
	for _, e := range h.rec.ofType("child") {
		if title, ok := e.Fields["title"].(string); ok && e.Fields["session"] == child.ID {
			told = title
		}
	}
	if told != "renamed while launching" {
		t.Errorf("the launcher was told %q", told)
	}
}

// A message a page named and that starts a turn is published with that name
// before anything the turn streams; one that joins the queue is not.
func TestANamedMessageIsPublishedAheadOfItsTurn(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := loose(t, h)
	sent, err := h.engine.SendAs(s.ID, "hello", nil, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	h.rec.mu.Lock()
	events := append([]Event(nil), h.rec.events...)
	h.rec.mu.Unlock()
	first := -1
	for i, e := range events {
		if e.Type == "message" {
			if e.Fields["client"] != "c-1" || e.Fields["entry"] != sent.Seq {
				t.Errorf("published %v, want client c-1 and seq %d", e.Fields, sent.Seq)
			}
			first = i
			break
		}
	}
	if first == -1 {
		t.Fatal("the message was not published")
	}
	for _, e := range events[:first] {
		if e.Type == "delta" || e.Type == "reasoning" {
			t.Errorf("the turn streamed %s before its message", e.Type)
		}
	}
}

// A message that comes off the queue is published once, ahead of its turn,
// and says it came off the queue.
func TestAFlushedMessageIsPublishedOnce(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := loose(t, h)
	h.engine.save(s)
	s.mu.Lock()
	s.steer = &Steer{Text: "waiting", From: sender{Client: "c-q"}}
	s.mu.Unlock()
	h.engine.flushSteer(s)
	waitIdle(t, s)
	var echoes []Event
	for _, e := range h.rec.ofType("message") {
		echoes = append(echoes, e)
	}
	if len(echoes) != 1 || echoes[0].Fields["dequeued"] != true {
		t.Errorf("published %d echoes: %v", len(echoes), echoes)
	}
}

// A message queued against a running turn is published once when the turn
// takes it.
func TestAnAbsorbedMessageIsPublishedOnce(t *testing.T) {
	h := newHarness(t)
	release := h.replay.hold("Alpha-12B")
	h.replay.forModel("Alpha-12B", "stream_plain.txt", "stream_plain.txt", "stream_plain.txt")
	s := loose(t, h)
	if _, err := h.engine.SendAs(s.ID, "first", nil, "c-a"); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StatePreflight)
	if sent, err := h.engine.SendAs(s.ID, "second", nil, "c-b"); err != nil || !sent.Queued {
		t.Fatalf("the second message = %+v, %v, want queued", sent, err)
	}
	release()
	waitIdle(t, s)
	count := map[any]int{}
	for _, e := range h.rec.ofType("message") {
		count[e.Fields["entry"]]++
	}
	for entry, n := range count {
		if n != 1 {
			t.Errorf("entry %v was published %d times", entry, n)
		}
	}
	if len(count) != 2 {
		t.Errorf("published %d entries, want 2: %v", len(count), count)
	}
}

// A message queued while the echo of the one taken is held is announced
// after it, so no page reads it as taken.
func TestTheEchoOfATakenMessageComesBeforeTheNextQueued(t *testing.T) {
	h := newHarness(t)
	release := h.replay.hold("Alpha-12B")
	h.replay.forModel("Alpha-12B", "stream_plain.txt", "stream_plain.txt", "stream_plain.txt")
	s := loose(t, h)
	if _, err := h.engine.SendAs(s.ID, "first", nil, "c-1"); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StatePreflight)
	if _, err := h.engine.SendAs(s.ID, "A", nil, "c-a"); err != nil {
		t.Fatal(err)
	}
	queued := make(chan error, 1)
	var once sync.Once
	h.rec.mu.Lock()
	h.rec.onPublish = func(e Event) {
		if e.Type != "message" || e.Fields["dequeued"] != true {
			return
		}
		once.Do(func() {
			go func() {
				_, err := h.engine.SendAs(s.ID, "B", nil, "c-b")
				queued <- err
			}()
			time.Sleep(50 * time.Millisecond)
		})
	}
	h.rec.mu.Unlock()
	release()
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	took, queuedB := -1, -1
	h.rec.mu.Lock()
	for i, e := range h.rec.events {
		if e.Type == "message" && e.Fields["dequeued"] == true && took == -1 {
			took = i
		}
		if e.Type == "queued" && strings.Contains(fmt.Sprint(e.Fields["text"]), "B") && queuedB == -1 {
			queuedB = i
		}
	}
	h.rec.mu.Unlock()
	if took == -1 || (queuedB != -1 && queuedB < took) {
		t.Errorf("the echo of A is event %d and B queued is %d", took, queuedB)
	}
}

// The queue is announced before a turn can take it, so the stream never says
// a message is waiting after it was taken.
func TestAQueuedMessageIsAnnouncedBeforeItIsTaken(t *testing.T) {
	h := newHarness(t)
	release := h.replay.hold("Alpha-12B")
	h.replay.forModel("Alpha-12B", "stream_plain.txt", "stream_plain.txt", "stream_plain.txt")
	s := loose(t, h)
	if _, err := h.engine.SendAs(s.ID, "first", nil, "c-1"); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, StatePreflight)
	var once sync.Once
	h.rec.mu.Lock()
	h.rec.onPublish = func(e Event) {
		if e.Type == "queued" {
			once.Do(func() {
				release()
				time.Sleep(100 * time.Millisecond)
			})
		}
	}
	h.rec.mu.Unlock()
	sent, err := h.engine.SendAs(s.ID, "A", nil, "c-a")
	if err != nil || !sent.Queued {
		t.Fatalf("A = %+v, %v, want queued", sent, err)
	}
	waitIdle(t, s)
	announced, taken := -1, -1
	h.rec.mu.Lock()
	for i, e := range h.rec.events {
		if e.Type == "queued" && announced == -1 {
			announced = i
		}
		if e.Type == "message" && e.Fields["dequeued"] == true && taken == -1 {
			taken = i
		}
	}
	h.rec.mu.Unlock()
	if announced == -1 || taken == -1 || announced > taken {
		t.Errorf("queued is event %d and taken is %d", announced, taken)
	}
}

// A message is on disk before its echo says it was taken, and the entry a
// queued message becomes keeps the names its pages gave it.
func TestAnEchoFollowsTheSave(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := loose(t, h)
	h.engine.save(s)
	onDiskAtEcho := make(chan bool, 1)
	h.rec.mu.Lock()
	h.rec.onPublish = func(e Event) {
		if e.Type != "message" {
			return
		}
		doc, err := h.store.Load(s.ID)
		found := false
		if err == nil {
			for _, entry := range doc.Entries {
				if entry.Kind == "user" && entry.Seq == e.Fields["entry"] {
					found = true
				}
			}
		}
		select {
		case onDiskAtEcho <- found:
		default:
		}
	}
	h.rec.mu.Unlock()
	if _, err := h.engine.SendAs(s.ID, "hello", nil, "c-save"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	if !<-onDiskAtEcho {
		t.Error("the echo came before the message was saved")
	}

	s.mu.Lock()
	s.steer = &Steer{Text: "later", Clients: []string{"c-x", "c-y"}}
	s.mu.Unlock()
	h.engine.absorbSteer(s)
	doc, _, _, _ := s.Snapshot()
	last := doc.Entries[len(doc.Entries)-1]
	if got, _ := last.Meta["clients"].([]string); len(got) != 2 || got[0] != "c-x" {
		t.Errorf("the taken entry keeps clients %v", last.Meta["clients"])
	}
}

// A message that cannot be written is refused with nothing echoed and
// nothing kept, so it can be sent again.
func TestAMessageThatCannotBeSavedIsRefused(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := loose(t, h)
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	s.mu.Unlock()
	if err := os.Chmod(h.paths.Sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h.paths.Sessions, 0o700) })
	if _, err := h.engine.SendAs(s.ID, "lost?", nil, "c-lost"); err == nil {
		t.Fatal("a message that could not be saved was accepted")
	}
	if len(h.rec.ofType("message")) != 0 {
		t.Error("a message that could not be saved was echoed")
	}
	if s.State() != StateIdle {
		t.Errorf("the session is %s, want idle", s.State())
	}
	doc, _, _, _ := s.Snapshot()
	if len(doc.Entries) != 0 {
		t.Errorf("the session holds %d entries", len(doc.Entries))
	}
}

// A message the disk will not keep stays on the queue as it was, whether a
// turn took it or a flush delivered it, and goes once the disk takes it.
func TestAQueuedMessageTheDiskRefusesStaysQueued(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	s := loose(t, h)
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	held := &Steer{Text: "A", From: sender{Name: "Reviewer", Session: "sess_x"}, Clients: []string{"c-a"}}
	s.steer = held
	s.mu.Unlock()
	if err := os.Chmod(h.paths.Sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h.paths.Sessions, 0o700) })

	if h.engine.absorbSteer(s) {
		t.Fatal("a turn took a message the disk refused")
	}
	h.engine.flushSteer(s)
	s.mu.Lock()
	got, entries, state := s.steer, len(s.Entries), s.state
	s.mu.Unlock()
	if got == nil || got.Text != "A" || got.From.Name != "Reviewer" || len(got.Clients) != 1 || entries != 0 || state != StateIdle {
		t.Fatalf("after the refusals the queue is %+v with %d entries in %s", got, entries, state)
	}
	if len(h.rec.ofType("message")) != 0 {
		t.Error("a refused message was echoed")
	}

	if err := os.Chmod(h.paths.Sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	h.engine.flushSteer(s)
	waitIdle(t, s)
	doc, _, _, _ := s.Snapshot()
	if len(doc.Entries) == 0 || doc.Entries[0].Content != "A" || s.Steer() != nil {
		t.Errorf("once the disk took it the session holds %+v and queue %+v", doc.Entries, s.Steer())
	}
}

// A message left on the queue after the disk refused it goes before the next
// one sent, which joins it.
func TestARefusedQueuedMessageGoesBeforeTheNext(t *testing.T) {
	h := newHarness(t, "stream_plain.txt", "stream_plain.txt")
	s := loose(t, h)
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	s.steer = &Steer{Text: "A", Clients: []string{"c-a"}}
	s.mu.Unlock()
	if err := os.Chmod(h.paths.Sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h.paths.Sessions, 0o700) })
	h.engine.flushSteer(s)
	if s.Steer() == nil {
		t.Fatal("the refused message left the queue")
	}
	if err := os.Chmod(h.paths.Sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	sent, err := h.engine.SendAs(s.ID, "B", nil, "c-b")
	if err != nil || sent.Queued {
		t.Fatalf("B = %+v, %v", sent, err)
	}
	waitIdle(t, s)
	doc, err := h.store.Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	for _, e := range doc.Entries {
		if e.Kind == "user" {
			users = append(users, e.Content)
		}
	}
	if len(users) != 1 || users[0] != "A\nB" {
		t.Errorf("the session holds %q, want A then B in one message", users)
	}
}

// A send waiting for a session held by a slow write does not hold the
// engine, so other sessions are still reached.
func TestAWaitingSendDoesNotHoldTheEngine(t *testing.T) {
	h := newHarness(t, "stream_plain.txt")
	busy, other := loose(t, h), loose(t, h)
	busy.mu.Lock()
	done := make(chan struct{})
	go func() {
		h.engine.SendAs(busy.ID, "waits", nil, "c-w")
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	reached := make(chan error, 1)
	go func() {
		_, err := h.engine.Get(other.ID)
		reached <- err
	}()
	select {
	case err := <-reached:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("another session could not be reached while one was held")
	}
	busy.mu.Unlock()
	<-done
	waitIdle(t, busy)
}

// When the disk refuses a message joined to one waiting, the one waiting
// waits as it was and the new one is refused.
func TestARefusedJoinLeavesTheQueueAsItWas(t *testing.T) {
	h := newHarness(t)
	s := loose(t, h)
	s.mu.Lock()
	h.engine.waitSavedLocked(s)
	s.steer = &Steer{Text: "A", Clients: []string{"c-a"}}
	s.mu.Unlock()
	if err := os.Chmod(h.paths.Sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(h.paths.Sessions, 0o700) })
	before := len(h.rec.events)
	if _, err := h.engine.SendAs(s.ID, "B", nil, "c-b"); err == nil {
		t.Fatal("a message the disk refused was accepted")
	}
	got := s.Steer()
	if got == nil || got.Text != "A" || len(got.Clients) != 1 || s.State() != StateIdle {
		t.Errorf("the queue is %+v in %s, want A alone at rest", got, s.State())
	}
	h.rec.mu.Lock()
	for _, e := range h.rec.events[before:] {
		if e.Type == "queued" || e.Type == "message" {
			t.Errorf("the refusal published %s %v", e.Type, e.Fields)
		}
	}
	h.rec.mu.Unlock()
}

// A delete that finds a member held by a long write answers busy without
// holding the engine meanwhile.
func TestADeleteWaitingForAWriteDoesNotHoldTheEngine(t *testing.T) {
	h := newHarness(t)
	busy, other := loose(t, h), loose(t, h)
	for _, m := range []*Session{busy, other} {
		m.mu.Lock()
		h.engine.waitSavedLocked(m)
		m.mu.Unlock()
	}
	busy.mu.Lock()
	deleted := make(chan error, 1)
	go func() { deleted <- h.engine.Delete(busy.ID) }()
	time.Sleep(20 * time.Millisecond)
	reached := make(chan error, 1)
	go func() {
		_, err := h.engine.Get(other.ID)
		reached <- err
	}()
	select {
	case err := <-reached:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Error("another session could not be reached while a delete waited")
	}
	err := <-deleted
	busy.mu.Unlock()
	if !errors.Is(err, ErrBusy) {
		t.Errorf("the delete = %v, want busy", err)
	}
}
