package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// State is where one server stands.
type State string

const (
	StateIdle     State = "idle"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateFailed   State = "failed"
)

// Limits on starting a server and on what it may put in front of the model.
const (
	startTimeout   = 60 * time.Second
	maxDescription = 2000
	logCap         = 1 << 20
)

// backoff is how long a server that failed waits before the next start, by
// the number of failures in a row.
var backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 30 * time.Second}

// Offered is one tool as a turn offers it: the name it is sent under, where
// it comes from, and the schema as the server listed it.
type Offered struct {
	Wire        string
	Server      string
	Tool        string
	Description string
	Schema      json.RawMessage
	ReadOnly    bool
}

// Manager owns every running server. A server starts the first time a turn
// needs it, once per project when its command line names the project, and
// runs until the harness stops or its record changes.
type Manager struct {
	cacheDir string
	logDir   string
	version  string

	mu      sync.Mutex
	servers map[string]Server
	order   []string
	inst    map[instKey]*instance
	tools   map[string][]Tool
	dirty   map[string]bool
	// probed is what Test listed on a session of its own, shown in the
	// settings until a running session lists. Offers never read it.
	probed map[string][]Tool
	closed bool
}

type instKey struct{ server, project string }

type instance struct {
	key instKey
	srv Server

	mu       sync.Mutex
	client   *Client
	state    State
	lastErr  string
	failures int
	retryAt  time.Time
	starting chan struct{}
	stopping bool
	log      *os.File
}

// NewManager keeps the tool cache in cacheDir and each server's stderr in
// logDir. Neither is touched until a server starts.
func NewManager(cacheDir, logDir, version string) *Manager {
	return &Manager{
		cacheDir: cacheDir, logDir: logDir, version: version,
		servers: map[string]Server{}, inst: map[instKey]*instance{},
		tools: map[string][]Tool{}, dirty: map[string]bool{}, probed: map[string][]Tool{},
	}
}

// launchFields are what a running instance was started with. A change to
// anything else, such as the allow list, leaves it running.
func launchFields(s Server) Server {
	return Server{Transport: s.Transport, Command: s.Command, Args: s.Args, Env: s.Env, Cwd: s.Cwd, URL: s.URL, Headers: s.Headers}
}

// Configure replaces the server list. Instances of a server that was removed,
// disabled or relaunched differently are stopped; the next turn that needs
// one starts it again.
func (m *Manager) Configure(servers []Server) {
	m.mu.Lock()
	next := map[string]Server{}
	var order []string
	for _, s := range servers {
		next[s.Name] = s.Clone()
		order = append(order, s.Name)
	}
	sort.Strings(order)
	for name := range m.probed {
		replacement, ok := next[name]
		if !ok || !reflect.DeepEqual(launchFields(m.servers[name]), launchFields(replacement)) {
			delete(m.probed, name)
		}
	}
	var stop []*instance
	for k, in := range m.inst {
		s, ok := next[k.server]
		if !ok || !s.Usable() || !reflect.DeepEqual(launchFields(s), launchFields(in.srv)) {
			stop = append(stop, in)
			delete(m.inst, k)
		}
	}
	m.servers, m.order = next, order
	m.mu.Unlock()
	for _, in := range stop {
		go in.stop()
	}
}

// Server is the current record of one server.
func (m *Manager) Server(name string) (Server, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.servers[name]
	return s.Clone(), ok
}

// Enabled reports whether any server would be offered to a turn.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if s.Usable() {
			return true
		}
	}
	return false
}

