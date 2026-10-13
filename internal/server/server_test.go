package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"quecto-harness/internal/agent"
	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/perms"
	"quecto-harness/internal/search"
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
	agent    string
	upstream *httptest.Server

	// The frames are swapped between turns while an earlier turn's request
	// may still be reading them, so they are guarded.
	framesMu   sync.Mutex
	chatFrames []string
	chatQueue  [][]string
	chatBlock  chan struct{}
	// chatLead is sent by a blocked request before it hangs, which holds a
	// reply part way through its stream.
	chatLead []string
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

// holdAfter makes every chat request send lead and then hang until the test
// ends.
func (f *fixture) holdAfter(t *testing.T, lead ...string) {
	f.framesMu.Lock()
	f.chatLead = lead
	f.framesMu.Unlock()
	f.block(t)
}

func (f *fixture) blocked() chan struct{} {
	f.framesMu.Lock()
	defer f.framesMu.Unlock()
	return f.chatBlock
}

func (f *fixture) lead() []string {
	f.framesMu.Lock()
	defer f.framesMu.Unlock()
	return append([]string(nil), f.chatLead...)
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
			// The measurement and the naming request are answered here, so
			// neither takes the frames a test queued for a turn.
			var body struct {
				Messages  []json.RawMessage `json:"messages"`
				Tools     []json.RawMessage `json:"tools"`
				MaxTokens int               `json:"max_tokens"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			// A completion capped at one token is the context measurement,
			// which reads the usage report and nothing else.
			if body.MaxTokens == 1 {
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"."},"finish_reason":"length"}]}`+"\n\n")
				fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":1,"total_tokens":21}}`+"\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				return
			}
			// The naming request stands on its own: one message and no schemas.
			if len(body.Messages) == 1 && len(body.Tools) == 0 {
				fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"A name"},"finish_reason":"stop"}]}`+"\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				return
			}
			if block := f.blocked(); block != nil {
				for _, fr := range f.lead() {
					fmt.Fprint(w, fr+"\n\n")
				}
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

	// One agent answers for the whole harness, so a session takes its model
	// from here rather than being created against one.
	_, ag, err := cfg.AddAgent(config.Agent{
		Name: "Local", Enabled: true, URL: f.upstream.URL, Model: "Alpha-12B",
		Effort: config.EffortMedium, PreserveReasoning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.agent = ag.ID

	pool := llm.NewPool()
	searches := search.NewProviders()
	hub := NewHub()
	broker := perms.New(cfg, func(sid string, r perms.Request) { hub.Publish(sid, agent.PermissionEvent(r)) })
	ctx, cancel := context.WithCancel(context.Background())
	engine := agent.NewEngine(ctx, cfg, sessions, blobs, agent.Clients{Pool: pool}, searches, broker, hub)
	// The temporary home goes away when the test ends, so nothing the engine
	// started may still be writing sessions into it.
	t.Cleanup(func() {
		cancel()
		engine.Wait(5 * time.Second)
	})

	srv := New(cfg, sessions, blobs, pastes, engine, broker, pool, searches, hub, testToken, "127.0.0.1:7331", "0.1.0-test")
	f.srv, f.handler, f.hub, f.engine, f.perms = srv, srv.Handler(), hub, engine, broker
	f.files = blobs
	return f
}

// pointAt moves the active agent to another address, the way the agents route
// does, so a test can put a different server behind the harness.
func (f *fixture) pointAt(t *testing.T, url string) {
	t.Helper()
	f.pointAtVia(t, url, "llamacpp")
}

// pointAtVia moves the fixture's agent to an address and says what it speaks.
func (f *fixture) pointAtVia(t *testing.T, url, protocol string) {
	t.Helper()
	w := f.do(t, "PUT", "/api/agents/"+f.agent,
		map[string]any{"url": url, "protocol": protocol})
	if w.Code != http.StatusOK {
		t.Fatalf("point at %s = %d %s", url, w.Code, w.Body.String())
	}
}

// addAgent creates an agent through the route and answers with its id.
func (f *fixture) addAgent(t *testing.T, body map[string]any) string {
	t.Helper()
	w := f.do(t, "POST", "/api/agents", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create agent = %d %s", w.Code, w.Body.String())
	}
	id, _ := f.saved(t, w)["id"].(string)
	if id == "" {
		t.Fatalf("create agent returned no id: %s", w.Body.String())
	}
	return id
}

// saved is the agent a write answered with.
func (f *fixture) saved(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out, _ := decode(t, w)["saved"].(map[string]any)
	if out == nil {
		t.Fatalf("no saved agent in %s", w.Body.String())
	}
	return out
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
	req := newRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	return w
}

// newRequest is a request as a browser on the printed URL sends it. The
// default Host of a test request is a domain name, which the server refuses.
func newRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = "127.0.0.1:7331"
	return req
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
	req := newRequest("GET", "/?token="+testToken, nil)
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
	req := newRequest("GET", "/api/state", nil)
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
	req := newRequest("GET", "/api/state", nil)
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
	req := newRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 with an instruction", w.Code)
	}
	if !strings.Contains(w.Body.String(), "quecto-harness open") {
		t.Errorf("body = %q", w.Body.String())
	}
}

// A tab icon is served without the cookie, since some browsers fetch it without
// one. Every other page still answers with the instruction.
func TestTabIconNeedsNoCookie(t *testing.T) {
	f := newFixture(t)
	f.srv.ui = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write([]byte("<svg/>"))
	})
	for _, path := range []string{"/favicon.svg", "/favicon.png", "/favicon.ico"} {
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, newRequest("GET", path, nil))
		if w.Body.String() != "<svg/>" {
			t.Errorf("%s without a cookie = %q, want the icon", path, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, newRequest("GET", "/index.html", nil))
	if !strings.Contains(w.Body.String(), "quecto-harness open") {
		t.Errorf("/index.html without a cookie = %q, want the instruction", w.Body.String())
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
		req := newRequest("GET", "/api/state", nil)
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

// A Host naming a domain other than the one listened on is a rebound name,
// refused before the token is looked at.
func TestHostRejection(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		host string
		want int
	}{
		{"127.0.0.1:7331", 200},
		{"localhost:7331", 200},
		{"[::1]:7331", 200},
		{"192.168.1.20:7331", 200},
		{"evil.example:7331", http.StatusForbidden},
		{"attacker.test", http.StatusForbidden},
	}
	for _, tc := range cases {
		req := newRequest("GET", "/api/state", nil)
		req.Host = tc.host
		req.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("host %q = %d, want %d", tc.host, w.Code, tc.want)
		}
	}
}

func TestCrossSiteFetchMetadataRejected(t *testing.T) {
	f := newFixture(t)
	req := newRequest("GET", "/api/state", nil)
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
	if cfg["listen"] != "127.0.0.1:7331" || cfg["agent"] != f.agent {
		t.Errorf("config = %v", cfg)
	}
	agents, _ := body["agents"].([]any)
	if len(agents) != 1 {
		t.Fatalf("agents = %v", agents)
	}
	one, _ := agents[0].(map[string]any)
	if one["name"] != "Local" || one["model"] != "Alpha-12B" || one["effort"] != "medium" {
		t.Errorf("agent = %v", one)
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
	f.pointAt(t, "http://127.0.0.1:1")
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
	f.pointAtVia(t, upstream.URL, "chat")

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
	f.pointAt(t, upstream.URL)
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

// TestProjectReorderRoute asserts that a whole new order is stored and that an
// order the project list does not hold is refused.
func TestProjectReorderRoute(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	real, _ := filepath.EvalSymlinks(dir)
	if w := f.do(t, "POST", "/api/projects", map[string]string{"path": dir}); w.Code != http.StatusOK {
		t.Fatalf("add project = %d %s", w.Code, w.Body.String())
	}

	if w := f.do(t, "PUT", "/api/projects", map[string]any{"paths": []string{real}}); w.Code != http.StatusBadRequest {
		t.Errorf("an incomplete order = %d, want 400", w.Code)
	}
	if w := f.do(t, "PUT", "/api/projects", map[string]any{"paths": []string{real, real}}); w.Code != http.StatusBadRequest {
		t.Errorf("a repeated path = %d, want 400", w.Code)
	}

	w := f.do(t, "PUT", "/api/projects", map[string]any{"paths": []string{real, f.project}})
	if w.Code != http.StatusOK {
		t.Fatalf("reorder = %d %s", w.Code, w.Body.String())
	}
	if got := stringList(t, w, "projects"); !slices.Equal(got, []string{real, f.project}) {
		t.Errorf("projects = %v, want the new order", got)
	}

	// What a page reads at startup holds the order, which is all the sidebar
	// asks of the state.
	state := f.do(t, "GET", "/api/state", nil)
	if got := stringList(t, state, "projects"); !slices.Equal(got, []string{real, f.project}) {
		t.Errorf("state projects = %v", got)
	}
}

// TestSessionListIsNewestFirst asserts that a conversation created later is
// what the listing leads with.
func TestSessionListIsNewestFirst(t *testing.T) {
	f := newFixture(t)
	a := f.newSession(t)
	b := f.newSession(t)
	c := f.newSession(t)

	list := f.do(t, "GET", "/api/sessions?project="+f.project, nil)
	if got := sessionIDs(t, list); !slices.Equal(got, []string{c, b, a}) {
		t.Errorf("sessions = %v, want newest first", got)
	}
}

// stringList is one named list of strings out of a JSON answer.
func stringList(t *testing.T, w *httptest.ResponseRecorder, name string) []string {
	t.Helper()
	v, _ := decode(t, w)[name].([]any)
	out := make([]string, 0, len(v))
	for _, s := range v {
		if str, ok := s.(string); ok {
			out = append(out, str)
		}
	}
	return out
}

// sessionIDs is the id of every row of a sessions answer.
func sessionIDs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	rows, _ := decode(t, w)["sessions"].([]any)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		m, _ := row.(map[string]any)
		if id, _ := m["id"].(string); id != "" {
			out = append(out, id)
		}
	}
	return out
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
		map[string]string{"project": f.project}))
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
	// ready to take a message and with no window read for it yet.
	snap := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
	if snap["state"] != "idle" || snap["n_ctx"] != 0.0 {
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

// One agent answers for the harness, and every session takes it.
func TestSelectingAnAgentRepointsEverySession(t *testing.T) {
	f := newFixture(t)
	first := f.newSession(t)
	second := f.newSession(t)

	other := f.addAgent(t, map[string]any{
		"name": "Other", "url": f.upstream.URL, "model": "Delta3.8-27B Q5"})
	w := f.do(t, "POST", "/api/agents/"+other+"/select", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if decode(t, w)["agent"] != other {
		t.Errorf("state = %v", decode(t, w))
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

	// Two short lines are less than a summary of them would be.
	if snap := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil)); snap["compactable"] != false {
		t.Errorf("compactable = %v after two short lines", snap["compactable"])
	}
	if w := f.do(t, "POST", "/api/sessions/"+id+"/compact", nil); w.Code != http.StatusConflict {
		t.Errorf("compact of two short lines = %d, want 409", w.Code)
	}
	long := strings.Repeat("The work so far. ", 300)
	if w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": long}); w.Code != http.StatusAccepted {
		t.Fatalf("message = %d: %s", w.Code, w.Body.String())
	}
	f.waitIdle(t, id)
	if snap := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil)); snap["compactable"] != true {
		t.Errorf("compactable = %v after a long message", snap["compactable"])
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
	if snap["compactable"] != false {
		t.Errorf("compactable = %v with only the summary in context", snap["compactable"])
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

// A sampling value is set by a number and cleared by null. A body that leaves
// the field out changes nothing, which is what every other setting relies on.
func TestPutAgentSetsAndClearsSampling(t *testing.T) {
	f := newFixture(t)
	temp := func() *float64 {
		a, _ := f.cfg.Config().AgentByID(f.agent)
		return a.Temperature
	}

	res := f.do(t, "PUT", "/api/agents/"+f.agent, map[string]any{"temperature": 0.35, "top_p": 0.9})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	if got := temp(); got == nil || *got != 0.35 {
		t.Fatalf("temperature = %v, want 0.35", got)
	}
	if got := f.saved(t, res)["temperature"]; got != 0.35 {
		t.Errorf("the reply said temperature = %v", got)
	}

	f.do(t, "PUT", "/api/agents/"+f.agent, map[string]any{"effort": "low"})
	if got := temp(); got == nil || *got != 0.35 {
		t.Errorf("temperature = %v after a body that left it out", got)
	}

	res = f.do(t, "PUT", "/api/agents/"+f.agent, map[string]any{"temperature": nil})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	if got := temp(); got != nil {
		t.Errorf("temperature = %v, want it back with the model server", *got)
	}
	if got := f.saved(t, res)["temperature"]; got != nil {
		t.Errorf("the reply said temperature = %v, want null", got)
	}
	a, _ := f.cfg.Config().AgentByID(f.agent)
	if a.TopP == nil || *a.TopP != 0.9 {
		t.Errorf("top_p = %v, want 0.9 left alone", a.TopP)
	}
}

// The call list is applied on a write and comes back in the agent JSON.
func TestPutAgentCalls(t *testing.T) {
	f := newFixture(t)
	other := f.addAgent(t, map[string]any{"name": "Reviewer"})
	res := f.do(t, "PUT", "/api/agents/"+f.agent, map[string]any{"calls": []string{other}})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	a, _ := f.cfg.Config().AgentByID(f.agent)
	if len(a.Calls) != 1 || a.Calls[0] != other {
		t.Errorf("calls = %v, want [%s]", a.Calls, other)
	}
	if got, _ := f.saved(t, res)["calls"].([]any); len(got) != 1 || got[0] != other {
		t.Errorf("the reply said calls = %v, want [%s]", got, other)
	}
	// A body that leaves the list out keeps it.
	res = f.do(t, "PUT", "/api/agents/"+f.agent, map[string]any{"description": "Main work."})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	a, _ = f.cfg.Config().AgentByID(f.agent)
	if len(a.Calls) != 1 || a.Calls[0] != other {
		t.Errorf("calls moved to %v on a body that left the list out", a.Calls)
	}
	// A body that sends null clears it.
	res = f.do(t, "PUT", "/api/agents/"+f.agent, map[string]any{"calls": nil})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	a, _ = f.cfg.Config().AgentByID(f.agent)
	if a.Calls != nil {
		t.Errorf("calls = %v, want cleared", a.Calls)
	}
}

// A window nothing reported is unknown, and the agent's override fills it in
// for the session that answers with that agent.
func TestSnapshotWindowComesFromTheAgent(t *testing.T) {
	f := newFixture(t)
	id := f.newSession(t)
	if res := f.do(t, "PUT", "/api/agents/"+f.agent, map[string]any{"max_context": 200000}); res.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", res.Code, res.Body.String())
	}
	snap := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
	if snap["n_ctx"] != 200000.0 || snap["n_ctx_source"] != "agent" {
		t.Errorf("n_ctx = %v from %v, want the override", snap["n_ctx"], snap["n_ctx_source"])
	}
}

func TestPutAgentRejectsValuesOutOfRange(t *testing.T) {
	f := newFixture(t)
	for _, body := range []map[string]any{
		{"temperature": 2.5}, {"top_p": 1.5}, {"effort": "extreme"}, {"url": "ftp://x"},
		{"max_context": 64}, {"max_context": 20_000_000},
	} {
		if res := f.do(t, "PUT", "/api/agents/"+f.agent, body); res.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", body, res.Code)
		}
	}
}

func TestPermissionRoute(t *testing.T) {
	f := newFixture(t)
	go f.perms.Ask(context.Background(), perms.Request{
		ID: "perm_test000000", SessionID: "s1", Project: f.project,
		Tool: "bash", Command: "go test ./...", Prefix: "go test",
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

// The allowlist rule a card keeps is a shell rule, so the decision is refused
// for every other tool before the prefix is checked.
func TestPermissionAlwaysIsShellOnly(t *testing.T) {
	f := newFixture(t)
	go f.perms.Ask(context.Background(), perms.Request{
		ID: "perm_search000", SessionID: "s1", Project: f.project,
		Tool: "web_search", Command: "rm -rf node_modules alternatives",
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := f.perms.Lookup("perm_search000"); ok {
			break
		}
		time.Sleep(time.Millisecond)
	}

	w := f.do(t, "POST", "/api/permissions/perm_search000",
		map[string]string{"decision": "always", "prefix": "rm -rf"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "shell commands only") {
		t.Errorf("always on a search card = %d %s, want the refusal", w.Code, w.Body.String())
	}
	if _, ok := f.cfg.AllowMatch(f.project, "rm -rf"); ok {
		t.Error("a search card stored a shell rule")
	}

	w = f.do(t, "POST", "/api/permissions/perm_search000", map[string]string{"decision": "once"})
	if w.Code != 200 {
		t.Errorf("allow once = %d %s", w.Code, w.Body.String())
	}
}

// TestQuestionRoute drives a question through a turn and answers it over the
// route, since the id to answer is minted inside the turn.
func TestQuestionRoute(t *testing.T) {
	f := newFixture(t)
	id := f.newSession(t)

	args := `{"questions":[{"question":"Where should the cache live?","options":[{"label":"In memory"},{"label":"On disk"}]}]}`
	f.nextFrames(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"ask_user_question","arguments":`+strconv.Quote(args)+`}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	f.setFrames(
		`data: {"choices":[{"delta":{"content":"on disk then"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
	)
	if w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "add a cache"}); w.Code != http.StatusAccepted {
		t.Fatalf("message = %d: %s", w.Code, w.Body.String())
	}

	var ask string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if q := f.engine.PendingQuestion(id); q != nil {
			ask = q.ID
			break
		}
		time.Sleep(time.Millisecond)
	}
	if ask == "" {
		t.Fatal("no question was asked")
	}

	// A page loaded while the turn is blocked needs the id to answer, and the
	// question itself is already on the row the call drew.
	snap := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
	question, _ := snap["question"].(map[string]any)
	if question["id"] != ask || question["call_id"] != "call_1" || question["index"] != float64(0) {
		t.Errorf("snapshot question = %v", snap["question"])
	}

	w := f.do(t, "POST", "/api/questions/"+ask, map[string]any{"options": []int{7}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "no option with that number") {
		t.Errorf("out of range = %d %s", w.Code, w.Body.String())
	}
	w = f.do(t, "POST", "/api/questions/"+ask, map[string]any{"options": []int{0, 1}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "takes one answer") {
		t.Errorf("two answers to one question = %d %s", w.Code, w.Body.String())
	}
	w = f.do(t, "POST", "/api/questions/"+ask, map[string]any{"options": []int{}, "text": "  "})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "answer is empty") {
		t.Errorf("empty answer = %d %s", w.Code, w.Body.String())
	}
	w = f.do(t, "POST", "/api/questions/ask_nothing", map[string]any{"options": []int{0}})
	if w.Code != 404 {
		t.Errorf("unknown id = %d, want 404", w.Code)
	}

	// The card is the row the call drew, so the stream carries the id to
	// answer and the browser attaches it to that row.
	var carried map[string]any
	// The whole replay is read, so the count comes from the hub rather than
	// from a guess at how many events a blocked turn has published.
	for _, e := range f.readStream(t, "/api/events?session="+id+"&from=0", int(f.hub.Seq(id))) {
		if e["type"] == "question" {
			carried = e
		}
	}
	if carried == nil || carried["ask"] != ask || carried["id"] != "call_1" {
		t.Errorf("question event = %v", carried)
	}

	w = f.do(t, "POST", "/api/questions/"+ask, map[string]any{"options": []int{1}})
	if w.Code != 200 {
		t.Fatalf("answer = %d %s", w.Code, w.Body.String())
	}
	w = f.do(t, "POST", "/api/questions/"+ask, map[string]any{"options": []int{0}})
	if w.Code != 404 {
		t.Errorf("second click = %d, want 404", w.Code)
	}
	f.waitIdle(t, id)

	snap = decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
	if _, still := snap["question"]; still {
		t.Error("the answered question is still on the snapshot")
	}
	entries, _ := snap["entries"].([]any)
	found := false
	for _, raw := range entries {
		e, _ := raw.(map[string]any)
		if e["tool_name"] == "ask_user_question" && e["content"] == "On disk" {
			found = true
		}
	}
	if !found {
		t.Errorf("the answer is not the tool result: %v", entries)
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
	// The first request reads the file and the second writes it, since a file
	// is replaced only once it has been read; everything after them, the
	// title request included, is a plain reply.
	f.nextFrames(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"read","arguments":"{\"path\":\"hub.go\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	)
	f.nextFrames(
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":`+strconv.Quote(args)+`}}]}}]}`,
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

// The file route is a path guard first: it reaches what a tool could and no
// further, and it serves bytes only for images, since a page or drawing served
// from this origin would run with the reader's session.
func TestProjectFileRoute(t *testing.T) {
	f := newFixture(t)
	id := f.newSession(t)
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	for name, data := range map[string][]byte{
		"hello.py":    []byte("print('Hello, World!')\n"),
		"drawing.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"dot.png":     png,
	} {
		if err := os.WriteFile(filepath.Join(f.project, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("no"), 0o644)
	get := func(path string) *httptest.ResponseRecorder {
		return f.do(t, "GET", "/api/sessions/"+id+"/file?"+url.Values{"path": {path}}.Encode(), nil)
	}

	body := decode(t, get("hello.py"))
	if body["kind"] != "text" || body["content"] != "print('Hello, World!')\n" || body["path"] != "hello.py" {
		t.Errorf("text = %v", body)
	}
	// An absolute path inside the project comes back relative to it.
	if body := decode(t, get(filepath.Join(f.project, "hello.py"))); body["path"] != "hello.py" {
		t.Errorf("absolute path = %v, want it named relative to the project", body["path"])
	}
	if body := decode(t, get("drawing.svg")); body["kind"] != "text" {
		t.Errorf("svg kind = %v, want text: it can carry script", body["kind"])
	}
	if body := decode(t, get("dot.png")); body["kind"] != "image" || body["content"] != "" {
		t.Errorf("image = %v, want its kind and no content", body)
	}

	for _, path := range []string{"../secret.txt", outside} {
		if w := get(path); w.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", path, w.Code)
		}
	}
	if w := get("missing.txt"); w.Code != http.StatusNotFound {
		t.Errorf("missing = %d, want 404", w.Code)
	}
	if w := get("."); w.Code != http.StatusBadRequest {
		t.Errorf("folder = %d, want 400", w.Code)
	}

	raw := func(path string) *httptest.ResponseRecorder {
		return f.do(t, "GET", "/api/sessions/"+id+"/file?raw=1&"+url.Values{"path": {path}}.Encode(), nil)
	}
	w := raw("dot.png")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), png) {
		t.Errorf("raw png = %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("raw png csp = %q", w.Header().Get("Content-Security-Policy"))
	}
	for _, path := range []string{"drawing.svg", "hello.py"} {
		if w := raw(path); w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("raw %s = %d, want 415", path, w.Code)
		}
	}
}

func TestProjectDirRoute(t *testing.T) {
	f := newFixture(t)
	id := f.newSession(t)
	os.MkdirAll(filepath.Join(f.project, "web", "src"), 0o755)
	os.WriteFile(filepath.Join(f.project, "Makefile"), []byte("all:\n"), 0o644)
	os.WriteFile(filepath.Join(f.project, "AGENTS.md"), []byte("# rules\n"), 0o644)
	os.WriteFile(filepath.Join(f.project, "web", "index.html"), []byte("<p>hi</p>"), 0o644)
	get := func(path string) *httptest.ResponseRecorder {
		return f.do(t, "GET", "/api/sessions/"+id+"/dir?"+url.Values{"path": {path}}.Encode(), nil)
	}

	body := decode(t, get(""))
	if body["path"] != "" || body["root"] != f.project {
		t.Errorf("root listing names %v under %v", body["path"], body["root"])
	}
	var names []string
	for _, e := range body["entries"].([]any) {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	// Folders first, then files by name without regard to case.
	if strings.Join(names, ",") != "web,AGENTS.md,Makefile" {
		t.Errorf("entries = %v", names)
	}
	if body := decode(t, get("web")); body["path"] != "web" || len(body["entries"].([]any)) != 2 {
		t.Errorf("web = %v", body)
	}
	if w := get(".."); w.Code != http.StatusForbidden {
		t.Errorf("parent = %d, want 403", w.Code)
	}
	if w := get("Makefile"); w.Code != http.StatusBadRequest {
		t.Errorf("file = %d, want 400", w.Code)
	}
	if w := get("nope"); w.Code != http.StatusNotFound {
		t.Errorf("missing = %d, want 404", w.Code)
	}
}

func TestProjectFilesRoute(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{"hello.py", "web/src/hello_test.py", "node_modules/hello.py", "notes/say-hello.md", "readme.md"} {
		path := filepath.Join(f.project, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte("x"), 0o644)
	}
	get := func(project, q string) *httptest.ResponseRecorder {
		return f.do(t, "GET", "/api/projects/files?"+url.Values{"project": {project}, "q": {q}}.Encode(), nil)
	}
	body := decode(t, get(f.project, "hello"))
	var files []string
	for _, v := range body["files"].([]any) {
		files = append(files, v.(string))
	}
	// A name that starts with the word ranks first, then shorter paths, and
	// the folders grep skips are not walked.
	if strings.Join(files, ",") != "hello.py,web/src/hello_test.py,notes/say-hello.md" {
		t.Errorf("files = %v", files)
	}
	if w := get(t.TempDir(), "x"); w.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", w.Code)
	}
}

func TestPutConfig(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "PUT", "/api/config", map[string]any{
		"permissions": "full", "compact_at": 0.5, "max_tool_calls": 10,
	}))
	if body["permissions"] != "full" || body["compact_at"] != 0.5 || body["max_tool_calls"] != 10.0 {
		t.Errorf("config = %v", body)
	}
	if f.cfg.Config().Mode != config.ModeFull {
		t.Errorf("the store was not updated: mode = %v", f.cfg.Config().Mode)
	}
	if w := f.do(t, "PUT", "/api/config", map[string]any{"permissions": "sometimes"}); w.Code != 400 {
		t.Errorf("bad permissions = %d", w.Code)
	}
	body = decode(t, f.do(t, "PUT", "/api/config", map[string]any{"listen": "127.0.0.1:9999"}))
	if body["note"] != "Listen changes take effect on restart" {
		t.Errorf("note = %v", body["note"])
	}
}

// The harness pane's debug view lists every tool with its parameters and
// renders the system prompt of each toolset it sends.
func TestTheToolsDebugView(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "GET", "/api/tools", nil))
	list, _ := body["tools"].([]any)
	if len(list) != 14 {
		t.Fatalf("the view lists %d tools, want 14", len(list))
	}
	sent, _ := body["sent"].(map[string]any)
	minimal, _ := sent["minimal"].([]any)
	if len(minimal) != 5 || minimal[0] != "read" || minimal[4] != "processes" {
		t.Errorf("sent minimal = %v", minimal)
	}
	prompts, _ := body["prompts"].(map[string]any)
	p, _ := prompts["minimal"].(string)
	if !strings.Contains(p, "Tools: read, write, edit, bash, processes.") {
		t.Errorf("the minimal prompt does not name its tools: %q", p)
	}
	if strings.Contains(p, "grep") {
		t.Errorf("the minimal prompt names a tool it does not send: %q", p)
	}
	p, _ = prompts["full"].(string)
	if !strings.Contains(p, "Tools: read, read_image, grep, web_search, write, edit, todo_write, ask_user_question, write_plan, exit_plan_mode, subagent, send_message, bash, processes.") {
		t.Errorf("the full prompt does not name every tool: %q", p)
	}
}

