package e1e

import (
	"testing"
)

func TestShellHistoryPersistence(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// First session: register and run a command
	pty, _, keyFile, _ := registerForExeDev(t)
	pty.SendLine("ls")
	pty.Want("No VMs found")
	pty.WantPrompt()
	pty.Disconnect()

	// Second session: reconnect and use up arrow to recall history
	pty = sshToExeDev(t, keyFile)
	pty.WantPrompt()

	// History is: [..., "whoami", "ls", "exit"]. Up arrow twice skips "exit" to get "ls".
	pty.Send("\x1b[A\x1b[A\n")
	pty.Want("No VMs found")

	pty.Disconnect()
}
