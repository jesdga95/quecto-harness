package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"quecto-harness/internal/agent"
	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

const testToken = "test-token"

type fixture struct {
	srv      *Server
	handler  http.Handler
	files    *store.Files
	cfg      *config.Store
	hub      *Hub
	engine   *agent.Engine
	perms    *perms.Broker
	project  string
	upstream *httptest.Server

	// The frames are swapped between turns while an earlier turn's request
	// may still be reading them, so they are guarded.
	framesMu   sync.Mutex
	chatFrames []string
	chatQueue  [][]string
	chatBlock  chan struct{}
}

// block makes every chat request hang until the test ends, which is how a
// session is held busy.
func (f *fixture) block(t *testing.T) {
	ch := make(chan struct{})
	f.framesMu.Lock()
	f.chatBlock = ch
	f.framesMu.Unlock()
	t.Cleanup(func() { close(ch) })
}

func (f *fixture) blocked() chan struct{} {
	f.framesMu.Lock()
	defer f.framesMu.Unlock()
	return f.chatBlock
}

func (f *fixture) setFrames(frames ...string) {
	f.framesMu.Lock()
	defer f.framesMu.Unlock()
	f.chatFrames = frames
}

// nextFrames answers one request with its own frames, ahead of whatever
// setFrames left standing. A turn that calls a tool needs the reply that ends
// it to differ from the one that started it.
func (f *fixture) nextFrames(frames ...string) {
	f.framesMu.Lock()
	defer f.framesMu.Unlock()
	f.chatQueue = append(f.chatQueue, frames)
}

func (f *fixture) frames() []string {
	f.framesMu.Lock()
	defer f.framesMu.Unlock()
	if len(f.chatQueue) > 0 {
		next := f.chatQueue[0]
		f.chatQueue = f.chatQueue[1:]
		return next
	}
	return append([]string(nil), f.chatFrames...)
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	p := config.DiscoverPathsIn(home)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if _, err := cfg.AddProject(project); err != nil {
		t.Fatal(err)
	}
	// There is one model and it is the harness's, so a session takes it from
	// here rather than being created against one.
	if _, err := cfg.Update(func(c *config.Config) { c.Model = "Alpha-12B" }); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(project)

	blobs, err := store.OpenFiles(p.Files)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := store.Open(p.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	pastes, err := store.OpenUploads(p.Uploads)
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{cfg: cfg, project: real}
	f.upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			if r.URL.Query().Get("model") != "" && r.URL.Query().Get("autoload") == "" {
				http.Error(w, "model-scoped GET without autoload", 500)
				return
			}
			if r.URL.Query().Get("model") != "" {
				serveTestdata(t, w, "props-model.json")
				return
			}
			serveTestdata(t, w, "props-router.json")
		case "/v1/models":
			serveTestdata(t, w, "v1-models.json")
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, _ := w.(http.Flusher)
			// The naming request stands on its own: one message and no
			// schemas. It answers here so it does not take the frames a test
			// queued for a turn.
			var body struct {
				Messages []json.RawMessage `json:"messages"`
				Tools    []json.RawMessage `json:"tools"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if len(body.Messages) == 1 && len(body.Tools) == 0 {
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"A name"},"finish_reason":"stop"}]}`+"\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				return
			}
			if block := f.blocked(); block != nil {
				if flusher != nil {
					flusher.Flush()
				}
				<-block
				return
			}
			for _, fr := range f.frames() {
				fmt.Fprint(w, fr+"\n\n")
				if flusher != nil {
					flusher.Flush()
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.upstream.Close)

	client := llm.New(f.upstream.URL, "")
	hub := NewHub()
	broker := perms.New(cfg, func(sid string, r perms.Request) { hub.Publish(sid, agent.PermissionEvent(r)) })
	ctx, cancel := context.WithCancel(context.Background())
	engine := agent.NewEngine(ctx, cfg, sessions, blobs, client, broker, hub)
	// The temporary home goes away when the test ends, so nothing the engine
	// started may still be writing sessions into it.
	t.Cleanup(func() {
		cancel()
		engine.Wait(5 * time.Second)
	})

	srv := New(cfg, sessions, blobs, pastes, engine, broker, client, hub, testToken, "127.0.0.1:7331", "0.1.0-test")
	f.srv, f.handler, f.hub, f.engine, f.perms = srv, srv.Handler(), hub, engine, broker
	f.files = blobs
	return f
}

func serveTestdata(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "llm", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (f *fixture) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON (%d): %s", w.Code, w.Body.String())
	}
	return out
}

func TestAuthTokenToCookie(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("GET", "/?token="+testToken, nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cookieName || cookies[0].Value != testToken {
		t.Fatalf("cookie = %+v", cookies)
	}
	if !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags = %+v", cookies[0])
	}
	if cookies[0].Secure {
		t.Error("Secure is set on a plain HTTP loopback cookie, so it would never be stored")
	}
}

func TestAuthCookieAndBearer(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("cookie auth = %d", w.Code)
	}
	if got := f.do(t, "GET", "/api/state", nil).Code; got != 200 {
		t.Errorf("bearer auth = %d", got)
	}
}