// A model is a property of the agent, so the config route must not be a second
// way to set one.
func TestPutConfigIgnoresTheModel(t *testing.T) {
	f := newFixture(t)
	if w := f.do(t, "PUT", "/api/config", map[string]any{"model": "Delta3.8-27B Q5"}); w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	a, _ := f.cfg.Config().AgentByID(f.agent)
	if a.Model != "Alpha-12B" {
		t.Errorf("model = %q, want it unchanged", a.Model)
	}
}

func TestMovingAnAgentAddressResetsTheCache(t *testing.T) {
	f := newFixture(t)
	f.do(t, "GET", "/api/state", nil)
	f.pointAt(t, "http://127.0.0.1:1")
	body := decode(t, f.do(t, "GET", "/api/state", nil))
	if body["detect_error"] == "" {
		t.Error("the reading of the old address was still served")
	}
}

func TestAgentsCRUD(t *testing.T) {
	f := newFixture(t)
	second := f.addAgent(t, map[string]any{"name": "Studio", "url": f.upstream.URL, "model": "Beta 30B Q5"})

	body := decode(t, f.do(t, "GET", "/api/agents", nil))
	agents, _ := body["agents"].([]any)
	if len(agents) != 2 {
		t.Fatalf("agents = %v", agents)
	}
	if body["agent"] != f.agent {
		t.Errorf("adding an agent moved the selection to %v", body["agent"])
	}

	if w := f.do(t, "POST", "/api/agents", map[string]any{"name": "studio"}); w.Code != http.StatusConflict {
		t.Errorf("a duplicate name = %d, want 409", w.Code)
	}
	if w := f.do(t, "POST", "/api/agents", map[string]any{"name": "  "}); w.Code != http.StatusBadRequest {
		t.Errorf("a blank name = %d, want 400", w.Code)
	}
	if w := f.do(t, "PUT", "/api/agents/agt_nothing", map[string]any{"name": "x"}); w.Code != http.StatusNotFound {
		t.Errorf("unknown agent = %d, want 404", w.Code)
	}

	w := f.do(t, "PUT", "/api/agents/"+second, map[string]any{"name": "Renamed", "instructions": " Be brief. "})
	if w.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", w.Code, w.Body.String())
	}
	if saved := f.saved(t, w); saved["name"] != "Renamed" || saved["instructions"] != "Be brief." {
		t.Errorf("saved = %v", saved)
	}

	// Deleting the active agent hands the turn to the one left, and deleting
	// the last leaves the state a first run is in.
	body = decode(t, f.do(t, "DELETE", "/api/agents/"+f.agent, nil))
	if body["agent"] != second {
		t.Errorf("agent = %v, want the one remaining", body["agent"])
	}
	body = decode(t, f.do(t, "DELETE", "/api/agents/"+second, nil))
	if body["agent"] != "" || len(body["agents"].([]any)) != 0 {
		t.Errorf("state after the last delete = %v", body)
	}
	if w := f.do(t, "POST", "/api/sessions", map[string]string{"project": f.project}); w.Code != 400 {
		t.Errorf("a session with no agent = %d, want 400", w.Code)
	}
}

