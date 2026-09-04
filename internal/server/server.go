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
	"quecto-harness/internal/llama"
	"quecto-harness/internal/perms"
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
	cfg    *config.Store
	store  *store.Sessions
	engine *agent.Engine
	perms  *perms.Broker
	client *llama.Client
	hub    *Hub

	token   string
	listen  string
	version string
	ui      http.Handler

	mu        sync.Mutex
	modelsAt  time.Time
	modelsVal []llama.Model
	modelsErr error
	infoAt    time.Time
	infoVal   llama.Info
	infoErr   error
}

func New(cfg *config.Store, st *store.Sessions, engine *agent.Engine, pb *perms.Broker,
	cl *llama.Client, hub *Hub, token, listen, version string) *Server {
	return &Server{
		cfg: cfg, store: st, engine: engine, perms: pb, client: cl, hub: hub,
		token: token, listen: listen, version: version, ui: web.Handler(),
	}
}

// SetClient swaps the model client after server.url changed, and clears the
// caches that were filled from the old one.
func (s *Server) SetClient(cl *llama.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.client = cl
	s.modelsAt, s.infoAt = time.Time{}, time.Time{}
	s.modelsVal, s.modelsErr = nil, nil
	s.infoVal, s.infoErr = llama.Info{}, nil
}

func (s *Server) llamaClient() *llama.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/state", s.getState)
	mux.HandleFunc("GET /api/models", s.getModels)
	mux.HandleFunc("GET /api/models/props", s.getModelProps)
	mux.HandleFunc("POST /api/fs/pick", s.postPick)
	mux.HandleFunc("POST /api/projects", s.postProject)
	mux.HandleFunc("DELETE /api/projects", s.deleteProject)
	mux.HandleFunc("GET /api/allowlist", s.getAllowlist)
	mux.HandleFunc("DELETE /api/allowlist", s.deleteAllowlist)
	mux.HandleFunc("GET /api/sessions", s.listSessions)
	mux.HandleFunc("POST /api/sessions", s.createSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.getSession)
	mux.HandleFunc("PUT /api/sessions/{id}", s.rebindSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/message", s.postMessage)
	mux.HandleFunc("POST /api/sessions/{id}/stop", s.postStop)
	mux.HandleFunc("POST /api/sessions/{id}/compact", s.postCompact)
	mux.HandleFunc("POST /api/permissions/{id}", s.postPermission)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("GET /api/events", s.getEvents)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint: check the path")
	})
	mux.HandleFunc("/", s.serveUI)

	return recoverPanic(logRequest(s.checkOrigin(s.auth(mux))))
}

func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.Path, v, debug.Stack())
				writeError(w, http.StatusInternalServerError, "internal error")
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
			writeError(w, http.StatusForbidden, "cross-site requests are refused: open the URL printed by quecto-harness")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
			writeError(w, http.StatusForbidden, "cross-site requests are refused: open the URL printed by quecto-harness")
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
				writeError(w, http.StatusUnauthorized, "not authorized: open the URL printed by quecto-harness")
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
		w.Write([]byte("open the URL printed by quecto-harness, or run quecto-harness open\n"))
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
		writeError(w, http.StatusBadRequest, "the request body is not valid JSON: "+err.Error())
		return false
	}
	return true
}
