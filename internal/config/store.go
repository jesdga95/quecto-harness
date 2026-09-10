package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"quecto-harness/internal/llm"
)

// Store holds the resolved configuration and the allowlist, and is the only
// writer of the two files. All methods are safe for concurrent use.
type Store struct {
	mu    sync.RWMutex
	paths Paths
	cfg   Config
	allow map[string][]string
	notes []string
}

// Open reads both files, defaulting whatever is absent and writing the
// result back, so a first run leaves complete files on disk.
func Open(p Paths) (*Store, error) {
	s := &Store{paths: p, allow: map[string][]string{}}

	var file File
	raw, err := os.ReadFile(p.Config)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("Read %s: %w: fix the JSON or delete the file to start from defaults", p.Config, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("Read %s: %w", p.Config, err)
	}
	s.cfg, s.notes = file.Resolve(Defaults())

	rawAllow, err := os.ReadFile(p.Allowlist)
	switch {
	case err == nil:
		var m map[string][]string
		if err := json.Unmarshal(rawAllow, &m); err != nil {
			return nil, fmt.Errorf("Read %s: %w: fix the JSON or delete the file to start with no rules", p.Allowlist, err)
		}
		for project, prefixes := range m {
			s.allow[project] = normalisePrefixes(prefixes)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("Read %s: %w", p.Allowlist, err)
	}

	if err := s.persistConfig(); err != nil {
		return nil, err
	}
	if err := s.persistAllow(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Paths() Paths { return s.paths }

func (s *Store) Config() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cfg
	c.Agents = cloneAgents(s.cfg.Agents)
	return c
}

func (s *Store) Notes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.notes...)
}

// Update applies fn to a copy, validates it, and persists. The lock is held
// across the write so two concurrent updates cannot interleave.
func (s *Store) Update(fn func(*Config)) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg
	next.Projects = append([]string(nil), s.cfg.Projects...)
	next.Agents = cloneAgents(s.cfg.Agents)
	fn(&next)
	resolved, notes := next.File().Resolve(Defaults())
	s.cfg = resolved
	s.notes = notes
	if err := s.persistConfig(); err != nil {
		return s.cfg, err
	}
	return s.cfg, nil
}

