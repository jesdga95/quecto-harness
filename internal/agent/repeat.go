package agent

import (
	"encoding/json"

	"quecto-harness/internal/llm"
	"quecto-harness/internal/prompts"
)

// repeats counts the identical tool calls a turn has made in a row. Two calls
// are identical when the name and the arguments match, key order aside.
type repeats struct {
	key string
	n   int
}

// untracked calls change only the session's own records, and are made again
// by design, so they neither count nor break a run of other calls.
var untracked = map[string]bool{"todo_write": true, "write_plan": true}

// see counts one call and answers with the note its result carries, empty
// below the thresholds.
func (r *repeats) see(call llm.ToolCall) string {
	name := call.Function.Name
	if untracked[name] {
		return ""
	}
	key := name + "\x00" + canonical(call.Function.Arguments)
	if key == r.key {
		r.n++
	} else {
		r.key, r.n = key, 1
	}
	switch {
	case r.n == 3:
		return prompts.Repeat(name, r.n, false)
	case r.n == 5 || r.n >= 8:
		return prompts.Repeat(name, r.n, true)
	}
	return ""
}

// reset starts counting again, for when a message from outside the turn
// arrives and may have changed what a call would find.
func (r *repeats) reset() {
	r.key, r.n = "", 0
}

// canonical renders arguments with their keys sorted, which is what
// encoding/json does for a map. Arguments that do not decode are compared as
// they were sent.
func canonical(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(out)
}
