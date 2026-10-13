package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/jesdga95/quecto-harness/internal/agent"
	"github.com/jesdga95/quecto-harness/internal/config"
	"github.com/jesdga95/quecto-harness/internal/llm"
	"github.com/jesdga95/quecto-harness/internal/perms"
	"github.com/jesdga95/quecto-harness/internal/prompts"
	"github.com/jesdga95/quecto-harness/internal/store"
	"github.com/jesdga95/quecto-harness/internal/tools"
)

type modelJSON struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	CtxHint    int      `json:"ctx_hint"`
	CtxSource  string   `json:"ctx_source"`
	Modalities []string `json:"modalities"`
	Failed     bool     `json:"failed"`
	ExitCode   int      `json:"exit_code,omitempty"`
}

// patch is one field of a config update. A field the body leaves out keeps
// the value it has; a field sent as null is cleared, which is how a sampling
// value goes back to the model server's own.
type patch[T any] struct {
	Set   bool
	Value *T
}

func (p *patch[T]) UnmarshalJSON(b []byte) error {
	p.Set = true
	if string(b) == "null" {
		p.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	p.Value = &v
	return nil
}

type configJSON struct {
	Agent            string  `json:"agent"`
	Permissions      string  `json:"permissions"`
	Toolset          string  `json:"toolset"`
	CompactAt        float64 `json:"compact_at"`
	Shell            string  `json:"shell"`
	CommandTimeoutS  int     `json:"command_timeout_s"`
	OutputLimitBytes int     `json:"output_limit_bytes"`
	MaxToolCalls     int     `json:"max_tool_calls"`
	Listen           string  `json:"listen"`
	Note             string  `json:"note,omitempty"`
}

func toConfigJSON(c config.Config) configJSON {
	return configJSON{
		Agent: c.AgentID, Permissions: string(c.Mode), Toolset: string(c.Toolset), CompactAt: c.CompactAt,
		Shell: c.Shell, CommandTimeoutS: int(c.CommandTimeout / time.Second),
		OutputLimitBytes: c.OutputLimitBytes, MaxToolCalls: c.MaxToolCalls, Listen: c.Listen,
	}
}

// agentJSON is the one wire shape for an agent.
type agentJSON struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Enabled           bool     `json:"enabled"`
	Calls             []string `json:"calls"`
	URL               string   `json:"url"`
	Key               string   `json:"key"`
	Protocol          string   `json:"protocol"`
	Model             string   `json:"model"`
	Effort            string   `json:"effort"`
	PreserveReasoning bool     `json:"preserve_reasoning"`
	Temperature       *float64 `json:"temperature"`
	TopP              *float64 `json:"top_p"`
	TopK              *int     `json:"top_k"`
	RepeatPenalty     *float64 `json:"repeat_penalty"`
	MaxTokens         *int     `json:"max_tokens"`
	MaxContext        *int     `json:"max_context"`
	Seed              *int     `json:"seed"`
	Concurrency       int      `json:"concurrency"`
	Description       string   `json:"description"`
	Instructions      string   `json:"instructions"`
}

func toAgentJSON(a config.Agent) agentJSON {
	calls := a.Calls
	if calls == nil {
		calls = []string{}
	}
	return agentJSON{
		ID: a.ID, Name: a.Name, Enabled: a.Enabled, Calls: calls, URL: a.URL, Key: a.Key, Protocol: string(a.Protocol),
		Model: a.Model, Effort: string(a.Effort), PreserveReasoning: a.PreserveReasoning,
		Temperature: a.Temperature, TopP: a.TopP, TopK: a.TopK,
		RepeatPenalty: a.RepeatPenalty, MaxTokens: a.MaxTokens, MaxContext: a.MaxContext, Seed: a.Seed,
		Concurrency: a.Concurrency, Description: a.Description, Instructions: a.Instructions,
	}
}

func toAgentsJSON(in []config.Agent) []agentJSON {
	out := make([]agentJSON, 0, len(in))
	for _, a := range in {
		out = append(out, toAgentJSON(a))
	}
	return out
}