// Tools is what a turn in project offers, in a fixed order: servers by name,
// tools by name. A server that will not start keeps the tools it listed last
// time, so the head of the prompt does not change because it is down; only
// one that has never listed any is left out, with a note.
func (m *Manager) Tools(ctx context.Context, project string) ([]Offered, []string) {
	m.mu.Lock()
	var usable []Server
	for _, n := range m.order {
		if s := m.servers[n]; s.Usable() {
			usable = append(usable, s)
		}
	}
	m.mu.Unlock()
	if len(usable) == 0 {
		return nil, nil
	}

	lists := make([][]Tool, len(usable))
	errs := make([]error, len(usable))
	var wg sync.WaitGroup
	for i, s := range usable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lists[i], errs[i] = m.listFor(ctx, s, project)
		}()
	}
	wg.Wait()

	var out []Offered
	var notes []string
	taken := map[string]string{}
	for i, s := range usable {
		tools := lists[i]
		if tools == nil {
			if errs[i] != nil {
				notes = append(notes, fmt.Sprintf("MCP server %s is left out: %s", s.Name, errs[i]))
			}
			continue
		}
		sorted := append([]Tool(nil), tools...)
		sort.Slice(sorted, func(a, b int) bool { return sorted[a].Name < sorted[b].Name })
		for _, t := range sorted {
			wire, ok := WireName(s.Name, t.Name)
			if !ok {
				notes = append(notes, fmt.Sprintf("MCP tool %s on %s is left out: its name is too long to send", t.Name, s.Name))
				continue
			}
			if prev, dup := taken[wire]; dup {
				notes = append(notes, fmt.Sprintf("MCP tool %s on %s is left out: it is sent under the same name as %s", t.Name, s.Name, prev))
				continue
			}
			taken[wire] = t.Name
			out = append(out, Offered{
				Wire: wire, Server: s.Name, Tool: t.Name,
				Description: describe(t), Schema: t.InputSchema, ReadOnly: s.ReadOnly,
			})
		}
	}
	return out, notes
}

// describe is the description the model reads, capped so one server cannot
// fill the head of the prompt.
func describe(t Tool) string {
	d := strings.TrimSpace(t.Description)
	if d == "" {
		d = t.Title
	}
	if utf8.RuneCountInString(d) <= maxDescription {
		return d
	}
	r := []rune(d)
	return string(r[:maxDescription]) + "…"
}

// listFor returns a server's tools: the list in memory while it is current,
// a fresh one from a running instance when the server said it changed, and
// the cache on disk when the server cannot be reached.
func (m *Manager) listFor(ctx context.Context, s Server, project string) ([]Tool, error) {
	m.mu.Lock()
	tools, known := m.tools[s.Name]
	dirty := m.dirty[s.Name]
	m.mu.Unlock()
	if known && !dirty {
		return tools, nil
	}
	in := m.instanceFor(s, project)
	client, err := in.ensure(ctx, m)
	if err == nil {
		var fresh []Tool
		fresh, _, err = client.ListTools(ctx)
		if errors.Is(err, errSessionExpired) {
			if client, err = in.renew(ctx, m, client); err == nil {
				fresh, _, err = client.ListTools(ctx)
			}
		}
		if err == nil {
			m.keepTools(s.Name, fresh)
			return fresh, nil
		}
	}
	if known {
		return tools, nil
	}
	if cached, ok := m.readCache(s.Name); ok {
		return cached, nil
	}
	return nil, err
}

func (m *Manager) keepTools(server string, tools []Tool) {
	if tools == nil {
		tools = []Tool{}
	}
	m.mu.Lock()
	m.tools[server] = tools
	delete(m.dirty, server)
	m.mu.Unlock()
	m.writeCache(server, tools)
}

func (m *Manager) cachePath(server string) string {
	return filepath.Join(m.cacheDir, server+".json")
}

