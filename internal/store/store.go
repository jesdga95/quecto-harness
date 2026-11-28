// Package store persists sessions, one JSON file each, and keeps an index for
// the sidebar. It does not know what a turn is.
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
)

var ErrNotFound = errors.New("No session with that id")

// ErrLeftBehind marks a delete that removed the session file but not its side
// folder. The folder is removed again by a later delete or the next start.
var ErrLeftBehind = errors.New("session folder left behind")

// ErrUnreadable marks a session file that cannot be read or does not hold the
// session its name gives.
var ErrUnreadable = errors.New("session file unreadable")

// fileError is a failure the person clears by fixing a file. Kind is the
// sentinel it matches; the message names the file and what to do.
type fileError struct {
	kind error
	msg  string
}

func (e *fileError) Error() string        { return e.msg }
func (e *fileError) Is(target error) bool { return target == e.kind }

// ledgerFile sits beside the sessions. Its name is not a session's, so Open
// never reads it as one.
const ledgerFile = "ledger.json"

// Parents records each run's last known launcher. Cleanup records output
// still to remove after a confirmed deletion.
type ledger struct {
	Version int `json:"version,omitempty"`
	// Archiving names the rename that releases the run and its output.
	Archiving string              `json:"archiving,omitempty"`
	Parents   map[string]relation `json:"parents,omitempty"`
	Cleanup   map[string]relation `json:"cleanup,omitempty"`
}

type relation struct {
	OutputManifest   string `json:"output_manifest,omitempty"`
	OutputGeneration string `json:"output_generation,omitempty"`
	Parent           string `json:"parent"`
	Project          string `json:"project"`
}

// Entry is one row of a session. Kind drives rendering; InContext drives what
// is sent to the model.
type Entry struct {
	Seq  int    `json:"seq"`
	Kind string `json:"kind"`
	Role string `json:"role"`

	Content   string `json:"content,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`

	ToolCalls  []llm.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ExitCode   *int           `json:"exit_code,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"`
	// TodoNote is the plan step the model reads under this result. It is kept
	// beside the output rather than inside it, so a row shows what the tool
	// answered and a later request sends the bytes it first sent.
	TodoNote string `json:"todo_note,omitempty"`

	Permission  *PermissionRecord `json:"permission,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	// Usage is what the model call that produced this entry reported. Entries
	// written before it was kept, and entries no call produced, have none.
	Usage *CallUsage `json:"usage,omitempty"`
	// Edited is set on a user entry whose text was rewritten and sent again.
	Edited bool `json:"edited,omitempty"`
	// From names the agent a message came from. Empty is the person.
	From string `json:"from,omitempty"`
	// FromSession is the session it was sent from, which is the handle a reply
	// is addressed to. Empty is the person.
	FromSession string `json:"from_session,omitempty"`

	InContext bool           `json:"in_context"`
	Visible   bool           `json:"visible"`
	At        time.Time      `json:"at"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// Attachment is a file the user attached to a message. Text is kept here and
// inlined into the prompt; an image lives in the file store and Kind says
// which of the two this is.
type Attachment struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Mime  string `json:"mime,omitempty"`
	Bytes int    `json:"bytes"`
	// File is the id in the file store, for an image, a document or a video.
	File string `json:"file,omitempty"`
	// Content is the whole file, for text.
	Content string `json:"content,omitempty"`
	// Refused is the model server's reason for not reading the file. A
	// refused file is named in the prompt and no longer sent.
	Refused string `json:"refused,omitempty"`
}

// CallUsage is what one model call spent, kept on the entry that call
// produced. A turn's and a session's totals are the sum over these, so a stop
// that ends a turn in the middle leaves every call that ran still counted, and
// the numbers can be recomputed from the saved session.
type CallUsage struct {
	// Reported says the server named the counts. A call cut off before any
	// usage frame arrives has it false, which is unknown rather than zero.
	Reported bool `json:"reported"`
	// Stopped is set when this harness ended the call rather than the model.
	Stopped bool `json:"stopped,omitempty"`
	// Compaction is set when the call wrote a summary instead of a reply. Its
	// prompt counted the messages the summary replaces, so it is never read as
	// a measure of what is in context now.
	Compaction bool `json:"compaction,omitempty"`
	// Prompt counts the cached part. Cached is nil where the server did not
	// say how much of it it reused, which is not a cache that held nothing.
	Prompt     int  `json:"prompt,omitempty"`
	Completion int  `json:"completion,omitempty"`
	Cached     *int `json:"cached,omitempty"`
	// ToolTokens is the estimated cost of the tool schemas this request sent.
	ToolTokens int `json:"tool_tokens,omitempty"`
	// FirstMs is the wait before the first token, GenMs the time spent
	// producing, TotalMs the whole request. Measured here, since no server
	// reports them alike; a request that streamed nothing has no first time.
	FirstMs int64 `json:"first_ms,omitempty"`
	GenMs   int64 `json:"gen_ms,omitempty"`
	TotalMs int64 `json:"total_ms,omitempty"`
	// PerSecond is the generation rate the server named, where it named one.
	PerSecond float64 `json:"per_second,omitempty"`
}