// Concurrency is on the record before anything reads it, so what a write does
// with it is the whole of its behaviour: a new agent works alone, a number in
// range is kept, and one out of range is refused rather than clamped.
func TestAgentConcurrency(t *testing.T) {
	f := newFixture(t)
	id := f.addAgent(t, map[string]any{"name": "Fanning", "url": f.upstream.URL})
	if got := f.saved(t, f.do(t, "PUT", "/api/agents/"+id, map[string]any{"name": "Fanning"})); got["concurrency"] != 1.0 {
		t.Errorf("a new agent's concurrency = %v, want 1", got["concurrency"])
	}
	if got := f.saved(t, f.do(t, "PUT", "/api/agents/"+id, map[string]any{"concurrency": 6})); got["concurrency"] != 6.0 {
		t.Errorf("concurrency = %v, want 6", got["concurrency"])
	}
	if a, _ := f.cfg.Config().AgentByID(id); a.Concurrency != 6 {
		t.Errorf("stored concurrency = %d, want 6", a.Concurrency)
	}
	for _, bad := range []int{0, config.MaxConcurrency + 1} {
		if w := f.do(t, "PUT", "/api/agents/"+id, map[string]any{"concurrency": bad}); w.Code != http.StatusBadRequest {
			t.Errorf("concurrency %d = %d, want 400", bad, w.Code)
		}
	}
	if a, _ := f.cfg.Config().AgentByID(id); a.Concurrency != 6 {
		t.Errorf("a refused write moved concurrency to %d", a.Concurrency)
	}
}

