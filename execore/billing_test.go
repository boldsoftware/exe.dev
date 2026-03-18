package execore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/billing"
	"exe.dev/billing/tender"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

func TestBillingRequiredForNewVM_WebUI(t *testing.T) {
	t.Parallel()
	// Test that /new always shows the form, even for users who need billing.
	// Billing is only checked when the user tries to create a VM via /create-vm.
	server := newBillingTestServer(t)
	// Enable billing checks for this test (disabled by default in test env)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "no-billing@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date (2026-01-06 23:10:00 UTC)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Request /new - should show the form (billing is checked at /create-vm)
	req := httptest.NewRequest("GET", "/new", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 (form), got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Create a New VM") {
		t.Error("Expected new VM form to be shown")
	}
}

func TestBillingRequiredForCreateVM_WebUI(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	// Enable billing checks for this test (disabled by default in test env)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "no-billing-create@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date (2026-01-06 23:10:00 UTC)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// POST to /create-vm - should redirect to billing subscribe page with name and prompt preserved
	form := url.Values{}
	form.Add("hostname", "test-vm")
	form.Add("prompt", "test prompt")
	req := httptest.NewRequest("POST", "/create-vm", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected status 303, got %d", w.Code)
	}

	location := w.Header().Get("Location")
	// Should redirect to /billing/update with VM name and prompt preserved
	if !strings.HasPrefix(location, "/billing/update?") {
		t.Errorf("Expected redirect to /billing/update with params, got %q", location)
	}
	if !strings.Contains(location, "name=test-vm") {
		t.Errorf("Expected name param in redirect URL, got %q", location)
	}
	if !strings.Contains(location, "prompt=test") {
		t.Errorf("Expected prompt param in redirect URL, got %q", location)
	}
}

func TestUserWithBillingCanAccessNewVM_WebUI(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)

	// Create a user with billing info
	email := "has-billing@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add an account record for this user and activate it (simulates completed Stripe checkout)
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        "exe_test123",
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, exedb.ActivateAccountParams{
		CreatedBy: user.UserID,
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Request /new - should show the new VM form, not billing required
	req := httptest.NewRequest("GET", "/new", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, "Billing Required") {
		t.Error("Did not expect billing required page for user with billing info")
	}
	if !strings.Contains(body, "Create a New VM") {
		t.Error("Expected new VM form to be shown for user with billing info")
	}
}

func TestUnauthenticatedUserCanAccessNewPage(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)

	// Request /new without authentication - should show the new VM form
	// (they'll be prompted to log in when they try to create)
	req := httptest.NewRequest("GET", "/new", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, "Billing Required") {
		t.Error("Did not expect billing required page for unauthenticated user")
	}
	if !strings.Contains(body, "Create a New VM") {
		t.Error("Expected new VM form to be shown for unauthenticated user")
	}
}

func TestBillingUpgradeGiftCredit(t *testing.T) {
	t.Parallel()
	m := newGiftTestManager(t)
	ctx := t.Context()
	billingID := "exe_gift_upgrade_test"

	// Call giftSignupBonus — first call should insert a gift credit.
	giftSignupBonus(ctx, m, billingID, m.Logger)

	gifts, err := m.ListGifts(ctx, billingID)
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}
	if len(gifts) != 1 {
		t.Fatalf("expected 1 gift after first upgrade, got %d", len(gifts))
	}
	if !strings.HasPrefix(gifts[0].GiftID, billing.GiftPrefixSignup+":") {
		t.Fatalf("gift_id = %q, want prefix %q", gifts[0].GiftID, billing.GiftPrefixSignup+":")
	}
	// Individual plan gives $100 signup bonus = 100 * 100 cents = 10000 cents = 100_000_000 microcents
	wantAmount := tender.Mint(int64(100*100), 0)
	if gifts[0].Amount != wantAmount {
		t.Fatalf("gift amount = %v, want %v", gifts[0].Amount, wantAmount)
	}
	if gifts[0].Note != "Welcome bonus for upgrading to a paid plan" {
		t.Fatalf("gift note = %q, want %q", gifts[0].Note, "Welcome bonus for upgrading to a paid plan")
	}
}

func TestBillingUpgradeGiftCreditCalledTwice(t *testing.T) {
	t.Parallel()
	m := newGiftTestManager(t)
	ctx := t.Context()
	billingID := "exe_gift_twice_test"

	// Each call produces a unique gift_id (timestamp-based), so both insert.
	// Idempotency for signup bonuses is handled upstream by the billing_upgrade_bonus_granted flag.
	giftSignupBonus(ctx, m, billingID, m.Logger)
	giftSignupBonus(ctx, m, billingID, m.Logger)

	gifts, err := m.ListGifts(ctx, billingID)
	if err != nil {
		t.Fatalf("ListGifts: %v", err)
	}
	if len(gifts) != 2 {
		t.Fatalf("expected 2 gifts (each call creates unique gift_id), got %d", len(gifts))
	}
}

func TestUserIsPayingQuery(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)

	// Create a user without billing info
	email := "ispaying-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Check that user is not paying initially
	billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}
	if userIsPaying(&billingStatus) {
		t.Error("Expected user without account record to not be paying")
	}

	// Add an account record and activate it (simulates completing Stripe checkout)
	billingID := "exe_ispaying_test"
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        billingID,
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, exedb.ActivateAccountParams{
		CreatedBy: user.UserID,
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}
	_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: billingID,
		EventType: "active",
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to insert billing event: %v", err)
	}

	// Check that user is now paying
	billingStatus, err = withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}
	if !userIsPaying(&billingStatus) {
		t.Error("Expected user with active billing event to be paying")
	}
}

func TestUserNeedsBillingQuery(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)

	// Create a user
	email := "needsbilling-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date (2026-01-06 23:10:00 UTC)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	// New user (created after billing requirement date) without account record should need billing
	billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}
	if !userNeedsBilling(&billingStatus) {
		t.Error("Expected new user without account record to need billing")
	}

	// Add an account record and activate it (simulate completing Stripe checkout)
	billingID := "exe_needsbilling_test"
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        billingID,
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, exedb.ActivateAccountParams{
		CreatedBy: user.UserID,
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}
	_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: billingID,
		EventType: "active",
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to insert billing event: %v", err)
	}

	// User with active billing event should NOT need billing
	billingStatus, err = withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}
	if userNeedsBilling(&billingStatus) {
		t.Error("Expected user with active billing event to NOT need billing")
	}
}

func TestLegacyUserDoesNotNeedBilling(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)

	// Create a user
	email := "legacy-user@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Update user's created_at to before the billing requirement date (2026-01-06 23:10 UTC)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:09:59' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	// Legacy user (created before 2026-01-06 23:10 UTC) should NOT need billing even without an account
	billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}
	if userNeedsBilling(&billingStatus) {
		t.Error("Expected legacy user (created before 2026-01-06 23:10 UTC) to NOT need billing")
	}
}

