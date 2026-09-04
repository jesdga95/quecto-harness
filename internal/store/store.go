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
	"quecto-harness/internal/llama"
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

	ToolCalls  []llama.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolName   string           `json:"tool_name,omitempty"`
	ExitCode   *int             `json:"exit_code,omitempty"`
	Truncated  bool             `json:"truncated,omitempty"`

	Permission  *PermissionRecord `json:"permission,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`

	InContext bool           `json:"in_context"`
	Visible   bool           `json:"visible"`
	At        time.Time      `json:"at"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// Attachment is a text file the user attached to a message. Its content is
// inlined into the message the model is sent.
type Attachment struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Content string `json:"content"`
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

// Session is the on-disk document. The system prompt is not in it: it is
// regenerated on every request, so editing the prompt reaches old sessions.
type Session struct {
	ID      string `json:"id"`
	Project string `json:"project"`
	Model   string `json:"model"`
	Title   string `json:"title"`
	// Named is set when the user typed the title. Nothing overwrites it.
	Named       bool         `json:"named"`
	Created     time.Time    `json:"created"`
	Updated     time.Time    `json:"updated"`
	NCtx        int          `json:"n_ctx"`
	NCtxSource  string       `json:"n_ctx_source"`
	Compactions int          `json:"compactions"`
	LastUsage   *llama.Usage `json:"last_usage,omitempty"`
	Todos       []Todo       `json:"todos,omitempty"`
	Entries     []Entry      `json:"entries"`
}

// Summary is what the sidebar needs, cheap enough to keep for every session.
type Summary struct {
	ID       string    `json:"id"`
	Project  string    `json:"project"`
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

var namePattern = regexp.MustCompile(`^sess_[a-z2-7]{16}\.json$`)

// Open builds the index once. A file that does not parse is named and
// skipped, because one bad file must not stop the program.
func Open(dir string) (*Sessions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: check the permissions on its parent", dir)
	}
	s := &Sessions{dir: dir, index: map[string]Summary{}, locks: map[string]*sync.Mutex{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: check the permissions on the folder", dir)
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

// List returns the sessions of one project, or every session when project is
// empty, newest first.
func (s *Sessions) List(project string) []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Summary, 0, len(s.index))
	for _, v := range s.index {
		if project == "" || v.Project == project {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Updated.Equal(out[j].Updated) {
			return out[i].ID < out[j].ID
		}
		return out[i].Updated.After(out[j].Updated)
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
		return nil, fmt.Errorf("read session %s: %w", id, err)
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("read session %s: %w: the file is not valid JSON", id, err)
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
		return fmt.Errorf("save session %s: %w", sess.ID, err)
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
	lock.Unlock()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete session %s: %w", id, err)
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