// The key is a field like any other: it round trips, a body that does not name
// it leaves it alone, and an empty one is no key.
func TestAgentKeyRoundTrips(t *testing.T) {
	f := newFixture(t)
	const key = "sk-a-key"
	id := f.addAgent(t, map[string]any{"name": "Keyed", "url": f.upstream.URL, "key": key})

	stored := func() string {
		a, _ := f.cfg.Config().AgentByID(id)
		return a.Key
	}
	if stored() != key {
		t.Fatalf("stored key = %q", stored())
	}
	if got := f.saved(t, f.do(t, "PUT", "/api/agents/"+id, map[string]any{"name": "Keyed"})); got["key"] != key {
		t.Errorf("a body that did not name the key changed it: %v", got["key"])
	}
	if got := f.saved(t, f.do(t, "PUT", "/api/agents/"+id, map[string]any{"key": ""})); got["key"] != "" {
		t.Errorf("an empty key was not taken as no key: %v", got["key"])
	}
	if stored() != "" {
		t.Errorf("stored key = %q after clearing", stored())
	}
}

// A key typed for an agent that has not been stored yet is what its address is
// read with, or the model list of a server that wants one is never fetched.
func TestProbeTakesAKeyThatIsNotStored(t *testing.T) {
	f := newFixture(t)
	var seen []string
	guarded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer sk-typed" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		serveTestdata(t, w, "openai-models.json")
	}))
	defer guarded.Close()

	body := decode(t, f.do(t, "POST", "/api/agents/probe",
		map[string]any{"url": guarded.URL, "key": "sk-typed"}))
	if got := body["models"].([]any); len(got) != 1 {
		t.Fatalf("probe with the typed key listed %d models: %v", len(got), body)
	}
	body = decode(t, f.do(t, "POST", "/api/agents/probe", map[string]any{"url": guarded.URL}))
	if body["models_kind"] != "auth" {
		t.Errorf("probe with no key = %v, want it refused", body)
	}
	if len(seen) == 0 {
		t.Fatal("the address was never asked")
	}
}