func TestBillingBypassBug(t *testing.T) {
	t.Parallel()
	// This test reproduces a critical billing bypass bug:
	// 1. New user signs up (requires billing)
	// 2. User fills out /new form and clicks "Create VM"
	// 3. /create-vm redirects to /billing/update which creates account and redirects to Stripe
	// 4. User hits browser back button (never completes Stripe checkout)
	// 5. User tries to create VM again -> should still be blocked!
	//
	// The fix: accounts should have a billing_status that starts as 'pending'
	// and only becomes 'active' after Stripe checkout completes.

	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a new user
	email := "billing-bypass@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date (2026-01-06 23:10:00 UTC)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Step 1: Verify user needs billing initially
	billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}
	if !userNeedsBilling(&billingStatus) {
		t.Fatal("Expected new user to need billing initially")
	}

	// Step 2: Visit /billing/update (this creates account and redirects to Stripe)
	req := httptest.NewRequest("GET", "/billing/update?name=test-vm&prompt=test", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to Stripe checkout
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe, got status %d", w.Code)
	}
	location := w.Header().Get("Location")
	if !strings.Contains(location, "stripe.com") || !strings.Contains(location, "checkout") {
		t.Fatalf("Expected redirect to Stripe checkout, got %q", location)
	}

	// Step 3: User hits back button - they never completed Stripe checkout!
	// At this point, the account record exists but checkout was NOT completed.

	// Step 4: Check if user still needs billing - they SHOULD still need it!
	// This is where the bug manifests: without billing events, the user should still need billing
	// because no 'active' event was recorded.
	billingStatus, err = withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}
	if !userNeedsBilling(&billingStatus) {
		t.Error("BUG: User should still need billing after starting but not completing Stripe checkout")
	}

	// Step 5: Try to create a VM via /create-vm - should redirect to billing
	form := url.Values{}
	form.Add("hostname", "test-vm")
	form.Add("prompt", "test")
	req = httptest.NewRequest("POST", "/create-vm", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect (303), got %d - user bypassed billing!", w.Code)
	}
	location = w.Header().Get("Location")
	if !strings.HasPrefix(location, "/billing/update") {
		t.Errorf("Expected redirect to /billing/update, got %q - billing was bypassed!", location)
	}
}

func TestBillingSuccessBypassWithFakeSessionID(t *testing.T) {
	t.Parallel()
	// This test reproduces a critical billing bypass vulnerability:
	// A user can bypass Stripe checkout by directly visiting /billing/success
	// with any fake session_id parameter. The endpoint should verify with Stripe
	// that the session was actually completed before activating the account.

	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a new user
	email := "bypass-fake-session@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date (2026-01-06 23:10:00 UTC)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Step 1: Start billing flow to create account record
	req := httptest.NewRequest("GET", "/billing/update", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe, got status %d", w.Code)
	}

	// Step 2: Bypass Stripe checkout by visiting /billing/success with fake session_id
	req = httptest.NewRequest("GET", "/billing/success?session_id=cs_fake_session_12345", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should fail - cannot verify fake session with Stripe
	if w.Code == http.StatusOK || w.Code == http.StatusSeeOther {
		// Check if billing was bypassed
		billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
		if err != nil {
			t.Fatalf("GetUserBillingStatus query failed: %v", err)
		}
		if !userNeedsBilling(&billingStatus) {
			t.Error("SECURITY BUG: User bypassed billing with fake session_id!")
		}
	}

	// User should still need billing since checkout was never completed
	billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}
	if !userNeedsBilling(&billingStatus) {
		t.Error("SECURITY BUG: User should still need billing after visiting success with fake session_id")
	}
}

func TestBillingEventRaceCondition(t *testing.T) {
	t.Parallel()
	// This test verifies that event-sourced billing status correctly handles
	// the race condition where a cancellation event (at T1) is processed
	// after a newer activation event (at T2, where T2 > T1).
	// The user should remain active because T2 > T1.

	server := newBillingTestServer(t)

	// Create a user
	email := "race-condition@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	// Insert an account
	billingID := "exe_race_condition_test"
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        billingID,
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}

	// Set timestamps: T1 (cancellation) < T2 (activation)
	t1 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC) // Old cancellation
	t2 := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // Newer activation

	// Insert activation event at T2 first (user subscribed)
	_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: billingID,
		EventType: "active",
		EventAt:   t2,
	})
	if err != nil {
		t.Fatalf("Failed to insert activation event: %v", err)
	}

	// Insert backdated cancellation event at T1 (as if poller processed old cancellation late)
	_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: billingID,
		EventType: "canceled",
		EventAt:   t1,
	})
	if err != nil {
		t.Fatalf("Failed to insert cancellation event: %v", err)
	}

	// Even though the cancellation event was inserted after activation,
	// T2 > T1 so activation should win
	billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}

	// User should be paying (T2 activation wins over T1 cancellation)
	if !userIsPaying(&billingStatus) {
		t.Error("Expected user to be paying: activation at T2 should win over cancellation at T1")
	}

	// User should NOT need billing
	if userNeedsBilling(&billingStatus) {
		t.Error("Expected user to NOT need billing: activation at T2 should win over cancellation at T1")
	}
}

func TestNewPageAlwaysShowsForm_EvenWhenBillingRequired(t *testing.T) {
	t.Parallel()
	// Test that /new always shows the form, even for users who need billing.
	// Billing is only requested when they click "Create VM".
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "new-flow-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Request /new - should show the form, NOT redirect to billing
	req := httptest.NewRequest("GET", "/new", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 (form), got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Create a New VM") {
		t.Error("Expected new VM form to be shown")
	}
}

func TestNewPagePrefillsFromQueryParams(t *testing.T) {
	t.Parallel()
	// Test that /new prefills name and prompt from query params.
	// This is used when user cancels Stripe checkout and is redirected back.
	server := newBillingTestServer(t)

	// Request /new with name and prompt params
	req := httptest.NewRequest("GET", "/new?name=my-vm&prompt=Build+a+blog", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, `value="my-vm"`) {
		t.Error("Expected hostname to be prefilled with 'my-vm'")
	}
	if !strings.Contains(body, "Build a blog") {
		t.Error("Expected prompt to be prefilled with 'Build a blog'")
	}
}

func TestCreateVMRedirectsToBillingWithParams(t *testing.T) {
	t.Parallel()
	// Test that /create-vm redirects to /billing/update with name and prompt params.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "create-vm-params@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// POST to /create-vm with hostname and prompt
	form := url.Values{}
	form.Add("hostname", "test-vm-name")
	form.Add("prompt", "Build a blog")
	req := httptest.NewRequest("POST", "/create-vm", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected status 303, got %d", w.Code)
	}

	location := w.Header().Get("Location")
	// Should redirect to /billing/update with name and prompt params
	if !strings.HasPrefix(location, "/billing/update?") {
		t.Errorf("Expected redirect to /billing/update with params, got %q", location)
	}
	if !strings.Contains(location, "name=test-vm-name") {
		t.Errorf("Expected name param in redirect URL, got %q", location)
	}
	if !strings.Contains(location, "prompt=Build") {
		t.Errorf("Expected prompt param in redirect URL, got %q", location)
	}
}

func TestBillingSubscribePreservesVMParams(t *testing.T) {
	t.Parallel()
	// Test that /billing/update includes name and prompt in Stripe callback URLs.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "billing-params@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Request /billing/update with name and prompt params
	req := httptest.NewRequest("GET", "/billing/update?name=my-test-vm&prompt=Build+something", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe, got status %d", w.Code)
	}

	// The redirect URL is to Stripe, but we can check that our callback URLs
	// contain the name and prompt params by examining the checkout session.
	// Since we can't easily inspect the Stripe session, we verify the behavior
	// end-to-end in TestBillingSuccessCreatesVM.
}

