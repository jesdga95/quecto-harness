package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

// SetScratchRoot names the folder that conversations started without a
// project get their own folders in. It is called once, before the first
// request.
func (e *Engine) SetScratchRoot(root string) { e.scratch = root }

// ScratchRoot is the folder SetScratchRoot named.
func (e *Engine) ScratchRoot() string { return e.scratch }

// Fence is what keeps the tools of s, or of no session when s is nil, out
// of the scratch folders of other conversations. s reaches its own folder,
// and the one a move left, which holds what the move did not copy.
func (e *Engine) Fence(s *Session) tools.Fence {
	f := tools.Fence{Root: e.scratch}
	if s == nil || e.scratch == "" {
		return f
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Scratch {
		f.Own = append(f.Own, s.Project)
	}
	if s.LeftIn != "" {
		f.Own = append(f.Own, s.LeftIn)
	}
	return f
}

// CreateScratch starts a conversation in a new folder of its own, for work
// that needs no existing project. The record is on disk before the id is
// handed out. A failed write reports where to recover the folder.
func (e *Engine) CreateScratch() (*Session, error) {
	if e.scratch == "" {
		return nil, errors.New("No folder is set for conversations without a project")
	}
	a, err := e.active()
	if err != nil {
		return nil, err
	}
	dir, err := store.MintScratch(e.scratch, time.Now())
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("Identify the new folder at %s: %w", dir, err)
	}
	creationError := func(cause error) error {
		trash, err := store.TrashScratch(e.scratch, dir, store.FileID(info))
		if err != nil {
			return fmt.Errorf("Create the conversation: %w. Could not move %s to the trash: %v", cause, dir, err)
		}
		return fmt.Errorf("Create the conversation: %w. Its folder was moved to %s", cause, trash)
	}
	s := newSession(store.NewID(), dir, a.ID, a.Model, e.cfg.Config().Toolset)
	s.Scratch, s.ScratchID, s.scratchInfo = true, store.FileID(info), info
	// The owner is on record before the session, so no folder a session
	// names lacks one.
	ledger := e.scratchLedger()
	if err := ledger.Own(dir, s.ID, info); err != nil {
		return nil, creationError(err)
	}
	s.mu.Lock()
	doc := s.doc()
	s.mu.Unlock()
	if err := e.store.Save(doc); err != nil {
		ledger.Forget(dir)
		return nil, creationError(err)
	}
	e.mu.Lock()
	e.setLive(s)
	e.mu.Unlock()
	return s, nil
}

// commit writes the session now and reports whether the disk took it, for
// the steps a later one depends on. Any write under way finishes first, so an
// older snapshot cannot land after this one.
func (e *Engine) commit(s *Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.waitSavedLocked(s)
	if s.gone {
		return ErrNotFound
	}
	s.dirty, s.durable = false, false
	return e.store.Save(s.doc())
}

// claims lists every folder a session names, on disk or only in memory. ok
// is false while a session file could not be read.
func (e *Engine) claims() ([]string, bool) {
	claims, ok := e.store.Claims()
	for _, s := range e.liveSessions() {
		s.mu.Lock()
		claims = append(claims, s.Project, s.Pending, s.LeftIn)
		s.mu.Unlock()
	}
	return claims, ok
}

// scratchLedger is the record of who made each scratch folder and which may
// go. There is one, so removals by a delete and by the sweep exclude each other.
func (e *Engine) scratchLedger() *store.ScratchLedger {
	e.ledgerOnce.Do(func() {
		e.ledger = store.NewScratchLedger(filepath.Join(e.cfg.Paths().Home, "scratch-owners"), e.scratch, e.store.Identity())
	})
	return e.ledger
}

// TrashFailures reads the storage status without contacting the model endpoint.
func (e *Engine) TrashFailures() ([]store.TrashFailure, error) {
	if e.scratch == "" {
		return []store.TrashFailure{}, nil
	}
	if err := e.store.Replaced(); err != nil {
		return nil, err
	}
	return e.scratchLedger().TrashFailures()
}

