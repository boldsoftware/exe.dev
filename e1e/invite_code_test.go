// This file tests the invite code signup flow.

package e1e

import (
	"context"
	"testing"

	"exe.dev/e1e/testinfra"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// TestInviteCodeSignup tests that using an invite code as the SSH username
// during signup applies the invite code to the user.
func TestInviteCodeSignup(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	ctx := context.Background()
	inviteCode := "TESTINVITE" + t.Name()

	// Open the test database to create the invite code
	db, err := sqlite.New(Env.servers.Exed.DBPath, 1)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	// Create an invite code in the database
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		// Add to pool first
		queries := exedb.New(tx.Conn())
		if err := queries.AddInviteCodeToPool(ctx, inviteCode); err != nil {
			return err
		}
		// Draw from pool
		code, err := queries.DrawInviteCodeFromPool(ctx)
		if err != nil {
			return err
		}
		if code != inviteCode {
			t.Fatalf("expected code %s, got %s", inviteCode, code)
		}
		// Create the invite code with "free" plan type
		_, err = queries.CreateInviteCode(ctx, exedb.CreateInviteCodeParams{
			Code:             inviteCode,
			PlanType:         "free",
			AssignedToUserID: nil,
			AssignedBy:       "test",
			AssignedFor:      nil,
		})
		return err
	})
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Generate a new SSH key for the test
	keyFile, _ := genSSHKey(t)

	// Connect to exed with the invite code as the username
	pty := makePty(t, "ssh localhost with invite code")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.pty, inviteCode, keyFile)
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.pty.SetPrompt(testinfra.ExeDevPrompt)

	// Go through registration flow
	pty.want(testinfra.Banner)
	pty.want("Invite code accepted: free account")
	pty.want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.wantPrompt()
	pty.disconnect()

	// Verify the invite code was applied
	var billingExemption *string
	var signedUpWithInviteID *int64
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		user, err := queries.GetUserByEmail(ctx, email)
		if err != nil {
			return err
		}
		exemption, err := queries.GetUserBillingExemption(ctx, user.UserID)
		if err != nil {
			return err
		}
		billingExemption = exemption.BillingExemption
		signedUpWithInviteID = exemption.SignedUpWithInviteID
		return nil
	})
	if err != nil {
		t.Fatalf("failed to get user billing exemption: %v", err)
	}

	if billingExemption == nil || *billingExemption != "free" {
		t.Errorf("expected billing exemption 'free', got %v", billingExemption)
	}
	if signedUpWithInviteID == nil {
		t.Error("expected signed_up_with_invite_id to be set")
	}

	// Verify the invite code is marked as used
	var usedByUserID *string
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		invite, err := queries.GetInviteCodeByCode(ctx, inviteCode)
		if err != nil {
			return err
		}
		usedByUserID = invite.UsedByUserID
		return nil
	})
	if err != nil {
		t.Fatalf("failed to get invite code: %v", err)
	}
	if usedByUserID == nil {
		t.Error("invite code should be marked as used")
	}
}

// TestInviteCodeTrialExpiry tests that trial invite codes set the correct expiry.
func TestInviteCodeTrialExpiry(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	ctx := context.Background()
	inviteCode := "TESTTRIAL" + t.Name()

	// Open the test database to create the invite code
	db, err := sqlite.New(Env.servers.Exed.DBPath, 1)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	// Create an invite code with "trial" plan type
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		if err := queries.AddInviteCodeToPool(ctx, inviteCode); err != nil {
			return err
		}
		if _, err := queries.DrawInviteCodeFromPool(ctx); err != nil {
			return err
		}
		_, err = queries.CreateInviteCode(ctx, exedb.CreateInviteCodeParams{
			Code:             inviteCode,
			PlanType:         "trial",
			AssignedToUserID: nil,
			AssignedBy:       "test",
			AssignedFor:      nil,
		})
		return err
	})
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Generate a new SSH key and register with the invite code
	keyFile, _ := genSSHKey(t)

	pty := makePty(t, "ssh localhost with trial invite code")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.pty, inviteCode, keyFile)
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.pty.SetPrompt(testinfra.ExeDevPrompt)

	pty.want(testinfra.Banner)
	pty.want("Invite code accepted: 1 month free trial")
	pty.want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.wantPrompt()
	pty.disconnect()

	// Verify the trial exemption was applied with an expiry
	var billingExemption *string
	var hasTrialExpiry bool
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		user, err := queries.GetUserByEmail(ctx, email)
		if err != nil {
			return err
		}
		exemption, err := queries.GetUserBillingExemption(ctx, user.UserID)
		if err != nil {
			return err
		}
		billingExemption = exemption.BillingExemption
		hasTrialExpiry = exemption.BillingTrialEndsAt != nil
		return nil
	})
	if err != nil {
		t.Fatalf("failed to get user billing exemption: %v", err)
	}

	if billingExemption == nil || *billingExemption != "trial" {
		t.Errorf("expected billing exemption 'trial', got %v", billingExemption)
	}
	if !hasTrialExpiry {
		t.Error("expected billing_trial_ends_at to be set for trial")
	}
}

// TestInvalidInviteCodeIgnored tests that an invalid invite code is ignored
// and registration proceeds normally.
func TestInvalidInviteCodeIgnored(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	ctx := context.Background()

	// Generate a new SSH key
	keyFile, _ := genSSHKey(t)

	// Connect with an invalid invite code as username
	invalidCode := "INVALIDCODE123"
	pty := makePty(t, "ssh localhost with invalid invite code")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.pty, invalidCode, keyFile)
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.pty.SetPrompt(testinfra.ExeDevPrompt)

	// Registration should still work, but no invite code message
	pty.want(testinfra.Banner)
	pty.reject("Invite code accepted")
	pty.want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.sendLine(email)
	pty.wantRe("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.wantPrompt()
	pty.disconnect()

	// Verify the user was created without any billing exemption
	db, err := sqlite.New(Env.servers.Exed.DBPath, 1)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	var billingExemption *string
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		user, err := queries.GetUserByEmail(ctx, email)
		if err != nil {
			return err
		}
		exemption, err := queries.GetUserBillingExemption(ctx, user.UserID)
		if err != nil {
			return err
		}
		billingExemption = exemption.BillingExemption
		return nil
	})
	if err != nil {
		t.Fatalf("failed to get user billing exemption: %v", err)
	}

	// User should not have any billing exemption since the invite code was invalid
	if billingExemption != nil {
		t.Errorf("expected no billing exemption, got %v", *billingExemption)
	}
}