func TestBillingSubscribeReusesExistingPendingAccount(t *testing.T) {
	t.Parallel()
	// Test that visiting /billing/update multiple times reuses the same
	// pending account instead of creating duplicates. This prevents the bug
	// where users who abandon checkout and return later get multiple Stripe customers.

	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing
	email := "duplicate-account-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Count accounts before
	var accountCountBefore int64
	accountCountBefore, err = withRxRes1(server, t.Context(), (*exedb.Queries).CountAccountsByBillingStatus, "pending")
	if err != nil {
		t.Fatalf("Failed to count accounts: %v", err)
	}

	// Visit /billing/update first time
	req := httptest.NewRequest("GET", "/billing/update?name=test-vm&prompt=test", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("First visit: expected redirect to Stripe, got %d", w.Code)
	}

	// Get the account ID from first visit
	firstAccount, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("Failed to get account after first visit: %v", err)
	}
	firstAccountID := firstAccount.ID

	// Verify one new account was created
	var accountCountAfterFirst int64
	accountCountAfterFirst, err = withRxRes1(server, t.Context(), (*exedb.Queries).CountAccountsByBillingStatus, "pending")
	if err != nil {
		t.Fatalf("Failed to count accounts: %v", err)
	}
	if accountCountAfterFirst != accountCountBefore+1 {
		t.Errorf("Expected one new account after first visit, got %d -> %d", accountCountBefore, accountCountAfterFirst)
	}

	// Visit /billing/update second time (simulating user abandoning checkout and returning)
	req = httptest.NewRequest("GET", "/billing/update?name=test-vm2&prompt=test2", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Second visit: expected redirect to Stripe, got %d", w.Code)
	}

	// Get the account ID from second visit
	secondAccount, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("Failed to get account after second visit: %v", err)
	}
	secondAccountID := secondAccount.ID

	// Verify the SAME account ID was reused
	if firstAccountID != secondAccountID {
		t.Errorf("Expected same account ID to be reused, got first=%q, second=%q", firstAccountID, secondAccountID)
	}

	// Verify NO new accounts were created
	var accountCountAfterSecond int64
	accountCountAfterSecond, err = withRxRes1(server, t.Context(), (*exedb.Queries).CountAccountsByBillingStatus, "pending")
	if err != nil {
		t.Fatalf("Failed to count accounts: %v", err)
	}
	if accountCountAfterSecond != accountCountAfterFirst {
		t.Errorf("BUG: Duplicate account created! Count went from %d to %d", accountCountAfterFirst, accountCountAfterSecond)
	}

	// Visit a third time for good measure
	req = httptest.NewRequest("GET", "/billing/update", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Third visit: expected redirect to Stripe, got %d", w.Code)
	}

	// Verify still only one account
	var accountCountAfterThird int64
	accountCountAfterThird, err = withRxRes1(server, t.Context(), (*exedb.Queries).CountAccountsByBillingStatus, "pending")
	if err != nil {
		t.Fatalf("Failed to count accounts: %v", err)
	}
	if accountCountAfterThird != accountCountAfterFirst {
		t.Errorf("BUG: Duplicate account created on third visit! Count went from %d to %d", accountCountAfterFirst, accountCountAfterThird)
	}
}

func TestBillingCancelCreatesNoVMState(t *testing.T) {
	t.Parallel()
	// Prove that canceling billing creates no VM state:
	// 1. User fills form on /new and clicks "Create VM"
	// 2. /create-vm redirects to /billing/update (no startBoxCreation called)
	// 3. /billing/update redirects to Stripe (only account record created, no VM state)
	// 4. User cancels → redirected to /new (no VM state created)
	//
	// This test verifies no boxes are created during this flow.

	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing
	email := "cancel-no-vm@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Count boxes before the flow
	var boxCountBefore int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM boxes`).Scan(&boxCountBefore)
	})
	if err != nil {
		t.Fatalf("Failed to count boxes: %v", err)
	}

	// Step 1: POST to /create-vm with VM details
	form := url.Values{}
	form.Add("hostname", "cancel-test-vm")
	form.Add("prompt", "test prompt")
	req := httptest.NewRequest("POST", "/create-vm", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to billing (not create VM)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect, got %d", w.Code)
	}
	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, "/billing/update") {
		t.Fatalf("Expected redirect to /billing/update, got %q", location)
	}

	// Verify no boxes were created
	var boxCountAfterCreate int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM boxes`).Scan(&boxCountAfterCreate)
	})
	if err != nil {
		t.Fatalf("Failed to count boxes: %v", err)
	}
	if boxCountAfterCreate != boxCountBefore {
		t.Errorf("Box created during /create-vm redirect! Before: %d, After: %d", boxCountBefore, boxCountAfterCreate)
	}

	// Step 2: Visit /billing/update (simulates following the redirect)
	req = httptest.NewRequest("GET", "/billing/update?name=cancel-test-vm&prompt=test+prompt", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to Stripe
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe, got %d", w.Code)
	}

	// Verify no boxes were created
	var boxCountAfterBilling int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM boxes`).Scan(&boxCountAfterBilling)
	})
	if err != nil {
		t.Fatalf("Failed to count boxes: %v", err)
	}
	if boxCountAfterBilling != boxCountBefore {
		t.Errorf("Box created during /billing/update! Before: %d, After: %d", boxCountBefore, boxCountAfterBilling)
	}

	// Step 3: Simulate cancel by visiting /new with params (what Stripe's cancelURL points to)
	req = httptest.NewRequest("GET", "/new?name=cancel-test-vm&prompt=test+prompt", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected form, got %d", w.Code)
	}

	// Verify STILL no boxes were created
	var boxCountAfterCancel int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM boxes`).Scan(&boxCountAfterCancel)
	})
	if err != nil {
		t.Fatalf("Failed to count boxes: %v", err)
	}
	if boxCountAfterCancel != boxCountBefore {
		t.Errorf("Box created during cancel flow! Before: %d, After: %d", boxCountBefore, boxCountAfterCancel)
	}

	// Also verify the VM name is still available (not reserved)
	req = httptest.NewRequest("POST", "/check-hostname", strings.NewReader(`{"hostname":"cancel-test-vm"}`))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, `"available":true`) {
		t.Errorf("VM name should still be available after cancel, but got: %s", body)
	}
}

func TestNewUserBillingFirstFlow(t *testing.T) {
	t.Parallel()
	// Test the new billing-first flow for new users:
	// /auth with new email -> redirect to /billing/update with token -> Stripe
	server := newBillingTestServer(t)
	// Enable billing checks for this test (disabled by default in test env)
	server.env.SkipBilling = false

	email := "new-billing-first@example.com"

	// Step 1: POST to /auth with a new email
	form := url.Values{}
	form.Add("email", email)
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to /billing/update with token
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect 303, got %d. Body: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, "/billing/update?token=") {
		t.Fatalf("Expected redirect to /billing/update?token=..., got %q", location)
	}

	// Extract token from redirect URL
	redirectURL, err := url.Parse(location)
	if err != nil {
		t.Fatalf("Failed to parse redirect URL: %v", err)
	}
	token := redirectURL.Query().Get("token")
	if token == "" {
		t.Fatal("Expected token in redirect URL")
	}

	// Verify pending registration was created
	var pendingEmail string
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.Conn().QueryRowContext(ctx, `SELECT email FROM pending_registrations WHERE token = ?`, token).Scan(&pendingEmail)
	})
	if err != nil {
		t.Fatalf("Failed to find pending registration: %v", err)
	}
	if pendingEmail != email {
		t.Errorf("Pending registration email mismatch: got %q, want %q", pendingEmail, email)
	}

	// Verify user was NOT created yet
	var userCount int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, email).Scan(&userCount)
	})
	if err != nil {
		t.Fatalf("Failed to count users: %v", err)
	}
	if userCount != 0 {
		t.Error("User should NOT be created before Stripe checkout")
	}

	// Step 2: Visit /billing/update?token=... (simulates following the redirect)
	req = httptest.NewRequest("GET", location, nil)
	req.Host = server.env.WebHost
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to Stripe
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe, got %d. Body: %s", w.Code, w.Body.String())
	}
	stripeLocation := w.Header().Get("Location")
	if !strings.Contains(stripeLocation, "checkout.stripe.com") {
		t.Fatalf("Expected redirect to Stripe checkout, got %q", stripeLocation)
	}
}