// LastMeasured is the newest entry whose call reported counts that stand in
// the context now, with its record, or -1 and nil where no such call is kept.
// A summary's prompt counted the messages it replaces, and a call that
// streamed no usage report says nothing, so neither measures what is there.
// A call made before a summary in context measured the messages it replaced.
func LastMeasured(entries []Entry) (int, *CallUsage) {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind == "compaction" && entries[i].InContext {
			return -1, nil
		}
		u := entries[i].Usage
		if u != nil && u.Reported && !u.Compaction {
			return i, u
		}
	}
	return -1, nil
}

// PermissionRecord is the decision as it was made, kept so a reloaded page
// shows the card in its answered state.
type PermissionRecord struct {
	ID       string `json:"id"`
	Tool     string `json:"tool"`
	Command  string `json:"command,omitempty"`
	Path     string `json:"path,omitempty"`
	Decision string `json:"decision"`
	Prefix   string `json:"prefix,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Todo is one step of the plan the model keeps for the session. Status is
// pending, in_progress or completed.
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// Plan is a plan in its sections. Plan mode writes it as a draft, and once
// approved execute mode carries it in its system prompt and its steps are what
// the todo list started from.
type Plan struct {
	Title      string   `json:"title"`
	Goal       string   `json:"goal"`
	Code       []string `json:"code"`
	Tests      []string `json:"tests"`
	Validation []string `json:"validation"`
	Migration  string   `json:"migration"`
	Risks      []string `json:"risks"`
	Steps      []string `json:"steps"`
	Open       []string `json:"open"`
}

// Session is the on-disk document. The system prompt is not in it: it is
// regenerated on every request, so editing the prompt reaches old sessions.
type Session struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	// Agent is the agent this session last ran under. It is the only record of
	// that after a restart, which is what tells a reloaded session it moved.
	Agent string `json:"agent"`
	// Parent names the session that launched this one, for a subagent run.
	// Empty is a conversation somebody started.
	Parent string `json:"parent,omitempty"`
	Model  string `json:"model"`
	// Mode is the work mode the next turn runs in.
	Mode config.WorkMode `json:"mode"`
	// Effort replaces the agent's thinking level for this session. Empty is
	// the agent's own.
	Effort config.Effort `json:"effort,omitempty"`
	// Toolset is the set every turn is offered, fixed when the session was
	// created. Empty reads as full.
	Toolset config.Toolset `json:"toolset"`
	// Plan is the last plan the user approved, or nil.
	Plan *Plan `json:"plan,omitempty"`
	// Draft is the plan being written in plan mode, or nil. It stays after
	// approval, so the panel still shows the plan being carried out.
	Draft *Plan  `json:"draft,omitempty"`
	Title string `json:"title"`
	// Named is set when the user typed the title. Nothing overwrites it.
	Named       bool      `json:"named"`
	Created     time.Time `json:"created"`
	Updated     time.Time `json:"updated"`
	NCtx        int       `json:"n_ctx"`
	NCtxSource  string    `json:"n_ctx_source"`
	Compactions int       `json:"compactions"`
	// ContextBase is the estimated fixed part of every prompt this model sees.
	ContextBase *ContextBase `json:"context_base,omitempty"`
	Todos       []Todo       `json:"todos,omitempty"`
	Entries     []Entry      `json:"entries"`
}

// ContextBase is the estimated token cost of the three parts of a prompt that
// never change: the system message, the project's rules inside it and the tool
// schemas. It is a character-count estimate rather than a measurement, so a
// turn re-takes it instead of anything having to notice a change.
type ContextBase struct {
	System int `json:"system"`
	Rules  int `json:"rules"`
	Tools  int `json:"tools"`
}

// Summary is what the sidebar needs, cheap enough to keep for every session.
type Summary struct {
	unreadable bool
	checked    os.FileInfo
	ID         string    `json:"id"`
	Project    string    `json:"project"`
	Agent      string    `json:"agent"`
	Parent     string    `json:"parent,omitempty"`
	Model      string    `json:"model"`
	Title      string    `json:"title"`
	Created    time.Time `json:"created"`
	Updated    time.Time `json:"updated"`
	Messages   int       `json:"messages"`
	Bytes      int64     `json:"bytes"`
	// Fill is the context after the last call the server reported on, and zero
	// where it never reported one.
	Fill int `json:"fill,omitempty"`
}

// Sessions indexes one directory for its exclusive writer. Session IDs share
// a fixed set of locks for the lifetime of the store.
type Sessions struct {
	dir string

	mu    sync.RWMutex
	index map[string]Summary
	locks [256]sync.Mutex

	// ledMu guards led and serialises writes of its file.
	ledMu sync.Mutex
	led   ledger
}

var (
	archivePattern = regexp.MustCompile(`^(sess_[a-z2-7]{16})\.json\.unreadable-([1-9][0-9]*|[a-z2-7]{26,})$`)
	namePattern    = regexp.MustCompile(`^sess_[a-z2-7]{16}\.json$`)
)

// Open builds the index once. A file that does not parse is named and
// skipped, because one bad file must not stop the program.
func Open(dir string) (*Sessions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("Create %s: check the permissions on its parent", dir)
	}
	s := &Sessions{dir: dir, index: map[string]Summary{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("Read %s: check the permissions on the folder", dir)
	}
	path := filepath.Join(dir, ledgerFile)
	s.led, err = readLedger(path)
	if errors.Is(err, os.ErrNotExist) {
		s.led = s.recoverFailedRemovals(entries)
		// Without either ledger, a marker beside an archive may predate its rename.
		// Keep that output available for explicit recovery.
		for _, entry := range entries {
			match := archivePattern.FindStringSubmatch(entry.Name())
			if match == nil {
				continue
			}
			id := match[1]
			if _, ok := s.led.Cleanup[id]; !ok {
				continue
			}
			if err := s.preserveMarker(id); err != nil {
				return nil, err
			}
			delete(s.led.Cleanup, id)
		}
	} else if err != nil {
		return nil, err
	}
	if err == nil && len(s.led.Parents) == 0 && len(s.led.Cleanup) == 0 && s.led.Archiving == "" {
		if err := s.reconcileEmptyLedger(path, entries); err != nil {
			return nil, err
		}
	}
	parents := maps.Clone(s.led.Parents)
	if parents == nil {
		parents = map[string]relation{}
	}
	present := make(map[string]bool, len(entries))
	for _, e := range entries {
		present[e.Name()] = true
		if !namePattern.MatchString(e.Name()) {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		summary, err := s.readSummary(id, nil)
		if err != nil {
			log.Printf("sessions/%s: %v: the file is skipped", e.Name(), err)
			keepRelation(parents, s.led, id)
			continue
		}
		s.index[id] = summary
		delete(parents, id)
		if summary.Parent != "" {
			parents[id] = relation{Parent: summary.Parent, Project: summary.Project}
		}
	}
	// A completed rename releases its records before cleanup resumes.
	// An unversioned ledger identifies an archive by its name and lack of a cleanup order.
	for name := range present {
		if s.led.Version != 0 && name != s.led.Archiving {
			continue
		}
		match := archivePattern.FindStringSubmatch(name)
		if match == nil || present[match[1]+".json"] {
			continue
		}
		id := match[1]
		_, pending := s.led.Cleanup[id]
		if name == s.led.Archiving || !pending {
			if err := s.clearFailedRemoval(id); err != nil {
				return nil, fmt.Errorf("Finish archiving %s: %w", id, err)
			}
			delete(parents, id)
			delete(s.led.Cleanup, id)
		}
	}
	s.led.Archiving = ""
	if s.led.Version == 0 {
		s.led.Version = 1
	}
	for id, r := range parents {
		if !present[id+".json"] && !present[r.Parent+".json"] && !present[id+".files"] {
			delete(parents, id)
		}
	}
	s.led.Parents = parents
	if len(parents) > 0 || len(s.led.Cleanup) > 0 || present[ledgerFile] || present[ledgerFile+".backup"] {
		if err := s.writeLedgerLocked(true); err != nil {
			log.Printf("sessions/%s: %v", ledgerFile, err)
		}
	}
	s.retryCleanup("")
	return s, nil
}

// keepRelation carries a run's recorded launcher over when its file cannot be
// read, since the file no longer says.
func keepRelation(into map[string]relation, previous ledger, id string) {
	r, ok := previous.Parents[id]
	if !ok {
		r = previous.Cleanup[id]
	}
	if r.Parent != "" {
		into[id] = r
	}
}

// Family returns every run known to belong to a launcher: those indexed, those
// whose file could not be read, and those whose output is still to be removed.
func (s *Sessions) Family(parent string) []string {
	if parent == "" {
		return nil
	}
	var out []string
	s.mu.RLock()
	for _, v := range s.index {
		if v.Parent == parent {
			out = append(out, v.ID)
		}
	}
	s.mu.RUnlock()
	s.ledMu.Lock()
	for _, m := range []map[string]relation{s.led.Parents, s.led.Cleanup} {
		for id, r := range m {
			if r.Parent == parent && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	s.ledMu.Unlock()
	sort.Strings(out)
	return out
}

// relate records a run's launcher before its file is written, so the file is
// never on disk without it.
func (s *Sessions) relate(sess *Session) error {
	r := relation{Parent: sess.Parent, Project: sess.Project}
	s.ledMu.Lock()
	defer s.ledMu.Unlock()
	old, had := s.led.Parents[sess.ID]
	if sess.Parent == "" {
		if !had {
			return nil
		}
		delete(s.led.Parents, sess.ID)
	} else {
		if had && old == r {
			return nil
		}
		if s.led.Parents == nil {
			s.led.Parents = map[string]relation{}
		}
		s.led.Parents[sess.ID] = r
	}
	if err := s.writeLedgerLocked(false); err != nil {
		if had {
			s.led.Parents[sess.ID] = old
		} else {
			delete(s.led.Parents, sess.ID)
		}
		return fmt.Errorf("Save session %s: %w", sess.ID, err)
	}
	return nil
}

// readRecord follows links to regular files. Only a missing directory entry
// counts as absent. A link whose target is missing needs repair.
func readRecord(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("not a regular file")
		}
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			return data, nil
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		if _, err = os.Lstat(path); err == nil {
			return nil, errors.New("the entry is present but its target is missing")
		}
	}
	return nil, err
}

// readLedger tries the primary record and then its backup. Malformed copies
// are moved aside. An inaccessible copy requires repair.
func readLedger(path string) (ledger, error) {
	for _, candidate := range []string{path, path + ".backup"} {
		l, err := readLedgerCopy(candidate)
		if !errors.Is(err, os.ErrNotExist) {
			return l, err
		}
	}
	return ledger{}, os.ErrNotExist
}

func readLedgerCopy(path string) (ledger, error) {
	var l ledger
	raw, err := readRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, err
	}
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return l, fmt.Errorf("Read %s: check the permissions on the file: %w", path, err)
		}
		return l, fmt.Errorf("Read %s: %v: restore a readable regular file at that path", path, err)
	}
	if err := decodeLedger(raw, &l); err != nil {
		if errors.Is(err, errLedgerVersion) {
			return l, fmt.Errorf("Read %s: update the harness to read this ledger version", path)
		}
		aside, rerr := numberedName(path + ".unreadable")
		if rerr == nil {
			rerr = os.Rename(path, aside)
		}
		if rerr != nil {
			return l, fmt.Errorf("Read %s: its ledger format is invalid and it cannot be moved aside: move or remove it", path)
		}
		log.Printf("sessions/%s: %v: moved to %s", filepath.Base(path), err, filepath.Base(aside))
		return ledger{}, os.ErrNotExist
	}
	for _, m := range []map[string]relation{l.Parents, l.Cleanup} {
		for id, r := range m {
			if !ValidID(id) || (r.Parent != "" && !ValidID(r.Parent)) {
				log.Printf("sessions/%s: %q is not a session: the entry is dropped", ledgerFile, id)
				delete(m, id)
			}
		}
	}
	return l, nil
}

var errLedgerVersion = errors.New("unsupported ledger version")

// Cleanup accepts relations keyed by session ID or an array of IDs.
// Array entries take their relation from Parents.
func decodeLedger(raw []byte, l *ledger) error {
	var file *struct {
		Version   int                 `json:"version"`
		Archiving string              `json:"archiving"`
		Parents   map[string]relation `json:"parents"`
		Cleanup   json.RawMessage     `json:"cleanup"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return err
	}
	if file == nil {
		return errors.New("expected a ledger object")
	}
	if file.Version == 0 && file.Archiving == "" && file.Parents == nil && file.Cleanup == nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		if len(fields) > 0 {
			known := false
			for key := range fields {
				switch strings.ToLower(key) {
				case "version", "archiving", "parents", "cleanup":
					known = true
				}
			}
			if !known {
				return errLedgerVersion
			}
		}
	}
	if file.Version < 0 {
		return errors.New("invalid ledger version")
	}
	if file.Version > 1 {
		return errLedgerVersion
	}
	l.Version, l.Archiving, l.Parents = file.Version, file.Archiving, file.Parents
	trimmed := strings.TrimSpace(string(file.Cleanup))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var ids []string
		if err := json.Unmarshal(file.Cleanup, &ids); err != nil {
			return err
		}
		l.Cleanup = map[string]relation{}
		for _, id := range ids {
			l.Cleanup[id] = file.Parents[id]
		}
		return nil
	}
	return json.Unmarshal(file.Cleanup, &l.Cleanup)
}

