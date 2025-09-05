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
}

func TestRegistrationHappensOnce(t *testing.T) {
	vouch.For("josh")
	t.Parallel()

	keyFile, publicKey := genSSHKey(t)

	// initial registration
	pty := sshToExeDev(t, keyFile)
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
	pty.want(ps1)
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)

	// second login: no re-registration, no banner
	pty = sshToExeDev(t, keyFile)
	pty.reject(banner)
	pty.want(ps1)
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
}
