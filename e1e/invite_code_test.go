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

// TestInviteAllocation tests that POSTing to /invite allocates one invite at a time
// and each invite is only shown once.
func TestInviteAllocation(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	ctx := context.Background()

	// Open the test database
	db, err := sqlite.New(Env.servers.Exed.DBPath, 1)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	// Sign up a user first
	email := t.Name() + testinfra.FakeEmailSuffix
	cookies, err := Env.servers.WebLoginWithEmail(email)
	if err != nil {
		t.Fatalf("web login failed: %v", err)
	}

	// Get the user ID
	var userID string
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		user, err := queries.GetUserByEmail(ctx, email)
		if err != nil {
			return err
		}
		userID = user.UserID
		return nil
	})
	if err != nil {
		t.Fatalf("failed to get user: %v", err)
	}

	// Create invite codes assigned to the user
	inviteCode1 := "USERINV1" + t.Name()
	inviteCode2 := "USERINV2" + t.Name()
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())

		// Create codes directly assigned to user (one at a time to ensure order)
		for _, code := range []string{inviteCode1, inviteCode2} {
			// Add to pool
			if err := queries.AddInviteCodeToPool(ctx, code); err != nil {
				return err
			}
			// Draw (will get the same code since only one in pool)
			drawnCode, err := queries.DrawInviteCodeFromPool(ctx)
			if err != nil {
				return err
			}
			// Create invite assigned to user
			_, err = queries.CreateInviteCode(ctx, exedb.CreateInviteCodeParams{
				Code:             drawnCode,
				PlanType:         "trial",
				AssignedToUserID: &userID,
				AssignedBy:       "test",
				AssignedFor:      nil,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create invite codes: %v", err)
	}

	// POST to /invite to allocate first invite
	inviteURL := fmt.Sprintf("http://localhost:%d/invite", Env.servers.Exed.HTTPPort)
	req, err := http.NewRequest("POST", inviteURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to POST /invite: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Verify the page shows exactly ONE invite code (the first one)
	if !strings.Contains(string(body), inviteCode1) {
		t.Errorf("expected to see first invite code %s in response", inviteCode1)
	}
	// Should NOT show the second invite yet
	if strings.Contains(string(body), inviteCode2) {
		t.Errorf("should NOT see second invite code %s yet", inviteCode2)
	}

	// Verify it shows usage instructions
	if !strings.Contains(string(body), "ssh ") {
		t.Error("expected to see SSH usage instructions")
	}
	if !strings.Contains(string(body), "/auth?invite=") {
		t.Error("expected to see web usage URL")
	}

	// POST again to allocate second invite
	req2, err := http.NewRequest("POST", inviteURL, nil)
	if err != nil {
		t.Fatalf("failed to create second request: %v", err)
	}
	for _, c := range cookies {
		req2.AddCookie(c)
	}

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("failed to POST /invite second time: %v", err)
	}
	defer resp2.Body.Close()

	body2, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatalf("failed to read second response body: %v", err)
	}

	// Second POST should show the second invite
	if !strings.Contains(string(body2), inviteCode2) {
		t.Errorf("expected to see second invite code %s in second response", inviteCode2)
	}
	// Should NOT show the first invite again
	if strings.Contains(string(body2), inviteCode1) {
		t.Errorf("should NOT see first invite code %s again", inviteCode1)
	}

	// Verify both invites are now allocated in the database
	var allocatedCount int
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		row := rx.Conn().QueryRowContext(ctx,
			"SELECT COUNT(*) FROM invite_codes WHERE assigned_to_user_id = ? AND allocated_at IS NOT NULL",
			userID)
		return row.Scan(&allocatedCount)
	})
	if err != nil {
		t.Fatalf("failed to count allocated invites: %v", err)
	}
	if allocatedCount != 2 {
		t.Errorf("expected 2 allocated invites, got %d", allocatedCount)
	}
}

// TestDashboardShowsInviteCount tests that the dashboard shows the invite count
// and appropriate links.
func TestDashboardShowsInviteCount(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	ctx := context.Background()

	// Open the test database
	db, err := sqlite.New(Env.servers.Exed.DBPath, 1)
	if err != nil {
		t.Fatalf("failed to open test database: %v", err)
	}
	defer db.Close()

	// Sign up a user
	email := t.Name() + testinfra.FakeEmailSuffix
	cookies, err := Env.servers.WebLoginWithEmail(email)
	if err != nil {
		t.Fatalf("web login failed: %v", err)
	}

	// Visit dashboard - should show "0 invites" with "Request More" link
	dashboardURL := fmt.Sprintf("http://localhost:%d/", Env.servers.Exed.HTTPPort)
	req, err := http.NewRequest("GET", dashboardURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to GET /: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Should show "0 invites" and "Request More" link
	if !strings.Contains(string(body), "0 invite") {
		t.Error("expected to see '0 invites' on dashboard")
	}
	if !strings.Contains(string(body), "/invite/request") {
		t.Error("expected to see 'Request More' link when user has 0 invites")
	}

	// Now add an invite code to the user
	var userID string
	err = db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		user, err := queries.GetUserByEmail(ctx, email)
		if err != nil {
			return err
		}
		userID = user.UserID
		return nil
	})
	if err != nil {
		t.Fatalf("failed to get user: %v", err)
	}

	inviteCode := "DASHTEST" + t.Name()
	err = db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		if err := queries.AddInviteCodeToPool(ctx, inviteCode); err != nil {
			return err
		}
		code, err := queries.DrawInviteCodeFromPool(ctx)
		if err != nil {
			return err
		}
		_, err = queries.CreateInviteCode(ctx, exedb.CreateInviteCodeParams{
			Code:             code,
			PlanType:         "trial",
			AssignedToUserID: &userID,
			AssignedBy:       "test",
			AssignedFor:      nil,
		})
		return err
	})
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Visit dashboard again - should show "1 invite" with "Allocate" link
	req2, err := http.NewRequest("GET", dashboardURL, nil)
	if err != nil {
		t.Fatalf("failed to create second request: %v", err)
	}
	for _, c := range cookies {
		req2.AddCookie(c)
	}

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("failed to GET / second time: %v", err)
	}
	defer resp2.Body.Close()

	body2, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Should show "1 invite" and "Allocate" form
	if !strings.Contains(string(body2), "1 invite") {
		t.Errorf("expected to see '1 invite' on dashboard, got body:\n%s", string(body2))
	}
	if !strings.Contains(string(body2), `action="/invite"`) {
		t.Error("expected to see 'Allocate' form when user has invites")
	}
}
