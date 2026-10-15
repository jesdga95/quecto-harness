package agent

import (
	"testing"
	"time"

	"github.com/jesdga95/quecto-harness/internal/store"
)

// A turn can lose one call to a stop and lose another to a failure whose
// server reported nothing. unrecorded counts every call missing from the sums,
// and stopped the ones among them this harness cut short, so stopped is never
// larger than unrecorded. A cut call whose report arrived is in the sums and
// belongs to neither figure.
func TestUncountedCallsAreNamedByWhyTheyAreMissing(t *testing.T) {
	entries := []store.Entry{
		{Kind: "user"},
		{Kind: "assistant", Usage: &store.CallUsage{Reported: true, Stopped: true, Prompt: 900, Completion: 20}},
		{Kind: "status", Usage: &store.CallUsage{Stopped: true}},
		{Kind: "status", Usage: &store.CallUsage{}},
	}
	card := turnUsageOf(entries, 0).fields(3*time.Second, time.Second, "", "")
	if card["requests"] != 3 {
		t.Errorf("requests = %v, want all three calls after the message counted", card["requests"])
	}
	if card["unrecorded"] != 2 || card["stopped"] != 1 {
		t.Errorf("card = unrecorded %v, stopped %v, want two calls missing and one of them cut",
			card["unrecorded"], card["stopped"])
	}
	if card["tokens"] != 20 || card["input_tokens"] != 900 {
		t.Errorf("card = tokens %v, input_tokens %v, want the report that arrived counted once",
			card["tokens"], card["input_tokens"])
	}
}
