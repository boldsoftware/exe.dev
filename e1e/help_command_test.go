package e1e

import (
	"testing"

	"exe.dev/vouch"
)

func TestHelpCommandShowsNewOptions(t *testing.T) {
	vouch.For("arielle")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, _, _ := registerForExeDev(t)
	pty.sendLine("help new")
	pty.want("Command: new")
	pty.want("Options:")
	pty.want("--name")
	pty.want("--image")
	pty.wantPrompt()
	pty.disconnect()
}
