package agent

import (
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

func parseTodos(raw json.RawMessage) ([]store.Todo, error) {
	var args struct {
		Todos []struct {
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"todos"`
	}
	if len(raw) == 0 {
		return nil, errors.New("write_todos: the arguments were empty")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("write_todos: the arguments are not valid JSON: %s", err)
	}
	if len(args.Todos) > maxTodos {
		return nil, fmt.Errorf("write_todos: %d steps, keep the plan under %d", len(args.Todos), maxTodos)
	}
	out := make([]store.Todo, 0, len(args.Todos))
	for _, t := range args.Todos {
		text := strings.TrimSpace(t.Text)
		if text == "" {
			continue
		}
		out = append(out, store.Todo{Text: text, Status: todoStatus(t.Status)})
	}
	return out, nil
}

// todoStatus keeps the three states the schema names. Anything else is a step
// nobody has started.
func todoStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "doing", "in_progress", "active":
		return "doing"
	case "done", "completed":
		return "done"
	}
	return "todo"
}

// renderTodos is the plan as the model is shown it back.
func renderTodos(todos []store.Todo) string {
	if len(todos) == 0 {
		return "the plan is empty"
	}
	marks := map[string]string{"done": "x", "doing": ">", "todo": " "}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", countTodos(todos))
	for _, t := range todos {
		fmt.Fprintf(&b, "- [%s] %s\n", marks[t.Status], t.Text)
	}
	return b.String()
}

// countTodos is the one line the collapsed row and the tool result share.
func countTodos(todos []store.Todo) string {
	done, doing := 0, 0
	for _, t := range todos {
		switch t.Status {
		case "done":
			done++
		case "doing":
			doing++
		}
	}
	if doing > 0 {
		return fmt.Sprintf("%d of %d done, %d in hand", done, len(todos), doing)
	}
	return fmt.Sprintf("%d of %d done", done, len(todos))
}