func TestAuthMissingIs401(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("GET", "/api/state", nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Not authorized") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestAuthWrongTokenOnRootServesInstructions(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 with an instruction", w.Code)
	}
	if !strings.Contains(w.Body.String(), "quecto-harness open") {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestOriginRejection(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		origin string
		want   int
	}{
		{"", 200},
		{"http://127.0.0.1:7331", 200},
		{"http://localhost:7331", 200},
		{"http://evil.example", http.StatusForbidden},
		{"http://127.0.0.1:9999", http.StatusForbidden},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/api/state", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("origin %q = %d, want %d", tc.origin, w.Code, tc.want)
		}
	}
}

func TestCrossSiteFetchMetadataRejected(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestOptionsIsRefusedAndNoCORSHeader(t *testing.T) {
	f := newFixture(t)
	w := f.do(t, "OPTIONS", "/api/state", nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
	w = f.do(t, "GET", "/api/state", nil)
	if w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("a CORS header was sent")
	}
}

func TestGetState(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "GET", "/api/state", nil))
	if body["mode"] != "router" {
		t.Errorf("mode = %v", body["mode"])
	}
	if body["build"] != "b10812-8b4b3558f" {
		t.Errorf("build = %v", body["build"])
	}
	models, _ := body["models"].([]any)
	if len(models) != 6 {
		t.Errorf("models = %d, want 6", len(models))
	}
	first, _ := models[0].(map[string]any)
	if first["id"] != "Alpha-12B" || first["ctx_hint"] != 131072.0 || first["ctx_source"] != "meta" {
		t.Errorf("first model = %v", first)
	}
	cfg, _ := body["config"].(map[string]any)
	if cfg["effort"] != "medium" || cfg["listen"] != "127.0.0.1:7331" {
		t.Errorf("config = %v", cfg)
	}
	if _, ok := cfg["server_token"]; ok {
		t.Error("the server token was returned")
	}
	if cfg["server_token_set"] != false {
		t.Errorf("server_token_set = %v", cfg["server_token_set"])
	}
	if body["detect_error"] != "" || body["models_error"] != "" {
		t.Errorf("errors = %v %v", body["detect_error"], body["models_error"])
	}
	if body["version"] != "0.1.0-test" {
		t.Errorf("version = %v", body["version"])
	}
}

func TestGetStateSurvivesADeadModelServer(t *testing.T) {
	f := newFixture(t)
	f.srv.SetClient(llm.New("http://127.0.0.1:1", ""))
	body := decode(t, f.do(t, "GET", "/api/state", nil))
	if body["mode"] != "unknown" {
		t.Errorf("mode = %v, want unknown", body["mode"])
	}
	if body["detect_error"] == "" || body["models_error"] == "" {
		t.Error("the failure was not reported")
	}
	if _, ok := body["config"]; !ok {
		t.Error("the rest of the payload was dropped")
	}
}

func TestGetStateWithAnOpenAICompatibleServer(t *testing.T) {
	f := newFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		serveTestdata(t, w, "openai-models.json")
	}))
	defer upstream.Close()
	f.srv.SetClient(llm.New(upstream.URL, ""))

	body := decode(t, f.do(t, "GET", "/api/state", nil))
	if body["mode"] != "openai" {
		t.Errorf("mode = %v, want openai", body["mode"])
	}
	if body["detect_error"] != "" || body["models_error"] != "" {
		t.Errorf("a server with no properties endpoint was reported as broken: %v %v",
			body["detect_error"], body["models_error"])
	}
	models, _ := body["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models = %d, want the one the list names", len(models))
	}
	first, _ := models[0].(map[string]any)
	if first["id"] != "Delta3.8-27B" || first["ctx_hint"] != 240000.0 || first["ctx_source"] != "models" {
		t.Errorf("first model = %v", first)
	}
}

func TestGetModelsRefresh(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "GET", "/api/models?refresh=1", nil))
	models, _ := body["models"].([]any)
	if len(models) != 6 {
		t.Errorf("models = %d", len(models))
	}
}

