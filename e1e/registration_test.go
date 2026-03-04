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
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.WantRE("Pairing code: .*[0-9]{6}.*")
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.Want("Welcome to EXE.DEV!") // check that we show welcome message for users who haven't created boxes
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want(email)
	pty.Want(publicKey)
	pty.Disconnect()
}

func TestRegistrationHappensOnce(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	keyFile, publicKey := genSSHKey(t)

	// initial registration
	pty := sshToExeDev(t, keyFile)
	pty.Want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.WantRE("Pairing code: .*[0-9]{6}.*")
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	// Check that we show welcome message for first login.
	pty.Want("Welcome to EXE.DEV!")
	pty.Want("create your first VM")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want(email)
	pty.Want(publicKey)
	pty.WantPrompt()
	pty.Disconnect()

	// second login: no re-registration, but should still show welcome since user hasn't created boxes
	pty = sshToExeDev(t, keyFile)
	pty.Reject(testinfra.Banner)
	pty.Reject("Please enter your email")
	// No registration flow, no welcome message
	// but should still hint about how to create boxes,
	// because they haven't yet.
	pty.Want("create your first VM")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want(email)
	pty.Want(publicKey)
	pty.WantPrompt()

	pty.Disconnect()
}

func TestRegisterMultipleKeys(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	for i := range 3 {
		keyFile, publicKey := genSSHKey(t)
		pty := sshToExeDev(t, keyFile)
		pty.Want(testinfra.Banner)
		pty.Want("Please enter your email")
		email := t.Name() + testinfra.FakeEmailSuffix
		pty.SendLine(email)
		pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
		// pty.WantRE("Pairing code: .*[0-9]{6}.*")
		waitForEmailAndVerify(t, email)
		pty.Want("Email verified successfully")
		pty.Want("Registration complete")
		if i == 0 {
			pty.WantRE("account.*created")
		} else {
			pty.WantRE("key.*added")
		}
		if i == 0 {
			pty.Want("Welcome to EXE.DEV!") // welcome message only on first time
		}
		pty.WantPrompt()
		pty.SendLine("whoami")
		pty.Want(email)
		pty.Want(publicKey)
		pty.Disconnect()
	}
}

func TestRegisterWebThenKey(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	email := t.Name() + testinfra.FakeEmailSuffix
	baseURL := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())

	resp, err := http.PostForm(baseURL+"/auth", url.Values{"email": {email}})
	if err != nil {
		t.Fatalf("POST /auth: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d from /auth: %s", resp.StatusCode, string(body))
	}

	// Verify the email using the standard flow
	waitForEmailAndVerify(t, email)

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.WantRE("Pairing code: .*[0-9]{6}.*")

	waitForEmailAndVerify(t, email)

	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.Want("Your new ssh key has been added to your existing account.")
	pty.Want("Welcome to EXE.DEV!")
	pty.Want("create your first VM")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want(email)
	pty.Want(publicKey)
	pty.WantPrompt()
	pty.Disconnect()
}

func TestRegisterGitHubKey(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	keyDir := t.TempDir()
	keyFile := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte(ghuser.FakePrivateKey0), 0o600); err != nil {
		t.Fatalf("failed to write GitHub private key: %v", err)
	}

	pty := sshToExeDev(t, keyFile)
	pty.Want(testinfra.Banner)
	pty.Want("Email:")
	pty.Want("fake-for-tests@example.com")
	pty.SendLine("")

	pty.Want("Welcome to EXE.DEV!")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want("fake-for-tests@example.com")
	pty.Want(ghuser.FakePublicKey0)
	pty.WantPrompt()
	pty.Disconnect()
}

func TestRegisterGitHubKeyUnderDifferentEmail(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	keyDir := t.TempDir()
	keyFile := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(keyFile, []byte(ghuser.FakePrivateKey1), 0o600); err != nil {
		t.Fatalf("failed to write GitHub private key: %v", err)
	}

	pty := sshToExeDev(t, keyFile)
	pty.Want(testinfra.Banner)
	pty.Want("Email:")
	pty.Want(ghuser.FakeEmail1)
	// change email from "fake-for-tests@example.com" to "fake-for-tests@example.combinatorics"
	suffix := "binatorics"
	// This triggers a verification email, despite the known SSH key.
	pty.SendLine(suffix)
	newEmail := ghuser.FakeEmail1 + suffix

	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(newEmail))
	// pty.WantRE("Pairing code: .*[0-9]{6}.*")

	waitForEmailAndVerify(t, newEmail)

	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.Want("Welcome to EXE.DEV!")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want("fake-for-tests@example.com")
	pty.Want(ghuser.FakePublicKey1)
	pty.WantPrompt()
	pty.Disconnect()
}

