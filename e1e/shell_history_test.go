package e1e

import (
	"testing"
)

func TestShellHistoryPersistence(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// First session: register and run a command
	pty, _, keyFile, _ := registerForExeDev(t)
	pty.sendLine("ls")
	pty.want("No VMs found")
	pty.wantPrompt()
	pty.disconnect()

	// Second session: reconnect and use up arrow to recall history
	pty = sshToExeDev(t, keyFile)
	pty.wantPrompt()

	// History is: [..., "whoami", "ls", "exit"]. Up arrow twice skips "exit" to get "ls".
	pty.send("\x1b[A\x1b[A\n")
	pty.want("No VMs found")

	pty.disconnect()
}
