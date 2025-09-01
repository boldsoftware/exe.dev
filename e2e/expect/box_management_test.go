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

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(banner)

	pty.want("Please enter your email")
	email := t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))

	emailMsg := Env.email.waitForEmail(t, email)
	clickVerifyLinkInEmail(t, emailMsg)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Press any key to continue")
	pty.sendLine("")
	pty.want("commands:") // check that we show help menu on first login
	pty.wantRe("exe\\.dev.*▶")

	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)

	// Create a box.
	boxName := "test-box-" + strings.ToLower(t.Name())
	boxNameRe := regexp.QuoteMeta(boxName)
	pty.sendLine("new --name=" + boxName)
	pty.wantRe("Creating .*" + boxNameRe)
	t.Skip("broken: fails with 'subnet 10.179.0.0/24 overlaps with other one on this address space'")
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