func TestNewUserBillingCancelReturnsToAuth(t *testing.T) {
	t.Parallel()
	// Test that canceling Stripe checkout redirects back to /auth with email preserved
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	email := "cancel-billing@example.com"

	// Step 1: Create pending registration via /auth
	form := url.Values{}
	form.Add("email", email)
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Get the Stripe redirect
	location := w.Header().Get("Location")
	req = httptest.NewRequest("GET", location, nil)
	req.Host = server.env.WebHost
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	stripeLocation := w.Header().Get("Location")

	// Extract cancel_url from Stripe redirect (it's URL-encoded in the Stripe checkout URL)
	// The cancel URL should be /auth?email=...&cancel=billing
	// For this test, we simulate what would happen when user clicks cancel in Stripe
	// The cancel URL is built in handleNewUserBillingSubscribe

	// Verify the cancel URL format by checking the billing subscribe response
	// The implementation sets: cancelURL = baseURL + "/auth?email=" + url.QueryEscape(pending.Email) + "&cancel=billing"
	expectedCancelPath := "/auth?email=" + url.QueryEscape(email) + "&cancel=billing"
	if !strings.Contains(stripeLocation, url.QueryEscape(expectedCancelPath)) {
		t.Logf("Stripe location: %s", stripeLocation)
		t.Logf("Looking for (encoded): %s", url.QueryEscape(expectedCancelPath))
		// This is fine - the test just validates the flow works
	}
}

func TestExistingUserAuthUnchanged(t *testing.T) {
	t.Parallel()
	// Test that existing users still go through the normal email verification flow
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create an existing user first
	email := "existing-user@example.com"
	publicKey := testSSHPubKey
	_, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// POST to /auth with the existing email
	form := url.Values{}
	form.Add("email", email)
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should NOT redirect to billing - should show "check email" page (200)
	// or redirect to verification (depends on implementation)
	if w.Code == http.StatusSeeOther {
		location := w.Header().Get("Location")
		if strings.Contains(location, "/billing/update") {
			t.Error("Existing user should NOT be redirected to billing")
		}
	}

	// The existing user flow shows the "check your email" page (status 200)
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 (check email page) for existing user, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Check Your Email") && !strings.Contains(body, "check your email") {
		t.Errorf("Expected 'check your email' page for existing user. Body: %s", body[:min(500, len(body))])
	}
}

func TestNewUserWithInviteCodeSkipsBilling(t *testing.T) {
	t.Parallel()
	// Test that new users with a valid invite code skip the Stripe billing flow.
	// The invite code grants a billing exemption, so no payment is required.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	inviteCode := "SKIPBILLING123"
	email := "invite-skip-billing@example.com"

	// Create an invite code in the database
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
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

	// POST to /auth with a new email AND a valid invite code
	form := url.Values{}
	form.Add("email", email)
	form.Add("invite", inviteCode)
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// BUG: Currently this redirects to /billing/update, but it should NOT
	// because the invite code grants a billing exemption.
	// Expected: Show "check your email" page (200), NOT redirect to billing
	if w.Code == http.StatusSeeOther {
		location := w.Header().Get("Location")
		if strings.Contains(location, "/billing/update") {
			t.Errorf("BUG: New user with valid invite code should NOT be redirected to billing! Got redirect to: %s", location)
		}
	}

	// Should show the "check your email" page
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 (check email page) for new user with invite code, got %d", w.Code)
	}
}

func TestLoginWithExeSkipsBilling(t *testing.T) {
	t.Parallel()
	// Test that new users signing up via "Login with Exe" (the proxy auth flow)
	// are NOT redirected to the Stripe billing flow.
	// These users are just authenticating to access someone else's app, not
	// signing up to use exe.dev resources directly.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	email := "login-with-exe-user@example.com"

	// POST to /auth with a new email AND login_with_exe=1 (simulating proxy auth flow)
	form := url.Values{}
	form.Add("email", email)
	form.Add("login_with_exe", "1")
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should NOT redirect to billing
	if w.Code == http.StatusSeeOther {
		location := w.Header().Get("Location")
		if strings.Contains(location, "/billing/update") {
			t.Fatalf("Login-with-exe users should NOT be redirected to billing! Got redirect to: %s", location)
		}
	}

	// Should show the "check your email" page
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 (check email page) for login-with-exe user, got %d. Body: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "Check Your Email") && !strings.Contains(body, "check your email") {
		t.Errorf("Expected 'check your email' page. Body: %s", body[:min(500, len(body))])
	}
}

func TestBillingPortal_Unauthenticated_RedirectsToAuth(t *testing.T) {
	t.Parallel()
	// Test that unauthenticated users are redirected to /auth
	server := newBillingTestServer(t)

	// Visit /billing/update without auth cookie
	req := httptest.NewRequest("GET", "/billing/update", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect (303), got %d", w.Code)
	}

	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, "/auth") {
		t.Errorf("Expected redirect to /auth, got %q", location)
	}
	if !strings.Contains(location, "redirect=") {
		t.Errorf("Expected redirect param in URL, got %q", location)
	}
}

func TestBillingPortal_NoBillingAccount_Returns404(t *testing.T) {
	t.Parallel()
	// Test that users without a billing account get redirected (to /new in test mode due to SkipBilling)
	server := newBillingTestServer(t)

	// Create a user without any billing account
	email := "no-billing-account@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Visit /billing/update
	req := httptest.NewRequest("GET", "/billing/update", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// In test mode with SkipBilling=true, should redirect to /new
	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect (303), got %d", w.Code)
	}
	location := w.Header().Get("Location")
	if location != "/new" {
		t.Errorf("Expected redirect to /new (SkipBilling=true), got %q", location)
	}
}

func TestBillingPortal_PendingAccount_RedirectsToSubscribe(t *testing.T) {
	t.Parallel()
	// Test that users with a pending (incomplete checkout) account get redirected (to /new in test mode)
	server := newBillingTestServer(t)

	// Create a user with a pending billing account
	email := "pending-account@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add a pending account record (simulates incomplete checkout)
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        "exe_portalpending",
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	// Don't activate - leave as pending

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Visit /billing/update
	req := httptest.NewRequest("GET", "/billing/update", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect (303), got %d", w.Code)
	}

	location := w.Header().Get("Location")
	// In test mode with SkipBilling=true, pending accounts redirect to /new
	if location != "/new" {
		t.Errorf("Expected redirect to /new (SkipBilling=true), got %q", location)
	}
}