// orderRemoval records that the scratch folder dir, made for session id, may
// be removed. It is written before the session record goes, so a crash in
// between leaves the folder held by the record rather than unexplained. A
// folder whose record allows no order is kept; an order the disk refuses is
// an error, so the delete stops.
func (e *Engine) orderRemoval(dir, id string) (bool, error) {
	if !store.IsScratch(e.scratch, dir) {
		return false, nil
	}
	err := e.scratchLedger().Order(dir, id, store.RemoveDeleted, "", time.Now())
	if errors.Is(err, store.ErrOrderNotWritten) {
		return false, fmt.Errorf("Record that %s may be removed: check the permissions on %s: %w",
			dir, filepath.Join(e.cfg.Paths().Home, "scratch-owners"), err)
	}
	if err != nil {
		log.Printf("%s is kept: %v", dir, err)
		return false, nil
	}
	return true, nil
}

// Kept is the folder that stays on disk when the conversation id is deleted,
// empty when none does. A scratch folder stays unless its owner record lets
// the conversation order it removed. A folder a move left stays when the copy
// was not whole, or when it changed after the copy.
func (e *Engine) Kept(id string) (string, error) {
	s, err := e.Get(id)
	if err != nil {
		return "", err
	}
	e.store.Reread()
	kept, _ := e.keptBy(s, true)
	return kept, nil
}

// keptBy is Kept for a session in hand. A run whose launcher is gone holds
// the launcher's folder in its place. told is a person asked: a folder that
// could go stays while a session file is unread, and deferred reports that.
func (e *Engine) keptBy(s *Session, told bool) (kept string, deferred bool) {
	s.mu.Lock()
	owner, project, scratch, parent, leftIn := s.ID, s.Project, s.Scratch, s.Parent, s.LeftIn
	keeps := (&store.Session{Parent: s.Parent, LeftIn: s.LeftIn, Carried: s.Carried}).Keeps()
	s.mu.Unlock()
	if scratch && store.IsScratch(e.scratch, project) && e.scratchLedger().Missing(project) {
		return "", false
	}
	if parent != "" {
		if !e.LauncherGone(parent) {
			return "", false
		}
		owner = parent
		if scratch && told && e.siblingHolds(parent, s.ID, project) {
			return project, false
		}
	}
	replaced := e.store.Replaced() != nil
	unread := told && len(e.store.Unread()) > 0
	switch {
	case scratch && store.IsScratch(e.scratch, project):
		if replaced || !e.scratchLedger().Owns(project, owner) {
			return project, false
		}
		return keptIf(project, unread)
	case keeps != "":
		if store.IsScratch(e.scratch, keeps) && e.scratchLedger().Missing(keeps) {
			return "", false
		}
		return keeps, false
	case leftIn != "":
		if replaced {
			return e.scratchLedger().KeptCopy(leftIn, true), false
		}
		if kept := e.scratchLedger().KeptCopy(leftIn, false); kept != "" || !unread {
			return kept, false
		}
		return keptIf(e.scratchLedger().KeptCopy(leftIn, true), true)
	}
	return "", false
}

// keptIf is folder, deferred, while a session file is unread, and none after.
func keptIf(folder string, unread bool) (string, bool) {
	if !unread || folder == "" {
		return "", false
	}
	return folder, true
}

// siblingHolds checks the other runs that can still use an absent launcher's folder.
func (e *Engine) siblingHolds(parent, id, folder string) bool {
	for _, run := range e.store.Children(parent) {
		if run.ID != id && run.Project == folder {
			return true
		}
	}
	for _, run := range e.liveSessions() {
		if run.ID != id && run.Parent == parent {
			if project, _ := run.Where(); project == folder {
				return true
			}
		}
	}
	return false
}

// LauncherGone reports whether the session id has no record and is not in
// memory, which leaves the runs it launched holding its folder.
func (e *Engine) LauncherGone(id string) bool {
	return e.store.Gone(id) && !e.Holds(id)
}

// removeScratch trashes an ordered folder only if the session store is intact
// and every session is readable and has released the folder.
func (e *Engine) removeScratch(dir string) *store.TrashFailure {
	if err := e.store.Replaced(); err != nil {
		log.Printf("%s is left behind: %v", dir, err)
		return nil
	}
	claims, ok := e.claims()
	if !ok {
		log.Printf("%s is left behind: a session file could not be read", dir)
		return nil
	}
	for _, c := range claims {
		if filepath.Clean(c) == filepath.Clean(dir) {
			return nil
		}
	}
	if _, err := e.scratchLedger().Remove(dir, time.Now()); err != nil {
		if failure := store.RetainedFolder(err); failure != nil {
			log.Printf("%s is kept: %s", failure.Path, failure.Reason)
			return failure
		}
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("%s: %v: the folder is left behind", dir, err)
		}
	}
	return nil
}

