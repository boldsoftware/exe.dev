// This file tests registration flows.

package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"exe.dev/ghuser"
	"exe.dev/vouch"
)

func TestNewKeyRegistration(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(banner)
	expectSSHRegistrationPrompt(t, pty)
	email := t.Name() + "@example.com"
	pty.sendLine(email)
	expectVerificationCodePrompt(t, pty, email)
	code := waitForVerificationCodeEmail(t, email)
	pty.sendLine(code)
	expectRegistrationComplete(t, pty, true, email)
	pty.want("Welcome to EXE.DEV!") // check that we show welcome message for users who haven't created boxes
	pty.want("create your first box")
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
	pty.want(banner)
	expectSSHRegistrationPrompt(t, pty)
	email := t.Name() + "@example.com"
	pty.sendLine(email)
	expectVerificationCodePrompt(t, pty, email)
	code := waitForVerificationCodeEmail(t, email)
	pty.sendLine(code)
	expectRegistrationComplete(t, pty, true, email)
	// Check that we show welcome message for first login.
	pty.want("Welcome to EXE.DEV!")
	pty.want("create your first box")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()
	pty.disconnect()

	// second login: no re-registration, but should still show welcome since user hasn't created boxes
	pty = sshToExeDev(t, keyFile)
	pty.reject(banner)
	pty.reject("Verification code sent")
	pty.reject("Email:")
	// No registration flow, no welcome message
	// but should still hint about how to create boxes,
	// because they haven't yet.
	pty.want("create your first box")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()

	pty.disconnect()
}

func TestRegisterMultipleKeys(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	for i := range 3 {
		keyFile, publicKey := genSSHKey(t)
		pty := sshToExeDev(t, keyFile)
		pty.want(banner)
		expectSSHRegistrationPrompt(t, pty)
		email := t.Name() + "@example.com"
		pty.sendLine(email)
		expectVerificationCodePrompt(t, pty, email)
		code := waitForVerificationCodeEmail(t, email)
		pty.sendLine(code)
		expectRegistrationComplete(t, pty, i == 0, email)
		if i == 0 {
			pty.want("Welcome to EXE.DEV!") // welcome message only on first time
			pty.want("create your first box")
		}
		pty.wantPrompt()
		pty.sendLine("whoami")
		pty.want(email)
		pty.want(publicKey)
		pty.disconnect()
	}
}

func TestRegisterWebThenKey(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	email := t.Name() + "@example.com"
	baseURL := fmt.Sprintf("http://localhost:%d", Env.exed.HTTPPort)

	resp, err := http.PostForm(baseURL+"/m/email-auth", url.Values{"email": {email}})
	if err != nil {
		t.Fatalf("POST /m/email-auth: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d from /m/email-auth: %s", resp.StatusCode, string(body))
	}

	// Verify the email using the mobile flow link
	emailMsg := Env.email.waitForEmail(t, email)
	mobileRe := regexp.MustCompile(`http://localhost:\d+/m/verify-token\?token=[a-zA-Z0-9]+`)
	verifyLink := mobileRe.FindString(emailMsg.Body)
	if verifyLink == "" {
		t.Fatalf("did not find mobile verify link in email:\n%s", emailMsg.Body)
	}

	verifyResp, err := http.Get(verifyLink)
	if err != nil {
		t.Fatalf("GET mobile verify link: %v", err)
	}
	verifyRespBody, _ := io.ReadAll(verifyResp.Body)
	verifyResp.Body.Close()
	if verifyResp.StatusCode != http.StatusOK && verifyResp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("mobile verify returned status %d body %q", verifyResp.StatusCode, string(verifyRespBody))
	}

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(banner)
	expectSSHRegistrationPrompt(t, pty)
	pty.sendLine(email)
	expectVerificationCodePrompt(t, pty, email)
	code := waitForVerificationCodeEmail(t, email)
	pty.sendLine(code)

	expectRegistrationComplete(t, pty, false, email)
	pty.want("create your first box")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()
	pty.disconnect()
}

func TestRegisterGitHubKey(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyDir := t.TempDir()
	keyFile := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte(ghuser.FakePrivateKey0), 0o600); err != nil {
		t.Fatalf("failed to write GitHub private key: %v", err)
	}

	pty := sshToExeDev(t, keyFile)
	pty.want(banner)
	expectSSHRegistrationPrompt(t, pty)
	email := ghuser.FakeEmail0
	pty.sendLine(email)
	expectVerificationCodePrompt(t, pty, email)
	code := waitForVerificationCodeEmail(t, email)
	pty.sendLine(code)
	expectRegistrationComplete(t, pty, true, email)
	pty.want("Welcome to EXE.DEV!")
	pty.want("create your first box")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(ghuser.FakePublicKey0)
	pty.wantPrompt()
	pty.disconnect()
}

func TestRegisterGitHubKeyUnderDifferentEmail(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyDir := t.TempDir()
	keyFile := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte(ghuser.FakePrivateKey1), 0o600); err != nil {
		t.Fatalf("failed to write GitHub private key: %v", err)
	}

	pty := sshToExeDev(t, keyFile)
	pty.want(banner)
	expectSSHRegistrationPrompt(t, pty)
	// change email from "fake-for-tests@example.com" to "fake-for-tests@example.combinatorics"
	suffix := "binatorics"
	newEmail := ghuser.FakeEmail1 + suffix
	pty.sendLine(newEmail)
	expectVerificationCodePrompt(t, pty, newEmail)
	code := waitForVerificationCodeEmail(t, newEmail)
	pty.sendLine(code)
	expectRegistrationComplete(t, pty, true, newEmail)
	pty.want("Welcome to EXE.DEV!")
	pty.want("create your first box")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(newEmail)
	pty.want(ghuser.FakePublicKey1)
	pty.wantPrompt()
	pty.disconnect()
}

// TestSSHTerminalInputDuringRegistration verifies that terminal input works
// character-by-character at the email prompt during registration.
// (We had early issues with ssh input buffers.)
func TestSSHTerminalInputDuringRegistration(t *testing.T) {
	vouch.For("josh")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(banner)
	expectSSHRegistrationPrompt(t, pty)

	email := t.Name() + "@example.com"

	// Type the email one character at a time to simulate interactive typing.
	for _, ch := range email {
		pty.send(string(ch))
	}
	pty.send("\n")

	expectVerificationCodePrompt(t, pty, email)
	code := waitForVerificationCodeEmail(t, email)
	pty.sendLine(code)
	expectRegistrationComplete(t, pty, true, email)

	// After first-time registration, we show a welcome message and a prompt.
	pty.want("Welcome to EXE.DEV!")
	pty.want("create your first box")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.disconnect()
}