func TestGetModelProps(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "GET", "/api/models/props?id=Alpha-12B", nil))
	if body["n_ctx"] != 131072.0 || body["total_slots"] != 2.0 {
		t.Errorf("props = %v", body)
	}
	caps, _ := body["caps"].(map[string]any)
	if caps["known"] != true || caps["tool_calls"] != true || caps["reasoning_effort"] != false {
		t.Errorf("caps = %v", caps)
	}
	if w := f.do(t, "GET", "/api/models/props", nil); w.Code != 400 {
		t.Errorf("missing id = %d", w.Code)
	}
}

func TestGetModelPropsUnloadedIs409(t *testing.T) {
	f := newFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"model is not loaded"}}`)
	}))
	defer upstream.Close()
	f.srv.SetClient(llm.New(upstream.URL, ""))
	w := f.do(t, "GET", "/api/models/props?id=Beta%2030B%20Q5", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "loads on the first message") {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestProjectRoutes(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)

	body := decode(t, f.do(t, "POST", "/api/projects", map[string]string{"path": dir}))
	projects, _ := body["projects"].([]any)
	if len(projects) != 2 {
		t.Fatalf("projects = %v", projects)
	}
	if w := f.do(t, "POST", "/api/projects", map[string]string{"path": dir}); w.Code != http.StatusConflict {
		t.Errorf("duplicate = %d, want 409", w.Code)
	}
	if w := f.do(t, "POST", "/api/projects", map[string]string{"path": "/no/such/place"}); w.Code != 400 {
		t.Errorf("missing directory = %d, want 400", w.Code)
	}
	body = decode(t, f.do(t, "DELETE", "/api/projects", map[string]string{"path": real}))
	projects, _ = body["projects"].([]any)
	if len(projects) != 1 {
		t.Errorf("after delete = %v", projects)
	}
}

func TestAllowlistRoutes(t *testing.T) {
	f := newFixture(t)
	f.cfg.AddAllow(f.project, "go test")
	f.cfg.AddAllow(f.project, "ls")

	body := decode(t, f.do(t, "GET", "/api/allowlist?project="+f.project, nil))
	prefixes, _ := body["prefixes"].([]any)
	if len(prefixes) != 2 || prefixes[0] != "go test" {
		t.Errorf("prefixes = %v, want longest first", prefixes)
	}

	body = decode(t, f.do(t, "GET", "/api/allowlist?project=/nowhere", nil))
	if got, _ := body["prefixes"].([]any); len(got) != 0 {
		t.Errorf("unknown project = %v, want an empty array", got)
	}

	body = decode(t, f.do(t, "DELETE", "/api/allowlist", map[string]string{"project": f.project, "prefix": "go test"}))
	if got, _ := body["prefixes"].([]any); len(got) != 1 {
		t.Errorf("after delete = %v", got)
	}
	w := f.do(t, "DELETE", "/api/allowlist", map[string]string{"project": f.project, "prefix": "not there"})
	if w.Code != 200 {
		t.Errorf("removing an absent prefix = %d, want 200", w.Code)
	}
}

func TestSessionLifecycle(t *testing.T) {
	f := newFixture(t)
	created := decode(t, f.do(t, "POST", "/api/sessions",
		map[string]string{"project": f.project, "model": "Alpha-12B"}))
	id, _ := created["id"].(string)
	// Creating one reads no model, so it is idle from the start.
	if id == "" || created["state"] != "idle" {
		t.Fatalf("created = %v", created)
	}

	list := decode(t, f.do(t, "GET", "/api/sessions?project="+f.project, nil))
	if got, _ := list["sessions"].([]any); len(got) != 1 {
		t.Errorf("sessions = %v", got)
	}
	if got := decode(t, f.do(t, "GET", "/api/sessions", nil)); len(got["sessions"].([]any)) != 1 {
		t.Error("listing without a project did not return the session")
	}

	// Opening a session sends nothing to the model server, so it comes back
	// ready to take a message.
	snap := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
	if snap["state"] != "idle" || snap["n_ctx"] == 0.0 {
		t.Errorf("snapshot = %v", snap)
	}
	if _, ok := snap["last_seq"]; !ok {
		t.Error("snapshot has no last_seq")
	}
	if _, ok := snap["pending"].([]any); !ok {
		t.Error("snapshot has no pending array")
	}

	if w := f.do(t, "DELETE", "/api/sessions/"+id, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete = %d, want 204", w.Code)
	}
	if w := f.do(t, "GET", "/api/sessions/"+id, nil); w.Code != 404 {
		t.Errorf("get after delete = %d", w.Code)
	}
	if w := f.do(t, "DELETE", "/api/sessions/sess_aaaaaaaaaaaaaaaa", nil); w.Code != 404 {
		t.Errorf("delete unknown = %d", w.Code)
	}
}

func TestCreateSessionRejectsUnknownProject(t *testing.T) {
	f := newFixture(t)
	w := f.do(t, "POST", "/api/sessions", map[string]string{"project": "/nope"})
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// The model is chosen for the harness, and every session takes it.
func TestModelRouteRepointsEverySession(t *testing.T) {
	f := newFixture(t)
	first := f.newSession(t)
	second := f.newSession(t)

	w := f.do(t, "PUT", "/api/config", map[string]any{"model": "Delta3.8-27B Q5"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if decode(t, w)["model"] != "Delta3.8-27B Q5" {
		t.Errorf("config = %v", decode(t, w))
	}
	for _, id := range []string{first, second} {
		f.waitIdle(t, id)
		doc := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
		if doc["model"] != "Delta3.8-27B Q5" {
			t.Errorf("session %s = %v", id, doc["model"])
		}
	}
}

func TestMessageStopAndCompact(t *testing.T) {
	f := newFixture(t)
	f.setFrames(
		`data: {"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`data: [DONE]`,
	)
	id := f.newSession(t)

	w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "hi"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("message = %d: %s", w.Code, w.Body.String())
	}
	if decode(t, w)["state"] != "preflight" {
		t.Errorf("state = %v", decode(t, w))
	}
	f.waitIdle(t, id)

	if w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "  "}); w.Code != 400 {
		t.Errorf("empty text = %d", w.Code)
	}
	if w := f.do(t, "POST", "/api/sessions/"+id+"/stop", nil); w.Code != 200 || decode(t, w)["state"] != "idle" {
		t.Errorf("stop on idle = %d %s", w.Code, w.Body.String())
	}

	f.setFrames(
		`data: {"choices":[{"delta":{"content":"a summary"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	if w := f.do(t, "POST", "/api/sessions/"+id+"/compact", nil); w.Code != http.StatusAccepted {
		t.Errorf("compact = %d: %s", w.Code, w.Body.String())
	}
	f.waitIdle(t, id)
	snap := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
	if snap["compactions"] != 1.0 {
		t.Errorf("compactions = %v", snap["compactions"])
	}
}

func TestBusySession(t *testing.T) {
	f := newFixture(t)
	id := f.newSession(t)
	f.block(t)

	f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "hi"})
	// Waiting for the request to reach the model, rather than for any busy
	// state: a message queued before it gets picked up in the same turn.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && f.engine.State(id) != agent.StateStreaming {
		time.Sleep(time.Millisecond)
	}
	if f.engine.State(id) != agent.StateStreaming {
		t.Fatalf("the turn is %s, want it waiting on the model", f.engine.State(id))
	}
	// A message waits for the turn rather than being refused, and the snapshot
	// carries it so a tab that refetches still draws it.
	w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "again"})
	if w.Code != http.StatusAccepted || decode(t, w)["queued"] != true {
		t.Errorf("message while busy = %d: %s", w.Code, w.Body.String())
	}
	steer, _ := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))["steer"].(map[string]any)
	if steer == nil || steer["text"] != "again" {
		t.Errorf("snapshot steer = %v, want the queued message", steer)
	}
	if w := f.do(t, "POST", "/api/sessions/"+id+"/compact", nil); w.Code != http.StatusConflict {
		t.Errorf("compact while busy = %d, want 409", w.Code)
	}
	if w := f.do(t, "DELETE", "/api/sessions/"+id, nil); w.Code != http.StatusConflict {
		t.Errorf("delete while busy = %d", w.Code)
	}
	// The stop delivers the queued message, which starts a turn of its own.
	// The entry is the signal that the delivery happened, since the session is
	// busy either way until it does.
	f.do(t, "POST", "/api/sessions/"+id+"/stop", nil)
	deadline = time.Now().Add(5 * time.Second)
	delivered := false
	for time.Now().Before(deadline) && !delivered {
		entries, _ := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))["entries"].([]any)
		for _, e := range entries {
			row, _ := e.(map[string]any)
			if row["content"] == "again" {
				delivered = true
			}
		}
		time.Sleep(time.Millisecond)
	}
	if !delivered {
		t.Fatal("the stop did not deliver the queued message")
	}
	f.do(t, "POST", "/api/sessions/"+id+"/stop", nil)
	f.waitIdle(t, id)
}

// An unreadable policy is rejected rather than stored.
func TestPutConfigRejectsAnUnknownMode(t *testing.T) {
	f := newFixture(t)
	res := f.do(t, "PUT", "/api/config", map[string]any{"permissions": "everything"})
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.Code)
	}
	if f.cfg.Config().Mode != config.ModeAsk {
		t.Errorf("the store changed anyway: %v", f.cfg.Config().Mode)
	}
}

func TestPermissionRoute(t *testing.T) {
	f := newFixture(t)
	go f.perms.Ask(context.Background(), perms.Request{
		ID: "perm_test000000", SessionID: "s1", Project: f.project,
		Tool: "run_shell", Command: "go test ./...", Prefix: "go test",
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := f.perms.Lookup("perm_test000000"); ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	w := f.do(t, "POST", "/api/permissions/perm_test000000",
		map[string]string{"decision": "always", "prefix": "rm -rf"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "does not match the command") {
		t.Errorf("mismatched prefix = %d %s", w.Code, w.Body.String())
	}

	w = f.do(t, "POST", "/api/permissions/perm_test000000",
		map[string]string{"decision": "always", "prefix": "go test"})
	if w.Code != 200 {
		t.Fatalf("decision = %d %s", w.Code, w.Body.String())
	}
	if _, ok := f.cfg.AllowMatch(f.project, "go test ./..."); !ok {
		t.Error("the rule was not stored")
	}

	w = f.do(t, "POST", "/api/permissions/perm_test000000", map[string]string{"decision": "once"})
	if w.Code != 404 {
		t.Errorf("second click = %d, want 404", w.Code)
	}
	w = f.do(t, "POST", "/api/permissions/perm_x", map[string]string{"decision": "nonsense"})
	if w.Code != 400 {
		t.Errorf("bad decision = %d", w.Code)
	}
}

// TestChangesRoute drives a write through a turn and reads the panel's route,
// which is the whole path from the tool keeping a copy to the browser getting
// a diff.
func TestChangesRoute(t *testing.T) {
	f := newFixture(t)
	f.cfg.Update(func(c *config.Config) { c.Mode = config.ModeFull })

	target := filepath.Join(f.project, "hub.go")
	if err := os.WriteFile(target, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := f.newSession(t)

	// A session with no writes has an empty list, not an error.
	body := decode(t, f.do(t, "GET", "/api/sessions/"+id+"/changes", nil))
	if files, _ := body["files"].([]any); len(files) != 0 {
		t.Fatalf("an untouched session lists %v", body["files"])
	}

	args := `{"path":"hub.go","content":"one\nTWO\nthree\n"}`
	// The first request asks for the write; everything after it, the title
	// request included, is a plain reply.
	f.nextFrames(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write_file","arguments":`+strconv.Quote(args)+`}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	f.setFrames(
		`data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	if w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "edit it"}); w.Code != http.StatusAccepted {
		t.Fatalf("message = %d: %s", w.Code, w.Body.String())
	}
	f.waitIdle(t, id)

	if written, err := os.ReadFile(target); err != nil || string(written) != "one\nTWO\nthree\n" {
		t.Fatalf("hub.go = %q %v", written, err)
	}

	body = decode(t, f.do(t, "GET", "/api/sessions/"+id+"/changes", nil))
	if body["added"] != 1.0 || body["removed"] != 1.0 {
		t.Errorf("totals = +%v -%v, want +1 -1", body["added"], body["removed"])
	}
	files, _ := body["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %v", body["files"])
	}
	file, _ := files[0].(map[string]any)
	if file["path"] != "hub.go" || file["status"] != "changed" {
		t.Fatalf("file = %v", file)
	}
	hunks, _ := file["hunks"].([]any)
	if len(hunks) != 1 {
		t.Fatalf("hunks = %v", file["hunks"])
	}
	hunk, _ := hunks[0].(map[string]any)
	lines, _ := hunk["lines"].([]any)
	var removed, added string
	for _, l := range lines {
		line, _ := l.(map[string]any)
		switch line["kind"] {
		case "removed":
			removed, _ = line["text"].(string)
		case "added":
			added, _ = line["text"].(string)
		}
	}
	if removed != "two" || added != "TWO" {
		t.Errorf("the hunk turns %q into %q", removed, added)
	}
}

