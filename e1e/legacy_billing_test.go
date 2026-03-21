package e1e

import (
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestLegacyPaidUserCanCreateVM verifies that a user who signs up via SSH
// and gets billing activated via the debug endpoint can create a VM.
// This tests the legacy entitlement path (billing_events, not account_plans).
func TestLegacyPaidUserCanCreateVM(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)

	keyFile, _ := genSSHKey(t)
	pty := makePty(t, "ssh register")
	cmd, err := Env.servers.SSHToExeDev(Env.context(t), pty.PTY(), keyFile)
	if err != nil {
		t.Fatalf("SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	email := t.Name() + testinfra.FakeEmailSuffix
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.SetPrompt(testinfra.ExeDevPrompt)
	pty.WantPrompt()

	// Activate billing via debug endpoint
	addBillingForEmail(t, email)

	// Create a VM — should succeed
	boxName := newBox(t, pty)
	if boxName == "" {
		t.Fatal("expected box to be created")
	}
	pty.deleteBox(boxName)
	pty.Disconnect()
}

// TestLegacyFreeInviteCanCreateVM verifies that a user who signs up with a
// free invite code can create a VM without billing.
func TestLegacyFreeInviteCanCreateVM(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)

	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("CreateInviteCode: %v", err)
	}

	keyFile, _ := genSSHKey(t)
	pty := makePty(t, "ssh with free invite")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.PTY(), inviteCode, keyFile)
	if err != nil {
		t.Fatalf("SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	email := t.Name() + testinfra.FakeEmailSuffix
	pty.Want(testinfra.Banner)
	pty.Want("Invite code accepted: free account")
	pty.Want("Please enter your email")
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.SetPrompt(testinfra.ExeDevPrompt)
	pty.WantPrompt()

	// Free invite user should be able to create a VM without billing
	boxName := newBox(t, pty)
	if boxName == "" {
		t.Fatal("expected box to be created")
	}
	pty.deleteBox(boxName)
	pty.Disconnect()
}

// TestLegacyTrialInviteCanCreateVM verifies that a user who signs up with a
// trial invite code can create a VM during their trial period.
func TestLegacyTrialInviteCanCreateVM(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)

	inviteCode, err := Env.servers.CreateInviteCode("trial")
	if err != nil {
		t.Fatalf("CreateInviteCode: %v", err)
	}

	keyFile, _ := genSSHKey(t)
	pty := makePty(t, "ssh with trial invite")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.PTY(), inviteCode, keyFile)
	if err != nil {
		t.Fatalf("SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	email := t.Name() + testinfra.FakeEmailSuffix
	pty.Want(testinfra.Banner)
	pty.Want("Invite code accepted")
	pty.Want("Please enter your email")
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.SetPrompt(testinfra.ExeDevPrompt)
	pty.WantPrompt()

	// Trial user should be able to create a VM without billing
	boxName := newBox(t, pty)
	if boxName == "" {
		t.Fatal("expected box to be created")
	}
	pty.deleteBox(boxName)
	pty.Disconnect()
}

// TestLegacyUnpaidUserCannotCreateVM verifies that a user without billing
// gets "Billing Required" when trying to create a VM via SSH.
func TestLegacyUnpaidUserCannotCreateVM(t *testing.T) {
	testinfra.SkipWithoutStripe(t) // SkipBilling=true bypasses entitlement checks
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	// Use a free invite to create a user, then revoke their exemption
	// to simulate an unpaid user. This avoids the Stripe redirect issue
	// with WebLoginWithEmail when SkipBilling=false.
	keyFile, _ := genSSHKey(t)
	pty := makePty(t, "ssh unpaid user")
	cmd, err := Env.servers.SSHToExeDev(Env.context(t), pty.PTY(), keyFile)
	if err != nil {
		t.Fatalf("SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })

	email := t.Name() + testinfra.FakeEmailSuffix
	pty.Want(testinfra.Banner)
	pty.Want("Please enter your email")
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.SetPrompt(testinfra.ExeDevPrompt)
	pty.WantPrompt()

	// Try to create a VM — should fail with Billing Required
	pty.SendLine("new --name=unpaid-test-vm")
	pty.WantRE("Billing Required")
	pty.Disconnect()
}
