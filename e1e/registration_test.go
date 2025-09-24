// This file tests registration flows.

package e1e

import (
	"regexp"
	"testing"

	"exe.dev/vouch"
)

func TestNewKeyRegistration(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(banner)
	pty.want("Please enter your email")
	email := t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	pty.wantRe("Verification code: .*[0-9]{6}.*")
	emailMsg := Env.email.waitForEmail(t, email)
	clickVerifyLinkInEmail(t, emailMsg)
	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Press any key to continue")
	pty.sendLine("")
	pty.want("Welcome to EXE.DEV!") // check that we show welcome message for users who haven't created boxes
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.disconnect()
}

func TestRegistrationHappensOnce(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyFile, publicKey := genSSHKey(t)

	// initial registration
	pty := sshToExeDev(t, keyFile)
	pty.want("Please enter your email")
	email := t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	pty.wantRe("Verification code: .*[0-9]{6}.*")
	emailMsg := Env.email.waitForEmail(t, email)
	clickVerifyLinkInEmail(t, emailMsg)
	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Press any key to continue")
	pty.sendLine("")
	// Check that we show welcome message for first login.
	pty.want("Welcome to EXE.DEV!")
	pty.want("To create your first box, run:")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()
	pty.disconnect()

	// second login: no re-registration, but should still show welcome since user hasn't created boxes
	pty = sshToExeDev(t, keyFile)
	pty.reject(banner)
	pty.reject("Please enter your email")
	// No registration flow, no welcome message
	// but should still hint about how to create boxes,
	// because they haven't yet.
	pty.want("To create your first box, run:")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()

	pty.disconnect()
}