func TestPutConfig(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "PUT", "/api/config", map[string]any{
		"effort": "auto", "permissions": "full", "compact_at": 0.5, "model": "Alpha-12B",
	}))
	if body["effort"] != "auto" || body["permissions"] != "full" || body["compact_at"] != 0.5 {
		t.Errorf("config = %v", body)
	}
	if f.cfg.Config().Mode != config.ModeFull {
		t.Errorf("the store was not updated: mode = %v", f.cfg.Config().Mode)
	}
	if f.cfg.Config().Effort != config.EffortAuto {
		t.Errorf("the store was not updated: %v", f.cfg.Config().Effort)
	}
	if w := f.do(t, "PUT", "/api/config", map[string]any{"effort": "extreme"}); w.Code != 400 {
		t.Errorf("bad effort = %d", w.Code)
	}
	body = decode(t, f.do(t, "PUT", "/api/config", map[string]any{"listen": "127.0.0.1:9999"}))
	if body["note"] != "Listen changes take effect on restart" {
		t.Errorf("note = %v", body["note"])
	}
}

func TestPutConfigServerURLResetsTheCache(t *testing.T) {
	f := newFixture(t)
	f.do(t, "GET", "/api/state", nil)
	w := f.do(t, "PUT", "/api/config", map[string]any{"server": map[string]any{"url": "http://127.0.0.1:1"}})
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := decode(t, f.do(t, "GET", "/api/state", nil))
	if body["detect_error"] == "" {
		t.Error("the detect cache was not invalidated")
	}
}