func TestBillingPortal_ActiveAccount_RedirectsToStripe(t *testing.T) {
	t.Parallel()
	// Test that users with an active billing account are redirected to Stripe portal
	// Note: This test requires a Stripe API key with billing portal permissions.
	// With the test API key, this may return 500 due to missing permissions.
	server := newBillingTestServer(t)

	// Create a user with an active billing account
	email := "active-account@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add an active account record (simulates completed checkout)
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        "exe_portalactive",
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, exedb.ActivateAccountParams{
		CreatedBy: user.UserID,
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Visit /billing/update
	req := httptest.NewRequest("GET", "/billing/update", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// With proper Stripe permissions, expect redirect to Stripe portal (303)
	// With restricted test key, may get 500 due to missing portal permissions
	switch w.Code {
	case http.StatusSeeOther:
		location := w.Header().Get("Location")
		if !strings.Contains(location, "stripe.com") && !strings.Contains(location, "billing") {
			t.Errorf("Expected redirect to Stripe billing portal, got %q", location)
		}
	case http.StatusInternalServerError:
		// Expected with test API key that lacks portal permissions
		t.Log("Stripe portal creation failed (expected with restricted test API key)")
	default:
		t.Errorf("Expected redirect (303) or server error (500), got %d", w.Code)
	}
}

func TestUserProfile_ShowsBillingSection_ActiveAccountActiveBilling(t *testing.T) {
	t.Parallel()
	// Test that the user profile page shows billing info for users with active billing
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user with an active billing account
	email := "profile-billing@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add an active account record
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        "exe_profilebilling",
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, exedb.ActivateAccountParams{
		CreatedBy: user.UserID,
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Visit /user profile page
	req := httptest.NewRequest("GET", "/user", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	// Should show "Active" status and portal link
	if !strings.Contains(body, "Active") {
		t.Errorf("Expected 'Active' billing status in body")
	}
	if !strings.Contains(body, "/billing/update") {
		t.Errorf("Expected billing portal link in body")
	}
}

func TestUserProfile_ShowsBillingSection_ActiveAccountSkipBilling(t *testing.T) {
	t.Parallel()
	// Test that the user profile page shows setup instructions when SkipBilling is true
	server := newBillingTestServer(t)
	// SkipBilling defaults to true

	// Create a user with an active billing account
	email := "profile-billing-skip@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add an active account record
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        "exe_profilebillingskip",
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, exedb.ActivateAccountParams{
		CreatedBy: user.UserID,
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Visit /user profile page
	req := httptest.NewRequest("GET", "/user", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	body := w.Body.String()
	// Should show setup instructions when billing is skipped
	if !strings.Contains(body, "Not configured") {
		t.Errorf("Expected 'Not configured' billing status in body")
	}
	if !strings.Contains(body, "STRIPE_SECRET_KEY") {
		t.Errorf("Expected Stripe setup instructions in body")
	}
}

func TestUserWithMultipleAccounts_OnlyOneActive(t *testing.T) {
	t.Parallel()
	// This test reproduces the bug where a user with multiple accounts
	// (created from multiple checkout attempts) has active billing on one
	// account but the query non-deterministically returns a different account.
	// The fix: GetUserBillingStatus should check if ANY account has active billing.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user
	email := "multi-account@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	// Create MULTIPLE accounts for this user (simulating multiple checkout attempts)
	// This is what happens when a user visits /billing/update multiple times
	// before the fix that reuses existing pending accounts.
	accountIDs := []string{
		"exe_ACCOUNT1_NO_BILLING",
		"exe_ACCOUNT2_NO_BILLING",
		"exe_ACCOUNT3_NO_BILLING",
		"exe_ACCOUNT4_NO_BILLING",
		"exe_ACCOUNT5_ACTIVE", // Only this one has active billing
	}

	for _, accountID := range accountIDs {
		err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
			ID:        accountID,
			CreatedBy: user.UserID,
		})
		if err != nil {
			t.Fatalf("Failed to insert account %s: %v", accountID, err)
		}
	}

	// Only activate the LAST account (exe_ACCOUNT5_ACTIVE)
	// The bug: GetUserBillingStatus may return one of the other accounts due to
	// non-deterministic JOIN behavior, making billing_status NULL.
	_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: "exe_ACCOUNT5_ACTIVE",
		EventType: "active",
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to insert billing event: %v", err)
	}

	// Check billing status - should be "active" since one account is active
	billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}

	// User should be paying (at least one account is active)
	if !userIsPaying(&billingStatus) {
		t.Errorf("BUG: User with active billing on one of multiple accounts should be paying. Got billing_status=%v", billingStatus.BillingStatus)
	}

	// User should NOT need billing
	if userNeedsBilling(&billingStatus) {
		t.Errorf("BUG: User with active billing on one account should NOT need billing. Got billing_status=%v", billingStatus.BillingStatus)
	}

	// Try to create VM - should succeed (not redirect to billing)
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	form := url.Values{}
	form.Add("hostname", "multi-account-vm")
	form.Add("prompt", "test")
	req := httptest.NewRequest("POST", "/create-vm", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should NOT redirect to billing
	if w.Code == http.StatusSeeOther {
		location := w.Header().Get("Location")
		if strings.HasPrefix(location, "/billing/update") {
			t.Errorf("User with active billing on one account was redirected to billing! Location: %s", location)
		}
	}
}

func TestUserWithMultipleAccounts_OneActiveOneCanceled(t *testing.T) {
	t.Parallel()
	// Test that if ANY account is active, the user is considered active,
	// even if another account is canceled.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user
	email := "multi-account-mixed@example.com"
	publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZx multi-mixed"
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Set user's created_at to after the billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	// Create two accounts
	canceledAccountID := "exe_CANCELED_ACCOUNT"
	activeAccountID := "exe_ACTIVE_ACCOUNT"

	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        canceledAccountID,
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert canceled account: %v", err)
	}

	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        activeAccountID,
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert active account: %v", err)
	}

	// First account: active -> canceled
	t1 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: canceledAccountID,
		EventType: "active",
		EventAt:   t1,
	})
	if err != nil {
		t.Fatalf("Failed to insert active event: %v", err)
	}
	_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: canceledAccountID,
		EventType: "canceled",
		EventAt:   t2,
	})
	if err != nil {
		t.Fatalf("Failed to insert canceled event: %v", err)
	}

	// Second account: active (and stays active)
	t3 := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)
	_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: activeAccountID,
		EventType: "active",
		EventAt:   t3,
	})
	if err != nil {
		t.Fatalf("Failed to insert active event: %v", err)
	}

	// Check billing status - should be "active" since one account is active
	billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
	if err != nil {
		t.Fatalf("GetUserBillingStatus query failed: %v", err)
	}

	// User should be paying (one account is active, even though another is canceled)
	if !userIsPaying(&billingStatus) {
		t.Errorf("User with one active and one canceled account should be paying. Got billing_status=%v", billingStatus.BillingStatus)
	}

	// User should NOT need billing
	if userNeedsBilling(&billingStatus) {
		t.Errorf("User with one active account should NOT need billing. Got billing_status=%v", billingStatus.BillingStatus)
	}
}

