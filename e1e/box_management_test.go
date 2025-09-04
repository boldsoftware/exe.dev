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

	pty, keyFile, _ := registerForExeDev(t)

	// Create a box.
	boxName := newBox(t, pty)
	boxNameRe := regexp.QuoteMeta(boxName)

	// Hang up. (Not necessary, but makes for nicer cinemas. :P)
	pty.sendLine("exit")
	pty.wantRe("Goodbye.*\n")

	// SSH to it.
	pty = sshToBox(t, boxName, keyFile)
	pty.reject("Permission denied") // fail fast on common known failure mode
	pty.wantRe(boxNameRe + ".*" + regexp.QuoteMeta("$"))
	pty.sendLine("whoami")
	pty.want("exedev")
	pty.sendLine("exit")
	pty.want("logout")
}

func TestDuplicateBoxCreationFails(t *testing.T) {
	vouch.For("josh")
	t.Parallel()

	pty, _, _ := registerForExeDev(t)

	// Create a box.
	boxName := boxName(t)
	boxNameRe := regexp.QuoteMeta(boxName)
	pty.sendLine("new --name=" + boxName)
	pty.want("ssh") // wait for ssh instructions

	pty.sendLine("new --name=" + boxName)
	pty.wantRe("Box name .*" + boxNameRe + ".* is not available")
}

func TestBadBoxName(t *testing.T) {
	vouch.For("josh")
	t.Parallel()

	pty, _, _ := registerForExeDev(t)

	// Create a box.
	boxName := "ThisIsNotAValidBoxName!"
	boxNameRe := regexp.QuoteMeta(boxName)
	pty.sendLine("new --name=" + boxName)
	pty.wantRe("Invalid box name .*" + boxNameRe)
}