// The model list belongs to the address it was read from, so a second agent
// elsewhere is not answered out of the first one's cache.
func TestModelsFollowTheActiveAgent(t *testing.T) {
	f := newFixture(t)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		serveTestdata(t, w, "openai-models.json")
	}))
	defer other.Close()

	if got := decode(t, f.do(t, "GET", "/api/models", nil))["models"].([]any); len(got) != 6 {
		t.Fatalf("the first agent listed %d models, want 6", len(got))
	}
	id := f.addAgent(t, map[string]any{"name": "Elsewhere", "url": other.URL, "model": "Delta3.8-27B"})
	if w := f.do(t, "POST", "/api/agents/"+id+"/select", nil); w.Code != 200 {
		t.Fatalf("select = %d %s", w.Code, w.Body.String())
	}
	got := decode(t, f.do(t, "GET", "/api/models", nil))["models"].([]any)
	if len(got) != 1 {
		t.Fatalf("the second agent listed %d models, want the one its own address names", len(got))
	}
}

// The probe answers a bad address in the body, so the dialog draws it in the
// form rather than as a failed request.
func TestProbeReportsAFailureAt200(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "POST", "/api/agents/probe", map[string]any{"url": f.upstream.URL}))
	if body["mode"] != "router" || len(body["models"].([]any)) != 6 {
		t.Errorf("probe of a live address = %v", body)
	}

	body = decode(t, f.do(t, "POST", "/api/agents/probe", map[string]any{"url": "http://127.0.0.1:1"}))
	if body["mode"] != "unknown" || body["detect_error"] == "" || body["detect_kind"] != "unreachable" {
		t.Errorf("probe of a dead address = %v", body)
	}
	if w := f.do(t, "POST", "/api/agents/probe", map[string]any{"url": "ftp://x"}); w.Code != 400 {
		t.Errorf("probe of a bad address = %d, want 400", w.Code)
	}
}

func TestStateWithNoAgents(t *testing.T) {
	f := newFixture(t)
	f.do(t, "DELETE", "/api/agents/"+f.agent, nil)
	body := decode(t, f.do(t, "GET", "/api/state", nil))
	if body["mode"] != "unknown" || body["agent"] != "" {
		t.Errorf("state = %v", body)
	}
	if len(body["agents"].([]any)) != 0 || len(body["models"].([]any)) != 0 {
		t.Errorf("state named an agent or a model: %v", body)
	}
	if !strings.Contains(body["detect_error"].(string), "No agent is selected") {
		t.Errorf("detect_error = %v", body["detect_error"])
	}
}