func (m *Manager) readCache(server string) ([]Tool, bool) {
	raw, err := os.ReadFile(m.cachePath(server))
	if err != nil {
		return nil, false
	}
	var c struct {
		Tools []Tool `json:"tools"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Tools == nil {
		return nil, false
	}
	return c.Tools, true
}

func (m *Manager) writeCache(server string, tools []Tool) {
	if m.cacheDir == "" {
		return
	}
	data, err := json.MarshalIndent(map[string]any{"tools": tools}, "", "  ")
	if err != nil || os.MkdirAll(m.cacheDir, 0o700) != nil {
		return
	}
	tmp := m.cachePath(server) + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, m.cachePath(server))
	}
}

func (m *Manager) instanceFor(s Server, project string) *instance {
	k := instKey{server: s.Name}
	if s.PerProject() {
		k.project = project
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	in := m.inst[k]
	if in == nil {
		in = &instance{key: k, srv: s.Clone(), state: StateIdle}
		if !m.closed {
			m.inst[k] = in
		}
	}
	return in
}

// ErrNotRunning wraps the reason a call could not reach its server.
var ErrNotRunning = errors.New("not running")

// Call runs one tool on a server for a project. The deadline is the server's
// timeout, else fallback.
func (m *Manager) Call(ctx context.Context, project, server, tool string, args json.RawMessage, fallback time.Duration) (Rendered, error) {
	s, ok := m.Server(server)
	if !ok || !s.Usable() {
		return Rendered{}, fmt.Errorf("MCP server %s is not configured or is turned off", server)
	}
	in := m.instanceFor(s, project)
	client, err := in.ensure(ctx, m)
	if err != nil {
		return Rendered{}, fmt.Errorf("MCP server %s is %w: %s", server, ErrNotRunning, err)
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = fallback
	}
	cctx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	res, err := client.CallTool(cctx, tool, args)
	if errors.Is(err, errSessionExpired) {
		// The server no longer knows the session, so the call never ran there.
		if client, err = in.renew(cctx, m, client); err != nil {
			if cctx.Err() != nil {
				return Rendered{}, cctx.Err()
			}
			return Rendered{}, fmt.Errorf("MCP server %s is %w: %s", server, ErrNotRunning, err)
		}
		res, err = client.CallTool(cctx, tool, args)
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrClosed):
			return Rendered{}, fmt.Errorf("MCP server %s stopped during the call: %s", server, err)
		case ctx.Err() != nil:
			return Rendered{}, ctx.Err()
		case errors.Is(err, context.DeadlineExceeded):
			return Rendered{}, fmt.Errorf("%s on %s did not answer within %s", tool, server, timeout)
		}
		return Rendered{}, fmt.Errorf("%s on %s failed: %s", tool, server, err)
	}
	return Render(res), nil
}

// ensure returns a running client, starting one when there is none. A start
// already under way is waited on rather than repeated, and a server that
// failed recently answers with its failure until its backoff has passed.
func (in *instance) ensure(ctx context.Context, m *Manager) (*Client, error) {
	for {
		in.mu.Lock()
		if in.client != nil && in.state == StateRunning {
			c := in.client
			in.mu.Unlock()
			return c, nil
		}
		if in.stopping {
			in.mu.Unlock()
			return nil, errors.New("it is being stopped")
		}
		if wait := in.starting; wait != nil {
			in.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if m.isClosed() {
			in.mu.Unlock()
			return nil, errors.New("the harness is stopping")
		}
		if in.state == StateFailed && time.Now().Before(in.retryAt) {
			err := errors.New(in.lastErr)
			in.mu.Unlock()
			return nil, err
		}
		done := make(chan struct{})
		in.starting, in.state = done, StateStarting
		in.mu.Unlock()

		c, err := in.start(ctx, m)

		in.mu.Lock()
		in.starting = nil
		close(done)
		if err != nil && ctx.Err() != nil {
			// The caller gave up, which says nothing about the server.
			in.idle()
			in.mu.Unlock()
			return nil, ctx.Err()
		}
		if err != nil {
			in.state, in.lastErr = StateFailed, err.Error()
			in.retryAt = time.Now().Add(backoff[min(in.failures, len(backoff)-1)])
			in.failures++
			in.mu.Unlock()
			return nil, err
		}
		if in.stopping {
			in.mu.Unlock()
			c.Close()
			return nil, errors.New("it is being stopped")
		}
		in.client, in.state, in.lastErr, in.failures = c, StateRunning, "", 0
		in.mu.Unlock()
		go in.watch(m, c)
		return c, nil
	}
}

func (in *instance) start(ctx context.Context, m *Manager) (*Client, error) {
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	spec := in.srv.Spec(in.key.project)
	if spec.Stdio != nil {
		in.openLog(m.logDir)
		if in.log != nil {
			spec.Stdio.Stderr = in.log
		}
	}
	name := in.key.server
	c, err := Start(sctx, spec, Options{Version: m.version, OnNotify: func(method string, _ json.RawMessage) {
		if method == "notifications/tools/list_changed" {
			m.mu.Lock()
			m.dirty[name] = true
			m.mu.Unlock()
		}
	}})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("it did not finish starting within %s", startTimeout)
		}
		return nil, err
	}
	return c, nil
}

// watch marks the instance failed when its connection ends without being
// asked to, so the next call starts it again after the backoff. A session the
// server ended is no failure, and the next offer lists its tools again.
func (in *instance) watch(m *Manager, c *Client) {
	<-c.Done()
	in.mu.Lock()
	if in.client != c || in.stopping {
		in.mu.Unlock()
		return
	}
	in.client = nil
	if c.Expired() {
		in.idle()
		in.mu.Unlock()
		m.mu.Lock()
		m.dirty[in.key.server] = true
		m.mu.Unlock()
		return
	}
	defer in.mu.Unlock()
	in.state = StateFailed
	in.lastErr = "it stopped"
	if err := c.Err(); err != nil {
		in.lastErr = strings.TrimPrefix(err.Error(), ErrClosed.Error()+": ")
	}
	in.retryAt = time.Now().Add(backoff[min(in.failures, len(backoff)-1)])
	in.failures++
}

// renew replaces a client whose session the server ended with a new one.
// The caller sends its request again on the result once, and no more. The
// new session may list other tools, so the next offer lists again.
func (in *instance) renew(ctx context.Context, m *Manager, old *Client) (*Client, error) {
	in.forget(old)
	m.mu.Lock()
	m.dirty[in.key.server] = true
	m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return in.ensure(ctx, m)
}

// forget drops a client whose session the server ended, so the next call
// starts a new session without a backoff. A client the instance no longer
// holds is left alone, along with whatever state replaced it.
func (in *instance) forget(c *Client) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.client == c {
		in.client = nil
		in.idle()
	}
}

// idle leaves the instance ready to start, with no failure on record. The
// caller holds in.mu.
func (in *instance) idle() {
	in.state, in.lastErr, in.retryAt = StateIdle, "", time.Time{}
}

// openLog opens the server's stderr log, moving a full one aside first so
// the log keeps one previous generation.
func (in *instance) openLog(dir string) {
	if dir == "" || in.log != nil {
		return
	}
	path := filepath.Join(dir, "mcp-"+in.key.server+".log")
	if info, err := os.Stat(path); err == nil && info.Size() > logCap {
		_ = os.Rename(path, path+".1")
	}
	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600); err == nil {
		in.log = f
	}
}

func (in *instance) stop() {
	in.mu.Lock()
	in.stopping = true
	c := in.client
	in.client, in.state = nil, StateIdle
	in.mu.Unlock()
	if c != nil {
		c.Close()
	}
	in.mu.Lock()
	if in.log != nil {
		in.log.Close()
		in.log = nil
	}
	in.mu.Unlock()
}

// ServerStatus is one server as the settings page shows it.
type ServerStatus struct {
	Name    string   `json:"name"`
	State   State    `json:"state"`
	Error   string   `json:"error,omitempty"`
	Tools   []string `json:"tools"`
	Running int      `json:"running"`
}

// Status reports every configured server. A server running in several
// projects reads as running when any instance is, and failed when one has
// failed and none runs.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	order := append([]string(nil), m.order...)
	byServer := map[string][]*instance{}
	for k, in := range m.inst {
		byServer[k.server] = append(byServer[k.server], in)
	}
	known := map[string][]Tool{}
	for k, v := range m.probed {
		known[k] = v
	}
	for k, v := range m.tools {
		known[k] = v
	}
	m.mu.Unlock()

	out := make([]ServerStatus, 0, len(order))
	for _, n := range order {
		st := ServerStatus{Name: n, State: StateIdle, Tools: []string{}}
		tools, ok := known[n]
		if !ok {
			tools, _ = m.readCache(n)
		}
		for _, t := range tools {
			st.Tools = append(st.Tools, t.Name)
		}
		sort.Strings(st.Tools)
		for _, in := range byServer[n] {
			in.mu.Lock()
			switch in.state {
			case StateRunning:
				st.Running++
				st.State = StateRunning
			case StateStarting:
				if st.State != StateRunning {
					st.State = StateStarting
				}
			case StateFailed:
				if st.State == StateIdle {
					st.State, st.Error = StateFailed, in.lastErr
				}
			}
			in.mu.Unlock()
		}
		out = append(out, st)
	}
	return out
}

// Probe starts a server that may not be saved, lists its tools and stops it.
// It shares nothing with the running instances.
func (m *Manager) Probe(ctx context.Context, s Server, project string) (ServerInfo, []Tool, []string, error) {
	sctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	c, err := Start(sctx, s.Spec(project), Options{Version: m.version})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("it did not finish starting within %s", startTimeout)
		}
		return ServerInfo{}, nil, nil, err
	}
	defer c.Close()
	tools, notes, err := c.ListTools(sctx)
	if err != nil {
		return c.Info, nil, nil, err
	}
	// Test's session is its own and may list other tools than the session
	// turns run on, so its list is kept for the settings alone.
	m.mu.Lock()
	if cur, ok := m.servers[s.Name]; ok && s.Name != "" && reflect.DeepEqual(launchFields(cur), launchFields(s)) {
		m.probed[s.Name] = tools
	}
	m.mu.Unlock()
	return c.Info, tools, notes, nil
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// Close stops every server and refuses new ones.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	all := make([]*instance, 0, len(m.inst))
	for _, in := range m.inst {
		all = append(all, in)
	}
	m.inst = map[instKey]*instance{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, in := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in.stop()
		}()
	}
	wg.Wait()
}
