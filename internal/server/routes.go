package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"quecto-harness/internal/agent"
	"quecto-harness/internal/config"
	"quecto-harness/internal/llama"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/store"
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

type configJSON struct {
	Model             string  `json:"model"`
	Effort            string  `json:"effort"`
	PreserveReasoning bool    `json:"preserve_reasoning"`
	Permissions       string  `json:"permissions"`
	CompactAt         float64 `json:"compact_at"`
	Shell             string  `json:"shell"`
	CommandTimeoutS   int     `json:"command_timeout_s"`
	OutputLimitBytes  int     `json:"output_limit_bytes"`
	MaxToolCalls      int     `json:"max_tool_calls"`
	Listen            string  `json:"listen"`
	ServerURL         string  `json:"server_url"`
	ServerTokenSet    bool    `json:"server_token_set"`
	Note              string  `json:"note,omitempty"`
}

func toConfigJSON(c config.Config) configJSON {
	return configJSON{
		Model: c.Model, Effort: string(c.Effort), PreserveReasoning: c.PreserveReasoning,
		Permissions: string(c.Mode), CompactAt: c.CompactAt,
		Shell: c.Shell, CommandTimeoutS: int(c.CommandTimeout / time.Second),
		OutputLimitBytes: c.OutputLimitBytes, MaxToolCalls: c.MaxToolCalls, Listen: c.Listen,
		ServerURL: c.ServerURL, ServerTokenSet: c.ServerToken != "",
	}
}

