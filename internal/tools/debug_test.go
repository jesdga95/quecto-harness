package tools

import (
	"testing"

	"github.com/jesdga95/quecto-harness/internal/config"
)

// The debug view spells each parameter out in the order the schema writes it,
// which is the order the model sees it: a plain map sorts its keys, and an
// ordered schema is kept as written.
func TestTheDebugViewSpellsOutTheParameters(t *testing.T) {
	full := Offer{Set: config.ToolsetFull, Mode: config.WorkExecute, Ask: true, Agents: true, Message: true, Search: true}
	specs := DebugTools(full)
	if len(specs) != AllCount() {
		t.Fatalf("the view lists %d tools, want %d", len(specs), AllCount())
	}
	byName := make(map[string]ToolDebug, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
	}
	// write lists path before content, and the view keeps that order.
	w := byName["write"]
	if len(w.Params) != 2 || w.Params[0].Name != "path" || w.Params[1].Name != "content" {
		t.Errorf("write's parameters = %v, want path then content", w.Params)
	}
	if !w.Params[0].Required || !w.Params[1].Required {
		t.Errorf("write's parameters are not both required: %v", w.Params)
	}
	// read's offset is optional and keeps the floor its schema states.
	r := byName["read"]
	if len(r.Params) != 3 || !r.Params[2].Required {
		t.Errorf("read's parameters = %v", r.Params)
	}
	for _, p := range r.Params {
		if p.Name == "offset" && (p.Required || p.Minimum == nil || *p.Minimum != 1) {
			t.Errorf("read's offset = %v, want optional with a floor of 1", p)
		}
	}
	// bash's timeout carries a floor and a default, both of which the view states.
	for _, p := range byName["bash"].Params {
		if p.Name == "timeout_s" && (p.Required || p.Minimum == nil || *p.Minimum != 1) {
			t.Errorf("bash's timeout_s = %v, want optional with a floor of 1", p)
		}
	}
	// A minimal toolset spells out only its five.
	min := DebugTools(Offer{Set: config.ToolsetMinimal, Mode: config.WorkExecute})
	if len(min) != 5 {
		t.Errorf("the minimal view lists %d tools, want 5", len(min))
	}
}