// Sweep timing: the first pass waits for the harness to settle, and later
// ones run a few times a day.
const (
	sweepFirst = 2 * time.Minute
	sweepEvery = 6 * time.Hour
)

// Sweep moves to the trash, now and then, the scratch folders no conversation
// holds. It returns when ctx ends.
func (e *Engine) Sweep(ctx context.Context) {
	if e.scratch == "" {
		return
	}
	wait := sweepFirst
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = sweepEvery
		e.sweepOnce(time.Now())
	}
}

// sweepOnce is one pass. Nothing is moved while a session file cannot be
// read, since that one may hold any folder, or once the session folder is
// not the one opened.
func (e *Engine) sweepOnce(now time.Time) []string {
	if err := e.store.Replaced(); err != nil {
		log.Printf("Scratch sweep skipped: %v", err)
		return nil
	}
	claims, ok := e.claims()
	if !ok {
		log.Printf("Scratch sweep skipped: a session file could not be read")
		return nil
	}
	removed := e.scratchLedger().Sweep(claims, now)
	if len(removed) > 0 {
		log.Printf("Scratch sweep moved %d folders to the trash", len(removed))
	}
	return removed
}

// parseDirectoryArgs reads the path change_directory or request_directory
// names, trimmed, and empty when it names none.
func parseDirectoryArgs(call llm.ToolCall) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("read the arguments: %w", err)
	}
	return strings.TrimSpace(args.Path), nil
}

// AdmitFolder resolves the folder a directory tool names: absolute, existing,
// and not one the harness keeps for itself.
func (e *Engine) AdmitFolder(arg string) (string, error) {
	if !filepath.IsAbs(arg) {
		return "", fmt.Errorf("%s is not an absolute path: pass the folder's full path", arg)
	}
	real, err := config.ResolveProject(arg)
	if err != nil {
		return "", err
	}
	if config.ContainsDirectory(e.scratch, real) {
		return "", fmt.Errorf("%s is in the folder the harness keeps for conversations without a project, so it cannot be used", real)
	}
	if home := e.cfg.Paths().Home; config.ContainsDirectory(home, real) {
		return "", fmt.Errorf("%s is in the harness state folder, so it cannot be used", real)
	}
	return real, nil
}

// AdmitProject is AdmitFolder for a folder a conversation moves to, which
// also may not hold the scratch folders: its tree would reach them all.
func (e *Engine) AdmitProject(arg string) (string, error) {
	real, err := e.AdmitFolder(arg)
	if err != nil {
		return "", err
	}
	if config.ContainsDirectory(real, e.scratch) {
		return "", fmt.Errorf("%s holds the folders of conversations without a project, so a conversation cannot move to it: choose a folder inside it", real)
	}
	return real, nil
}

// OverlapsScratch reports whether a tree contains the scratch root or lies inside it.
func (e *Engine) OverlapsScratch(dir string) bool {
	return config.ContainsDirectory(dir, e.scratch) || config.ContainsDirectory(e.scratch, dir)
}

// directoryRequest builds the card for a directory tool, or reports that the
// call needs none. refused is why a folder that cannot be used is turned
// down without a card, outside full mode, so the tool never resolves it
// afresh with nobody having seen it.
func (e *Engine) directoryRequest(s *Session, call llm.ToolCall, mode config.Mode) (req perms.Request, ask bool, refused error) {
	req = perms.Request{Tool: call.Function.Name, Args: string(call.Function.Arguments)}
	path, err := parseDirectoryArgs(call)
	if err != nil {
		return req, false, err
	}
	switch {
	case call.Function.Name == "request_directory" && path == "":
		// The picker is the user's own act, so it is shown in every mode.
		return req, true, nil
	case call.Function.Name == "change_directory" && s.Parent != "", mode == config.ModeFull:
		return req, false, nil
	}
	admit := e.AdmitFolder
	if call.Function.Name == "change_directory" {
		admit = e.AdmitProject
	}
	real, err := admit(path)
	if err != nil {
		return req, false, err
	}
	req.Path = real
	if _, scratch := s.Where(); scratch && call.Function.Name == "change_directory" {
		req.Occupied = !emptyFolder(real)
	}
	return req, true, nil
}

