package agent

import (
	"context"
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

	mu      sync.Mutex
	scripts [][]string
	next    int
	// byModel queues scripts answered only to a request naming that model, and
	// held gates them, so a launcher and the agent it launched are scripted
	// and timed apart from each other.
	byModel map[string][][]string
	nextFor map[string]int
	held    map[string]chan struct{}
	Delay   time.Duration
	Props   time.Duration
	Status  int
	// NoVision refuses a request carrying an image, the way a server with
	// vision turned off does, without using up a script.
	NoVision bool
	Requests []map[string]any
	Titles   []map[string]any
	Queries  []string
	Loads    []string
	// Order records what the server was asked for, in the order it was
	// asked, so a test can assert that a side request did not land between
	// two of a turn's own requests.
	Order []string
	// paused is closed once a script has been sent up to its hold marker and
	// the connection is being held open, which is when a test stops a turn.
	paused chan struct{}
}

// holdMarker is a frame that is never sent. Where a script ends with it the
// connection stays open instead, so a test can stop a turn that has streamed
// everything up to that point and is still waiting for the next frame.
const holdMarker = "data: [HOLD]"

func newReplay(t *testing.T, fixtures ...string) *replay {
	t.Helper()
	r := &replay{t: t, byModel: map[string][][]string{}, nextFor: map[string]int{},
		held: map[string]chan struct{}{}, paused: make(chan struct{})}
	for _, f := range fixtures {
		r.scripts = append(r.scripts, frames(t, f))
	}
	return r
}

// forModel queues scripts for one model alone. A test with two agents answering
// at once cannot share the one queue, since the order they arrive in is theirs
// rather than the test's.
func (r *replay) forModel(model string, fixtures ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, f := range fixtures {
		r.byModel[model] = append(r.byModel[model], frames(r.t, f))
	}
}

// hold makes every request for one model wait, so a test can look at a turn
// that has started and not finished. The returned function releases them.
func (r *replay) hold(model string) func() {
	gate := make(chan struct{})
	r.mu.Lock()
	r.held[model] = gate
	r.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
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
			r.mu.Lock()
			hold := r.Props
			r.mu.Unlock()
			if hold > 0 {
				time.Sleep(hold)
			}
			// A model-scoped read without autoload=false would load a model
			// on the router, so it fails here rather than in production.
			if model := req.URL.Query().Get("model"); model != "" {
				r.mu.Lock()
				r.Queries = append(r.Queries, req.URL.RawQuery)
				r.mu.Unlock()
				// A model-scoped read has to say which it is asking for. On a
				// router, autoload=true is what loads the model, so a request
				// that says neither fails here rather than in production.
				switch req.URL.Query().Get("autoload") {
				case "false":
				case "true":
					r.mu.Lock()
					r.Loads = append(r.Loads, model)
					r.mu.Unlock()
				default:
					http.Error(w, "model-scoped GET without autoload", 500)
					return
				}
				// One capture reports a template that takes named levels and
				// can hold thinking, the other reports neither.
				if model == "Delta3.8-27B Q5" {
					serveFixture(r.t, w, "props-reasoning.json")
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

	// The naming request ends on the title instruction. It answers from its
	// own fixture so the queued scripts stay lined up with the turns the test
	// is about.
	if msgs, _ := body["messages"].([]any); len(msgs) > 0 {
		last, _ := msgs[len(msgs)-1].(map[string]any)
		if content, _ := last["content"].(string); strings.HasPrefix(content, "Give the task quoted below a title.") {
			r.mu.Lock()
			r.Titles = append(r.Titles, body)
			r.Order = append(r.Order, "title")
			r.mu.Unlock()
			r.stream(req.Context(), w, frames(r.t, "stream_title.txt"))
			return
		}
	}

	model, _ := body["model"].(string)
	r.mu.Lock()
	gate := r.held[model]
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}

	r.mu.Lock()
	r.Requests = append(r.Requests, body)
	r.Order = append(r.Order, "chat")
	if r.NoVision && carriesImage(body) {
		r.mu.Unlock()
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"message":"Vision is disabled for this server"}}`)
		return
	}
	script := []string(nil)
	// A model with a queue of its own answers from it, so two agents talking
	// at once never take each other's script.
	if queued := r.byModel[model]; r.nextFor[model] < len(queued) {
		script = queued[r.nextFor[model]]
		r.nextFor[model]++
	} else if r.next < len(r.scripts) {
		script = r.scripts[r.next]
		r.next++
	}
	status := r.Status
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
	r.stream(req.Context(), w, script)
}

func (r *replay) stream(ctx context.Context, w http.ResponseWriter, script []string) {
	r.mu.Lock()
	delay := r.Delay
	r.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	f, _ := w.(http.Flusher)
	for _, frame := range script {
		if frame == holdMarker {
			r.pause()
			<-ctx.Done()
			return
		}
		fmt.Fprint(w, frame+"\n\n")
		if f != nil {
			f.Flush()
		}
		if delay > 0 {
			time.Sleep(delay)
		}
	}
}

// pause tells a test the script is spent and the connection is being held.
func (r *replay) pause() {
	r.mu.Lock()
	already := false
	select {
	case <-r.paused:
		already = true
	default:
	}
	r.mu.Unlock()
	if !already {
		close(r.paused)
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

// carriesImage reports whether any message in a chat body holds an image part.
func carriesImage(body map[string]any) bool {
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		msg, _ := m.(map[string]any)
		parts, _ := msg["content"].([]any)
		for _, p := range parts {
			if part, _ := p.(map[string]any); part["type"] == "image_url" {
				return true
			}
		}
	}
	return false
}
