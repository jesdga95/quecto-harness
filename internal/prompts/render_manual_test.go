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
			{Name: "Reviewer", Role: "Reviews Go and does not write it.", Slots: 1},
			{Name: "Local llama.cpp", Slots: 2},
		},
		Tools: []string{"read", "grep", "write", "edit", "bash", "subagent", "ask_user_question"},
	}))
	t.Log("\n=== a subagent ===\n" + System(SystemVars{
		Project: "/src/x", OS: "darwin", CanMessage: true,
		Launcher: "sess_aaaaaaaaaaaaaaaa", Instructions: "Answer in British English.",
	}))
}