func toModelJSON(models []llama.Model) []modelJSON {
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

// info returns the cached detect result, retrying a failure once the cache is
// older than the TTL so a model server that came up is noticed.
func (s *Server) info(ctx context.Context) (llama.Info, error) {
	s.mu.Lock()
	if time.Since(s.infoAt) < cacheTTL {
		v, err := s.infoVal, s.infoErr
		s.mu.Unlock()
		return v, err
	}
	s.mu.Unlock()

	if s.llamaClient().BaseURL == "" {
		err := errors.New("No model server is set: add its address in setup")
		s.mu.Lock()
		s.infoVal, s.infoErr, s.infoAt = llama.Info{}, err, time.Now()
		s.mu.Unlock()
		return llama.Info{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v, err := s.llamaClient().Detect(callCtx)

	s.mu.Lock()
	s.infoVal, s.infoErr, s.infoAt = v, err, time.Now()
	s.mu.Unlock()
	return v, err
}

func (s *Server) models(ctx context.Context, refresh bool) ([]llama.Model, error) {
	s.mu.Lock()
	if !refresh && time.Since(s.modelsAt) < cacheTTL {
		v, err := s.modelsVal, s.modelsErr
		s.mu.Unlock()
		return v, err
	}
	s.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := s.llamaClient().Models(callCtx)

	s.mu.Lock()
	s.modelsVal, s.modelsErr, s.modelsAt = v, err, time.Now()
	s.mu.Unlock()
	return v, err
}

// Detect warms the caches at startup, so the first page load does not wait.
func (s *Server) Detect(ctx context.Context) {
	s.info(ctx)
	s.models(ctx, false)
}

func (s *Server) getState(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Config()
	info, infoErr := s.info(r.Context())
	models, modelsErr := s.models(r.Context(), false)

	mode := info.Mode
	if mode == "" {
		mode = llama.ModeUnknown
	}
	if infoErr != nil {
		mode = llama.ModeUnknown
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
		"server_url":   cfg.ServerURL,
		"autoload":     info.Autoload,
		"detect_error": errText(infoErr),
		// The kind separates a server that refused from one that never
		// answered, which is the difference setup cares about.
		"detect_kind":  string(llama.Kind(infoErr)),
		"config":       toConfigJSON(cfg),
		"config_notes": notes,
		"projects":     projects,
		"models":       toModelJSON(models),
		"models_error": errText(modelsErr),
		"models_kind":  string(llama.Kind(modelsErr)),
		"os":           runtime.GOOS,
		"version":      s.version,
	})
}

func (s *Server) getModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.models(r.Context(), r.URL.Query().Get("refresh") == "1")
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
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	props, err := s.llamaClient().Props(ctx, id)
	if err != nil {
		var le *llama.Error
		if errors.As(err, &le) && le.Status >= 400 && le.Status < 500 {
			writeError(w, http.StatusConflict, "model \""+id+"\" is not loaded: it loads on the first message")
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
		out["note"] = "a session is still running and was kept"
	}
	writeJSON(w, http.StatusOK, out)
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
		Model   string `json:"model"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	sess, err := s.engine.Create(body.Project, body.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.applyModelHint(r.Context(), sess)
	doc, state, _, _ := sess.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"id": doc.ID, "project": doc.Project, "model": doc.Model, "title": doc.Title,
		"created": doc.Created, "updated": doc.Updated, "messages": 0, "bytes": 0,
		"state": string(state),
	})
}

// applyModelHint gives a new session a context size from the cached model
// list, which costs no request to the model server.
func (s *Server) applyModelHint(ctx context.Context, sess *agent.Session) {
	models, err := s.models(ctx, false)
	if err != nil {
		return
	}
	doc, _, _, _ := sess.Snapshot()
	for _, m := range models {
		if m.ID == doc.Model {
			s.engine.SetModelHint(sess, m.CtxHint, m.CtxSource)
			return
		}
	}
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	doc, state, fill, _ := sess.Snapshot()
	nctx, source := doc.NCtx, doc.NCtxSource
	if nctx <= 0 {
		nctx, source = 32768, "assumed"
	}
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
	writeJSON(w, http.StatusOK, map[string]any{
		"id": doc.ID, "project": doc.Project, "model": doc.Model,
		"state": string(state), "n_ctx": nctx, "n_ctx_source": source,
		"fill": fill, "compactions": doc.Compactions,
		"pending": pending, "entries": entries, "todos": todos,
		"last_seq": s.hub.Seq(doc.ID),
	})
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

func (s *Server) rebindSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Model string  `json:"model"`
		Title *string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	// A title alone renames and answers; a model changes what the session talks to.
	if body.Title != nil && body.Model == "" {
		title := strings.TrimSpace(*body.Title)
		if len([]rune(title)) > 120 {
			title = string([]rune(title)[:120])
		}
		if err := s.engine.Rename(sess.ID, title); err != nil {
			writeEngineError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": sess.ID, "title": title})
		return
	}
	models, err := s.models(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var picked *llama.Model
	for i := range models {
		if models[i].ID == body.Model {
			picked = &models[i]
		}
	}
	if picked == nil {
		writeError(w, http.StatusBadRequest, "model \""+body.Model+"\" is not on the model server: pick another from the model list")
		return
	}
	if err := s.engine.Rebind(sess.ID, picked.ID, picked.CtxHint, picked.CtxSource); err != nil {
		writeEngineError(w, err)
		return
	}
	doc, _, fill, _ := sess.Snapshot()
	nctx, source := doc.NCtx, doc.NCtxSource
	if nctx <= 0 {
		nctx, source = 32768, "assumed"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": doc.ID, "model": doc.Model, "n_ctx": nctx, "n_ctx_source": source, "fill": fill,
	})
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
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.engine.Send(sess.ID, body.Text, body.Attachments); err != nil {
		writeEngineError(w, err)
		return
	}
	// The attachments come back with the ids the harness gave them, so the
	// page can draw the image it just sent without refetching the session.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"state":       string(agent.StatePreflight),
		"attachments": sentAttachments(sess),
	})
}

// sentAttachments describes the files on the message that was just appended.
// The content of a text file is left out: the page has no use for it and it
// is already in the prompt.
func sentAttachments(sess *agent.Session) []map[string]any {
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
		break
	}
	return out
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
		prefix := config.NormaliseCommand(body.Prefix)
		command := config.NormaliseCommand(req.Command)
		if prefix == "" || !strings.HasPrefix(command, prefix) {
			writeError(w, http.StatusBadRequest,
				"prefix \""+body.Prefix+"\" does not match the command: edit it or choose allow once")
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

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model             *string  `json:"model"`
		Effort            *string  `json:"effort"`
		PreserveReasoning *bool    `json:"preserve_reasoning"`
		Permissions       *string  `json:"permissions"`
		CompactAt         *float64 `json:"compact_at"`
		Shell             *string  `json:"shell"`
		CommandTimeoutS   *int     `json:"command_timeout_s"`
		OutputLimitBytes  *int     `json:"output_limit_bytes"`
		MaxToolCalls      *int     `json:"max_tool_calls"`
		Listen            *string  `json:"listen"`
		Server            *struct {
			URL   *string `json:"url"`
			Token *string `json:"token"`
		} `json:"server"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Effort != nil && !llama.ValidEffort(*body.Effort) {
		writeError(w, http.StatusBadRequest, "effort \""+*body.Effort+"\" is not "+config.EffortNames)
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
	before := s.cfg.Config()
	cfg, err := s.cfg.Update(func(c *config.Config) {
		if body.Model != nil {
			c.Model = *body.Model
		}
		if body.Effort != nil {
			c.Effort = config.Effort(*body.Effort)
		}
		if body.PreserveReasoning != nil {
			c.PreserveReasoning = *body.PreserveReasoning
		}
		if body.Permissions != nil {
			c.Mode = config.Mode(*body.Permissions)
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
		if body.Server != nil {
			if body.Server.URL != nil {
				c.ServerURL = *body.Server.URL
			}
			if body.Server.Token != nil {
				c.ServerToken = *body.Server.Token
			}
		}
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg.ServerURL != before.ServerURL || cfg.ServerToken != before.ServerToken {
		client := llama.New(cfg.ServerURL, cfg.ServerToken)
		s.SetClient(client)
		s.engine.SetClient(client)
		go s.Detect(context.Background())
	}
	out := toConfigJSON(cfg)
	if body.Listen != nil && cfg.Listen != before.Listen {
		out.Note = "listen changes take effect on restart"
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
	case errors.Is(err, agent.ErrBusy):
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
