package agent

import (
	"fmt"

	"quecto-harness/internal/config"
	"quecto-harness/internal/llm"
	"quecto-harness/internal/prompts"
	"quecto-harness/internal/store"
)

// Bounds on carrying on with an approved plan. A turn is sent back at most
// maxCarryRounds times, and stops being sent back once two rounds in a row
// completed no step.
const (
	maxCarryRounds = 20
	maxStalled     = 2
)

// carry is one turn's record of sending the model back to its plan.
type carry struct {
	rounds  int
	stalled int
	done    int
}

func newCarry() carry {
	return carry{done: -1}
}

// carryOn sends a turn that ended short of its approved plan back to the next
// step, and reports whether it did. A reply cut off at the token limit, a
// subagent, a session with no plan and a turn answering a later request are
// left to end.
func (e *Engine) carryOn(s *Session, res llm.Result, mode config.WorkMode, c *carry) bool {
	if mode != config.WorkExecute || llm.CutOff(res.FinishReason) {
		return false
	}
	s.mu.Lock()
	if s.Parent != "" || s.Plan == nil || len(s.Todos) == 0 || !s.onKickoff() {
		s.mu.Unlock()
		return false
	}
	done, _ := tallyTodos(s.Todos)
	total, next := len(s.Todos), nextTodo(s.Todos)
	if done == total {
		s.mu.Unlock()
		return false
	}
	if done > c.done {
		c.stalled = 0
	} else {
		c.stalled++
	}
	c.done = done
	if c.stalled >= maxStalled || c.rounds >= maxCarryRounds {
		s.mu.Unlock()
		return false
	}
	c.rounds++
	s.appendEntry(store.Entry{Kind: "user", Role: "user",
		Content: prompts.PlanContinue(total-done, total, next), InContext: true, Visible: false,
		Meta: map[string]any{"mode": string(config.WorkExecute), "continue": true}})
	note := s.appendEntry(store.Entry{Kind: "status", InContext: false, Visible: true,
		Content: fmt.Sprintf("The plan has %d of %d steps left: carrying on", total-done, total)})
	s.mu.Unlock()
	e.save(s)
	e.publish(s.ID, ev("status_entry", map[string]any{"content": note.Content}))
	return true
}

// onKickoff reports whether the request the turn answers is the one an
// approved plan started. A message sent since asks for something else, and the
// plan is not pushed on top of it. The caller holds the lock.
func (s *Session) onKickoff() bool {
	i := s.lastUserEntry()
	return i >= 0 && s.Entries[i].Meta["kickoff"] == true
}

// nextTodo is the step to take next: the one in progress, else the first not
// started.
func nextTodo(todos []store.Todo) string {
	for _, t := range todos {
		if t.Status == todoInProgress {
			return t.Content
		}
	}
	for _, t := range todos {
		if t.Status == todoPending {
			return t.Content
		}
	}
	return ""
}