func toModelJSON(models []llm.Model) []modelJSON {
	out := make([]modelJSON, 0, len(models))
	for _, m := range models {
		mod := m.Modalities
		if mod == nil {
			mod = []string{}
		}
		out = append(out, modelJSON{ID: m.ID, Status: string(m.Status), CtxHint: m.CtxHint,
			CtxSource: m.CtxSource, Modalities: mod, Failed: m.Failed, ExitCode: m.ExitCode})
	}
	return out
}

// info returns what one address said it is, retrying a failure once the
// reading is older than the TTL so a model server that came up is noticed.
func (s *Server) info(ctx context.Context, a config.Agent) (llm.Info, error) {
	s.mu.Lock()
	pr := s.probeFor(a)
	if time.Since(pr.infoAt) < cacheTTL {
		v, err := pr.info, pr.infoErr
		s.mu.Unlock()
		return v, err
	}
	s.mu.Unlock()

	if a.URL == "" {
		err := errors.New("The agent has no address: add it in the agents dialog")
		s.mu.Lock()
		pr.info, pr.infoErr, pr.infoAt = llm.Info{}, err, time.Now()
		s.mu.Unlock()
		return llm.Info{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := s.pool.Get(a.URL, a.Key, a.Protocol).Detect(callCtx)

	s.mu.Lock()
	pr.info, pr.infoErr, pr.infoAt = v, err, time.Now()
	s.mu.Unlock()
	return v, err
}

func (s *Server) models(ctx context.Context, a config.Agent, refresh bool) ([]llm.Model, error) {
	s.mu.Lock()
	pr := s.probeFor(a)
	if !refresh && time.Since(pr.modelsAt) < cacheTTL {
		v, err := pr.models, pr.modelsErr
		s.mu.Unlock()
		return v, err
	}
	s.mu.Unlock()

	if a.URL == "" {
		err := errors.New("The agent has no address: add it in the agents dialog")
		s.mu.Lock()
		pr.models, pr.modelsErr, pr.modelsAt = nil, err, time.Now()
		s.mu.Unlock()
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := s.pool.Get(a.URL, a.Key, a.Protocol).Models(callCtx)

	s.mu.Lock()
	pr.models, pr.modelsErr, pr.modelsAt = v, err, time.Now()
	s.mu.Unlock()
	return v, err
}

// Detect warms the reading for the active agent at startup, so the first page
// load does not wait. With no agent selected there is nothing to read.
func (s *Server) Detect(ctx context.Context) {
	a, ok := s.cfg.Config().Agent()
	if !ok {
		return
	}
	s.info(ctx, a)
	s.models(ctx, a, false)
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Config()
	// Only the active agent is read. Probing every address on a state read
	// would cost one call each to draw one chip; the agents dialog asks for
	// the rest itself, one at a time, when a row is opened.
	a, ok := cfg.Agent()
	var info llm.Info
	var models []llm.Model
	infoErr, modelsErr := error(nil), error(nil)
	if ok {
		info, infoErr = s.info(r.Context(), a)
		models, modelsErr = s.models(r.Context(), a, false)
	} else if len(cfg.Agents) > 0 {
		infoErr = agent.ErrAgentsOff
	} else {
		infoErr = agent.ErrNoAgent
	}

	mode := info.Mode
	if mode == "" {
		mode = llm.ModeUnknown
	}
	if infoErr != nil {
		mode = llm.ModeUnknown
	}
	notes := s.cfg.Notes()
	if notes == nil {
		notes = []string{}
	}
	projects := cfg.Projects
	if projects == nil {
		projects = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":         string(mode),
		"build":        info.Build,
		"agents":       toAgentsJSON(cfg.Agents),
		"agent":        cfg.AgentID,
		"autoload":     info.Autoload,
		"detect_error": errText(infoErr),
		// The kind separates a server that refused from one that never
		// answered, which is the difference setup cares about.
		"detect_kind":  string(llm.Kind(infoErr)),
		"config":       toConfigJSON(cfg),
		"config_notes": notes,
		"search":       toSearchJSON(cfg.Search),
		"projects":     projects,
		"models":       toModelJSON(models),
		"models_error": errText(modelsErr),
		"models_kind":  string(llm.Kind(modelsErr)),
		"os":           runtime.GOOS,
		"version":      s.version,
	})
}

// getModels lists what the active agent's address holds. It is read on every
// status event to redraw the dots, and it always wants the agent answering, so
// it takes no agent of its own. The dialog asks about the others through the
// probe route.
func (s *Server) getModels(w http.ResponseWriter, r *http.Request) {
	a, ok := s.cfg.Config().Agent()
	if !ok {
		writeError(w, http.StatusConflict, "No agent is selected: create one in the agents dialog")
		return
	}
	models, err := s.models(r.Context(), a, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": toModelJSON(models)})
}

func (s *Server) getModelProps(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "No model id: pass ?id=<model>")
		return
	}
	a, ok := s.cfg.Config().Agent()
	if !ok {
		writeError(w, http.StatusConflict, "No agent is selected: create one in the agents dialog")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	props, err := s.pool.Get(a.URL, a.Key, a.Protocol).Props(ctx, id)
	if err != nil {
		var le *llm.Error
		if errors.As(err, &le) && le.Status >= 400 && le.Status < 500 {
			writeError(w, http.StatusConflict, "Model \""+id+"\" is not loaded: it loads on the first message")
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model": props.Model, "n_ctx": props.NCtx, "total_slots": props.TotalSlots,
		"sleeping": props.Sleeping, "caps": props.Caps,
	})
}

func (s *Server) postProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	cfg, err := s.cfg.AddProject(body.Path)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already in the project list") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": orEmpty(cfg.Projects)})
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	// A project that is gone has no conversations, so its sessions go with it.
	// A running one is left alone and reported rather than killed underfoot.
	removed, busy := 0, 0
	for _, sum := range s.store.List(body.Path) {
		if err := s.engine.Delete(sum.ID); err != nil {
			busy++
			continue
		}
		removed++
	}
	cfg, err := s.cfg.RemoveProject(body.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := map[string]any{"projects": orEmpty(cfg.Projects), "sessions_deleted": removed}
	if busy > 0 {
		out["note"] = "A session is still running and was kept"
	}
	writeJSON(w, http.StatusOK, out)
}