func TestSSEReplayAndResync(t *testing.T) {
	f := newFixture(t)
	// Every event here is one the hub passes straight through, so the ring
	// holds one frame per publish.
	for i := 0; i < 3; i++ {
		f.hub.Publish("s1", agent.Event{Type: "status_entry", Fields: map[string]any{"content": fmt.Sprint(i)}})
	}

	got := f.readStream(t, "/api/events?session=s1&from=1", 2)
	if len(got) != 2 || got[0]["seq"] != 2.0 || got[1]["content"] != "2" {
		t.Errorf("replay from ?from= = %v", got)
	}

	req := newRequest("GET", "/api/events?session=s1", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Last-Event-ID", "2")
	got = f.readStreamReq(t, req, 1)
	if len(got) != 1 || got[0]["seq"] != 3.0 {
		t.Errorf("replay from Last-Event-ID = %v", got)
	}

	// Roll the ring past the requested sequence.
	for i := 0; i < ringSize+10; i++ {
		f.hub.Publish("s1", agent.Event{Type: "status_entry", Fields: map[string]any{"content": "x"}})
	}
	got = f.readStream(t, "/api/events?session=s1&from=1", 1)
	if len(got) != 1 || got[0]["type"] != "resync" {
		t.Errorf("rolled ring = %v, want a resync", got)
	}
}

// TestSSEResyncAfterRestart asserts that a mark from before a restart, which is
// ahead of anything the new hub sent, is answered with a resync.
func TestSSEResyncAfterRestart(t *testing.T) {
	f := newFixture(t)
	f.hub.Publish("s1", agent.Event{Type: "status_entry", Fields: map[string]any{"content": "0"}})
	_, cancel, replay := f.hub.Subscribe("s1", 800)
	defer cancel()
	if len(replay) != 1 || replay[0].Type != resyncEvType {
		t.Errorf("replay for a mark ahead of the hub = %+v, want one resync", replay)
	}
	_, cancel2, replay := f.hub.Subscribe("s1", 1)
	defer cancel2()
	if len(replay) != 0 {
		t.Errorf("replay for a mark at the last frame = %+v, want none", replay)
	}
}

func TestSlowSubscriberGetsResyncAndStaysAttached(t *testing.T) {
	f := newFixture(t)
	ch, cancel, _ := f.hub.Subscribe("s2", 0)
	defer cancel()
	for i := 0; i < subscriberQ+50; i++ {
		f.hub.Publish("s2", agent.Event{Type: "status_entry", Fields: map[string]any{"content": "x"}})
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

func TestStreamedTextGoesOutAsOneFrame(t *testing.T) {
	f := newFixture(t)
	for _, word := range []string{"one ", "two ", "three"} {
		f.hub.Publish("s4", agent.Event{Type: "delta", Fields: map[string]any{"text": word}})
	}
	got := f.readStream(t, "/api/events?session=s4&from=0", 1)
	if len(got) != 1 || got[0]["text"] != "one two three" {
		t.Errorf("joined text = %v", got)
	}
}

func TestAnEventSendsTheTextWaitingBeforeIt(t *testing.T) {
	f := newFixture(t)
	f.hub.Publish("s5", agent.Event{Type: "delta", Fields: map[string]any{"text": "hi"}})
	f.hub.Publish("s5", agent.Event{Type: "done", Fields: map[string]any{}})
	got := f.readStream(t, "/api/events?session=s5&from=0", 2)
	if len(got) != 2 || got[0]["text"] != "hi" || got[1]["type"] != "done" {
		t.Errorf("order = %v, want the text then done", got)
	}
}

func TestThinkingAndTheReplyAreSeparateFrames(t *testing.T) {
	f := newFixture(t)
	f.hub.Publish("s6", agent.Event{Type: "reasoning", Fields: map[string]any{"text": "why"}})
	f.hub.Publish("s6", agent.Event{Type: "delta", Fields: map[string]any{"text": "because"}})
	got := f.readStream(t, "/api/events?session=s6&from=0", 2)
	if len(got) != 2 || got[0]["type"] != "reasoning" || got[0]["text"] != "why" {
		t.Fatalf("first frame = %v, want the thinking on its own", got)
	}
	if got[1]["type"] != "delta" || got[1]["text"] != "because" {
		t.Errorf("second frame = %v, want the reply on its own", got[1])
	}
}

// A browser only remembers a frame's id if the field is named the way the
// specification names it, and an id it does not remember is one it cannot
// reconnect from.
func TestAFrameNamesItsIDInLowercase(t *testing.T) {
	f := newFixture(t)
	f.hub.Publish("s7", agent.Event{Type: "status_entry", Fields: map[string]any{"content": "x"}})
	req := newRequest("GET", "/api/events?session=s7&from=0", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	lines := f.readStreamLines(t, req, 1)
	if len(lines) == 0 || lines[0] != "id: 1" {
		t.Errorf("the stream opened with %q, want \"id: 1\"", lines)
	}
}

// A browser reconnecting on its own resends the URL it was opened with, so the
// stream would rewind to where that URL was built and send the middle of the
// turn twice.
func TestAReconnectResumesFromTheHeaderNotTheStaleURL(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 4; i++ {
		f.hub.Publish("s8", agent.Event{Type: "status_entry", Fields: map[string]any{"content": fmt.Sprint(i)}})
	}
	req := newRequest("GET", "/api/events?session=s8&from=1", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Last-Event-ID", "3")
	got := f.readStreamReq(t, req, 1)
	if len(got) != 1 || got[0]["seq"] != 4.0 {
		t.Errorf("replay = %v, want frame 4 alone", got)
	}
}

// A session opened part way through a reply draws what was streamed before it
// was opened, and the stream it opens from last_seq carries on from there.
func TestAViewCarriesTheReplyStillStreaming(t *testing.T) {
	f := newFixture(t)
	id := f.newSession(t)
	f.holdAfter(t,
		`data: {"choices":[{"delta":{"reasoning_content":"Thinking"}}]}`,
		`data: {"choices":[{"delta":{"content":"Hello "}}]}`,
		`data: {"choices":[{"delta":{"content":"world"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
	)
	f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "hi"})

	var body map[string]any
	var reply map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body = decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))
		reply, _ = body["reply"].(map[string]any)
		calls, _ := reply["tool_calls"].([]any)
		if len(calls) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if reply == nil {
		t.Fatalf("snapshot has no reply: %v", body)
	}
	if reply["content"] != "Hello world" || reply["reasoning"] != "Thinking" {
		t.Errorf("reply = %v, want the text and the thinking so far", reply)
	}
	since, err := time.Parse(time.RFC3339Nano, fmt.Sprint(reply["since"]))
	if err != nil || time.Since(since) > time.Minute || time.Until(since) > 0 {
		t.Errorf("reply since = %v, want when the request was sent", reply["since"])
	}
	calls, _ := reply["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("reply calls = %v, want one", reply["tool_calls"])
	}
	call, _ := calls[0].(map[string]any)
	if call["id"] != "call_1" || call["name"] != "read" || call["arguments"] != `{"pa` {
		t.Errorf("call = %v", call)
	}

	// Everything the reply holds went out at or before the mark, so nothing
	// after it repeats the text.
	seq, _ := body["last_seq"].(float64)
	var text, thought string
	for _, e := range f.readStream(t, "/api/events?session="+id+"&from=0", int(seq)) {
		switch e["type"] {
		case "delta":
			text += e["text"].(string)
		case "reasoning":
			thought += e["text"].(string)
		}
	}
	if text != "Hello world" || thought != "Thinking" {
		t.Errorf("frames up to the mark carry %q and %q", text, thought)
	}

	f.do(t, "POST", "/api/sessions/"+id+"/stop", nil)
	f.waitIdle(t, id)
	if _, ok := decode(t, f.do(t, "GET", "/api/sessions/"+id, nil))["reply"]; ok {
		t.Error("an idle session still reports a reply")
	}
}

// The session feed opens with every session that is working and then sends
// each change, whichever session is open in the page.
func TestSessionFeedReportsEverySession(t *testing.T) {
	f := newFixture(t)
	busy := f.newSession(t)
	quiet := f.newSession(t)
	f.block(t)
	f.do(t, "POST", "/api/sessions/"+busy+"/message", map[string]string{"text": "hi"})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && f.engine.State(busy) != agent.StateStreaming {
		time.Sleep(time.Millisecond)
	}

	// Stopped once the feed is watching, so the change lands after the list.
	go func() {
		for {
			f.hub.mu.Lock()
			n := len(f.hub.watchers)
			f.hub.mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		f.do(t, "POST", "/api/sessions/"+busy+"/stop", nil)
	}()

	var idle bool
	got := f.readStreamUntil(t, "/api/events/sessions", func(e map[string]any) bool {
		idle = e["session"] == busy && e["state"] == "idle"
		return idle
	})
	states, _ := got[0]["states"].(map[string]any)
	if states[busy] != "streaming" {
		t.Errorf("opening list = %v, want %s streaming", states, busy)
	}
	if _, ok := states[quiet]; ok {
		t.Errorf("opening list = %v, want no idle session in it", states)
	}
	if !idle {
		t.Errorf("feed = %v, want the stop to reach it", got)
	}
	f.waitIdle(t, busy)
}

// A process count reaches the watchers and not the session's own stream.
func TestWatcherFeedCarriesProcessCounts(t *testing.T) {
	f := newFixture(t)
	session := f.newSession(t)
	go func() {
		for {
			f.hub.mu.Lock()
			n := len(f.hub.watchers)
			f.hub.mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		f.hub.Publish(session, agent.Event{Type: "procs", Fields: map[string]any{"running": 2}})
	}()

	got := f.readStreamUntil(t, "/api/events/sessions", func(e map[string]any) bool {
		return e["session"] == session && e["running"] == 2
	})
	if _, ok := got[0]["running"]; !ok {
		t.Errorf("opening list = %v, want a running map", got[0])
	}
	if len(got) < 2 {
		t.Errorf("feed = %v, want the process count in it", got)
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
		"index": 0, "id": "call_0", "name": "bash", "arguments": "{"}})
	f.hub.Publish("s3", agent.Event{Type: "tool_output", Fields: map[string]any{
		"index": 0, "id": "call_0", "name": "bash", "output": "x", "exit_code": 0, "truncated": false}})
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
	h.ServeHTTP(w, newRequest("GET", "/boom", nil))
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
	req := newRequest("GET", "/", nil)
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
		map[string]string{"project": f.project}))
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
	req := newRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	return f.readStreamReq(t, req, want)
}

// readStreamLines reads the stream as it goes on the wire until it has seen
// want data lines, then cancels the request so the handler returns.
func (f *fixture) readStreamLines(t *testing.T, req *http.Request, want int) []string {
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

	var lines []string
	seen := 0
	sc := bufio.NewScanner(pr)
	for seen < want && sc.Scan() {
		line := sc.Text()
		lines = append(lines, line)
		if strings.HasPrefix(line, "data: ") {
			seen++
		}
	}
	cancel()
	<-done
	return lines
}

// readStreamUntil reads a stream's events until stop accepts one.
func (f *fixture) readStreamUntil(t *testing.T, path string, stop func(map[string]any) bool) []map[string]any {
	t.Helper()
	req := newRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	pr, pw := newPipeRecorder()
	done := make(chan struct{})
	go func() {
		f.handler.ServeHTTP(pw, req)
		pw.w.Close()
		close(done)
	}()
	var out []map[string]any
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("event is not JSON: %q", line)
		}
		out = append(out, v)
		if stop(v) {
			break
		}
	}
	cancel()
	<-done
	return out
}

// readStreamReq reads the replay an event source receives.
func (f *fixture) readStreamReq(t *testing.T, req *http.Request, want int) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range f.readStreamLines(t, req, want) {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &v); err != nil {
			t.Fatalf("event is not JSON: %q", line)
		}
		out = append(out, v)
	}
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

// A changed address has to reach the turn loop too, not just the routes that
// list models. The engine resolves the agent every turn, so what proves it is
// the request arriving at the new address.
func TestATurnFollowsTheAgentAddress(t *testing.T) {
	f := newFixture(t)
	var askedMu sync.Mutex
	var asked []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		askedMu.Lock()
		asked = append(asked, r.URL.Path)
		askedMu.Unlock()
		switch r.URL.Path {
		case "/props":
			serveTestdata(t, w, "props-router.json")
		case "/v1/models":
			serveTestdata(t, w, "v1-models.json")
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}))
	defer other.Close()

	f.pointAt(t, other.URL)
	id := f.newSession(t)
	if w := f.do(t, "POST", "/api/sessions/"+id+"/message", map[string]string{"text": "hi"}); w.Code != http.StatusAccepted {
		t.Fatalf("message = %d %s", w.Code, w.Body.String())
	}
	f.waitIdle(t, id)

	askedMu.Lock()
	defer askedMu.Unlock()
	for _, path := range asked {
		if path == "/v1/chat/completions" {
			return
		}
	}
	t.Fatalf("the turn went to the old address, the new one saw %v", asked)
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