// emptyFolder reports whether dir holds nothing. A folder that cannot be read
// is not called empty.
func emptyFolder(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	names, err := f.Readdirnames(1)
	return len(names) == 0 && errors.Is(err, io.EOF)
}

// changeDirectory grants the folder at once and moves the conversation there
// when the turn ends. approved is the folder the card showed, if any. Nothing
// is granted unless the disk keeps it.
func (e *Engine) changeDirectory(s *Session, runner *tools.Runner, call llm.ToolCall, approved string) (tools.Result, error) {
	if s.Parent != "" {
		return tools.Result{}, errors.New("only the conversation that launched you can move to another folder: use request_directory to reach one")
	}
	path, err := parseDirectoryArgs(call)
	if err != nil {
		return tools.Result{}, err
	}
	real, err := e.AdmitProject(path)
	if err != nil {
		return tools.Result{}, err
	}
	if approved != "" && real != approved {
		return tools.Result{}, fmt.Errorf("%s now leads to another folder than the one approved: ask again", path)
	}
	info, err := os.Stat(real)
	if err != nil {
		return tools.Result{}, err
	}
	s.mu.Lock()
	if real == s.Project {
		s.mu.Unlock()
		return tools.Result{Output: "This conversation already works in " + real + "."}, nil
	}
	before := s.moveState()
	// A move asked for earlier in the turn is replaced, and the access it
	// granted for its own folder goes with it.
	if s.PendingGrant && s.Pending != real {
		s.Folders = slices.DeleteFunc(s.Folders, func(f string) bool { return f == s.Pending })
	}
	// Asking again for the same folder keeps whether the move granted it.
	grant := s.PendingGrant && s.Pending == real
	s.Pending, s.pendingInfo, s.PendingGrant = real, info, grant
	if !slices.Contains(s.Folders, real) {
		s.Folders = append(s.Folders, real)
		s.PendingGrant = true
	}
	s.mu.Unlock()
	if err := e.commitFolders(s, runner, before); err != nil {
		return tools.Result{}, fmt.Errorf("save the move: %w", err)
	}
	return tools.Result{Output: prompts.DirectoryGranted(real)}, nil
}

// requestDirectory gives the conversation an extra folder: the one named, or
// the one the user picked on the card.
func (e *Engine) requestDirectory(s *Session, runner *tools.Runner, call llm.ToolCall, d perms.Decision) (tools.Result, error) {
	path, err := parseDirectoryArgs(call)
	if err != nil {
		return tools.Result{}, err
	}
	arg := path
	if arg == "" {
		if d.Path == "" {
			return tools.Result{Output: "The user chose no folder."}, nil
		}
		arg = d.Path
	}
	real, err := e.AdmitFolder(arg)
	if err != nil {
		return tools.Result{}, err
	}
	if path != "" && d.Path != "" && real != d.Path {
		return tools.Result{}, fmt.Errorf("%s now leads to another folder than the one approved: ask again", path)
	}
	s.mu.Lock()
	before := s.moveState()
	if real != s.Project && !slices.Contains(s.Folders, real) {
		s.Folders = append(s.Folders, real)
	}
	// Granted on its own, the folder stays when a move to it does not happen.
	if real == s.Pending {
		s.PendingGrant = false
	}
	s.mu.Unlock()
	if err := e.commitFolders(s, runner, before); err != nil {
		return tools.Result{}, fmt.Errorf("save the folder: %w", err)
	}
	if path == "" {
		return tools.Result{Output: "The user chose " + real + ". This conversation can reach it now."}, nil
	}
	return tools.Result{Output: "This conversation can reach " + real + " now."}, nil
}