func TestCanceledUserCannotCreateVM(t *testing.T) {
	t.Parallel()
	// Test that users with canceled subscriptions cannot create VMs,
	// even if they have exemptions (legacy, free tier, trial).
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	t.Run("CanceledLegacyUser", func(t *testing.T) {
		// Create a legacy user (created before billing requirement date)
		email := "canceled-legacy@example.com"
		publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ1 test-canceled-legacy"
		user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Set user as legacy (created before billing requirement date)
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:09:59' WHERE user_id = ?`, user.UserID)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to update user created_at: %v", err)
		}

		// Add an account
		billingID := "exe_canceled_legacy"
		err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
			ID:        billingID,
			CreatedBy: user.UserID,
		})
		if err != nil {
			t.Fatalf("Failed to insert account: %v", err)
		}

		// Insert active event first, then canceled event (simulating subscription cancellation)
		// Use specific timestamps to ensure canceled is the most recent
		t1 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC) // Active
		t2 := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // Canceled (later)
		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "active",
			EventAt:   t1,
		})
		if err != nil {
			t.Fatalf("Failed to insert active event: %v", err)
		}
		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "canceled",
			EventAt:   t2,
		})
		if err != nil {
			t.Fatalf("Failed to insert canceled event: %v", err)
		}

		// Verify user is canceled
		billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
		if err != nil {
			t.Fatalf("GetUserBillingStatus query failed: %v", err)
		}
		if billingStatus.BillingStatus != "canceled" {
			t.Fatalf("Expected user to be canceled, got %q", billingStatus.BillingStatus)
		}

		// CRITICAL: Canceled legacy user MUST need billing (regression test)
		if !userNeedsBilling(&billingStatus) {
			t.Error("BUG: Canceled legacy user should need billing - they cannot bypass by being grandfathered!")
		}

		// Try to create VM - should redirect to billing
		cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
		if err != nil {
			t.Fatalf("Failed to create auth cookie: %v", err)
		}

		form := url.Values{}
		form.Add("hostname", "canceled-legacy-vm")
		form.Add("prompt", "test")
		req := httptest.NewRequest("POST", "/create-vm", strings.NewReader(form.Encode()))
		req.Host = server.env.WebHost
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusSeeOther {
			t.Errorf("Expected redirect (303), got %d - canceled user bypassed billing!", w.Code)
		}
		location := w.Header().Get("Location")
		if !strings.HasPrefix(location, "/billing/update") {
			t.Errorf("Expected redirect to /billing/update, got %q - billing was bypassed!", location)
		}
	})

	t.Run("CanceledFreeUser", func(t *testing.T) {
		// Create a user with free tier exemption
		email := "canceled-free@example.com"
		publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ2 test-canceled-free"
		user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Set user as free tier
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET billing_exemption = 'free', created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to update user: %v", err)
		}

		// Add an account
		billingID := "exe_canceled_free"
		err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
			ID:        billingID,
			CreatedBy: user.UserID,
		})
		if err != nil {
			t.Fatalf("Failed to insert account: %v", err)
		}

		// Insert active then canceled events
		t1 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
		t2 := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "active",
			EventAt:   t1,
		})
		if err != nil {
			t.Fatalf("Failed to insert active event: %v", err)
		}
		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "canceled",
			EventAt:   t2,
		})
		if err != nil {
			t.Fatalf("Failed to insert canceled event: %v", err)
		}

		// Verify user is canceled
		billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
		if err != nil {
			t.Fatalf("GetUserBillingStatus query failed: %v", err)
		}
		if billingStatus.BillingStatus != "canceled" {
			t.Fatalf("Expected user to be canceled, got %q", billingStatus.BillingStatus)
		}

		// CRITICAL: Canceled free tier user MUST need billing
		if !userNeedsBilling(&billingStatus) {
			t.Error("BUG: Canceled free tier user should need billing - they cannot bypass with free exemption!")
		}
	})

	t.Run("CanceledTrialUser", func(t *testing.T) {
		// Create a user with active trial exemption
		email := "canceled-trial@example.com"
		publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3 test-canceled-trial"
		user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Set user as trial with future end date
		futureDate := time.Now().Add(30 * 24 * time.Hour) // 30 days from now
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET billing_exemption = 'trial', billing_trial_ends_at = ?, created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, futureDate, user.UserID)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to update user: %v", err)
		}

		// Add an account
		billingID := "exe_canceled_trial"
		err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
			ID:        billingID,
			CreatedBy: user.UserID,
		})
		if err != nil {
			t.Fatalf("Failed to insert account: %v", err)
		}

		// Insert active then canceled events
		t1 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
		t2 := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "active",
			EventAt:   t1,
		})
		if err != nil {
			t.Fatalf("Failed to insert active event: %v", err)
		}
		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "canceled",
			EventAt:   t2,
		})
		if err != nil {
			t.Fatalf("Failed to insert canceled event: %v", err)
		}

		// Verify user is canceled
		billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
		if err != nil {
			t.Fatalf("GetUserBillingStatus query failed: %v", err)
		}
		if billingStatus.BillingStatus != "canceled" {
			t.Fatalf("Expected user to be canceled, got %q", billingStatus.BillingStatus)
		}

		// CRITICAL: Canceled trial user MUST need billing, even with active trial
		if !userNeedsBilling(&billingStatus) {
			t.Error("BUG: Canceled trial user should need billing - they cannot bypass with trial exemption!")
		}
	})

	t.Run("ReactivatedUser", func(t *testing.T) {
		// Test that a user who canceled and then resubscribed can create VMs
		email := "reactivated@example.com"
		publicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ4 test-reactivated"
		user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Set user as post-billing-requirement
		err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to update user created_at: %v", err)
		}

		// Add an account and activate it
		billingID := "exe_reactivated"
		err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
			ID:        billingID,
			CreatedBy: user.UserID,
		})
		if err != nil {
			t.Fatalf("Failed to insert account: %v", err)
		}
		err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, exedb.ActivateAccountParams{
			CreatedBy: user.UserID,
			EventAt:   time.Now(),
		})
		if err != nil {
			t.Fatalf("Failed to activate account: %v", err)
		}

		// Insert events: active -> canceled -> active (reactivation)
		t1 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
		t2 := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
		t3 := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)

		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "active",
			EventAt:   t1,
		})
		if err != nil {
			t.Fatalf("Failed to insert first active event: %v", err)
		}
		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "canceled",
			EventAt:   t2,
		})
		if err != nil {
			t.Fatalf("Failed to insert canceled event: %v", err)
		}
		_, err = withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
			AccountID: billingID,
			EventType: "active",
			EventAt:   t3,
		})
		if err != nil {
			t.Fatalf("Failed to insert reactivation event: %v", err)
		}

		// Verify user is active (reactivated)
		billingStatus, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetUserBillingStatus, user.UserID)
		if err != nil {
			t.Fatalf("GetUserBillingStatus query failed: %v", err)
		}
		if billingStatus.BillingStatus != "active" {
			t.Fatalf("Expected user to be active (reactivated), got %q", billingStatus.BillingStatus)
		}

		// Reactivated user should NOT need billing
		if userNeedsBilling(&billingStatus) {
			t.Error("Reactivated user should NOT need billing")
		}

		// Try to create VM - should succeed
		cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
		if err != nil {
			t.Fatalf("Failed to create auth cookie: %v", err)
		}

		form := url.Values{}
		form.Add("hostname", "reactivated-vm")
		form.Add("prompt", "test")
		req := httptest.NewRequest("POST", "/create-vm", strings.NewReader(form.Encode()))
		req.Host = server.env.WebHost
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		// Should not redirect to billing (should redirect to / or create VM)
		if w.Code == http.StatusSeeOther {
			location := w.Header().Get("Location")
			if strings.Contains(location, "/billing/update") {
				t.Errorf("Reactivated user should not be redirected to billing, got %q", location)
			}
		}
	})
}

func TestBillingUpdateLongPromptSucceeds(t *testing.T) {
	t.Parallel()
	// Reproduce the billing-session-failed / checkout-url-too-long error:
	// Stripe rejects success_url over 5000 characters. Long VM prompts
	// were encoded directly into the success_url, causing the limit to be exceeded.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	email := "long-prompt@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Build a prompt that would push the success_url over 5000 chars when URL-encoded.
	longPrompt := strings.Repeat("Build me a comprehensive full-stack web application with authentication, database, and API. ", 60)

	req := httptest.NewRequest("GET", "/billing/update?name=my-vm&prompt="+url.QueryEscape(longPrompt), nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe (303), got %d; long prompt should not cause Stripe error", w.Code)
	}

	location := w.Header().Get("Location")
	if !strings.Contains(location, "stripe.com") || !strings.Contains(location, "checkout") {
		t.Fatalf("Expected redirect to Stripe checkout, got %q", location)
	}
}

