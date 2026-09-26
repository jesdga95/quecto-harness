// Package store persists sessions, one JSON file each, and keeps an index for
// the sidebar. It does not know what a turn is.
package store

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
)

var ErrNotFound = errors.New("No session with that id")

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

	Permission  *PermissionRecord `json:"permission,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
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
	// File is the id in the file store, for an image.
	File string `json:"file,omitempty"`
	// Content is the whole file, for text.
	Content string `json:"content,omitempty"`
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

// Todo is one step of the plan the model keeps for the session.
type Todo struct {
	Text   string `json:"text"`
	Status string `json:"status"`
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
	// Plan is the last plan the user approved, or nil.
	Plan *Plan `json:"plan,omitempty"`
	// Draft is the plan being written in plan mode, or nil. It stays after
	// approval, so the panel still shows the plan being carried out.
	Draft *Plan  `json:"draft,omitempty"`
	Title string `json:"title"`
	// Named is set when the user typed the title. Nothing overwrites it.
	Named       bool       `json:"named"`
	Created     time.Time  `json:"created"`
	Updated     time.Time  `json:"updated"`
	NCtx        int        `json:"n_ctx"`
	NCtxSource  string     `json:"n_ctx_source"`
	Compactions int        `json:"compactions"`
	LastUsage   *llm.Usage `json:"last_usage,omitempty"`
	// LastToolTokens is the tool schema cost inside LastUsage. It is zero when
	// the request that produced LastUsage was sent without tools.
	LastToolTokens int `json:"last_tool_tokens,omitempty"`
	// ContextBase is the measured fixed part of every prompt this model sees.
	ContextBase *ContextBase `json:"context_base,omitempty"`
	Todos       []Todo       `json:"todos,omitempty"`
	// Baselines are the files this session has written to, as they stood
	// before its first write to each. The copies live beside this document.
	Baselines []Baseline `json:"baselines,omitempty"`
	Entries   []Entry    `json:"entries"`
}

// ContextBase is the token cost of the two parts of a prompt that never
// change: the system message and the tool schemas. Model and Chars say what
// was measured, since a different tokenizer or a different system message
// makes the numbers something else.
type ContextBase struct {
	Model  string `json:"model"`
	Chars  int    `json:"chars"`
	System int    `json:"system"`
	Tools  int    `json:"tools"`
}

// Fits reports that the measurement was taken against this model and a system
// message of this size, which is what makes it still true.
func (b *ContextBase) Fits(model string, chars int) bool {
	return b != nil && b.Model == model && b.Chars == chars
}

// Summary is what the sidebar needs, cheap enough to keep for every session.
type Summary struct {
	ID       string    `json:"id"`
	Project  string    `json:"project"`
	Agent    string    `json:"agent"`
	Parent   string    `json:"parent,omitempty"`
	Model    string    `json:"model"`
	Title    string    `json:"title"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Messages int       `json:"messages"`
	Bytes    int64     `json:"bytes"`
}

// Sessions is the on-disk session set. One mutex per session id, so two
// sessions never serialise against each other.
type Sessions struct {
	dir string

	mu    sync.RWMutex
	index map[string]Summary
	locks map[string]*sync.Mutex
}

var (
	namePattern = regexp.MustCompile(`^sess_[a-z2-7]{16}\.json$`)
	blobPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// Open builds the index once. A file that does not parse is named and
// skipped, because one bad file must not stop the program.
func Open(dir string) (*Sessions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("Create %s: check the permissions on its parent", dir)
	}
	s := &Sessions{dir: dir, index: map[string]Summary{}, locks: map[string]*sync.Mutex{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("Read %s: check the permissions on the folder", dir)
	}
	for _, e := range entries {
		if e.IsDir() || !namePattern.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("sessions/%s: %v: the file is skipped", e.Name(), err)
			continue
		}
		var sess Session
		if err := json.Unmarshal(raw, &sess); err != nil {
			log.Printf("sessions/%s: %v: the file is skipped", e.Name(), err)
			continue
		}
		if sess.ID == "" {
			continue
		}
		s.index[sess.ID] = summarise(&sess, int64(len(raw)))
	}
	return s, nil
}

// List returns the sessions of one project, newest first, or every session
// when project is empty. Subagent runs stay out of the listing; Children is
// the only way to reach one.
func (s *Sessions) List(project string) []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Summary, 0, len(s.index))
	for _, v := range s.index {
		if v.Parent != "" {
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
// leaves them out, so this is the only way to reach one.
func (s *Sessions) Children(parent string) []Summary {
	if parent == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Summary
	for _, v := range s.index {
		if v.Parent == parent {
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
	raw, err := os.ReadFile(s.path(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("Read session %s: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("Read session %s: %w: the file is not valid JSON", id, err)
	}
	return &sess, nil
}

// Save writes the file and refreshes the index. Writes for one session are
// serialised; two sessions do not block each other.
func (s *Sessions) Save(sess *Session) error {
	if sess.ID == "" {
		return errors.New("Save session: the session has no id")
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("Save session %s: %w", sess.ID, err)
	}
	data = append(data, '\n')
	lock := s.lockFor(sess.ID)
	lock.Lock()
	err = config.WriteAtomic(s.path(sess.ID), data, 0o600)
	lock.Unlock()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.index[sess.ID] = summarise(sess, int64(len(data)))
	s.mu.Unlock()
	return nil
}

func (s *Sessions) Delete(id string) error {
	lock := s.lockFor(id)
	lock.Lock()
	err := os.Remove(s.path(id))
	// The baseline copies belong to this session alone, so they go with it.
	rmErr := os.RemoveAll(s.filesDir(id))
	lock.Unlock()
	if rmErr != nil {
		log.Printf("sessions/%s.files: %v: the folder is left behind", id, rmErr)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Delete session %s: %w", id, err)
	}
	s.mu.Lock()
	delete(s.index, id)
	delete(s.locks, id)
	s.mu.Unlock()
	if err != nil {
		return ErrNotFound
	}
	return nil
}

func (s *Sessions) Has(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.index[id]
	return ok
}

func (s *Sessions) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Sessions) lockFor(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.locks[id]; ok {
		return l
	}
	l := &sync.Mutex{}
	s.locks[id] = l
	return l
}

func summarise(sess *Session, size int64) Summary {
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