// putProjects sets the order of the project list. The body names the whole
// list, in its new order.
func (s *Server) putProjects(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paths []string `json:"paths"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	cfg, err := s.cfg.ReorderProjects(body.Paths)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": orEmpty(cfg.Projects)})
}

func (s *Server) getAllowlist(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	writeJSON(w, http.StatusOK, map[string]any{
		"project": project, "prefixes": orEmpty(s.cfg.Allowlist(project)),
	})
}

func (s *Server) deleteAllowlist(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
		Prefix  string `json:"prefix"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.cfg.RemoveAllow(body.Project, body.Prefix); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": body.Project, "prefixes": orEmpty(s.cfg.Allowlist(body.Project)),
	})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	list := s.store.List(r.URL.Query().Get("project"))
	if list == nil {
		list = []store.Summary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string `json:"project"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	sess, err := s.engine.Create(body.Project)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	doc, state, _, _ := sess.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"id": doc.ID, "project": doc.Project, "model": doc.Model, "title": doc.Title,
		"created": doc.Created, "updated": doc.Updated, "messages": 0, "bytes": 0,
		"state": string(state),
	})
}

// children lists the subagent runs one session launched, each with the name of
// the agent running it, what it is doing now and the context it holds. A
// reloaded page reads which are still working from here and nowhere else.
func (s *Server) children(id string) []map[string]any {
	kids := s.store.Children(id)
	out := make([]map[string]any, 0, len(kids))
	cfg := s.cfg.Config()
	for _, k := range kids {
		a, _ := cfg.AgentByID(k.Agent)
		// A working run's context moves with every request; a stopped one's is
		// what its last request reported.
		state := s.engine.LiveState(k.ID)
		fill := k.Fill
		if state != agent.StateIdle {
			if live, ok := s.engine.LiveFill(k.ID); ok {
				fill = live
			}
		}
		row := map[string]any{
			"id": k.ID, "title": k.Title, "agent": a.Name,
			"state": string(state),
		}
		if fill > 0 {
			row["fill"] = fill
		}
		out = append(out, row)
	}
	return out
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.engine.Open(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "No session with that id: it may have been deleted")
		return
	}
	view := sess.View(func() int64 { return s.hub.Mark(sess.ID) })
	doc, state, fill := view.Doc, view.State, view.Fill
	// The agent's override stands in for what the model server reported, and a
	// window neither named is zero: unknown rather than guessed.
	agent, _ := s.cfg.Config().AgentByID(doc.Agent)
	nctx, source := agent.Window(doc.NCtx, doc.NCtxSource)
	pending := s.perms.Pending(doc.ID)
	if pending == nil {
		pending = []perms.Request{}
	}
	entries := doc.Entries
	if entries == nil {
		entries = []store.Entry{}
	}
	todos := doc.Todos
	if todos == nil {
		todos = []store.Todo{}
	}
	body := map[string]any{
		"id": doc.ID, "project": doc.Project, "model": doc.Model, "parent": doc.Parent,
		"updated": doc.Updated, "mode": string(doc.Mode), "toolset": string(doc.Toolset), "plan": doc.Plan, "draft": doc.Draft,
		// A subagent run is reachable only from the transcript that launched
		// it, so a launcher carries the list of what it launched.
		"children": s.children(doc.ID),
		// A tab that refetches still draws the message waiting for the turn.
		"steer": steerJSON(sess.Steer()),
		"state": string(state), "n_ctx": nctx, "n_ctx_source": source,
		"fill": fill, "compactions": doc.Compactions, "compactable": sess.Compactable(),
		"pending": pending, "entries": entries, "todos": todos,
		"last_seq": view.Seq,
	}
	// The reply a request is still streaming is in no entry yet, and the
	// stream opened from last_seq carries on from its end.
	if view.Reply != nil {
		body["reply"] = view.Reply
	}
	// A question is answered on the row its own call already drew, so what a
	// reloaded page is missing is the id to answer, not the question.
	if q := s.engine.PendingQuestion(doc.ID); q != nil {
		body["question"] = q
	}
	if parts := sess.Breakdown(); parts != nil {
		body["breakdown"] = parts
	}
	writeJSON(w, http.StatusOK, body)
}

// getChanges compares every file the session wrote against the copy kept
// before its first write and the file on disk now.
func (s *Server) getChanges(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	files, err := s.engine.Changes(sess.ID)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	added, removed := 0, 0
	for _, f := range files {
		added += f.Added
		removed += f.Removed
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files": files, "added": added, "removed": removed,
	})
}

// getProcesses lists the commands this session started in the background.
func (s *Server) getProcesses(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"processes": s.engine.Processes(sess.ID)})
}

// stopProcess ends one background command. A process that already went is not
// an error, since the browser may be closing a panel that raced with its exit.
func (s *Server) stopProcess(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if err := s.engine.StopProcess(sess.ID, r.PathValue("proc")); err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// getProcessOutput is one command's output, cut to the same limit the file
// panel reads a file at.
func (s *Server) getProcessOutput(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	pr, out, trunc, found := s.engine.ProcessOutput(sess.ID, r.PathValue("proc"), viewLimit)
	if !found {
		writeError(w, http.StatusNotFound, "No such process: check the process list")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"process": pr, "output": out, "truncated": trunc})
}

// viewLimit is the most text the file panel is sent, and rawLimit the largest
// image it is served.
const (
	viewLimit = 1 << 20
	rawLimit  = 16 << 20
)

// getProjectFile reads one file in the session's project for the panel a file
// link opens. Text comes back as JSON. raw=1 serves the bytes of an image and
// of nothing else, since a page or drawing served from this origin would run
// with the reader's session.
func (s *Server) getProjectFile(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	arg := r.URL.Query().Get("path")
	v, err := tools.ViewFile(sess.Project, arg, viewLimit)
	switch {
	case errors.Is(err, tools.ErrOutside):
		writeError(w, http.StatusForbidden, "That path is outside the project")
		return
	case errors.Is(err, tools.ErrMissing):
		writeError(w, http.StatusNotFound, "No such file: "+v.Path)
		return
	case errors.Is(err, tools.ErrFolder):
		writeError(w, http.StatusBadRequest, v.Path+" is a folder, not a file")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if r.URL.Query().Get("raw") != "1" {
		writeJSON(w, http.StatusOK, map[string]any{
			"path": v.Path, "kind": v.Kind, "bytes": v.Bytes, "content": v.Content,
		})
		return
	}
	if v.Kind != tools.ViewImage {
		writeError(w, http.StatusUnsupportedMediaType, "Only images are served as they are")
		return
	}
	if v.Bytes > rawLimit {
		writeError(w, http.StatusRequestEntityTooLarge, "The image is too large to show")
		return
	}
	f, err := os.Open(v.Abs)
	if err != nil {
		writeError(w, http.StatusNotFound, "No such file: "+v.Path)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Read the file: "+err.Error())
		return
	}
	// The file can change under the same address, so nothing is kept.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Content-Type", v.Mime)
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// getProjectDir lists one folder in the session's project for the panel's
// file browser.
func (s *Server) getProjectDir(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	rel, entries, err := tools.ListDir(sess.Project, r.URL.Query().Get("path"))
	switch {
	case errors.Is(err, tools.ErrOutside):
		writeError(w, http.StatusForbidden, "That path is outside the project")
	case errors.Is(err, tools.ErrMissing):
		writeError(w, http.StatusNotFound, "No such folder: "+rel)
	case errors.Is(err, tools.ErrNotFolder):
		writeError(w, http.StatusBadRequest, rel+" is a file, not a folder")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"root": sess.Project, "path": rel, "entries": entries})
	}
}

// getProjectFiles finds files in a project by name, for the composer's @
// mentions. It takes the project rather than a session, since the message
// that mentions a file may be the one that creates the session.
func (s *Server) getProjectFiles(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if !s.cfg.HasProject(project) {
		writeError(w, http.StatusNotFound, "No project at that path: add it first")
		return
	}
	files, err := tools.FindFiles(project, r.URL.Query().Get("q"), 30)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "List the project files: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) rebindSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	title := strings.TrimSpace(body.Title)
	if len([]rune(title)) > 120 {
		title = string([]rune(title)[:120])
	}
	if err := s.engine.Rename(sess.ID, title); err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": sess.ID, "title": title})
}

// putMode sets the work mode of the session's next turn. A turn already
// running keeps the one it started in.
func (s *Server) putMode(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.engine.SetMode(sess.ID, config.WorkMode(body.Mode)); err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": sess.ID, "mode": body.Mode})
}

func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if err := s.engine.Delete(sess.ID); err != nil {
		writeEngineError(w, err)
		return
	}
	s.hub.Drop(sess.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Text        string   `json:"text"`
		Attachments []string `json:"attachments"`
		// Mode is the mode the message is sent in. Empty keeps the session's.
		Mode string `json:"mode"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Mode != "" {
		if err := s.engine.SetMode(sess.ID, config.WorkMode(body.Mode)); err != nil {
			writeEngineError(w, err)
			return
		}
	}
	sent, err := s.engine.Send(sess.ID, body.Text, body.Attachments)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	_, state, _, _ := sess.Snapshot()
	// A message typed while a turn was running waits for it. What comes back
	// is the queue as it now stands, so a tab that added to one draws the
	// joined message rather than its own half.
	if sent.Queued {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"state": string(state), "queued": true, "steer": steerJSON(sent.Steer),
		})
		return
	}
	// The attachments come back with the ids the harness gave them, so the
	// page can draw the image it just sent without refetching the session.
	// The sequence number is what an edit of this message names it by.
	_, attachments := sentMessage(sess)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"state":       string(state),
		"queued":      false,
		"seq":         sent.Seq,
		"attachments": attachments,
	})
}

