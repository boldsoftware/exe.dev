package e1e

import (
	"fmt"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestDefaultsCommand tests the hidden defaults command.
func TestDefaultsCommand(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDev(t)

	// Test defaults write/read/delete cycle
	pty.SendLine("defaults write dev.exe new-vm-email false")
	pty.WantPrompt()

	pty.SendLine("defaults read dev.exe new-vm-email")
	pty.Want("false")
	pty.WantPrompt()

	pty.SendLine("defaults read dev.exe")
	pty.Want("new-vm-email: false")
	pty.WantPrompt()

	pty.SendLine("defaults delete dev.exe new-vm-email")
	pty.WantPrompt()

	pty.SendLine("defaults read dev.exe new-vm-email")
	pty.Want("(not set)")
	pty.WantPrompt()

	// Now test that the default actually suppresses email.
	// Set new-vm-email to false, then create a VM and verify no email is sent.
	pty.SendLine("defaults write dev.exe new-vm-email off")
	pty.WantPrompt()

	// Poison the inbox - email server will panic if email arrives.
	Env.servers.Email.PoisonInbox(email)

	boxName := boxName(t)
	if len(boxName) < 52 {
		boxName += strings.Repeat("b", 52-len(boxName))
		testinfra.AddCanonicalization(boxName, "BOX_NAME")
	}
	pty.SendLine(fmt.Sprintf("new --name=%s", boxName))
	pty.WantRE("Creating .*" + boxName)
	pty.Want("Ready")
	pty.WantPrompt()

	// If we got here without the email server panicking, the default worked.

	// Clean up
	cleanupBox(t, keyFile, boxName)
}