// numberedName finds an unused sibling for a recovery record.
func numberedName(prefix string) (string, error) {
	for n := 1; ; n++ {
		aside := fmt.Sprintf("%s-%d", prefix, n)
		if _, err := os.Lstat(aside); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return "", fmt.Errorf("Inspect %s: check access to the sessions directory: %w", aside, err)
			}
			continue
		}
		return aside, nil
	}
}

// writeLedgerLocked updates both copies under ledMu. At startup only missing
// or differing copies need an atomic write.
func (s *Sessions) writeLedgerLocked(onlyChanged bool) error {
	data, err := json.Marshal(s.led)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, ledgerFile)
	data = append(data, '\n')
	for _, copyPath := range []string{path + ".backup", path} {
		if onlyChanged {
			if old, err := readRecord(copyPath); err == nil && bytes.Equal(old, data) {
				continue
			}
		}
		if err := config.WriteAtomic(copyPath, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

// Pending reports whether a delete of the session removed its file and still
// has its side folder to remove.
func (s *Sessions) Pending(id string) bool {
	s.ledMu.Lock()
	defer s.ledMu.Unlock()
	_, ok := s.led.Cleanup[id]
	return ok
}

// PendingIn returns the sessions of a project, launched by nobody, whose file
// a delete removed and whose side folder is still to be removed.
func (s *Sessions) PendingIn(project string) []string {
	s.ledMu.Lock()
	defer s.ledMu.Unlock()
	var out []string
	for id, r := range s.led.Cleanup {
		if r.Parent == "" && r.Project == project {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// markPending records that the session's side folder is to be removed, before
// anything is. A failed write records nothing.
func (s *Sessions) markPending(id string) error {
	return s.orderOutput(id, "")
}

func (s *Sessions) orderOutput(id, generation string) error {
	var snapshot outputSnapshot
	if generation != "" {
		var err error
		snapshot, err = readOutputSnapshot(s.filesDir(id), false, relation{})
		if err != nil {
			return err
		}
		if snapshot.generation() != generation {
			return errOutputChanged
		}
	}
	s.mu.RLock()
	sum := s.index[id]
	s.mu.RUnlock()
	s.ledMu.Lock()
	defer s.ledMu.Unlock()
	previous, ordered := s.led.Cleanup[id]
	if ordered && (generation == "" || generation == previous.OutputGeneration) {
		return nil
	}
	if s.led.Cleanup == nil {
		s.led.Cleanup = map[string]relation{}
	}
	rel, ok := s.led.Parents[id]
	if !ok {
		rel = relation{Parent: sum.Parent, Project: sum.Project}
	}
	rel.OutputGeneration = generation
	rel.OutputManifest = snapshot.manifest
	s.led.Cleanup[id] = rel
	archive := s.led.Archiving
	if match := archivePattern.FindStringSubmatch(archive); match != nil && match[1] == id {
		s.led.Archiving = ""
	}
	if err := s.writeLedgerLocked(false); err != nil {
		s.led.Archiving = archive
		if ordered {
			s.led.Cleanup[id] = previous
		} else {
			delete(s.led.Cleanup, id)
		}
		return err
	}
	return nil
}

// settle drops the session's pending cleanup, and its launcher too once the
// session is gone. A failed write leaves the record available to retry.
func (s *Sessions) settle(id string, gone bool) error {
	s.ledMu.Lock()
	defer s.ledMu.Unlock()
	cleanup, was := s.led.Cleanup[id]
	parent, related := s.led.Parents[id]
	if !was && !(gone && related) {
		return nil
	}
	if err := s.clearFailedRemoval(id); err != nil {
		return err
	}
	delete(s.led.Cleanup, id)
	if gone {
		delete(s.led.Parents, id)
	}
	if err := s.writeLedgerLocked(false); err != nil {
		if was {
			s.led.Cleanup[id] = cleanup
		}
		if related {
			s.led.Parents[id] = parent
		}
		return fmt.Errorf("update %s: %w", ledgerFile, err)
	}
	return nil
}

// retryCleanup removes the side folders earlier deletes left, except the one
// of skip. A session whose file is back keeps its folder.
func (s *Sessions) retryCleanup(skip string) {
	s.ledMu.Lock()
	ids := slices.Collect(maps.Keys(s.led.Cleanup))
	s.ledMu.Unlock()
	for _, id := range ids {
		if id != skip {
			s.finishCleanup(id)
		}
	}
}

// FinishCleanup removes pending output if the transcript is still absent.
// A restored transcript goes through the usual session deletion checks.
func (s *Sessions) FinishCleanup(id string) (bool, error) {
	if !s.Pending(id) {
		return false, nil
	}
	s.retryCleanup(id)
	done, err := s.finishCleanup(id)
	if err != nil && !errors.Is(err, ErrRecoveryConflict) {
		return done, fmt.Errorf("Finish cleanup for session %s: %w", id, err)
	}
	return done, err
}

func (s *Sessions) finishCleanup(id string) (bool, error) {
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()
	if !s.Pending(id) {
		return false, nil
	}
	sess, size, err := s.readSession(id)
	if errors.Is(err, ErrNotFound) {
		return true, s.removeOutput(id, false)
	}
	if err != nil {
		return false, err
	}
	if err := s.relate(sess); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.index[id] = summarise(sess, size)
	s.mu.Unlock()
	return false, s.settle(id, false)
}

// RemoveMissingRun deletes the output of a recorded run whose transcript is
// absent. A nonempty parent limits the removal to its family.
func (s *Sessions) RemoveMissingRun(parent, id string) error {
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()
	s.ledMu.Lock()
	r := s.led.Parents[id]
	s.ledMu.Unlock()
	if r.Parent == "" || (parent != "" && r.Parent != parent) {
		return ErrNotFound
	}
	if _, err := os.Lstat(s.path(id)); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return fmt.Errorf("Check session %s: verify access to the sessions folder and retry: %w", id, err)
		}
		return fmt.Errorf("Session %s is present again: retry the deletion", id)
	}
	if err := s.markPending(id); err != nil {
		return fmt.Errorf("Record cleanup for session %s: %w", id, err)
	}
	return s.removeOutput(id, false)
}

// removeFolder removes a session's side folder. Only a folder that is then
// not there counts as removed.
func (s *Sessions) removeFolder(id string) error {
	if err := os.RemoveAll(s.filesDir(id)); err != nil {
		return err
	}
	_, err := os.Lstat(s.filesDir(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		err = errors.New("it is still there")
	}
	return err
}

// List returns the sessions of one project, newest first, or every session
// when project is empty. Subagent runs stay out of the listing.
func (s *Sessions) List(project string) []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Summary, 0, len(s.index))
	for _, v := range s.index {
		if v.unreadable || v.Parent != "" {
			continue
		}
		if project == "" || v.Project == project {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return newerFirst(out[i], out[j]) })
	return out
}

// newerFirst is the listing order, newest session first.
func newerFirst(a, b Summary) bool {
	if a.Updated.Equal(b.Updated) {
		return a.ID < b.ID
	}
	return a.Updated.After(b.Updated)
}

// Children returns the subagent runs one session launched, newest first. List
// leaves them out of the project listing.
func (s *Sessions) Children(parent string) []Summary {
	if parent == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Summary
	for _, v := range s.index {
		if !v.unreadable && v.Parent == parent {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.After(out[j].Created)
	})
	return out
}

func (s *Sessions) Load(id string) (*Session, error) {
	if !namePattern.MatchString(id + ".json") {
		return nil, ErrNotFound
	}
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()
	sess, _, err := s.readSession(id)
	return sess, err
}

func (s *Sessions) readSession(id string) (*Session, int64, error) {
	raw, err := readRecord(s.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, ErrNotFound
		}
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		return nil, 0, s.unreadable(id, err.Error())
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, 0, s.unreadable(id, "it is not valid JSON")
	}
	// What is done to a session is done by its id, so a file that names
	// another one is not taken at its word.
	if sess.ID != id {
		return nil, 0, s.unreadable(id, fmt.Sprintf("it names session %q", sess.ID))
	}
	return &sess, int64(len(raw)), nil
}

// UnreadableSessions lists transcript entries that need manual recovery.
func (s *Sessions) UnreadableSessions() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !ValidID(id) {
			continue
		}
		err := s.checkTranscript(id)
		if errors.Is(err, ErrUnreadable) {
			ids = append(ids, id)
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return ids, nil
}

// ArchiveUnreadable moves an unreadable entry aside under a free name.
// A transcript that became readable stays in place.
func (s *Sessions) ArchiveUnreadable(id string) (string, error) {
	if !ValidID(id) {
		return "", ErrNotFound
	}
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()
	_, _, err := s.readSession(id)
	if err == nil {
		return "", fmt.Errorf("Session %s is readable again: refresh the list", id)
	}
	if !errors.Is(err, ErrUnreadable) {
		return "", err
	}
	if len(s.Family(id)) != 0 {
		return "", fmt.Errorf("Session %s still has recorded runs: resolve them in Settings > Projects before archiving it", id)
	}
	aside := s.path(id) + ".unreadable-" + strings.ToLower(rand.Text())
	if err := s.archive(id, aside); err != nil {
		return "", fmt.Errorf("Archive session %s: %w", id, err)
	}
	s.mu.Lock()
	delete(s.index, id)
	s.mu.Unlock()
	return aside, nil
}

// archive records the destination before renaming a transcript with a launcher
// or cleanup order. The rename commits the archive, even if the final ledger
// write must be retried at startup.
func (s *Sessions) archive(id, aside string) error {
	s.ledMu.Lock()
	defer s.ledMu.Unlock()
	_, pending := s.led.Cleanup[id]
	_, related := s.led.Parents[id]
	if !pending && !related {
		_, err := os.Lstat(filepath.Join(s.dir, id+".cleanup"))
		if errors.Is(err, os.ErrNotExist) {
			return os.Rename(s.path(id), aside)
		}
		if err != nil {
			return err
		}
	}
	// The ledger has one archive slot. Retire its marker before reusing it.
	if previous := archivePattern.FindStringSubmatch(s.led.Archiving); previous != nil {
		if err := s.clearFailedRemoval(previous[1]); err != nil {
			return fmt.Errorf("finish archiving %s first: %w", previous[1], err)
		}
	}
	s.led.Archiving = filepath.Base(aside)
	if err := s.writeLedgerLocked(false); err != nil {
		s.led.Archiving = ""
		return err
	}
	if err := os.Rename(s.path(id), aside); err != nil {
		s.led.Archiving = ""
		return errors.Join(err, s.writeLedgerLocked(false))
	}
	delete(s.led.Parents, id)
	delete(s.led.Cleanup, id)
	if err := s.clearFailedRemoval(id); err != nil {
		log.Printf("Session %s is archived: %v", id, err)
		return nil
	}
	s.led.Archiving = ""
	if err := s.writeLedgerLocked(false); err != nil {
		s.led.Archiving = filepath.Base(aside)
		log.Printf("sessions/%s: %v: the archived transcript keeps its output", id, err)
	}
	return nil
}

// unreadable names a session file that cannot be used and what clears it.
func (s *Sessions) unreadable(id, why string) error {
	return &fileError{kind: ErrUnreadable, msg: fmt.Sprintf(
		"Session %s cannot be read from %s (%s): repair the file or archive it in Settings > Projects, then try again",
		id, s.path(id), why)}
}

// Save writes the file, flushes it to the disk and refreshes the index.
// IDs sharing a lock stripe are written one at a time.
func (s *Sessions) Save(sess *Session) error {
	return s.save(sess, true)
}

// SaveNoSync is Save without the flush to the disk.
func (s *Sessions) SaveNoSync(sess *Session) error {
	return s.save(sess, false)
}

func (s *Sessions) save(sess *Session, sync bool) error {
	if sess.ID == "" {
		return errors.New("Save session: the session has no id")
	}
	data, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("Save session %s: %w", sess.ID, err)
	}
	data = append(data, '\n')
	write := config.WriteAtomicNoSync
	if sync {
		write = config.WriteAtomic
	}
	lock := s.lockFor(sess.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.relate(sess); err != nil {
		return err
	}
	err = write(s.path(sess.ID), data, 0o600)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.index[sess.ID] = summarise(sess, int64(len(data)))
	s.mu.Unlock()
	return nil
}

// filesDir is the side folder holding one session's saved tool output.
func (s *Sessions) filesDir(id string) string {
	return filepath.Join(s.dir, id+".files")
}

// SpillDir is where a session's cut-off tool output is saved whole. It is
// inside the session's own folder, so it goes when the session does.
func (s *Sessions) SpillDir(id string) string {
	return filepath.Join(s.filesDir(id), "spill")
}

// Delete records the cleanup before removing the transcript. A failed
// removal keeps the output indexed and its cleanup pending.
func (s *Sessions) Delete(id string) error {
	s.retryCleanup(id)
	lock := s.lockFor(id)
	lock.Lock()
	defer lock.Unlock()
	_, statErr := os.Lstat(s.path(id))
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("Delete session %s: %w", id, statErr)
	}
	if statErr != nil && !s.Pending(id) {
		s.mu.Lock()
		delete(s.index, id)
		s.mu.Unlock()
		return ErrNotFound
	}
	if statErr == nil {
		s.ledMu.Lock()
		generation := s.led.Cleanup[id].OutputGeneration
		s.ledMu.Unlock()
		if generation != "" {
			path := s.filesDir(id)
			current, err := outputGeneration(path, false)
			if errors.Is(err, os.ErrNotExist) {
				if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
					current, err = generation, nil
				}
			}
			if err != nil {
				return fmt.Errorf("Check saved output: %w", err)
			}
			if current != generation {
				return fmt.Errorf("Delete session %s: %w", id, errors.Join(errOutputChanged, s.settle(id, false)))
			}
		}
	}
	if err := s.markPending(id); err != nil {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The session is still there, so its side folder stays with it and
		// the delete can be tried again.
		if cancelErr := s.settle(id, false); cancelErr != nil {
			return fmt.Errorf("Delete session %s: %w. Could not cancel the cleanup order: %w", id, err, cancelErr)
		}
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	return s.removeOutput(id, false)
}

