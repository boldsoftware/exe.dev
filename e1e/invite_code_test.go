// This file tests the invite code signup flow.

package e1e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// TestWebInviteCodeSignup tests that using an invite code via the web auth flow
// (https://exe.dev/auth?invite=invitecode) applies the invite code to the user.
func TestWebInviteCodeSignup(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	ctx := context.Background()
	inviteCode := "WEBINVITE" + t.Name()

	// Open the test database to create the invite code
	db, err := sqlite.New(Env.servers.Exed.DBPath, 1)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	// Create an invite code in the database
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		if err := queries.AddInviteCodeToPool(ctx, inviteCode); err != nil {
			return err
		}
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

	// Sign up via web with invite code
	email := t.Name() + testinfra.FakeEmailSuffix
	_, err = Env.servers.WebLoginWithInvite(email, inviteCode)
	if err != nil {
		t.Fatalf("web login with invite failed: %v", err)
	}

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

// TestWebInviteCodeAlreadyUsed tests that visiting /auth?invite=<code> with an
// already-used invite code shows the auth form (user can still sign up, just
// without the invite code benefit).
func TestWebInviteCodeAlreadyUsed(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	ctx := context.Background()
	inviteCode := "USEDCODE" + t.Name()

	// Open the test database
	db, err := sqlite.New(Env.servers.Exed.DBPath, 1)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	// Create and immediately mark an invite code as used
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		if err := queries.AddInviteCodeToPool(ctx, inviteCode); err != nil {
			return err
		}
		if _, err := queries.DrawInviteCodeFromPool(ctx); err != nil {
			return err
		}
		invite, err := queries.CreateInviteCode(ctx, exedb.CreateInviteCodeParams{
			Code:             inviteCode,
			PlanType:         "free",
			AssignedToUserID: nil,
			AssignedBy:       "test",
			AssignedFor:      nil,
		})
		if err != nil {
			return err
		}
		// Mark it as used
		fakeUserID := "fake-user-id"
		return queries.UseInviteCode(ctx, exedb.UseInviteCodeParams{
			UsedByUserID: &fakeUserID,
			ID:           invite.ID,
		})
	})
	if err != nil {
		t.Fatalf("failed to create used invite code: %v", err)
	}

	// Visit /auth?invite=<used_code> and verify we get the error message
	authURL := fmt.Sprintf("http://localhost:%d/auth?invite=%s", Env.servers.Exed.HTTPPort, inviteCode)
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatalf("failed to GET /auth: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Should show the error message
	if !strings.Contains(string(body), "Invalid or already used invite code") {
		t.Errorf("expected error message for used invite code, got: %s", string(body))
	}

	// Should NOT include the invite code in a hidden field
	if strings.Contains(string(body), `name="invite"`) {
		t.Error("should not include invite hidden field for invalid code")
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
