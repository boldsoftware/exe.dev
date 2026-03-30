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

// TestPromptCommandNoArgs verifies that prompt without arguments enters
// interactive mode and lets the user type the initial prompt.
func TestPromptCommandNoArgs(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// Run prompt with no args — should show banner and prompt for input.
	pty.Reject("internal error")
	pty.Reject("Raw error")
	pty.SendLine("prompt")
	pty.Want("prompt")
	pty.Want("> ")

	// Type the initial prompt — mock will process it like any other prompt.
	pty.SendLine("how many vms do i have")
	pty.Want("I found your VMs")

	// Exit the follow-up prompt.
	pty.Want("> ")
	pty.SendLine("")
	pty.WantPrompt()

	pty.Disconnect()
}

// TestPromptSuggestCommand verifies the suggest_command tool flow.
// The mock returns a suggest_command tool call; the user approves it,
// and the command runs successfully.
func TestPromptSuggestCommand(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// The initial prompt contains "suggest-test" which triggers the suggest scenario
	// in the mock. The mock returns suggest_command(help).
	pty.Reject("internal error")
	pty.Reject("Raw error")
	pty.SendLine("prompt suggest-test")

	// Should see the prompt banner
	pty.Want("prompt")

	// Should see the suggest_command prompt asking user to approve
	pty.Want("Run this command?")
	pty.Want("help")
	pty.Want("[y/N]")

	// Approve it
	pty.SendLine("y")

	// Should see the mock model's final text output
	pty.Want("MOCK_SUGGEST_DONE")

	// The loop prompts for follow-up input with "> ". Send empty line to exit.
	pty.Want("> ")
	pty.SendLine("")

	// Should return to the exe.dev prompt
	pty.WantPrompt()

	pty.Disconnect()
}
