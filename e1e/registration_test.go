// This file tests registration flows.

package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	"exe.dev/ghuser"
)

func TestNewKeyRegistration(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(testinfra.Banner)
	pty.want("Please enter your email")
	email := t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.wantRe("Pairing code: .*[0-9]{6}.*")
	waitForEmailAndVerify(t, email)
	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Welcome to EXE.DEV!") // check that we show welcome message for users who haven't created boxes
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.disconnect()
}

func TestRegistrationHappensOnce(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyFile, publicKey := genSSHKey(t)

	// initial registration
	pty := sshToExeDev(t, keyFile)
	pty.want("Please enter your email")
	email := t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.wantRe("Pairing code: .*[0-9]{6}.*")
	waitForEmailAndVerify(t, email)
	pty.want("Email verified successfully")
	pty.want("Registration complete")
	// Check that we show welcome message for first login.
	pty.want("Welcome to EXE.DEV!")
	pty.want("create your first VM")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()
	pty.disconnect()

	// second login: no re-registration, but should still show welcome since user hasn't created boxes
	pty = sshToExeDev(t, keyFile)
	pty.reject(testinfra.Banner)
	pty.reject("Please enter your email")
	// No registration flow, no welcome message
	// but should still hint about how to create boxes,
	// because they haven't yet.
	pty.want("create your first VM")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()

	pty.disconnect()
}

func TestRegisterMultipleKeys(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	for i := range 3 {
		keyFile, publicKey := genSSHKey(t)
		pty := sshToExeDev(t, keyFile)
		pty.want(testinfra.Banner)
		pty.want("Please enter your email")
		email := t.Name() + "@example.com"
		pty.sendLine(email)
		pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
		// pty.wantRe("Pairing code: .*[0-9]{6}.*")
		waitForEmailAndVerify(t, email)
		pty.want("Email verified successfully")
		pty.want("Registration complete")
		if i == 0 {
			pty.wantRe("account.*created")
		} else {
			pty.wantRe("key.*added")
		}
		if i == 0 {
			pty.want("Welcome to EXE.DEV!") // welcome message only on first time
		}
		pty.wantPrompt()
		pty.sendLine("whoami")
		pty.want(email)
		pty.want(publicKey)
		pty.disconnect()
	}
}

func TestRegisterWebThenKey(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	email := t.Name() + "@example.com"
	baseURL := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)

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
	emailMsg, err := Env.servers.Email.WaitForEmail(email)
	if err != nil {
		t.Fatal(err)
	}
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
	pty.want(testinfra.Banner)
	pty.want("Please enter your email")
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.wantRe("Pairing code: .*[0-9]{6}.*")

	waitForEmailAndVerify(t, email)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Your new ssh key has been added to your existing account.")
	pty.want("Welcome to EXE.DEV!")
	pty.want("create your first VM")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.wantPrompt()
	pty.disconnect()
}

func TestRegisterGitHubKey(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyDir := t.TempDir()
	keyFile := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte(ghuser.FakePrivateKey0), 0o600); err != nil {
		t.Fatalf("failed to write GitHub private key: %v", err)
	}

	pty := sshToExeDev(t, keyFile)
	pty.want(testinfra.Banner)
	pty.want("Email:")
	pty.want("fake-for-tests@example.com")
	pty.sendLine("")

	pty.want("Welcome to EXE.DEV!")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want("fake-for-tests@example.com")
	pty.want(ghuser.FakePublicKey0)
	pty.wantPrompt()
	pty.disconnect()
}

func TestRegisterGitHubKeyUnderDifferentEmail(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	keyDir := t.TempDir()
	keyFile := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte(ghuser.FakePrivateKey1), 0o600); err != nil {
		t.Fatalf("failed to write GitHub private key: %v", err)
	}

	pty := sshToExeDev(t, keyFile)
	pty.want(testinfra.Banner)
	pty.want("Email:")
	pty.want(ghuser.FakeEmail1)
	// change email from "fake-for-tests@example.com" to "fake-for-tests@example.combinatorics"
	suffix := "binatorics"
	// This triggers a verification email, despite the known SSH key.
	pty.sendLine(suffix)
	newEmail := ghuser.FakeEmail1 + suffix

	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(newEmail))
	// pty.wantRe("Pairing code: .*[0-9]{6}.*")

	waitForEmailAndVerify(t, newEmail)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Welcome to EXE.DEV!")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want("fake-for-tests@example.com")
	pty.want(ghuser.FakePublicKey1)
	pty.wantPrompt()
	pty.disconnect()
}

// TestSSHTerminalInputDuringRegistration verifies that terminal input works
// character-by-character at the email prompt during registration.
// (We had early issues with ssh input buffers.)
func TestSSHTerminalInputDuringRegistration(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(testinfra.Banner)
	pty.want("Please enter your email")

	email := t.Name() + "@example.com"

	// Type the email one character at a time to simulate interactive typing.
	for _, ch := range email {
		pty.send(string(ch))
	}
	pty.send("\n")

	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.wantRe("Pairing code: .*[0-9]{6}.*")

	waitForEmailAndVerify(t, email)

	pty.want("Email verified successfully")
	pty.want("Registration complete")

	// After first-time registration, we show a welcome message and a prompt.
	pty.want("Welcome to EXE.DEV!")
	pty.want("create your first VM")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.disconnect()
}

// TestRegistrationWithLatency tests that registration works correctly
// even when there is significant network latency.
func TestRegistrationWithLatency(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t) // real banner makes for ugly golden files

	// Add extra latency between us and the repl.
	proxy, err := testinfra.NewTCPProxy("add_latency")
	if err != nil {
		t.Fatalf("failed to create latency proxy: %v", err)
	}
	proxy.SetLatency(100 * time.Millisecond)
	proxy.SetDestPort(Env.servers.SSHPiperd.Port)

	go proxy.Serve(Env.context(t))
	t.Cleanup(proxy.Close)

	keyFile, publicKey := genSSHKey(t)

	// Use "real_banner_please" as the username to trigger the real banner.
	pty := makePty(t, "ssh localhost with latency")
	sshArgs := testinfra.SSHOpts()
	sshArgs = append(sshArgs,
		"-p", fmt.Sprint(proxy.Port()),
		"-o", "IdentityFile="+keyFile,
		"real_banner_please@localhost",
	)
	sshCmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
	sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")
	pty.attachAndStart(sshCmd)
	pty.pty.SetPrompt(testinfra.ExeDevPrompt)

	pty.want("███") // part of the banner
	pty.want("Please enter your email")

	// Reject OSC 11 responses, which look like: ]11;rgb:0000/0000/0000
	pty.reject("]11")
	pty.reject("rgb:")

	email := t.Name() + "@example.com"
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.wantRe("Pairing code: .*[0-9]{6}.*")
	waitForEmailAndVerify(t, email)
	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.want("Welcome to EXE.DEV!")
	pty.wantPrompt()
	pty.sendLine("whoami")
	pty.want(email)
	pty.want(publicKey)
	pty.disconnect()
}
