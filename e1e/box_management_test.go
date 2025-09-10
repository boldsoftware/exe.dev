// This file contains tests for box management functionality.

package e1e

import (
	"regexp"
	"testing"

	"exe.dev/vouch"
)

func TestSSHWorks(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, keyFile, _ := registerForExeDev(t)

	// Create a box.
	boxName := newBox(t, pty)
	pty.disconnect()

	// SSH to it.
	pty = sshToBox(t, boxName, keyFile)
	pty.reject("Permission denied") // fail fast on common known failure mode
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want("exedev")
	pty.want("\n") // exedev is also in the prompt! require a newline after it.
	pty.wantPrompt()
	pty.disconnect()
}

func TestDuplicateBoxCreationFails(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, _ := registerForExeDev(t)

	// Create a box.
	boxName := boxName(t)
	boxNameRe := regexp.QuoteMeta(boxName)
	pty.sendLine("new --name=" + boxName)
	pty.want("ssh") // wait for ssh instructions

	pty.sendLine("new --name=" + boxName)
	pty.wantRe("Box name .*" + boxNameRe + ".* is not available")
	pty.wantPrompt()
	pty.disconnect()
}

func TestBadBoxName(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, _, _ := registerForExeDev(t)

	// Create a box.
	boxName := "ThisIsNotAValidBoxName!"
	boxNameRe := regexp.QuoteMeta(boxName)
	pty.sendLine("new --name=" + boxName)
	pty.wantRe("Invalid box name .*" + boxNameRe)
	pty.wantPrompt()
	pty.disconnect()
}