// persistConfig writes config.json. The caller holds the lock.
func (s *Store) persistConfig() error {
	data, err := json.MarshalIndent(s.cfg.File(), "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(s.paths.Config, append(data, '\n'), 0o600)
}

// persistAllow writes allowlist.json. The caller holds the lock.
func (s *Store) persistAllow() error {
	out := map[string][]string{}
	for k, v := range s.allow {
		if len(v) > 0 {
			out[k] = v
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(s.paths.Allowlist, append(data, '\n'), 0o600)
}

func (s *Store) Allowlist(project string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.allow[project]...)
}

// AllowMatch reports the most specific stored prefix that covers command. A
// prefix only matches at a word boundary, so "git status" never covers
// "git statusfoo".
func (s *Store) AllowMatch(project, command string) (string, bool) {
	cmd := NormaliseCommand(command)
	if cmd == "" {
		return "", false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.allow[project] {
		if cmd == p {
			return p, true
		}
		if strings.HasPrefix(cmd, p) && len(cmd) > len(p) && cmd[len(p)] == ' ' {
			return p, true
		}
	}
	return "", false
}

func (s *Store) AddAllow(project, prefix string) error {
	p := NormaliseCommand(prefix)
	if p == "" {
		return errors.New("The rule is empty: type the command prefix to allow")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.allow[project] {
		if existing == p {
			return nil
		}
	}
	s.allow[project] = normalisePrefixes(append(s.allow[project], p))
	return s.persistAllow()
}

func (s *Store) RemoveAllow(project, prefix string) error {
	p := NormaliseCommand(prefix)
	s.mu.Lock()
	defer s.mu.Unlock()
	var keep []string
	for _, existing := range s.allow[project] {
		if existing != p {
			keep = append(keep, existing)
		}
	}
	s.allow[project] = keep
	return s.persistAllow()
}

// NormaliseCommand collapses runs of spaces and tabs so a stored prefix and a
// command are compared the same way.
func NormaliseCommand(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// normalisePrefixes drops empties and duplicates and sorts longest first, so
// AllowMatch reports the most specific rule.
func normalisePrefixes(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range in {
		n := NormaliseCommand(p)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// ResolveProject turns a user-supplied path into the canonical form used as
// the path guard root and as the allowlist key.
func ResolveProject(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("The path is empty: choose a folder")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("Resolve %s: %w", path, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("Open %s: no such folder, or it cannot be read", abs)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("Open %s: no such folder, or it cannot be read", abs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is a file: choose a folder", abs)
	}
	f, err := os.Open(real)
	if err != nil {
		return "", fmt.Errorf("Read %s: check the permissions on the folder", real)
	}
	f.Close()
	return real, nil
}

// AddProject stores the symlink-resolved absolute path, which is what the
// path guard compares against and what keys the allowlist.
func (s *Store) AddProject(path string) (Config, error) {
	real, err := ResolveProject(path)
	if err != nil {
		return s.Config(), err
	}
	s.mu.Lock()
	for _, p := range s.cfg.Projects {
		if p == real {
			s.mu.Unlock()
			return s.cfg, fmt.Errorf("%s is already in the project list", real)
		}
	}
	s.mu.Unlock()
	return s.Update(func(c *Config) { c.Projects = append(c.Projects, real) })
}

func (s *Store) RemoveProject(path string) (Config, error) {
	real := filepath.Clean(path)
	if r, err := ResolveProject(path); err == nil {
		real = r
	}
	return s.Update(func(c *Config) {
		var keep []string
		for _, p := range c.Projects {
			if p != real && p != filepath.Clean(path) {
				keep = append(keep, p)
			}
		}
		c.Projects = keep
	})
}

// HasProject reports whether path is one of the configured project roots.
func (s *Store) HasProject(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.cfg.Projects {
		if p == path {
			return true
		}
	}
	return false
}

// AddAgent stores a new agent and mints its id. It becomes the active one when
// nothing was selected, so a first run leaves a harness that can answer.
func (s *Store) AddAgent(a Agent) (Config, Agent, error) {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return s.Config(), Agent{}, errors.New("Name the agent: an agent needs a name")
	}
	if err := s.nameIsFree(a.Name, ""); err != nil {
		return s.Config(), Agent{}, err
	}
	a.ID = NewAgentID()
	cfg, err := s.Update(func(c *Config) { c.Agents = append(c.Agents, a) })
	if err != nil {
		return cfg, Agent{}, err
	}
	stored, _ := cfg.AgentByID(a.ID)
	return cfg, stored, nil
}

// UpdateAgent applies fn to one agent. The name is checked after fn runs, so a
// rename is refused before anything is written.
func (s *Store) UpdateAgent(id string, fn func(*Agent)) (Config, Agent, error) {
	s.mu.RLock()
	current, ok := s.cfg.AgentByID(id)
	s.mu.RUnlock()
	if !ok {
		return s.Config(), Agent{}, ErrNoSuchAgent
	}
	next := current
	next.Temperature, next.TopP = clonef(current.Temperature), clonef(current.TopP)
	fn(&next)
	next.Name = strings.TrimSpace(next.Name)
	if next.Name == "" {
		return s.Config(), Agent{}, errors.New("Name the agent: an agent needs a name")
	}
	if err := s.nameIsFree(next.Name, id); err != nil {
		return s.Config(), Agent{}, err
	}
	cfg, err := s.Update(func(c *Config) {
		for i := range c.Agents {
			if c.Agents[i].ID == id {
				c.Agents[i] = next
			}
		}
	})
	if err != nil {
		return cfg, Agent{}, err
	}
	stored, _ := cfg.AgentByID(id)
	return cfg, stored, nil
}

// RemoveAgent drops an agent. Removing the active one hands the turn to the
// first agent left that is enabled, and removing the last one leaves nothing
// selected, which is the state a first run is in.
func (s *Store) RemoveAgent(id string) (Config, error) {
	s.mu.RLock()
	_, ok := s.cfg.AgentByID(id)
	s.mu.RUnlock()
	if !ok {
		return s.Config(), ErrNoSuchAgent
	}
	return s.Update(func(c *Config) {
		keep := make([]Agent, 0, len(c.Agents))
		for _, a := range c.Agents {
			if a.ID != id {
				keep = append(keep, a)
			}
		}
		c.Agents = keep
		if c.AgentID == id {
			c.AgentID = firstEnabled(keep)
		}
	})
}

// SelectAgent points the harness at one agent. A half-configured one may be
// selected: what it is missing is reported when a message is sent.
func (s *Store) SelectAgent(id string) (Config, error) {
	s.mu.RLock()
	a, ok := s.cfg.AgentByID(id)
	s.mu.RUnlock()
	if !ok {
		return s.Config(), ErrNoSuchAgent
	}
	if !a.Enabled {
		return s.Config(), fmt.Errorf("Agent %q is disabled: enable it to select it", a.Name)
	}
	return s.Update(func(c *Config) { c.AgentID = id })
}

// AgentKeys is the identity of every agent's model client, for the pool to
// keep and for anything else to compare against.
func (s *Store) AgentKeys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.cfg.Agents))
	for _, a := range s.cfg.Agents {
		keys = append(keys, llm.Key(a.URL, a.Key, a.Protocol))
	}
	return keys
}

// ErrNoSuchAgent is returned by every method that takes an agent id, so the
// caller answers 404 without matching on a sentence.
var ErrNoSuchAgent = errors.New("No agent with that id: it may have been deleted")

// nameIsFree refuses a name another agent already has. The id is the identity,
// so this is about the reader, who picks an agent out of a list by its name.
func (s *Store) nameIsFree(name, own string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.cfg.Agents {
		if a.ID != own && strings.EqualFold(a.Name, name) {
			return fmt.Errorf("An agent named %q already exists: choose another name", a.Name)
		}
	}
	return nil
}
