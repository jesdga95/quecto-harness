package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
)

// agentBody is what a write may carry. A field the body leaves out keeps the
// value it has, and an empty key is no key.
type agentBody struct {
	Name              *string        `json:"name"`
	URL               *string        `json:"url"`
	Key               *string        `json:"key"`
	Model             *string        `json:"model"`
	Effort            *string        `json:"effort"`
	PreserveReasoning *bool          `json:"preserve_reasoning"`
	Temperature       patch[float64] `json:"temperature"`
	TopP              patch[float64] `json:"top_p"`
	Instructions      *string        `json:"instructions"`
}

// check refuses a value the agent cannot hold, before anything is written.
func (b agentBody) check() error {
	if b.URL != nil && strings.TrimSpace(*b.URL) != "" {
		if _, ok := config.ValidAddress(strings.TrimSpace(*b.URL)); !ok {
			return errors.New("Address \"" + *b.URL + "\" is not an http or https URL with a host")
		}
	}
	if b.Effort != nil && !llm.ValidEffort(*b.Effort) {
		return errors.New("Effort \"" + *b.Effort + "\" is not " + config.EffortNames)
	}
	if v := b.Temperature.Value; v != nil && (*v < 0 || *v > 2) {
		return errors.New("Temperature is outside 0 to 2: send null to leave it to the model server")
	}
	if v := b.TopP.Value; v != nil && (*v < 0 || *v > 1) {
		return errors.New("Top p is outside 0 to 1: send null to leave it to the model server")
	}
	return nil
}

// apply folds the body over an agent. Everything it does not name is left as
// it was.
func (b agentBody) apply(a *config.Agent) {
	if b.Name != nil {
		a.Name = *b.Name
	}
	if b.URL != nil {
		a.URL = strings.TrimRight(strings.TrimSpace(*b.URL), "/")
	}
	if b.Key != nil {
		a.Key = *b.Key
	}
	if b.Model != nil {
		a.Model = *b.Model
	}
	if b.Effort != nil {
		a.Effort = config.Effort(*b.Effort)
	}
	if b.PreserveReasoning != nil {
		a.PreserveReasoning = *b.PreserveReasoning
	}
	if b.Temperature.Set {
		a.Temperature = b.Temperature.Value
	}
	if b.TopP.Set {
		a.TopP = b.TopP.Value
	}
	if b.Instructions != nil {
		a.Instructions = *b.Instructions
	}
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	s.writeAgents(w, http.StatusOK, s.cfg.Config(), nil)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var body agentBody
	if !readJSON(w, r, &body) {
		return
	}
	if err := body.check(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a := config.DefaultAgent()
	body.apply(&a)
	cfg, stored, err := s.cfg.AddAgent(a)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	s.afterAgentWrite(stored)
	s.writeAgents(w, http.StatusCreated, cfg, &stored)
}

func (s *Server) updateAgent(w http.ResponseWriter, r *http.Request) {
	var body agentBody
	if !readJSON(w, r, &body) {
		return
	}
	if err := body.check(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	before, _ := s.cfg.Config().AgentByID(id)
	cfg, stored, err := s.cfg.UpdateAgent(id, body.apply)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	// An address or a key that moved leaves a reading of something else, and
	// the sessions on this agent holding a window it may no longer have.
	if stored.URL != before.URL || stored.Key != before.Key {
		s.forget(before.URL, before.Key)
		s.engine.Forget(id)
	}
	s.afterAgentWrite(stored)
	s.writeAgents(w, http.StatusOK, cfg, &stored)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	gone, _ := s.cfg.Config().AgentByID(id)
	cfg, err := s.cfg.RemoveAgent(id)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	s.forget(gone.URL, gone.Key)
	s.engine.SetAgent()
	s.writeAgents(w, http.StatusOK, cfg, nil)
}

// selectAgent points the harness at one agent. A half-configured one may be
// selected: what it is missing is reported when a message is sent.
func (s *Server) selectAgent(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.cfg.SelectAgent(r.PathValue("id"))
	if err != nil {
		writeAgentError(w, err)
		return
	}
	s.engine.SetAgent()
	if a, ok := cfg.Agent(); ok {
		go s.Detect(context.Background())
		_ = a
	}
	s.writeAgents(w, http.StatusOK, cfg, nil)
}

// probeAgent reads one address and answers with what it said, whether or not
// it answered well. The dialog draws a bad address inside the form, so a
// failure here is a body rather than a status. An agent id fills in the rest
// of a record when only one field is being tried.
func (s *Server) probeAgent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent string  `json:"agent"`
		URL   *string `json:"url"`
		Key   *string `json:"key"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	a, _ := s.cfg.Config().AgentByID(body.Agent)
	if body.URL != nil {
		a.URL = strings.TrimRight(strings.TrimSpace(*body.URL), "/")
	}
	if body.Key != nil {
		a.Key = *body.Key
	}
	if _, ok := config.ValidAddress(a.URL); a.URL != "" && !ok {
		writeError(w, http.StatusBadRequest, "Address \""+a.URL+"\" is not an http or https URL with a host")
		return
	}

	info, infoErr := s.info(r.Context(), a)
	models, modelsErr := s.models(r.Context(), a, r.URL.Query().Get("refresh") == "1")
	mode := info.Mode
	if mode == "" || infoErr != nil {
		mode = llm.ModeUnknown
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":         string(mode),
		"build":        info.Build,
		"autoload":     info.Autoload,
		"detect_error": errText(infoErr),
		"detect_kind":  string(llm.Kind(infoErr)),
		"models":       toModelJSON(models),
		"models_error": errText(modelsErr),
		"models_kind":  string(llm.Kind(modelsErr)),
	})
}

// afterAgentWrite keeps the client pool to the addresses in use and reads the
// active agent again, since a write may have been what made it usable.
func (s *Server) afterAgentWrite(a config.Agent) {
	s.pool.Retain(s.cfg.AgentKeys())
	if cfg := s.cfg.Config(); cfg.AgentID == a.ID {
		s.engine.SetAgent()
		go s.Detect(context.Background())
	}
}

func (s *Server) writeAgents(w http.ResponseWriter, status int, cfg config.Config, one *config.Agent) {
	out := map[string]any{
		"agents": toAgentsJSON(cfg.Agents),
		"agent":  cfg.AgentID,
	}
	if one != nil {
		out["saved"] = toAgentJSON(*one)
	}
	writeJSON(w, status, out)
}

func writeAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, config.ErrNoSuchAgent):
		writeError(w, http.StatusNotFound, err.Error())
	case strings.Contains(err.Error(), "already exists"):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