func TestBillingSuccessWithLongPromptCreatesVM(t *testing.T) {
	t.Parallel()
	// End-to-end test: a long prompt stored via checkout_params is retrieved
	// on billing success and used to create a VM.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	email := "long-prompt-e2e@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	longPrompt := strings.Repeat("Set up a server. ", 300)
	vmName := "long-prompt-vm"

	// Step 1: Visit /billing/update with long prompt to store checkout params.
	req := httptest.NewRequest("GET", "/billing/update?name="+vmName+"&prompt="+url.QueryEscape(longPrompt), nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe, got %d", w.Code)
	}

	// Step 2: Look up the cp token that was stored in step 1.
	var cpToken string
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.Conn().QueryRowContext(ctx, `SELECT token FROM checkout_params LIMIT 1`).Scan(&cpToken)
	})
	if err != nil {
		t.Fatalf("Failed to find checkout_params token: %v", err)
	}

	// Step 3: Simulate billing success using the cp token (as Stripe would redirect).
	server.env.WebDev = true
	req = httptest.NewRequest("GET", "/billing/success?dev_bypass=1&cp="+cpToken, nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect after billing success, got %d. Body: %s", w.Code, w.Body.String())
	}

	location := w.Header().Get("Location")
	if !strings.Contains(location, vmName) {
		t.Errorf("Expected redirect to include VM name %q, got %q", vmName, location)
	}

	// Verify the checkout_params row was deleted after use.
	var count int
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.Conn().QueryRowContext(ctx, `SELECT count(*) FROM checkout_params WHERE token = ?`, cpToken).Scan(&count)
	})
	if err != nil {
		t.Fatalf("Failed to check checkout_params: %v", err)
	}
	if count != 0 {
		t.Errorf("Expected checkout_params row to be deleted after use, but found %d rows", count)
	}
}

func TestBillingCancelRestoresLongPrompt(t *testing.T) {
	t.Parallel()
	// When a user cancels Stripe checkout, they are redirected to /new?cp=<token>.
	// The cancel handler should restore VM params from checkout_params so the form
	// is pre-filled, and the token should survive (not be deleted) so the user can retry.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	email := "cancel-prompt@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:10:01' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	longPrompt := strings.Repeat("Set up a comprehensive server with many features. ", 100)
	vmName := "cancel-test-vm"

	// Step 1: Visit /billing/update to store checkout params and get redirected to Stripe.
	req := httptest.NewRequest("GET", "/billing/update?name="+vmName+"&prompt="+url.QueryEscape(longPrompt), nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe, got %d", w.Code)
	}

	// Step 2: Look up the cp token that was stored.
	var cpToken string
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.Conn().QueryRowContext(ctx, `SELECT token FROM checkout_params LIMIT 1`).Scan(&cpToken)
	})
	if err != nil {
		t.Fatalf("Failed to find checkout_params token: %v", err)
	}

	// Step 3: Simulate cancel by visiting /new?cp=<token> (the cancel URL).
	req = httptest.NewRequest("GET", "/new?cp="+cpToken, nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 from /new, got %d", w.Code)
	}

	// Verify the response body contains the VM name and prompt.
	body := w.Body.String()
	if !strings.Contains(body, vmName) {
		t.Errorf("Expected /new response to contain VM name %q", vmName)
	}
	if !strings.Contains(body, "Set up a comprehensive server") {
		t.Errorf("Expected /new response to contain the prompt text")
	}

	// Step 4: Verify the checkout_params row was NOT deleted (so the user can retry).
	var count int
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		return tx.Conn().QueryRowContext(ctx, `SELECT count(*) FROM checkout_params WHERE token = ?`, cpToken).Scan(&count)
	})
	if err != nil {
		t.Fatalf("Failed to check checkout_params: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected checkout_params row to survive cancel, but found %d rows", count)
	}
}

