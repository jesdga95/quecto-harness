package prompts

import (
	"os"
	"testing"
)

// Prints both shapes of the system prompt, for reading the wording rather than
// asserting on it. Skipped unless QH_SHOW is set.
func TestShowSystemPrompt(t *testing.T) {
	if os.Getenv("QH_SHOW") == "" {
		t.Skip("set QH_SHOW to print the system prompt")
	}
	t.Log("\n=== a launcher ===\n" + System(SystemVars{
		Project: "/src/x", OS: "darwin", CanAsk: true, CanMessage: true,
		Agents: []AgentBrief{
			{Name: "Reviewer", Role: "Reviews Go and does not write it."},
			{Name: "Local llama.cpp"},
		},
	}))
	t.Log("\n=== a subagent ===\n" + System(SystemVars{
		Project: "/src/x", OS: "darwin", CanMessage: true,
		Launcher: "Local llama.cpp", Instructions: "Answer in British English.",
	}))
}
