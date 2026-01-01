package e1e

import (
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestHelpCommandShowsNewOptions(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	pty.sendLine("help new")
	pty.want("Command: new")
	pty.want("Options:")
	pty.want("--name")
	pty.want("--image")
	pty.wantPrompt()
	pty.disconnect()

	pty = sshToExeDev(t, keyFile)
	pty.reject(testinfra.Banner)
	pty.reject("enter your email")
	pty.reject("see a list of commands")
	pty.want("create your first VM")
	pty.wantPrompt()
	pty.disconnect()
}
