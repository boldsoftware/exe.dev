package e1e

import (
	"testing"

	"exe.dev/vouch"
)

func TestHelpCommandShowsNewOptions(t *testing.T) {
	vouch.For("arielle")
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
	pty.reject(banner)
	pty.reject("enter your email")
	pty.reject("see a list of commands")
	pty.want("create your first box")
	pty.want("login on the web")
	pty.wantPrompt()
	pty.disconnect()
}