// steerJSON renders the message waiting for a running turn.
func steerJSON(held *agent.Steer) map[string]any {
	if held == nil {
		return nil
	}
	return map[string]any{"text": held.Text, "from": held.From.Name,
		"attachments": agent.AttachmentFields(held.Attachments)}
}

// postResend runs the last message again, with the text rewritten when the
// body carries one. Naming the message by its sequence number is what stops a
// second tab editing a message it is no longer looking at.
func (s *Server) postResend(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Seq  int     `json:"seq"`
		Text *string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	seq, err := s.engine.Resend(sess.ID, body.Seq, body.Text)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"state": string(agent.StatePreflight), "seq": seq,
	})
}

// sentMessage describes the message that was just appended. The content of a
// text file is left out: the page has no use for it and it is already in the
// prompt.
func sentMessage(sess *agent.Session) (int, []map[string]any) {
	doc, _, _, _ := sess.Snapshot()
	out := []map[string]any{}
	for i := len(doc.Entries) - 1; i >= 0; i-- {
		if doc.Entries[i].Kind != "user" {
			continue
		}
		for _, a := range doc.Entries[i].Attachments {
			out = append(out, map[string]any{
				"path": a.Path, "kind": a.Kind, "bytes": a.Bytes, "file": a.File,
			})
		}
		return doc.Entries[i].Seq, out
	}
	return 0, out
}

