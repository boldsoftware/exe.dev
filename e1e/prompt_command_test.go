package e1e

import (
	"testing"
)

// TestPromptCommand verifies that the hidden "prompt" SSH command works end-to-end.
// It uses a mock Anthropic API (started by the test infra) that returns a canned
// tool_use + text response, exercising the full agentic loop through the SSH lobby.
func TestPromptCommand(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// Run the prompt command. The mock Anthropic server will:
	// 1. First call: return tool_use for "exe_command" with {"command": "ls"}
	// 2. Second call: return text "MOCK_PROMPT_RESULT: I found your VMs."
	// Then the loop will prompt for follow-up input.
	pty.Reject("internal error")
	pty.Reject("Raw error")
	pty.SendLine("prompt how many vms do i have")

	// Should see the prompt banner
	pty.Want("prompt")

	// Should see the mock model's text output
	pty.Want("I found your VMs")

	// The loop prompts for follow-up input with "> ". Send empty line to exit.
	pty.Want("> ")
	pty.SendLine("")

	// Should return to the exe.dev prompt
	pty.WantPrompt()

	pty.Disconnect()
}

// TestPromptCommandNoArgs verifies that prompt without arguments shows usage.
func TestPromptCommandNoArgs(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	pty.SendLine("prompt")
	pty.Want("usage:")
	pty.WantPrompt()

	pty.Disconnect()
}