func TestSSEReplayAndResync(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 3; i++ {
		f.hub.Publish("s1", agent.Event{Type: "delta", Fields: map[string]any{"text": fmt.Sprint(i)}})
	}

	got := f.readStream(t, "/api/events?session=s1&from=1", 2)
	if len(got) != 2 || got[0]["seq"] != 2.0 || got[1]["text"] != "2" {
		t.Errorf("replay from ?from= = %v", got)
	}

	req := httptest.NewRequest("GET", "/api/events?session=s1", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Last-Event-ID", "2")
	got = f.readStreamReq(t, req, 1)
	if len(got) != 1 || got[0]["seq"] != 3.0 {
		t.Errorf("replay from Last-Event-ID = %v", got)
	}

	// Roll the ring past the requested sequence.
	for i := 0; i < ringSize+10; i++ {
		f.hub.Publish("s1", agent.Event{Type: "delta", Fields: map[string]any{"text": "x"}})
	}
	got = f.readStream(t, "/api/events?session=s1&from=1", 1)
	if len(got) != 1 || got[0]["type"] != "resync" {
		t.Errorf("rolled ring = %v, want a resync", got)
	}
}

func TestSlowSubscriberGetsResyncAndStaysAttached(t *testing.T) {
	f := newFixture(t)
	ch, cancel, _ := f.hub.Subscribe("s2", 0)
	defer cancel()
	for i := 0; i < subscriberQ+50; i++ {
		f.hub.Publish("s2", agent.Event{Type: "delta", Fields: map[string]any{"text": "x"}})
	}
	select {
	case fr, open := <-ch:
		if !open {
			t.Fatal("a slow subscriber was disconnected")
		}
		if fr.Type != resyncEvType {
			t.Errorf("first frame after falling behind = %q, want resync", fr.Type)
		}
	default:
		t.Fatal("a slow subscriber was never told to resync")
	}
	select {
	case fr := <-ch:
		t.Errorf("the queue still holds %q, it should have been cleared", fr.Type)
	default:
	}
}

func TestSSENeedsASession(t *testing.T) {
	f := newFixture(t)
	if w := f.do(t, "GET", "/api/events", nil); w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestToolEventsCarryTheIndex(t *testing.T) {
	f := newFixture(t)
	f.hub.Publish("s3", agent.Event{Type: "tool_call", Fields: map[string]any{
		"index": 0, "id": "call_0", "name": "run_shell", "arguments": "{"}})
	f.hub.Publish("s3", agent.Event{Type: "tool_output", Fields: map[string]any{
		"index": 0, "id": "call_0", "name": "run_shell", "output": "x", "exit_code": 0, "truncated": false}})
	got := f.readStream(t, "/api/events?session=s3&from=0", 2)
	for _, e := range got {
		if _, ok := e["index"]; !ok {
			t.Errorf("%v has no index", e)
		}
	}
}

func TestUnknownAPIRouteIs404(t *testing.T) {
	f := newFixture(t)
	if w := f.do(t, "GET", "/api/nothing", nil); w.Code != 404 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestPanicIsContained(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/boom", func(w http.ResponseWriter, r *http.Request) { panic("boom") })
	h := recoverPanic(mux)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/boom", nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), "Internal error") {
		t.Errorf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestFolderPick(t *testing.T) {
	f := newFixture(t)
	original := folderPicker
	t.Cleanup(func() { folderPicker = original })

	folderPicker = func(context.Context) (string, error) { return "/Users/user/src/thing", nil }
	body := decode(t, f.do(t, "POST", "/api/fs/pick", nil))
	if body["path"] != "/Users/user/src/thing" {
		t.Errorf("path = %v", body["path"])
	}

	folderPicker = func(context.Context) (string, error) { return "", nil }
	w := f.do(t, "POST", "/api/fs/pick", nil)
	if w.Code != 200 {
		t.Fatalf("dismissal = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"path":null`) {
		t.Errorf("a dismissed dialog must answer null: %s", w.Body.String())
	}

	folderPicker = func(context.Context) (string, error) { return "", errNoDialog }
	w = f.do(t, "POST", "/api/fs/pick", nil)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", w.Code)
	}
	if !strings.Contains(w.Body.String(), "install zenity, or type the path") {
		t.Errorf("body = %s", w.Body.String())
	}
}

// TestPasteStoresTheFile asserts that bytes with no path of their own are
// written under a name a person can read, and that the path they come back as
// is one a message can attach.
func TestPasteStoresTheFile(t *testing.T) {
	f := newFixture(t)

	data := base64.StdEncoding.EncodeToString([]byte("pasted words\n"))
	body := decode(t, f.do(t, "POST", "/api/fs/paste",
		map[string]any{"name": "Pasted text.txt", "data": data}))
	path, _ := body["path"].(string)
	if filepath.Base(path) != "Pasted text.txt" {
		t.Fatalf("path = %v", body["path"])
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "pasted words\n" {
		t.Fatalf("stored file = %q %v", raw, err)
	}

	// A name from a browser is not a path, so it cannot reach out of the
	// uploads directory.
	body = decode(t, f.do(t, "POST", "/api/fs/paste",
		map[string]any{"name": "../../escape.txt", "data": data}))
	if got, _ := body["path"].(string); filepath.Base(got) != "escape.txt" ||
		filepath.Dir(filepath.Dir(got)) != filepath.Dir(filepath.Dir(path)) {
		t.Errorf("escaped the uploads directory: %v", body["path"])
	}

	// Text is inlined into the prompt, so it is refused here rather than when
	// the message that carries it is sent.
	big := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), tools.MaxAttachment+1))
	w := f.do(t, "POST", "/api/fs/paste", map[string]any{"name": "Huge.txt", "data": big})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized text = %d, want 413", w.Code)
	}
	if !strings.Contains(w.Body.String(), "the limit is 256 kb") {
		t.Errorf("body = %s", w.Body.String())
	}

	w = f.do(t, "POST", "/api/fs/paste", map[string]any{"name": "Empty.txt", "data": ""})
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty paste = %d, want 400", w.Code)
	}
	w = f.do(t, "POST", "/api/fs/paste", map[string]any{"name": "Bad.png", "data": "not base64!"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("undecodable paste = %d, want 400", w.Code)
	}
}

func TestNoGUISessionIsNamed(t *testing.T) {
	for _, stderr := range []string{
		"Gtk-WARNING: cannot open display: :0",
		"execution error: Not allowed to send Apple events to System Events. (-1743)",
		"No protocol specified",
	} {
		if !noGUISession(stderr) {
			t.Errorf("%q was not recognised as a session without a desktop", stderr)
		}
	}
	if noGUISession("user cancelled") {
		t.Error("a cancellation was reported as a missing desktop")
	}
}

func TestEmbedFallbackPage(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<") && !strings.Contains(body, "make web") {
		t.Errorf("neither the UI nor the fallback page was served: %q", body)
	}
}