// commitFolders writes the session and hands the runner its folders, or puts
// back the state before when the disk refuses the write.
func (e *Engine) commitFolders(s *Session, runner *tools.Runner, before moveState) error {
	if err := e.commit(s); err != nil {
		s.mu.Lock()
		s.restoreMove(before)
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	runner.Folders = slices.Clone(s.Folders)
	s.mu.Unlock()
	return nil
}

// applyMove moves the conversation to the folder change_directory named, and
// copies a scratch folder's files once the move is on disk. It runs when a
// turn ends and again before the next one starts, which finishes a copy a
// restart cut off. The page is told only of steps the disk kept.
func (e *Engine) applyMove(s *Session) {
	s.mu.Lock()
	pending, info := s.Pending, s.pendingInfo
	s.mu.Unlock()
	changed := false
	if pending != "" {
		changed = e.moveTo(s, pending, info)
	}
	if e.carry(s) {
		changed = true
	}
	if changed {
		project, _ := s.Where()
		e.publish(s.ID, ev("moved", map[string]any{"project": project}))
	}
}

// moveState is what a move changes, kept to put back when the disk refuses
// the change.
type moveState struct {
	project, pending, leftIn, leftInID, carryTo, carryToID string
	scratch, grant                                         bool
	folders                                                []string
	carried                                                *store.Carried
	info                                                   os.FileInfo
	entries                                                int
}

// moveState takes the fields a move changes. The caller holds the lock.
func (s *Session) moveState() moveState {
	return moveState{
		project: s.Project, pending: s.Pending,
		leftIn: s.LeftIn, leftInID: s.LeftInID, carryTo: s.CarryTo, carryToID: s.CarryToID,
		scratch: s.Scratch, grant: s.PendingGrant, folders: slices.Clone(s.Folders),
		carried: s.Carried, info: s.pendingInfo, entries: len(s.Entries),
	}
}

// restoreMove puts them back. The caller holds the lock.
func (s *Session) restoreMove(m moveState) {
	s.Project, s.Pending, s.LeftIn, s.LeftInID, s.CarryTo, s.CarryToID = m.project, m.pending, m.leftIn, m.leftInID, m.carryTo, m.carryToID
	s.Scratch, s.PendingGrant, s.Folders, s.Carried, s.pendingInfo = m.scratch, m.grant, m.folders, m.carried, m.info
	s.Entries = s.Entries[:m.entries]
}

// moveTo switches the project and reports whether the disk kept a change. A
// folder that moved, changed, or was approved before a restart is not moved
// to, and the access the move granted for it is taken back. A change the
// disk refuses is undone in memory as well, so the next turn tries again.
func (e *Engine) moveTo(s *Session, pending string, info os.FileInfo) bool {
	var err error
	if info == nil {
		err = errors.New("the harness restarted before the move was made: ask for it again")
	}
	real := pending
	if err == nil {
		real, err = config.ResolveProject(pending)
	}
	if err == nil && real != pending {
		err = fmt.Errorf("%s now leads to %s", pending, real)
	}
	if err == nil {
		if now, serr := os.Stat(real); serr != nil || !os.SameFile(now, info) {
			err = fmt.Errorf("%s is not the folder that was approved", pending)
		}
	}
	if err == nil && !e.cfg.HasProject(real) {
		_, err = e.cfg.AddProject(real)
	}
	// An order left by a delete that did not finish no longer holds once the
	// folder is left: it is kept until a copy shows nothing stays in it.
	if project, scratch := s.Where(); err == nil && scratch {
		if werr := e.scratchLedger().Withdraw(project); werr != nil {
			err = fmt.Errorf("keep %s: %w", project, werr)
		}
	}
	s.mu.Lock()
	before := s.moveState()
	if err != nil {
		if s.PendingGrant {
			s.Folders = slices.DeleteFunc(s.Folders, func(f string) bool { return f == pending })
		}
		s.Pending, s.pendingInfo, s.PendingGrant = "", nil, false
		s.appendEntry(store.Entry{Kind: "moved", Role: "system", Visible: true,
			Meta: map[string]any{"to": pending, "error": err.Error()}})
	} else {
		s.Project, s.Pending, s.pendingInfo, s.PendingGrant = real, "", nil, false
		s.Folders = slices.DeleteFunc(s.Folders, func(f string) bool { return f == real })
		if before.scratch {
			// The copy follows once this is on disk. The folders are named
			// with their identities, so a copy a restart cut off goes only
			// from the folder left to the folder moved to.
			s.Scratch, s.LeftIn, s.CarryTo, s.Carried = false, before.project, real, nil
			// The folder left is checked against what it was when it was made,
			// not when the move happened.
			s.LeftInID, s.CarryToID = s.ScratchID, store.FileID(info)
			s.leftInfo, s.carryInfo = s.scratchInfo, info
		} else {
			s.appendEntry(store.Entry{Kind: "moved", Role: "system", Visible: true, Meta: map[string]any{"to": real}})
		}
	}
	s.mu.Unlock()
	if cerr := e.commit(s); cerr != nil {
		s.mu.Lock()
		s.restoreMove(before)
		s.mu.Unlock()
		e.publish(s.ID, ev("error", map[string]any{"kind": "save", "message": "The move to " + pending + " was not saved: " + cerr.Error()}))
		return false
	}
	return true
}

// carry copies the files of the scratch folder a move left into the folder it
// moved to, once, and reports whether the disk kept the result. The copy runs
// between the two folders as opened and checked against the ones the move
// named; one that cannot be opened leaves the copy due for the next turn.
func (e *Engine) carry(s *Session) bool {
	s.mu.Lock()
	from, fromID, to, toID := s.LeftIn, s.LeftInID, s.CarryTo, s.CarryToID
	fromSeen, toSeen := s.leftInfo, s.carryInfo
	// A record that left a folder without naming where its files go keeps
	// them due and the folder held, with nothing guessed and no note.
	due := s.LeftIn != "" && s.Carried == nil && to != ""
	s.mu.Unlock()
	if !due {
		return false
	}
	c := store.Carried{To: to}
	// Anything in the folder changed after this is not in the copy.
	began := time.Now()
	retry := func(err error) bool {
		e.publish(s.ID, ev("error", map[string]any{"kind": "move",
			"message": "The files of this conversation's earlier folder were not copied yet: " + err.Error() + ". The copy is tried again before the next turn."}))
		return false
	}
	settle := func() bool {
		notice, err := e.checkCarry(s, true)
		if notice != nil && notice.Unreadable {
			return retry(fmt.Errorf("copy record %s %s", notice.Record, notice.recordStatus()))
		}
		if notice != nil {
			c.Interrupted, c.InterruptedNames, c.DestinationUnavailable = notice.Count, notice.Names, notice.Unavailable
		}
		return err == nil || retry(err)
	}
	switch src, serr := os.OpenRoot(from); {
	case errors.Is(serr, os.ErrNotExist):
		c.Gone = true
		if !settle() {
			return false
		}
	case serr != nil:
		return retry(serr)
	default:
		defer src.Close()
		dst, derr := os.OpenRoot(to)
		if derr != nil && !errors.Is(derr, syscall.ENOTDIR) {
			return retry(derr)
		}
		if dst != nil {
			defer dst.Close()
		}
		switch {
		case !sameFolder(src, fromSeen, fromID):
			c.Reason = "the folder this conversation left could not be confirmed as the same one"
		case dst == nil || !sameFolder(dst, toSeen, toID):
			c.Reason = "the folder this conversation moved to could not be confirmed as the same one"
		}
		switch {
		case c.Reason != "":
			if !settle() {
				return false
			}
		default:
			got, err := store.CarryRoots(src, dst, e.scratchLedger().Temps(from))
			if err != nil {
				return retry(err)
			}
			got.To = to
			c = got
		}
	}
	s.mu.Lock()
	before := s.moveState()
	s.Carried = &c
	s.leftInfo, s.carryInfo = nil, nil
	s.appendEntry(store.Entry{Kind: "moved", Role: "system", Visible: true, Meta: movedMeta(&c, from)})
	s.mu.Unlock()
	if err := e.commit(s); err != nil {
		s.mu.Lock()
		s.restoreMove(before)
		s.leftInfo, s.carryInfo = fromSeen, toSeen
		s.mu.Unlock()
		e.publish(s.ID, ev("error", map[string]any{"kind": "save", "message": err.Error()}))
		return false
	}
	e.forgetCarry(from)
	// Only a folder whose every file is now in the new one may go, and only
	// after the wait. One that keeps anything stays until the person acts.
	if c.Complete() {
		if err := e.scratchLedger().Order(from, s.ID, store.RemoveCopied, to, began); err != nil {
			log.Printf("%s is kept: %v", from, err)
		}
	}
	return true
}

// forgetCarry releases the names retained until the copy result or deletion
// is on disk. A record that cannot be read is kept for the person to check.
func (e *Engine) forgetCarry(from string) {
	if from == "" {
		return
	}
	path := e.scratchLedger().Temps(from)
	if _, err := store.InspectCarry(nil, nil, path); errors.Is(err, store.ErrCarryRecord) {
		log.Printf("The copy record %s is kept: %v", path, err)
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("Could not remove the copy record %s: %v", path, err)
	}
}

// CarryNotice describes an interrupted copy: the files it left unresolved, or
// its record when that cannot be read.
type CarryNotice struct {
	Destination   string   `json:"destination"`
	DestinationID string   `json:"destination_id,omitempty"`
	Names         []string `json:"names,omitempty"`
	Count         int      `json:"count"`
	Unavailable   bool     `json:"unavailable,omitempty"`
	Recovery      string   `json:"recovery,omitempty"`
	Record        string   `json:"record,omitempty"`
	Unreadable    bool     `json:"unreadable,omitempty"`
	DamagedLine   int      `json:"damaged_line,omitempty"`
}

func (n *CarryNotice) recordStatus() string {
	if n.DamagedLine > 0 {
		return fmt.Sprintf("is damaged at line %d", n.DamagedLine)
	}
	return "cannot be read"
}

// DeletionPreview is what a delete leaves on disk, shown before it runs. Kept
// is the folder that stays, Deferred that it goes once every session file
// reads, and Token stands for Carry, the files an interrupted copy left.
type DeletionPreview struct {
	Kept     string       `json:"kept"`
	Deferred bool         `json:"deferred,omitempty"`
	Carry    *CarryNotice `json:"carry,omitempty"`
	Token    string       `json:"carry_token,omitempty"`
}

func (n *CarryNotice) token() string {
	if n == nil {
		return ""
	}
	data, _ := json.Marshal(n)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// PreviewDelete is what deleting the session id would leave on disk.
func (e *Engine) PreviewDelete(id string) (DeletionPreview, error) {
	s, err := e.Get(id)
	if err != nil {
		return DeletionPreview{}, err
	}
	e.store.Reread()
	kept, deferred := e.keptBy(s, true)
	n, err := e.checkCarry(s, false)
	if err != nil {
		return DeletionPreview{}, fmt.Errorf("Could not check the interrupted copy: %w. Check the destination and try again", err)
	}
	return DeletionPreview{Kept: kept, Deferred: deferred, Carry: n, Token: n.token()}, nil
}

// checkCarry inspects the copy journal. Cleanup only touches the destination
// recorded by the move. A completed move reports the names saved with it.
func (e *Engine) checkCarry(s *Session, clean bool) (*CarryNotice, error) {
	s.mu.Lock()
	from := carryFolder{s.LeftIn, s.LeftInID, s.leftInfo}
	to := carryFolder{s.CarryTo, s.CarryToID, s.carryInfo}
	c := s.Carried
	s.mu.Unlock()
	if from.path == "" || to.path == "" {
		return nil, nil
	}
	temps := e.scratchLedger().Temps(from.path)
	n := &CarryNotice{Destination: to.path, DestinationID: to.id}
	if c != nil {
		n.Names, n.Count, n.Unavailable = c.InterruptedNames, c.Interrupted, c.DestinationUnavailable
		if n.Count > 0 && !n.Unavailable {
			if dst := to.openOriginal(); dst != nil {
				dst.Close()
			} else {
				n.Unavailable = true
			}
		}
	} else {
		_, err := os.Lstat(temps)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		var names []string
		if err == nil {
			names, err = checkPendingCarry(from, to, temps, clean)
		} else {
			err = fmt.Errorf("%w: %w", store.ErrCarryRecord, err)
		}
		if errors.Is(err, store.ErrCarryDestination) {
			n.Unavailable = true
			names, err = store.InspectCarry(nil, nil, temps)
		}
		if errors.Is(err, store.ErrCarryRecord) {
			notice := &CarryNotice{Destination: to.path, DestinationID: to.id, Record: temps, Unreadable: true}
			var damage *store.DamagedCarryRecord
			if errors.As(err, &damage) {
				notice.DamagedLine = damage.Line
			}
			return notice, nil
		}
		if err != nil {
			return nil, err
		}
		n.Names, n.Count = names, len(names)
	}
	if n.Count == 0 {
		return nil, nil
	}
	n.Recovery = temps + ".kept.json"
	return n, nil
}

type carryFolder struct {
	path, id string
	seen     os.FileInfo
}

// openOriginal returns the original directory when its identity can still be checked.
func (folder carryFolder) openOriginal() *os.Root {
	root, err := os.OpenRoot(folder.path)
	if err != nil {
		return nil
	}
	if !sameFolder(root, folder.seen, folder.id) {
		root.Close()
		return nil
	}
	return root
}

func checkPendingCarry(from, to carryFolder, temps string, clean bool) ([]string, error) {
	dst := to.openOriginal()
	if dst == nil {
		return nil, store.ErrCarryDestination
	}
	defer dst.Close()
	src := from.openOriginal()
	if src != nil {
		defer src.Close()
	}
	if clean {
		return store.ClearCarry(src, dst, temps)
	}
	return store.InspectCarry(src, dst, temps)
}

// finishCarryForDelete clears what an interrupted copy left and writes the
// recovery list. told is checked against the copy before and after clearing;
// a mismatch refuses the delete, which keeps the transcript and the source.
func (e *Engine) finishCarryForDelete(s *Session, told *DeletionPreview) (keepRecord bool, err error) {
	for _, clean := range []bool{false, true} {
		notice, err := e.checkCarry(s, clean)
		if err != nil {
			return false, fmt.Errorf("Could not finish the interrupted copy: %w. Check the destination and try again", err)
		}
		if told != nil && notice.token() != told.Token {
			return false, ErrKeptChanged
		}
		if notice != nil && notice.Unreadable {
			if told == nil {
				return false, fmt.Errorf("The copy record %s %s. Restore a readable record or delete this conversation separately to keep it", notice.Record, notice.recordStatus())
			}
			return true, nil
		}
		if notice != nil && (clean || notice.Unavailable) {
			data, err := json.MarshalIndent(notice, "", "  ")
			if err != nil {
				return false, err
			}
			if err := os.MkdirAll(filepath.Dir(notice.Recovery), 0700); err != nil {
				return false, fmt.Errorf("Create the recovery folder: %w", err)
			}
			return false, config.WriteAtomic(notice.Recovery, data, 0600)
		}
	}
	return false, nil
}

// sameFolder reports whether an open folder is the one a move named: the one
// this process saw, or else the one whose identity was kept. Without either
// it cannot be confirmed.
func sameFolder(r *os.Root, seen os.FileInfo, id string) bool {
	now, err := r.Stat(".")
	if err != nil {
		return false
	}
	return matchesFolderInfo(now, seen, id)
}

func matchesFolderInfo(now, seen os.FileInfo, id string) bool {
	if seen != nil {
		return os.SameFile(now, seen)
	}
	return id != "" && store.FileID(now) == id
}

// movedMeta is what the transcript shows of a move out of a scratch folder
// from. Every key is written, zero or not, and the counts stay integers.
func movedMeta(c *store.Carried, from string) map[string]any {
	return map[string]any{
		"to": c.To, "from": from, "copied": c.Copied, "there": c.There, "failed": c.Failed, "interrupted": c.Interrupted, "attrs": c.Attrs,
		"held": c.Held, "dependencies": c.Dependencies, "stopped": c.Stopped, "gone": c.Gone, "reason": c.Reason,
	}
}

// askDirectory puts a directory tool's card to the user and waits for it.
func (e *Engine) askDirectory(ctx context.Context, s *Session, a config.Agent, req perms.Request, record *store.PermissionRecord) (perms.Decision, *store.PermissionRecord, bool) {
	d, stopped := e.askCard(ctx, s, a, req, record)
	if stopped {
		return d, record, true
	}
	if d.Kind == perms.DecideOnce && req.Path != "" {
		d.Path = req.Path
	}
	record.Decision = string(d.Kind)
	record.Reason = d.Reason
	if d.Path != "" {
		record.Path = d.Path
	}
	e.publishDecided(s, record)
	return d, record, false
}

// recentLimit is how many project folders a no-folder conversation is told of.
const recentLimit = 5

// recentProjects lists the configured projects a conversation was active in,
// the most recent first, up to recentLimit.
func (e *Engine) recentProjects(cfg config.Config) []string {
	var out []string
	for _, sum := range e.store.List("") {
		if len(out) == recentLimit {
			break
		}
		if slices.Contains(cfg.Projects, sum.Project) && !slices.Contains(out, sum.Project) {
			out = append(out, sum.Project)
		}
	}
	return out
}