// The protocol is the agent's, so it has to survive the wire and the file, and
// a value nothing speaks has to be refused rather than stored.
func TestAgentProtocolIsCarriedAndChecked(t *testing.T) {
	f := newFixture(t)
	id := f.addAgent(t, map[string]any{
		"name": "Cloud", "url": "https://api.openai.com",
		"protocol": "responses", "model": "gpt-5.6-luna",
	})

	body := decode(t, f.do(t, "GET", "/api/agents", nil))
	agents, _ := body["agents"].([]any)
	var got map[string]any
	for _, a := range agents {
		m, _ := a.(map[string]any)
		if m["id"] == id {
			got = m
		}
	}
	if got == nil || got["protocol"] != "responses" {
		t.Fatalf("agent = %v, want protocol responses", got)
	}

	if w := f.do(t, "PUT", "/api/agents/"+id, map[string]any{"protocol": "grpc"}); w.Code != http.StatusBadRequest {
		t.Errorf("an unspoken protocol = %d, want 400", w.Code)
	}

	// The fixture's own agent never named one, so it is the harness's default.
	cfg := f.cfg.Config()
	first, ok := cfg.AgentByID(f.agent)
	if !ok || first.Protocol != config.ProtocolLlama {
		t.Errorf("default protocol = %q, want llamacpp", first.Protocol)
	}
	saved, ok := cfg.AgentByID(id)
	if !ok || saved.Protocol != config.ProtocolResponses {
		t.Errorf("stored protocol = %q, want responses", saved.Protocol)
	}
}

// One address answering both protocols is read as whichever the agent names,
// which is the whole reason the choice exists.
func TestProbeReadsTheAddressAsTheProtocolNamed(t *testing.T) {
	f := newFixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/props":
			serveTestdata(t, w, "props-router.json")
		case "/v1/models":
			serveTestdata(t, w, "openai-models.json")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	for _, tc := range []struct{ protocol, mode string }{
		{"llamacpp", "router"},
		{"chat", "openai"},
		{"responses", "responses"},
	} {
		body := decode(t, f.do(t, "POST", "/api/agents/probe?refresh=1",
			map[string]any{"url": upstream.URL, "protocol": tc.protocol}))
		if body["mode"] != tc.mode {
			t.Errorf("protocol %q read the address as %v, want %v", tc.protocol, body["mode"], tc.mode)
		}
		if body["detect_error"] != "" {
			t.Errorf("protocol %q: %v", tc.protocol, body["detect_error"])
		}
	}
}