func (s *Server) postStop(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	state, err := s.engine.Stop(sess.ID)
	if err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": string(state)})
}

func (s *Server) postCompact(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if err := s.engine.Compact(sess.ID); err != nil {
		writeEngineError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"state": string(agent.StateCompacting)})
}

func (s *Server) postPermission(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Decision string `json:"decision"`
		Prefix   string `json:"prefix"`
		Reason   string `json:"reason"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	kind := perms.DecisionKind(body.Decision)
	switch kind {
	case perms.DecideOnce, perms.DecideAlways, perms.DecideDeny:
	default:
		writeError(w, http.StatusBadRequest, "Decision \""+body.Decision+"\" is not once, always or deny")
		return
	}
	req, known := s.perms.Lookup(id)
	if !known {
		writeError(w, http.StatusNotFound, "That decision is no longer pending: the turn moved on")
		return
	}
	if kind == perms.DecideAlways {
		if req.Tool != "bash" {
			writeError(w, http.StatusBadRequest,
				"Allow always applies to shell commands only: choose allow once")
			return
		}
		prefix := config.NormaliseCommand(body.Prefix)
		command := config.NormaliseCommand(req.Command)
		if prefix == "" || !strings.HasPrefix(command, prefix) {
			writeError(w, http.StatusBadRequest,
				"Prefix \""+body.Prefix+"\" does not match the command: edit it or choose allow once")
			return
		}
		body.Prefix = prefix
	}
	if err := s.perms.Resolve(id, perms.Decision{Kind: kind, Prefix: body.Prefix, Reason: body.Reason}); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postQuestion answers the question a turn is blocked on. The answer is
// checked against the options that were offered, since the model reads it as
// the choice the user made.
func (s *Server) postQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Options []int  `json:"options"`
		Text    string `json:"text"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	q, open := s.engine.OpenQuestion(id)
	if !open {
		writeError(w, http.StatusNotFound, agent.ErrNoSuchQuestion.Error())
		return
	}
	for _, i := range body.Options {
		if i < 0 || i >= len(q.Options) {
			writeError(w, http.StatusBadRequest, agent.ErrNoSuchOption.Error())
			return
		}
	}
	if !q.Multi && len(body.Options) > 1 {
		writeError(w, http.StatusBadRequest, agent.ErrNotMultiSelect.Error())
		return
	}
	if len(body.Options) == 0 && strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, agent.ErrNoAnswer.Error())
		return
	}
	if err := s.engine.Answer(id, agent.Answer{Options: body.Options, Text: body.Text}); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// getTools answers the harness pane's debug view with the tools each toolset
