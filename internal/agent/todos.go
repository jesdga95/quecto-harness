package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"quecto-harness/internal/store"
	"quecto-harness/internal/tools"
)

// maxTodos bounds a plan. A model that writes more than this is not planning.
const maxTodos = 20

// writeTodos replaces the session's plan with the one the model sent and
// hands it straight back, so the model reads its own plan in the tool result.
func (e *Engine) writeTodos(s *Session, raw json.RawMessage) (tools.Result, error) {
	todos, err := parseTodos(raw)
	if err != nil {
		return tools.Result{}, err
	}
	s.mu.Lock()
	s.Todos = todos
	s.mu.Unlock()

	e.publish(s.ID, ev("todos", map[string]any{"todos": todos}))
	return tools.Result{Output: renderTodos(todos)}, nil
}

// The three states a step can be in.
const (
	todoPending    = "pending"
	todoInProgress = "in_progress"
	todoCompleted  = "completed"
)

// parseTodos refuses anything it would otherwise have to guess at: an unknown
// key or status, an empty step or one written twice. The model is told what
// was wrong and sends the list again.
func parseTodos(raw json.RawMessage) ([]store.Todo, error) {
	var args struct {
		Todos []store.Todo `json:"todos"`
	}
	if len(raw) == 0 {
		return nil, errors.New("todo_write: the arguments were empty")
	}
	if !json.Valid(raw) {
		return nil, errors.New("todo_write: the arguments are not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&args); err != nil {
		return nil, fmt.Errorf("todo_write: %s: send todos, each item with content and status only", err)
	}
	if args.Todos == nil {
		return nil, errors.New("todo_write: todos is missing: send the whole list, or an empty one to clear it")
	}
	if len(args.Todos) > maxTodos {
		return nil, fmt.Errorf("todo_write: %d steps, keep the plan under %d", len(args.Todos), maxTodos)
	}
	seen := make(map[string]bool, len(args.Todos))
	out := make([]store.Todo, 0, len(args.Todos))
	for i, t := range args.Todos {
		content := strings.TrimSpace(t.Content)
		if content == "" {
			return nil, fmt.Errorf("todo_write: item %d has no content", i+1)
		}
		switch t.Status {
		case todoPending, todoInProgress, todoCompleted:
		default:
			return nil, fmt.Errorf("todo_write: item %d has status %q: use pending, in_progress or completed", i+1, t.Status)
		}
		if seen[content] {
			return nil, fmt.Errorf("todo_write: %q is listed twice: each step appears once", content)
		}
		seen[content] = true
		out = append(out, store.Todo{Content: content, Status: t.Status})
	}
	return out, nil
}

// renderTodos is the plan as the model is shown it back.
func renderTodos(todos []store.Todo) string {
	if len(todos) == 0 {
		return "the plan is empty"
	}
	marks := map[string]string{todoCompleted: "x", todoInProgress: ">", todoPending: " "}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", countTodos(todos))
	for _, t := range todos {
		fmt.Fprintf(&b, "- [%s] %s\n", marks[t.Status], t.Content)
	}
	return b.String()
}

func tallyTodos(todos []store.Todo) (done, doing int) {
	for _, t := range todos {
		switch t.Status {
		case todoCompleted:
			done++
		case todoInProgress:
			doing++
		}
	}
	return done, doing
}

// carriesTodos reports whether a compaction sends the plan: only when a
// todo_write, or the approval that seeded it, set it since the request began.
// A steer does not restart the request. The approved plan in the system prompt
// has no marks, so its progress is carried like any other plan.
func (s *Session) carriesTodos() bool {
	if done, _ := tallyTodos(s.Todos); done == len(s.Todos) {
		return false
	}
	current := renderTodos(s.Todos)
	for i := len(s.Entries) - 1; i >= 0; i-- {
		e := s.Entries[i]
		if e.Meta["kickoff"] == true {
			return s.Plan != nil && renderTodos(seedTodos(*s.Plan)) == current
		}
		if e.Kind == "user" && e.From == "" && e.Meta["steer"] != true && e.Meta["continue"] != true {
			return false
		}
		if e.Kind == "tool" && e.ToolName == "todo_write" && e.Content == current {
			return true
		}
	}
	return false
}

// countTodos is the one line the collapsed row and the tool result share.
func countTodos(todos []store.Todo) string {
	done, doing := tallyTodos(todos)
	if doing > 0 {
		return fmt.Sprintf("%d of %d done, %d in hand", done, len(todos), doing)
	}
	return fmt.Sprintf("%d of %d done", done, len(todos))
}