// A subagent run is not a conversation anybody started, so it is not in the
// list, and it is reachable by its own id from the launcher that carries it.
func TestSubagentSessionsAreNotListed(t *testing.T) {
	f := newFixture(t)
	parent := decode(t, f.do(t, "POST", "/api/sessions", map[string]string{"project": f.project}))
	parentID, _ := parent["id"].(string)

	doc, err := f.srv.store.Load(parentID)
	if err != nil {
		t.Fatal(err)
	}
	child := *doc
	child.ID = store.NewID()
	child.Parent = parentID
	child.Title = "review internal/llm"
	child.LastUsage = &llm.Usage{PromptTokens: 9000, CompletionTokens: 200}
	if err := f.srv.store.Save(&child); err != nil {
		t.Fatal(err)
	}

	list := decode(t, f.do(t, "GET", "/api/sessions", nil))
	rows, _ := list["sessions"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["id"] == child.ID {
			t.Errorf("the sidebar listed a subagent run: %v", row)
		}
	}

	got := decode(t, f.do(t, "GET", "/api/sessions/"+child.ID, nil))
	if got["parent"] != parentID {
		t.Errorf("parent = %v, want %v", got["parent"], parentID)
	}
	held := decode(t, f.do(t, "GET", "/api/sessions/"+parentID, nil))
	kids, _ := held["children"].([]any)
	if len(kids) != 1 {
		t.Fatalf("children = %v, want the one launched", kids)
	}
	one, _ := kids[0].(map[string]any)
	if one["id"] != child.ID {
		t.Errorf("children = %v", kids)
	}
	// The panel names the agent and says whether the run is still working, and
	// this is the only place a reloaded page reads either from.
	if one["agent"] != "Local" {
		t.Errorf("agent = %v, want the name rather than the id", one["agent"])
	}
	if one["state"] != "idle" {
		t.Errorf("state = %v, want idle for a run nothing is running", one["state"])
	}
	// The context it holds is its last reported request, prompt and answer.
	if one["fill"] != float64(9200) {
		t.Errorf("fill = %v, want 9200", one["fill"])
	}
}

func searchFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "search", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// The state read carries the whole block, so the settings dialog makes no
// extra fetch, and known is the list the dialog did not have to know.
func TestStateCarriesTheSearchBlock(t *testing.T) {
	f := newFixture(t)
	body := decode(t, f.do(t, "GET", "/api/state", nil))
	s, _ := body["search"].(map[string]any)
	if s == nil {
		t.Fatalf("state has no search block")
	}
	if s["provider"] != "" || s["ready"] != false {
		t.Errorf("search = %v, want off by default", s)
	}
	if s["max_hits"] != 10.0 || s["max_chars"] != 4000.0 || s["timeout_s"] != 20.0 {
		t.Errorf("limits = %v, want the defaults", s)
	}
	known, _ := s["known"].([]any)
	if len(known) != 1 || known[0] != "exa" {
		t.Errorf("known = %v", known)
	}
	providers, _ := s["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("providers = %v", providers)
	}
	p, _ := providers[0].(map[string]any)
	if p["name"] != "exa" || p["url"] != "https://api.exa.ai" || p["key"] != "" {
		t.Errorf("provider = %v, want exa at its default with no key", p)
	}
}

func TestPutSearchRoundTrip(t *testing.T) {
	f := newFixture(t)
	w := f.do(t, "PUT", "/api/search", map[string]any{
		"provider": "exa", "max_hits": 5, "max_chars": 8000, "timeout_s": 30,
		"providers": []map[string]any{{"name": "exa", "url": "https://api.exa.ai", "key": "sk-exa-test"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	s, _ := decode(t, w)["search"].(map[string]any)
	if s["provider"] != "exa" || s["ready"] != true || s["max_hits"] != 5.0 || s["timeout_s"] != 30.0 {
		t.Errorf("search = %v", s)
	}
	acc, ok := f.cfg.Config().Search.Account()
	if !ok || acc.Key != "sk-exa-test" {
		t.Errorf("account = %+v (%v), want the saved key", acc, ok)
	}
	if !f.cfg.Config().Search.Enabled() {
		t.Error("search is not offered after saving a key")
	}

	// Naming the provider off keeps the account and its key: what the dialog
	// sends is every account, the untouched ones unchanged.
	w = f.do(t, "PUT", "/api/search", map[string]any{
		"provider":  "",
		"providers": []map[string]any{{"name": "exa", "url": "https://api.exa.ai", "key": "sk-exa-test"}},
	})
	s, _ = decode(t, w)["search"].(map[string]any)
	if s["provider"] != "" || s["ready"] != false {
		t.Errorf("search = %v, want off with the account kept", s)
	}
	providers, _ := s["providers"].([]any)
	p, _ := providers[0].(map[string]any)
	if p["key"] != "sk-exa-test" {
		t.Errorf("key = %v, want it kept while the provider is off", p["key"])
	}
}

func TestPutSearchRefusals(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		body map[string]any
		says string
	}{
		{map[string]any{"provider": "nope"}, "Provider \"nope\" is not in the list of allowed providers: leave it empty to turn search off"},
		{map[string]any{"max_hits": 0}, "Results is outside 1 to 25"},
		{map[string]any{"max_hits": 26}, "Results is outside 1 to 25"},
		{map[string]any{"max_chars": 100}, "Characters is outside 500 to 100000"},
		{map[string]any{"max_chars": 200000}, "Characters is outside 500 to 100000"},
		{map[string]any{"timeout_s": 0}, "Timeout is outside 1 to 120 seconds"},
		{map[string]any{"providers": []map[string]any{{"name": "exa", "url": "ftp://x"}}},
			"Address \"ftp://x\" is not an http or https URL with a host"},
	}
	for _, tc := range cases {
		w := f.do(t, "PUT", "/api/search", tc.body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%v: status = %d, want 400", tc.body, w.Code)
			continue
		}
		if got := decode(t, w)["error"]; got != tc.says {
			t.Errorf("%v: error = %v, want %q", tc.body, got, tc.says)
		}
	}
	if f.cfg.Config().Search.Enabled() {
		t.Error("a refused write changed the block")
	}
}

// The probe spends one billed request when the button is pressed and draws a
// failure inside the form rather than as a status.
func TestProbeSearch(t *testing.T) {
	f := newFixture(t)

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-exa-test" {
			t.Errorf("authorization = %q, want the key as a bearer token", r.Header.Get("Authorization"))
		}
		w.Write([]byte(searchFixture(t, "exa-check.json")))
	}))
	t.Cleanup(good.Close)

	w := f.do(t, "POST", "/api/search/probe",
		map[string]any{"provider": "exa", "url": good.URL, "key": "sk-exa-test"})
	body := decode(t, w)
	if w.Code != http.StatusOK || body["ok"] != true {
		t.Errorf("probe = %d %v, want 200 with ok", w.Code, body)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(searchFixture(t, "exa-401.json")))
	}))
	t.Cleanup(bad.Close)
	w = f.do(t, "POST", "/api/search/probe",
		map[string]any{"provider": "exa", "url": bad.URL, "key": "sk-wrong"})
	body = decode(t, w)
	if w.Code != http.StatusOK || body["ok"] != false {
		t.Errorf("a refused probe = %d %v, want 200 with ok false", w.Code, body)
	}
	if err, _ := body["error"].(string); !strings.Contains(err, "Invalid API key") {
		t.Errorf("error = %v, want the endpoint's own sentence", body["error"])
	}

	if w := f.do(t, "POST", "/api/search/probe", map[string]any{"provider": "", "key": "k"}); w.Code != http.StatusBadRequest {
		t.Errorf("no provider: status = %d, want 400", w.Code)
	}
	if w := f.do(t, "POST", "/api/search/probe", map[string]any{"provider": "nope", "key": "k"}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown provider: status = %d, want 400", w.Code)
	}
	if w := f.do(t, "POST", "/api/search/probe", map[string]any{"provider": "exa", "url": "ftp://x", "key": "k"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad address: status = %d, want 400", w.Code)
	}
}
