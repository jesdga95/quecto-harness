// Package server translates HTTP into calls on the engine, the store, the
// configuration and the model client, and owns the SSE hub.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"quecto-harness/internal/agent"
	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/search"
	"quecto-harness/internal/store"
	"quecto-harness/internal/web"
)

// cacheTTL is how long the model list and the detect result are reused. A
// detect that failed is retried sooner.
const (
	cacheTTL      = 15 * time.Second
	cookieName    = "qh"
	cookieMaxAge  = 30 * 24 * 60 * 60
	pickTimeout   = 120 * time.Second
	requestBudget = 1 << 20
)

type Server struct {
	cfg      *config.Store
	store    *store.Sessions
	files    *store.Files
	uploads  *store.Uploads
	engine   *agent.Engine
	perms    *perms.Broker
	pool     *llm.Pool
	searches *search.Providers
	hub      *Hub

	token   string
	listen  string
	version string
	ui      http.Handler

	mu     sync.Mutex
	probes map[string]*probe
}

// probe is what one address last said about itself and what it holds. Keyed by
// address and key, so two agents at one address share a reading and a changed
// address is a different entry.
type probe struct {
	infoAt    time.Time
	info      llm.Info
	infoErr   error
	modelsAt  time.Time
	models    []llm.Model
	modelsErr error
}

func New(cfg *config.Store, st *store.Sessions, files *store.Files, uploads *store.Uploads,
	engine *agent.Engine, pb *perms.Broker, pool *llm.Pool, searches *search.Providers, hub *Hub,
	token, listen, version string) *Server {
	return &Server{
		cfg: cfg, store: st, files: files, uploads: uploads, engine: engine, perms: pb,
		pool: pool, searches: searches, hub: hub,
		token: token, listen: listen, version: version, ui: web.Handler(),
		probes: map[string]*probe{},
	}
}

// forget drops what was read at one address, and drops the clients no agent
// points at any more. Called after a write that moved an address, a key or a
// protocol, any of which makes what was read no longer about the same thing.
func (s *Server) forget(a config.Agent) {
	s.mu.Lock()
	delete(s.probes, llm.Key(a.URL, a.Key, a.Protocol))
	s.mu.Unlock()
	s.pool.Retain(s.cfg.AgentKeys())
}

// probeFor is the reading for one address, key and protocol, created empty on
// the first look. The caller holds the lock.
func (s *Server) probeFor(a config.Agent) *probe {
	k := llm.Key(a.URL, a.Key, a.Protocol)
	pr, ok := s.probes[k]
	if !ok {
		pr = &probe{}
		s.probes[k] = pr
	}
	return pr
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/models", s.getModels)
	mux.HandleFunc("GET /api/models/props", s.getModelProps)
	mux.HandleFunc("POST /api/fs/pick", s.postPick)
	mux.HandleFunc("POST /api/fs/paste", s.postPaste)
	mux.HandleFunc("GET /api/files/{id}", s.getFile)
	mux.HandleFunc("POST /api/projects", s.postProject)
	mux.HandleFunc("DELETE /api/projects", s.deleteProject)
	mux.HandleFunc("GET /api/allowlist", s.getAllowlist)
	mux.HandleFunc("DELETE /api/allowlist", s.deleteAllowlist)
	mux.HandleFunc("GET /api/sessions", s.listSessions)
	mux.HandleFunc("POST /api/sessions", s.createSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.getSession)
	mux.HandleFunc("GET /api/sessions/{id}/changes", s.getChanges)
	mux.HandleFunc("PUT /api/sessions/{id}", s.rebindSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/message", s.postMessage)
	mux.HandleFunc("POST /api/sessions/{id}/resend", s.postResend)
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.postStop)
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.postCompact)
	mux.HandleFunc("GET /api/agents", s.listAgents)
	mux.HandleFunc("POST /api/agents", s.createAgent)
	mux.HandleFunc("POST /api/agents/probe", s.probeAgent)
	mux.HandleFunc("PUT /api/agents/{id}", s.updateAgent)
	mux.HandleFunc("DELETE /api/agents/{id}", s.deleteAgent)
	mux.HandleFunc("POST /api/agents/{id}/select", s.selectAgent)
	mux.HandleFunc("POST /api/permissions/{id}", s.postPermission)
	mux.HandleFunc("POST /api/questions/{id}", s.postQuestion)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("PUT /api/search", s.putSearch)
	mux.HandleFunc("POST /api/search/probe", s.probeSearch)
	mux.HandleFunc("GET /api/events", s.getEvents)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "No such endpoint: check the path")
	})
	mux.HandleFunc("/", s.serveUI)

	return recoverPanic(logRequest(s.checkOrigin(s.auth(mux))))
}

func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("Panic serving %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
				writeError(w, http.StatusInternalServerError, "Internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
	})
}

// checkOrigin refuses a cross-site request outright, so a page on another
// origin cannot reach a handler even though it could not read the reply.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusMethodNotAllowed, "OPTIONS is not accepted here")
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "Cross-site requests are refused: open the URL printed by quecto-harness")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
			writeError(w, http.StatusForbidden, "Cross-site requests are refused: open the URL printed by quecto-harness")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	_, port, err := net.SplitHostPort(s.listen)
	if err != nil {
		return false
	}
	for _, allowed := range []string{
		"http://localhost:" + port,
		"http://127.0.0.1:" + port,
		"http://" + s.listen,
	} {
		if origin == allowed {
			return true
		}
	}
	return false
}

// auth gates every API route on the cookie or a bearer token, and turns the
// token in the URL into a cookie so it leaves the address bar.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if !s.authorised(r) {
				writeError(w, http.StatusUnauthorized, "Not authorized: open the URL printed by quecto-harness")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if tok := r.URL.Query().Get("token"); tok != "" {
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1 {
				http.SetCookie(w, &http.Cookie{
					Name: cookieName, Value: s.token, Path: "/",
					HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: cookieMaxAge,
				})
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorised(r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil {
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1 {
			return true
		}
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), []byte(s.token)) == 1 {
			return true
		}
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1
	}
	return false
}

// serveUI hands an unauthenticated visitor an instruction rather than a
// browser auth dialog, because they typed the address by hand.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	if !s.authorised(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte("Open the URL printed by quecto-harness, or run quecto-harness open\n"))
		return
	}
	s.ui.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, requestBudget))
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "The request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}