// The resend route names the message by sequence number, so a tab holding an
// older transcript is refused rather than editing a message it is not showing.
func TestResendRoute(t *testing.T) {
	f := newFixture(t)
	f.setFrames(
		`data: {"choices":[{"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	id := f.newSession(t)

	w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "hi"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("message = %d: %s", w.Code, w.Body.String())
	}
	seq, ok := decode(t, w)["seq"].(float64)
	if !ok || seq == 0 {
		t.Fatalf("the message did not report its sequence number: %s", w.Body.String())
	}
	f.waitIdle(t, id)

	if w := f.do(t, "POST", "/api/sessions/"+id+"/resend",
		map[string]any{"seq": seq + 100, "text": "ho"}); w.Code != http.StatusConflict {
		t.Errorf("resend of a message that is not the last = %d, want 409", w.Code)
	}

	// The transcript every tab holds ends in entries the rewrite discards, so
	// the stream carries the resync that sends them back for a fresh one.
	mark, _ := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))["last_seq"].(float64)

	w = f.do(t, "POST", "/api/sessions/"+id+"/resend", map[string]any{"seq": seq, "text": "ho"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("resend = %d: %s", w.Code, w.Body.String())
	}
	f.waitIdle(t, id)

	// The title the model wrote for the first turn can land between the mark
	// and the rewrite, so the resync is looked for rather than counted to.
	resynced := false
	for _, fr := range f.readStream(t, fmt.Sprintf("/api/events?session=%s&from=%d", id, int64(mark)), 6) {
		if fr["type"] == "resync" {
			resynced = true
		}
	}
	if !resynced {
		t.Error("the rewrite did not tell the attached tabs to refetch")
	}

	entries, _ := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want the one turn the rewrite left", len(entries))
	}
	first, _ := entries[0].(map[string]any)
	if first["content"] != "ho" || first["edited"] != true {
		t.Errorf("first entry = %v, want the rewritten message", first)
	}
}

func (f *fixture) newSession(t *testing.T) string {
	t.Helper()
	body := decode(t, f.do(t, "POST", "/api/sessions",
		map[string]string{"project": f.project, "model": "Alpha-12B"}))
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("no session id: %v", body)
	}
	f.waitIdle(t, id)
	return id
}

func (f *fixture) waitIdle(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f.engine.State(id) == agent.StateIdle {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("session %s stayed in %s", id, f.engine.State(id))
}

func (f *fixture) readStream(t *testing.T, path string, want int) []map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	return f.readStreamReq(t, req, want)
}

// readStreamReq reads the replay an event source receives, then cancels the
// request so the handler returns.
func (f *fixture) readStreamReq(t *testing.T, req *http.Request, want int) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	pr, pw := newPipeRecorder()
	done := make(chan struct{})
	go func() {
		f.handler.ServeHTTP(pw, req)
		close(done)
	}()

	var out []map[string]any
	sc := bufio.NewScanner(pr)
	for len(out) < want && sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
			t.Fatalf("event is not JSON: %q", line)
		}
		out = append(out, v)
	}
	cancel()
	<-done
	return out
}

// pipeRecorder is a ResponseWriter whose body can be read while the handler
// is still writing, which is what an event source test needs.
type pipeRecorder struct {
	header http.Header
	w      *os.File
}

func newPipeRecorder() (*os.File, *pipeRecorder) {
	r, w, err := os.Pipe()
	if err != nil {
		panic("open a pipe for the event stream: " + err.Error())
	}
	return r, &pipeRecorder{header: http.Header{}, w: w}
}

func (p *pipeRecorder) Header() http.Header         { return p.header }
func (p *pipeRecorder) WriteHeader(int)             {}
func (p *pipeRecorder) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeRecorder) Flush()                      {}

// A changed server address has to reach the turn loop too, not just the
// routes that list models.
func TestConfigRepointsEngine(t *testing.T) {
	f := newFixture(t)
	before := f.srv.engine.Llama()
	w := f.do(t, "PUT", "/api/config", map[string]any{
		"server": map[string]string{"url": "http://192.0.2.9:9999", "token": "k"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	after := f.srv.engine.Llama()
	if after == before {
		t.Fatal("the engine kept the old client, so a turn would still use the old address")
	}
	cl, ok := after.(*llm.Client)
	if !ok {
		t.Fatalf("engine backend = %T, want a model client", after)
	}
	if cl.BaseURL != "http://192.0.2.9:9999" {
		t.Errorf("engine BaseURL = %q, want the address just saved", cl.BaseURL)
	}
}

// TestFileRouteServesAnAttachment asserts that an image is served back by its
// id, typed from its own bytes, and that nothing else names a file.
func TestFileRouteServesAnAttachment(t *testing.T) {
	f := newFixture(t)
	src := filepath.Join(t.TempDir(), "shot.png")
	png := []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
	}
	if err := os.WriteFile(src, png, 0o600); err != nil {
		t.Fatal(err)
	}
	id, _, err := f.files.Put(src)
	if err != nil {
		t.Fatal(err)
	}

	w := f.do(t, "GET", "/api/files/"+id, nil)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("content type = %q", got)
	}
	if !bytes.Equal(w.Body.Bytes(), png) {
		t.Errorf("body = %d bytes, want %d", w.Body.Len(), len(png))
	}

	if w := f.do(t, "GET", "/api/files/file_nope", nil); w.Code != 404 {
		t.Errorf("an id that was never minted returned %d", w.Code)
	}
}
