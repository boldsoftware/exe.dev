package e1e

import (
	"testing"
)

func TestLineContinuation(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// Simple continuation: "l\" + "s" = "ls"
	pty.Send("l\\\n")
	pty.Want("> ")
	pty.SendLine("s")
	pty.Want("No VMs found")
	pty.WantPrompt()

	// Multi-line continuation: "whoam\" + "i" = "whoami"
	pty.Send("whoam\\\n")
	pty.Want("> ")
	pty.SendLine("i")
	pty.Want("@")
	pty.WantPrompt()

	// Cancel a continuation with Ctrl-D, verify REPL stays usable
	pty.Send("partial\\\n")
	pty.Want("> ")
	pty.Send("\x04")
	pty.WantPrompt()

	// Confirm the REPL accepted the cancellation and still works
	pty.SendLine("whoami")
	pty.Want("@")
	pty.WantPrompt()

	pty.Disconnect()
}
