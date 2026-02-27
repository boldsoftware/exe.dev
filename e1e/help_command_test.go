package e1e

import (
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestHelpCommandShowsNewOptions(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.SendLine("help new")
	pty.Want("Command: new")
	pty.Want("Options:")
	pty.Want("--name")
	pty.Want("--image")
	pty.WantPrompt()
	pty.Disconnect()

	pty = sshToExeDev(t, keyFile)
	pty.Reject(testinfra.Banner)
	pty.Reject("enter your email")
	pty.Reject("see a list of commands")
	pty.Want("create your first VM")
	pty.WantPrompt()
	pty.Disconnect()
}