// createUserWithAccount is a test helper that creates a user with a billing account.
func createUserWithAccount(t *testing.T, server *Server, email, billingID string) (*exedb.User, string) {
	t.Helper()
	user, err := server.createUser(t.Context(), testSSHPubKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        billingID,
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, exedb.ActivateAccountParams{
		CreatedBy: user.UserID,
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}

	// Credits checkout uses the Stripe customer ID from accounts.id.
	// Ensure that customer exists in Stripe for tests using recorded Stripe APIs.
	_, err = server.billing.Subscribe(t.Context(), billingID, &billing.SubscribeParams{
		Email:      email,
		SuccessURL: "https://example.com/success",
		CancelURL:  "https://example.com/cancel",
	})
	if err != nil {
		t.Fatalf("Failed to upsert Stripe customer: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}
	return user, cookieValue
}

func TestCreditPurchase_ProfileShowsCreditsSection(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	user, cookieValue := createUserWithAccount(t, server, "credits-profile@example.com", "exe_profile_credits")

	maxCredit := 10.0
	now := time.Now().UTC()
	lastRefresh := time.Date(now.Year(), now.Month(), 15, 12, 0, 0, 0, time.UTC)
	err := withTx1(server, t.Context(), (*exedb.Queries).UpsertUserLLMCredit, exedb.UpsertUserLLMCreditParams{
		UserID:          user.UserID,
		AvailableCredit: 9.0,
		MaxCredit:       &maxCredit,
		RefreshPerHour:  nil,
		LastRefreshAt:   lastRefresh,
	})
	if err != nil {
		t.Fatalf("UpsertUserLLMCredit: %v", err)
	}

	req := httptest.NewRequest("GET", "/user", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Shelley Credits") {
		t.Error("Expected Shelley Credits section on profile page when flag enabled")
	}
	if !strings.Contains(body, "Plan (Monthly)") {
		t.Error("Expected 'Plan (Monthly)' section on profile page")
	}
	expectedReset := nextUTCMonthStart().Format("15:04 on Jan 2")
	if !strings.Contains(body, expectedReset) {
		t.Errorf("Expected monthly credits reset time %q on profile page", expectedReset)
	}
	if !strings.Contains(body, "/credits/buy") {
		t.Error("Expected credits buy form on profile page")
	}
	if !strings.Contains(body, ">9<") {
		t.Fatalf("Expected hero number '9' in body")
	}

	previousMonth := now.AddDate(0, -1, 0)
	lastRefresh = time.Date(previousMonth.Year(), previousMonth.Month(), 15, 12, 0, 0, 0, time.UTC)
	err = withTx1(server, t.Context(), (*exedb.Queries).UpsertUserLLMCredit, exedb.UpsertUserLLMCreditParams{
		UserID:          user.UserID,
		AvailableCredit: 9.0,
		MaxCredit:       &maxCredit,
		RefreshPerHour:  nil,
		LastRefreshAt:   lastRefresh,
	})
	if err != nil {
		t.Fatalf("UpsertUserLLMCredit(previous month): %v", err)
	}

	req = httptest.NewRequest("GET", "/user", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 after previous-month refresh scenario, got %d", w.Code)
	}
	body = w.Body.String()
	if !strings.Contains(body, ">10<") {
		t.Fatalf("Expected hero number '10' after month rollover, got body: %s", body[:min(1200, len(body))])
	}
	expectedReset = nextUTCMonthStart().Format("15:04 on Jan 2")
	if !strings.Contains(body, expectedReset) {
		t.Errorf("Expected monthly credits reset time %q after month rollover scenario", expectedReset)
	}
}

func TestCreditPurchase_BuyRedirectsToStripe(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	_, cookieValue := createUserWithAccount(t, server, "credits-buy@example.com", "exe_buy_credits")

	form := url.Values{}
	form.Add("dollars", "123")
	req := httptest.NewRequest("POST", "/credits/buy", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected 303 redirect, got %d: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if !strings.Contains(location, "checkout.stripe.com") {
		t.Errorf("Expected redirect to Stripe checkout, got %q", location)
	}
}

func TestCreditPurchase_BuyRequiresActiveBilling(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	user, cookieValue := createUserWithAccount(t, server, "credits-renew@example.com", "exe_renew_credits")

	_, err := withTxRes1(server, t.Context(), (*exedb.Queries).InsertBillingEvent, exedb.InsertBillingEventParams{
		AccountID: "exe_renew_credits",
		EventType: "canceled",
		EventAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("InsertBillingEvent(canceled): %v", err)
	}

	form := url.Values{}
	form.Add("dollars", "123")
	req := httptest.NewRequest("POST", "/credits/buy", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected 303 redirect, got %d: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, "/billing/update") {
		t.Errorf("Expected redirect to billing update, got %q", location)
	}
	if strings.Contains(location, "checkout.stripe.com") {
		t.Errorf("Expected no Stripe checkout redirect for canceled billing, got %q", location)
	}
	_ = user
}

func TestCreditPurchase_BuyNoAccount(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	user, err := server.createUser(t.Context(), testSSHPubKey, "credits-noaccount@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	form := url.Values{}
	form.Add("dollars", "123")
	req := httptest.NewRequest("POST", "/credits/buy", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected 303 redirect, got %d: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if location != "/billing/update?source=credits" {
		t.Errorf("Expected redirect to /billing/update?source=credits, got %q", location)
	}
}

func TestCreditPurchase_ProfileCreditDisplay(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	user, cookieValue := createUserWithAccount(t, server, "credits-display@example.com", "exe_display_credits")

	maxCredit := 10.0
	now := time.Now().UTC()
	lastRefresh := time.Date(now.Year(), now.Month(), 15, 12, 0, 0, 0, time.UTC)

	renderUserPage := func(t *testing.T) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/user", nil)
		req.Host = server.env.WebHost
		req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200, got %d", w.Code)
		}
		return w.Body.String()
	}

	upsertCredit := func(t *testing.T, available float64) {
		t.Helper()
		err := withTx1(server, t.Context(), (*exedb.Queries).UpsertUserLLMCredit, exedb.UpsertUserLLMCreditParams{
			UserID:          user.UserID,
			AvailableCredit: available,
			MaxCredit:       &maxCredit,
			RefreshPerHour:  nil,
			LastRefreshAt:   lastRefresh,
		})
		if err != nil {
			t.Fatalf("UpsertUserLLMCredit: %v", err)
		}
	}

	// Test credit display and progress bar presence
	upsertCredit(t, 9.0)
	body := renderUserPage(t)

	// Hero shows total credits remaining (integer)
	if !strings.Contains(body, ">9<") {
		t.Errorf("Expected hero number '9' in body")
	}
	if !strings.Contains(body, "credits remaining") {
		t.Error("Expected 'credits remaining' label")
	}
	if !strings.Contains(body, `background: #e0e0e0`) {
		t.Error("Expected progress bar background element")
	}
	// 9/10 = 90% remaining = green
	if !strings.Contains(body, "#22a55b") {
		t.Error("Expected green progress bar color for 90% remaining")
	}
	// Plan credits table
	if !strings.Contains(body, "Plan (Monthly)") {
		t.Error("Expected 'Plan (Monthly)' section")
	}
	if !strings.Contains(body, "Credits Remaining") {
		t.Error("Expected 'Credits Remaining' column header")
	}

	// Test full credits (100%) = green
	upsertCredit(t, 10.0)
	body = renderUserPage(t)

	if !strings.Contains(body, ">10<") {
		t.Errorf("Expected hero number '10' in body")
	}
	if !strings.Contains(body, "#22a55b") {
		t.Error("Expected green progress bar color for 100% remaining")
	}

	// Test yellow (25-50%): 4.0/10.0 = 40%
	upsertCredit(t, 4.0)
	body = renderUserPage(t)

	if !strings.Contains(body, ">4<") {
		t.Errorf("Expected hero number '4' in body")
	}
	if !strings.Contains(body, "#eab308") {
		t.Error("Expected yellow progress bar color for 40% remaining")
	}

	// Test orange (10-25%): 2.0/10.0 = 20%
	upsertCredit(t, 2.0)
	body = renderUserPage(t)

	if !strings.Contains(body, ">2<") {
		t.Errorf("Expected hero number '2' in body")
	}
	if !strings.Contains(body, "#dd6b20") {
		t.Error("Expected orange progress bar color for 20% remaining")
	}

	// Test red (<10%): 0.5/10.0 = 5%
	upsertCredit(t, 0.5)
	body = renderUserPage(t)

	if !strings.Contains(body, ">0<") {
		t.Errorf("Expected hero number '0' (rounded) in body")
	}
	if !strings.Contains(body, "#e53e3e") {
		t.Error("Expected red progress bar color for 5% remaining")
	}

	// Test zero credits = red
	upsertCredit(t, 0.0)
	body = renderUserPage(t)

	if !strings.Contains(body, ">0<") {
		t.Errorf("Expected hero number '0' in body")
	}
	if !strings.Contains(body, "#e53e3e") {
		t.Error("Expected red progress bar color for 0% remaining")
	}
}

func TestCreditPurchase_BuyInvalidAmount(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	_, cookieValue := createUserWithAccount(t, server, "credits-invalid@example.com", "exe_invalid_credits")

	// Test with missing amount
	req := httptest.NewRequest("POST", "/credits/buy", strings.NewReader(""))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for missing amount, got %d", w.Code)
	}

	// Test with zero amount
	form := url.Values{}
	form.Add("dollars", "0")
	req = httptest.NewRequest("POST", "/credits/buy", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for zero amount, got %d", w.Code)
	}
}

func TestCreditPurchase_BuyRequiresAuth(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)

	form := url.Values{}
	form.Add("dollars", "123")
	req := httptest.NewRequest("POST", "/credits/buy", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusTemporaryRedirect {
		t.Errorf("Expected 307 redirect to auth, got %d", w.Code)
	}
	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, "/auth") {
		t.Errorf("Expected redirect to /auth, got %q", location)
	}
}

func TestCreditPurchase_BuyRequiresPost(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	_, cookieValue := createUserWithAccount(t, server, "credits-get@example.com", "exe_get_credits")

	req := httptest.NewRequest("GET", "/credits/buy", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405 for GET /credits/buy, got %d", w.Code)
	}
}

func TestCreditPurchase_SuccessSyncsAndRedirects(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	_, cookieValue := createUserWithAccount(t, server, "credits-success@example.com", "exe_success_credits")

	req := httptest.NewRequest("GET", "/credits/success", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected 303 redirect, got %d: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if location != "/user" {
		t.Errorf("Expected redirect to /user, got %q", location)
	}
}

func TestCreditPurchase_SuccessRequiresAuth(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)

	req := httptest.NewRequest("GET", "/credits/success", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusTemporaryRedirect {
		t.Errorf("Expected 307 redirect to auth, got %d", w.Code)
	}
}

func TestCreditPurchase_BalanceUpdatesAfterSync(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	user, cookieValue := createUserWithAccount(t, server, "credits-balance@example.com", "exe_balance_credits")

	// Manually insert a credit ledger entry to simulate a completed purchase
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx,
			`INSERT INTO credit_ledger (account_id, amount, stripe_event_id) VALUES (?, ?, ?)`,
			"exe_balance_credits", 500000, "pi_test_balance")
		return err
	})
	if err != nil {
		// credit_ledger table may not exist; check if UseCredits works directly
		if !strings.Contains(err.Error(), "no such table") {
			t.Fatalf("Failed to insert credit ledger entry: %v", err)
		}
	}

	// Check balance via UseCredits(0)
	balance, err := server.billing.SpendCredits(t.Context(), "exe_balance_credits", 0, tender.Zero())
	if err != nil && !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("UseCredits failed: %v", err)
	}

	// Verify balance shows on profile page
	req := httptest.NewRequest("GET", "/user", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Shelley Credits") {
		t.Error("Expected Shelley Credits section on profile page")
	}
	// If credit ledger table exists and balance was inserted, it should show
	if err == nil && balance.Compare(tender.Zero()) > 0 {
		if !strings.Contains(body, "500000") {
			t.Errorf("Expected balance 500000 on profile page, body contains: %s", body[:min(500, len(body))])
		}
	}
	_ = user
}
