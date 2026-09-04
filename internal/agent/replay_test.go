package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// replay serves recorded SSE bodies in the order they were queued and records
// the request that asked for each one.
type replay struct {
	t *testing.T

	mu       sync.Mutex
	scripts  [][]string
	next     int
	Delay    time.Duration
	Status   int
	Requests []map[string]any
	Queries  []string
}

func newReplay(t *testing.T, fixtures ...string) *replay {
	t.Helper()
	r := &replay{t: t}
	for _, f := range fixtures {
		r.scripts = append(r.scripts, frames(t, f))
	}
	return r
}

func frames(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var out []string
	for _, block := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n\n") {
		if strings.TrimSpace(block) != "" {
			out = append(out, block)
		}
	}
	return out
}

func (r *replay) Server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/props":
			// A model-scoped read without autoload=false would load a model
			// on the router, so it fails here rather than in production.
			if req.URL.Query().Get("model") != "" {
				r.mu.Lock()
				r.Queries = append(r.Queries, req.URL.RawQuery)
				r.mu.Unlock()
				if req.URL.Query().Get("autoload") != "false" {
					http.Error(w, "model-scoped GET without autoload=false", 500)
					return
				}
				serveFixture(r.t, w, "props-model.json")
				return
			}
			serveFixture(r.t, w, "props-router.json")
		case "/v1/models":
			serveFixture(r.t, w, "v1-models.json")
		case "/v1/chat/completions":
			r.serveChat(w, req)
		default:
			http.NotFound(w, req)
		}
	}))
}

func (r *replay) serveChat(w http.ResponseWriter, req *http.Request) {
	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	r.Requests = append(r.Requests, body)
	script := []string(nil)
	if r.next < len(r.scripts) {
		script = r.scripts[r.next]
		r.next++
	}
	status, delay := r.Status, r.Delay
	r.mu.Unlock()

	if status != 0 && status != 200 {
		w.WriteHeader(status)
		fmt.Fprint(w, `{"error":{"message":"replay refused the request"}}`)
		return
	}
	if script == nil {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"error":{"message":"the replay server ran out of scripted responses"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	for _, frame := range script {
		fmt.Fprint(w, frame+"\n\n")
		if f != nil {
			f.Flush()
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

func (r *replay) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.Requests)
}

func (r *replay) Body(i int) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if i < 0 || i >= len(r.Requests) {
		r.t.Fatalf("no request %d, %d were made", i, len(r.Requests))
	}
	return r.Requests[i]
}

func (r *replay) LastBody() map[string]any {
	r.mu.Lock()
	n := len(r.Requests)
	r.mu.Unlock()
	return r.Body(n - 1)
}

func serveFixture(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