func (s *Sessions) removeOutput(id string, freshOrder bool) error {
	s.ledMu.Lock()
	order := s.led.Cleanup[id]
	s.ledMu.Unlock()
	var rmErr error
	if order.OutputGeneration == "" {
		rmErr = s.removeFolder(id)
	} else {
		rmErr = removeConfirmedOutput(s.filesDir(id), order, freshOrder)
		if errors.Is(rmErr, errOutputChanged) {
			return fmt.Errorf("Check saved output for %s: %w", id, errors.Join(rmErr, s.settle(id, true)))
		}
		if errors.Is(rmErr, ErrRecoveryConflict) {
			return fmt.Errorf("Check saved output: %w", rmErr)
		}
	}
	if rmErr != nil {
		log.Printf("sessions/%s.files: %v: the folder is left behind", id, rmErr)
		if err := s.recordFailedRemoval(id); err != nil {
			log.Printf("sessions/%s: could not record the failed output removal: %v", id, err)
		}
		return &fileError{kind: ErrLeftBehind, msg: fmt.Sprintf(
			"Session %s is deleted, but its saved output in %s could not be removed: check the permissions on that folder, then delete the session again",
			id, s.filesDir(id))}
	}
	if err := s.settle(id, true); err != nil {
		return &fileError{kind: ErrLeftBehind, msg: fmt.Sprintf("Session %s and its saved output are gone, but its cleanup record remains: %v. Retry the deletion", id, err)}
	}
	s.mu.Lock()
	delete(s.index, id)
	s.mu.Unlock()
	return nil
}