// TestSSHTerminalInputDuringRegistration verifies that terminal input works
// character-by-character at the email prompt during registration.
// (We had early issues with ssh input buffers.)
func TestSSHTerminalInputDuringRegistration(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	keyFile, publicKey := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")

	email := t.Name() + testinfra.FakeEmailSuffix

	// Type the email one character at a time to simulate interactive typing.
	for _, ch := range email {
		pty.Send(string(ch))
	}
	pty.Send("\n")

	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.WantRE("Pairing code: .*[0-9]{6}.*")

	waitForEmailAndVerify(t, email)

	pty.Want("Email verified successfully")
	pty.Want("Registration complete")

	// After first-time registration, we show a welcome message and a prompt.
	pty.Want("Welcome to EXE.DEV!")
	pty.Want("create your first VM")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want(email)
	pty.Want(publicKey)
	pty.Disconnect()
}

// TestRegistrationWithLatency tests that registration works correctly
// even when there is significant network latency.
func TestRegistrationWithLatency(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
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
	pty.AttachAndStart(sshCmd)
	pty.SetPrompt(testinfra.ExeDevPrompt)

	pty.Want("███") // part of the banner
	pty.Want("Please enter your email")

	// Reject OSC 11 responses, which look like: ]11;rgb:0000/0000/0000
	pty.Reject("]11")
	pty.Reject("rgb:")

	email := t.Name() + testinfra.FakeEmailSuffix
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
	// pty.WantRE("Pairing code: .*[0-9]{6}.*")
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.Want("Welcome to EXE.DEV!")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want(email)
	pty.Want(publicKey)
	pty.Disconnect()
}

// TestWarpTerminalBootstrap tests that Warp terminal's shell bootstrap script
// is detected and treated as an interactive session rather than a command.
// Warp sends a command like "export TERM_PROGRAM=WarpTerminal ..." when connecting,
// which previously caused the server to reject unregistered users with
// "Please complete registration" instead of showing the registration flow.
// See https://github.com/boldsoftware/exe.dev/issues/39
func TestWarpTerminalBootstrap(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	keyFile, publicKey := genSSHKey(t)
	warpBootstrapCmd := `export TERM_PROGRAM=WarpTerminal; echo "warp bootstrap"`

	sshWarp := func() *expectPty {
		pty := makePty(t, "ssh localhost (warp simulation)")
		sshArgs := Env.servers.BaseSSHArgs("", keyFile)
		sshArgs = append(sshArgs, "-t", warpBootstrapCmd)
		sshCmd := exec.CommandContext(Env.context(t), "ssh", sshArgs...)
		sshCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK=")
		pty.AttachAndStart(sshCmd)
		pty.SetPrompt(testinfra.ExeDevPrompt)
		return pty
	}

	// First connection: should get registration flow, not "Please complete registration"
	pty := sshWarp()
	pty.Reject("Please complete registration")
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
	waitForEmailAndVerify(t, email)
	pty.Want("Registration complete")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want(email)
	pty.Want(publicKey)
	pty.Disconnect()

	// Second connection: should get main menu directly
	pty = sshWarp()
	pty.Reject(testinfra.Banner)
	pty.Reject("Please enter your email")
	pty.WantPrompt()
	pty.SendLine("whoami")
	pty.Want(email)
	pty.Disconnect()
}

// TestBogusEmailDomainBlocked verifies that emails to bogus domains
// (like example.com) are silently dropped and never delivered.
func TestBogusEmailDomainBlocked(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Use a bogus domain that should be blocked
	email := t.Name() + "@example.com"

	// Poison the inbox: if an email is accidentally sent, the test fails immediately.
	Env.servers.Email.PoisonInbox(email)

	keyFile, _ := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")

	pty.SendLine(email)

	// The server should still say it sent the email (anti-fraud measure)
	pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))

	// If we reach here, no email was sent (otherwise the poison would have panicked).
	// Close the connection without waiting for EOF (server is waiting for verification).
	pty.Close()
}