// sends and the system prompt each renders. The render is representative: it
// takes the first project and today's date, and leaves out the rules and the
// agent instructions a real turn adds.
func (s *Server) getTools(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Config()
	project := "(no project)"
	if len(cfg.Projects) > 0 {
		project = cfg.Projects[0]
	}
	day := time.Now().Format("2006-01-02")
	offer := func(set config.Toolset) tools.Offer {
		return tools.Offer{Set: set, Mode: config.WorkExecute, Ask: true, Agents: true, Message: true, Search: true}
	}
	sent := map[string][]string{}
	rendered := map[string]string{}
	for _, set := range []config.Toolset{config.ToolsetFull, config.ToolsetMinimal} {
		names := tools.Names(offer(set))
		sent[string(set)] = names
		rendered[string(set)] = prompts.System(prompts.SystemVars{
			Project:     project,
			OS:          runtime.GOOS,
			Date:        day,
			CanAsk:      true,
			CanMessage:  true,
			CanSearch:   true,
			Tools:       names,
			NotAllTools: len(names) < tools.AllCount(),
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Tools   []tools.ToolDebug   `json:"tools"`
		Sent    map[string][]string `json:"sent"`
		Prompts map[string]string   `json:"prompts"`
	}{
		Tools:   tools.DebugTools(offer(config.ToolsetFull)),
		Sent:    sent,
		Prompts: rendered,
	})
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Permissions      *string  `json:"permissions"`
		Toolset          *string  `json:"toolset"`
		CompactAt        *float64 `json:"compact_at"`
		Shell            *string  `json:"shell"`
		CommandTimeoutS  *int     `json:"command_timeout_s"`
		OutputLimitBytes *int     `json:"output_limit_bytes"`
		MaxToolCalls     *int     `json:"max_tool_calls"`
		Listen           *string  `json:"listen"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.MaxToolCalls != nil && (*body.MaxToolCalls < 0 || *body.MaxToolCalls > 1000) {
		writeError(w, http.StatusBadRequest, "Max tool calls is outside 0 to 1000: 0 is no limit")
		return
	}
	if body.Permissions != nil && !config.ValidMode(*body.Permissions) {
		writeError(w, http.StatusBadRequest, "Permissions \""+*body.Permissions+"\" is not ask, allowlist or full")
		return
	}
	if body.Toolset != nil && !config.ValidToolset(*body.Toolset) {
		writeError(w, http.StatusBadRequest, "Toolset \""+*body.Toolset+"\" is not "+config.ToolsetNames)
		return
	}
	before := s.cfg.Config()
	cfg, err := s.cfg.Update(func(c *config.Config) {
		if body.Permissions != nil {
			c.Mode = config.Mode(*body.Permissions)
		}
		if body.Toolset != nil {
			c.Toolset = config.Toolset(*body.Toolset)
		}
		if body.CompactAt != nil {
			c.CompactAt = *body.CompactAt
		}
		if body.Shell != nil {
			c.Shell = *body.Shell
		}
		if body.CommandTimeoutS != nil {
			c.CommandTimeout = time.Duration(*body.CommandTimeoutS) * time.Second
		}
		if body.OutputLimitBytes != nil {
			c.OutputLimitBytes = *body.OutputLimitBytes
		}
		if body.MaxToolCalls != nil {
			c.MaxToolCalls = *body.MaxToolCalls
		}
		if body.Listen != nil {
			c.Listen = *body.Listen
		}
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := toConfigJSON(cfg)
	if body.Listen != nil && cfg.Listen != before.Listen {
		out.Note = "Listen changes take effect on restart"
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) (*agent.Session, bool) {
	sess, err := s.engine.Get(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "No session with that id: it may have been deleted")
		return nil, false
	}
	return sess, true
}

func writeEngineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrBusy), errors.Is(err, agent.ErrNotLast),
		errors.Is(err, agent.ErrCompacting), errors.Is(err, agent.ErrTooShort):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, agent.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// getFile serves an attached image to the page that shows it. The type is
// read back off the bytes rather than trusted from anything stored, and the
// id is the only thing that can name a file, so nothing here takes a path.
func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	path, err := s.files.Path(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "No file with that id: the session may have been deleted")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusNotFound, "No file with that id: the session may have been deleted")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Read the file: "+err.Error())
		return
	}
	// An id is minted once and never reused, so the browser may keep it.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", fileType(f))
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// fileType sniffs the content type and leaves the reader where it found it.
func fileType(f *os.File) string {
	head := make([]byte, 512)
	n, _ := f.Read(head)
	f.Seek(0, io.SeekStart)
	if n == 0 {
		return "application/octet-stream"
	}
	return strings.SplitN(http.DetectContentType(head[:n]), ";", 2)[0]
}