// Exists reports whether the session's file is still there: indexed, and not
// deleted with its output left to remove.
func (s *Sessions) Exists(id string) bool {
	return s.Has(id) && !s.Pending(id)
}

// ValidID reports whether id has the shape NewID gives a session.
func ValidID(id string) bool { return namePattern.MatchString(id + ".json") }

func (s *Sessions) Has(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.index[id]
	return ok
}

func (s *Sessions) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Sessions) lockFor(id string) *sync.Mutex {
	return &s.locks[crc32.ChecksumIEEE([]byte(id))%uint32(len(s.locks))]
}

func summarise(sess *Session, size int64) Summary {
	fill := 0
	if _, u := LastMeasured(sess.Entries); u != nil {
		fill = u.Prompt + u.Completion
	}
	messages := 0
	for _, e := range sess.Entries {
		if e.Visible {
			messages++
		}
	}
	return Summary{
		ID:       sess.ID,
		Project:  sess.Project,
		Agent:    sess.Agent,
		Parent:   sess.Parent,
		Model:    sess.Model,
		Title:    sess.Title,
		Created:  sess.Created,
		Updated:  sess.Updated,
		Messages: messages,
		Bytes:    size,
		Fill:     fill,
	}
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func NewID() string {
	b := make([]byte, 10)
	rand.Read(b)
	return "sess_" + idEncoding.EncodeToString(b)[:16]
}

// Title is the first 60 runes of the first user entry, newlines collapsed.
func Title(text string) string {
	t := strings.Join(strings.Fields(text), " ")
	r := []rune(t)
	if len(r) > 60 {
		return string(r[:60])
	}
	return t
}
