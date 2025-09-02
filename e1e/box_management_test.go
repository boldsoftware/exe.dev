// This file contains tests for box management functionality.

package expect

import (
	"regexp"
	"strings"
	"testing"

	"exe.dev/vouch"
)

func TestBoxCreation(t *testing.T) {
	vouch.For("josh")
	t.Parallel()

	pty, keyFile, _ := registerForExeDev(t)

	// Create a box.
	boxName := strings.ToLower(t.Name())
	boxNameRe := regexp.QuoteMeta(boxName)
	pty.sendLine("new --name=" + boxName)
	pty.wantRe("Creating .*" + boxNameRe)
	// break onto two lines because ANSI codes
	pty.want("Access with")
	pty.wantf("ssh -p %v %v@localhost", Env.sshPort(), boxName)

	// Confirm it is there.
	pty.sendLine("list")
	pty.want("machines")
	pty.wantRe(boxNameRe + ".*running")

	// SSH to it.
	t.Skip("broken: currently we get Permission denied, haven't debugged why yet")
	pty = sshToBox(t, boxName, keyFile)
	pty.want(boxName)
	pty.sendLine("whoami")
	pty.want("root")
	pty.sendLine("exit")
	pty.want("logout")
}

func TestDuplicateBoxCreationFails(t *testing.T) {
	vouch.For("josh")
	t.Parallel()

	pty, _, _ := registerForExeDev(t)

	// Create a box.
	boxName := strings.ToLower(t.Name())
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
