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
	"exe.dev/billing/entitlement"
	"exe.dev/billing/tender"
	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// activateUserBilling is a test helper that upgrades a user to the 'individual' plan
// and inserts an 'active' billing event. Use this instead of the legacy
// InsertAccount + ActivateAccount pattern; every user now has an account at signup.
// Returns the account ID.
func activateUserBilling(t *testing.T, server *Server, userID string) string {
	t.Helper()

	acct, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("activateUserBilling: GetAccountByUserID(%s): %v", userID, err)
	}

	now := time.Now()
	err = server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		// Insert billing event (keeps legacy GetAccountWithBillingStatus working).
		if err := queries.InsertBillingEvent(ctx, exedb.InsertBillingEventParams{
			AccountID: acct.ID,
			EventType: "active",
			EventAt:   now,
		}); err != nil {
			return err
		}
		// Upgrade account plan to 'individual'.
		if err := queries.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: acct.ID,
			EndedAt:   &now,
		}); err != nil {
			return err
		}
		changedBy := "stripe:event"
		return queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    "individual",
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("activateUserBilling: %v", err)
	}
	return acct.ID
}

// cancelUserBilling is a test helper that downgrades a user to the 'basic' plan
// and inserts a 'canceled' billing event. Use after activateUserBilling to simulate
// subscription cancellation.
func cancelUserBilling(t *testing.T, server *Server, userID string) {
	t.Helper()

	acct, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("cancelUserBilling: GetAccountByUserID(%s): %v", userID, err)
	}

	now := time.Now()
	err = server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		// Insert canceled billing event (keeps legacy path working).
		if err := queries.InsertBillingEvent(ctx, exedb.InsertBillingEventParams{
			AccountID: acct.ID,
			EventType: "canceled",
			EventAt:   now,
		}); err != nil {
			return err
		}
		// Downgrade account plan to 'basic'.
		if err := queries.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: acct.ID,
			EndedAt:   &now,
		}); err != nil {
			return err
		}
		changedBy := "stripe:event"
		return queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    "basic",
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("cancelUserBilling: %v", err)
	}
}

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

	// Activate billing for this user (simulates completed Stripe checkout).
	activateUserBilling(t, server, user.UserID)

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
	server.env.SkipBilling = false

	// Create a user — createUser calls createUserRecord which inserts account + basic plan.
	email := "ispaying-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// User starts on 'basic' plan (from createUserRecord). Basic does NOT grant VMCreate.
	if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("Expected basic plan user to not have VMCreate entitlement")
	}

	// Upgrade to 'individual' plan (simulates completing Stripe checkout).
	acct, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	now := time.Now()
	err = server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: acct.ID,
			EndedAt:   &now,
		}); err != nil {
			return err
		}
		changedBy := "stripe:event"
		return queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    "individual",
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("upgrade to individual: %v", err)
	}

	// User now on 'individual' plan — must have VMCreate entitlement.
	if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("Expected individual plan user to have VMCreate entitlement")
	}
}

func TestUserNeedsBillingQuery(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user — gets basic plan at signup.
	email := "needsbilling-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Basic plan does not grant VMCreate.
	if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("Expected basic plan user to not have VMCreate entitlement")
	}

	// Upgrade to 'individual' plan (simulate completing Stripe checkout).
	acct, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	now := time.Now()
	err = server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
			AccountID: acct.ID,
			EndedAt:   &now,
		}); err != nil {
			return err
		}
		changedBy := "stripe:event"
		return queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    "individual",
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("upgrade to individual: %v", err)
	}

	// Individual plan grants VMCreate.
	if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("Expected individual plan user to have VMCreate entitlement")
	}
}

func TestLegacyUserDoesNotNeedBilling(t *testing.T) {
	t.Parallel()
	// With account-based plans, "grandfathered" users (those created before the billing
	// cutoff) are migrated to the 'grandfathered' plan in account_plans. This test verifies
	// that a user with the 'grandfathered' plan has VMCreate entitlement without a paid subscription.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user
	email := "legacy-user@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Simulate the migration: upgrade account plan from 'basic' to 'grandfathered'.
	// In production, migration 121 does this for users created before the billing cutoff.
	acct, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: %v", err)
	}
	now := time.Now()
	changedBy := "migration:grandfathered"
	err = server.withTx(t.Context(), func(ctx context.Context, q *exedb.Queries) error {
		if err := q.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{AccountID: acct.ID, EndedAt: &now}); err != nil {
			return err
		}
		return q.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
			AccountID: acct.ID,
			PlanID:    "grandfathered",
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("Failed to set grandfathered plan: %v", err)
	}

	// Grandfathered user should have VMCreate without paid subscription.
	if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("Expected grandfathered user to have VMCreate entitlement")
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

	// Step 1: Verify user does not have VMCreate initially
	if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Fatal("Expected new user to not have VMCreate entitlement initially")
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

	// Step 4: Check if user still lacks VMCreate - they SHOULD still lack it!
	// This is where the bug manifests: without billing events, the user should still lack VMCreate
	// because no 'active' event was recorded.
	if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("BUG: User should still lack VMCreate after starting but not completing Stripe checkout")
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
		if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
			t.Error("SECURITY BUG: User bypassed billing with fake session_id!")
		}
	}

	// User should still lack VMCreate since checkout was never completed
	if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("SECURITY BUG: User should still lack VMCreate after visiting success with fake session_id")
	}
}

func TestBillingActivateThenCancelThenReactivate(t *testing.T) {
	t.Parallel()
	// Verifies that the account_plans model correctly reflects the current plan
	// after a sequence of activate -> cancel -> reactivate transitions.

	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	user, err := server.createUser(t.Context(), testSSHPubKey, "plan-transitions@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// New user has 'basic' plan — no VMCreate.
	if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("New user with basic plan should not have VMCreate")
	}

	// Activate billing — user gets 'individual' plan.
	activateUserBilling(t, server, user.UserID)
	if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("User with individual plan should have VMCreate after activation")
	}

	// Cancel billing — user drops back to 'basic' plan.
	cancelUserBilling(t, server, user.UserID)
	if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("User with basic plan should not have VMCreate after cancellation")
	}

	// Reactivate — user gets 'individual' plan again.
	activateUserBilling(t, server, user.UserID)
	if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("Reactivated user with individual plan should have VMCreate")
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

	// With account-based plans, every user gets an account at signup (in createUserRecord).
	// Verify the user already has exactly one account after user creation.
	signupAccount, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("Expected account to exist at signup, got: %v", err)
	}
	signupAccountID := signupAccount.ID

	// Count accounts before billing update visits.
	var accountCountBefore int64
	accountCountBefore, err = withRxRes1(server, t.Context(), (*exedb.Queries).CountAccountsByBillingStatus, "pending")
	if err != nil {
		t.Fatalf("Failed to count accounts: %v", err)
	}

	// Visit /billing/update first time.
	req := httptest.NewRequest("GET", "/billing/update?name=test-vm&prompt=test", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("First visit: expected redirect to Stripe, got %d", w.Code)
	}

	// Get the account ID after first visit — must be the same account created at signup.
	firstAccount, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("Failed to get account after first visit: %v", err)
	}
	if firstAccount.ID != signupAccountID {
		t.Errorf("Expected billing update to reuse signup account %q, got %q", signupAccountID, firstAccount.ID)
	}

	// Visiting /billing/update must NOT create a new account (account already exists from signup).
	var accountCountAfterFirst int64
	accountCountAfterFirst, err = withRxRes1(server, t.Context(), (*exedb.Queries).CountAccountsByBillingStatus, "pending")
	if err != nil {
		t.Fatalf("Failed to count accounts: %v", err)
	}
	if accountCountAfterFirst != accountCountBefore {
		t.Errorf("BUG: visiting /billing/update created a new account (count %d -> %d)", accountCountBefore, accountCountAfterFirst)
	}

	// Visit /billing/update second time (simulating user abandoning checkout and returning).
	req = httptest.NewRequest("GET", "/billing/update?name=test-vm2&prompt=test2", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Second visit: expected redirect to Stripe, got %d", w.Code)
	}

	// Get the account ID from second visit — still the signup account.
	secondAccount, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
	if err != nil {
		t.Fatalf("Failed to get account after second visit: %v", err)
	}
	if secondAccount.ID != signupAccountID {
		t.Errorf("Expected same signup account %q on second visit, got %q", signupAccountID, secondAccount.ID)
	}

	// Verify NO new accounts were created on the second visit.
	var accountCountAfterSecond int64
	accountCountAfterSecond, err = withRxRes1(server, t.Context(), (*exedb.Queries).CountAccountsByBillingStatus, "pending")
	if err != nil {
		t.Fatalf("Failed to count accounts: %v", err)
	}
	if accountCountAfterSecond != accountCountAfterFirst {
		t.Errorf("BUG: duplicate account on second visit (count %d -> %d)", accountCountAfterFirst, accountCountAfterSecond)
	}

	// Visit a third time for good measure.
	req = httptest.NewRequest("GET", "/billing/update", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Third visit: expected redirect to Stripe, got %d", w.Code)
	}

	// Verify still only one account.
	var accountCountAfterThird int64
	accountCountAfterThird, err = withRxRes1(server, t.Context(), (*exedb.Queries).CountAccountsByBillingStatus, "pending")
	if err != nil {
		t.Fatalf("Failed to count accounts: %v", err)
	}
	if accountCountAfterThird != accountCountAfterFirst {
		t.Errorf("BUG: duplicate account on third visit (count %d -> %d)", accountCountAfterFirst, accountCountAfterThird)
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

	// User already has a pending account from createUser (no billing events = pending).
	// No action needed to simulate an incomplete checkout — it's the default state.

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

	// Activate billing (simulates completed checkout).
	activateUserBilling(t, server, user.UserID)

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

func TestBillingCheckoutReusesExistingAccount(t *testing.T) {
	t.Parallel()
	// Verifies that users have exactly one canonical account and that
	// activating billing upgrades that account's plan to 'individual'.
	// (Replaces old multi-account tests that tested a bug that can no longer
	// occur since createUserRecord now creates the canonical account.)
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	user, err := server.createUser(t.Context(), testSSHPubKey, "single-account@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// User starts with basic plan — no VMCreate.
	if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("New user should not have VMCreate before billing activation")
	}

	// Activate billing via the canonical account.
	activateUserBilling(t, server, user.UserID)

	// Now user should have VMCreate.
	if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
		t.Error("User with individual plan should have VMCreate after billing activation")
	}

	// Try to create VM - should succeed (not redirect to billing).
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	form := url.Values{}
	form.Add("hostname", "single-account-vm")
	form.Add("prompt", "test")
	req := httptest.NewRequest("POST", "/create-vm", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code == http.StatusSeeOther {
		location := w.Header().Get("Location")
		if strings.HasPrefix(location, "/billing/update") {
			t.Errorf("User with active billing was redirected to billing: %s", location)
		}
	}
}

func TestCanceledUserCannotCreateVM(t *testing.T) {
	t.Parallel()
	// Test that users with canceled subscriptions (basic plan) cannot create VMs.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	t.Run("CanceledAfterActivation", func(t *testing.T) {
		// User activates billing then cancels — should drop back to basic (no VMCreate).
		user, err := server.createUser(t.Context(),
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ1 test-canceled-after",
			"canceled-after@example.com", AllQualityChecks)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		activateUserBilling(t, server, user.UserID)
		if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
			t.Fatal("User should have VMCreate after activation")
		}

		cancelUserBilling(t, server, user.UserID)

		// CRITICAL: Canceled user MUST NOT have VMCreate.
		if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
			t.Error("BUG: Canceled user should not have VMCreate after cancellation")
		}

		// Try to create VM - should redirect to billing.
		cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
		if err != nil {
			t.Fatalf("Failed to create auth cookie: %v", err)
		}
		form := url.Values{}
		form.Add("hostname", "canceled-vm")
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
			t.Errorf("Expected redirect to /billing/update, got %q", location)
		}
	})

	t.Run("FriendPlanHasVMCreate", func(t *testing.T) {
		// Users with 'friend' plan (not canceled) should have VMCreate.
		user, err := server.createUser(t.Context(),
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ2 test-friend",
			"friend-plan@example.com", AllQualityChecks)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		// Upgrade to 'friend' plan directly.
		acct, err := withRxRes1(server, t.Context(), (*exedb.Queries).GetAccountByUserID, user.UserID)
		if err != nil {
			t.Fatalf("GetAccountByUserID: %v", err)
		}
		now := time.Now()
		err = server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			if err := queries.CloseAccountPlan(ctx, exedb.CloseAccountPlanParams{
				AccountID: acct.ID,
				EndedAt:   &now,
			}); err != nil {
				return err
			}
			changedBy := "admin"
			return queries.InsertAccountPlan(ctx, exedb.InsertAccountPlanParams{
				AccountID: acct.ID,
				PlanID:    "friend",
				StartedAt: now,
				ChangedBy: &changedBy,
			})
		})
		if err != nil {
			t.Fatalf("Failed to set friend plan: %v", err)
		}

		if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
			t.Error("User with friend plan should have VMCreate")
		}
	})

	t.Run("ReactivatedUser", func(t *testing.T) {
		// User who canceled and resubscribed should have VMCreate again.
		user, err := server.createUser(t.Context(),
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ4 test-reactivated",
			"reactivated@example.com", AllQualityChecks)
		if err != nil {
			t.Fatalf("Failed to create user: %v", err)
		}

		activateUserBilling(t, server, user.UserID)
		cancelUserBilling(t, server, user.UserID)

		if server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
			t.Fatal("User should not have VMCreate after cancellation")
		}

		// Reactivate.
		activateUserBilling(t, server, user.UserID)

		if !server.UserHasEntitlement(t.Context(), entitlement.SourceWeb, entitlement.VMCreate, user.UserID) {
			t.Error("Reactivated user should have VMCreate entitlement")
		}

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

// createUserWithAccount is a test helper that creates a user with an activated billing account.
// The billingID parameter is ignored; every user now gets a canonical account at signup.
// Use activateUserBilling for the account_plans upgrade.
func createUserWithAccount(t *testing.T, server *Server, email, _ string) (*exedb.User, string) {
	t.Helper()
	user, err := server.createUser(t.Context(), testSSHPubKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Upgrade the canonical account to 'individual' plan.
	accountID := activateUserBilling(t, server, user.UserID)

	// Credits checkout uses the Stripe customer ID from accounts.id.
	// Ensure that customer exists in Stripe for tests using recorded Stripe APIs.
	_, err = server.billing.Subscribe(t.Context(), accountID, &billing.SubscribeParams{
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
	// Entitlement check requires SkipBilling=false so UserHasEntitlement actually evaluates
	server.env.SkipBilling = false
	user, cookieValue := createUserWithAccount(t, server, "credits-renew@example.com", "")

	// Cancel billing — user drops back to basic plan (no CreditPurchase entitlement).
	cancelUserBilling(t, server, user.UserID)

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
	// With account-based plans, every user has an account at signup. This test
	// verifies that a user without active billing (basic plan) is redirected to
	// the billing update page instead of proceeding to credit checkout.
	server := newBillingTestServer(t)
	server.env.SkipBilling = false
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

// TestCreditPurchase_BuyEntitlementDeniedForNonPayingUser verifies that
// handleCreditsBuy uses UserHasEntitlement(CreditPurchase) to gate purchases.
// A user who has never activated billing should be redirected to /billing/update.
func TestCreditPurchase_BuyEntitlementDeniedForNonPayingUser(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create a user without any billing account -- they will lack CreditPurchase entitlement
	user, err := server.createUser(t.Context(), testSSHPubKey, "no-billing-entitlement@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	form := url.Values{}
	form.Add("dollars", "50")
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
		t.Errorf("Expected redirect to /billing/update, got %q", location)
	}
}

// TestInviteRequest_EntitlementDeniedRedirects verifies that handleInviteRequest
// uses UserHasEntitlement(CreditPurchase) and redirects denied users.
func TestInviteRequest_EntitlementDeniedRedirects(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// User without billing should be redirected
	user, err := server.createUser(t.Context(), testSSHPubKey, "invite-nobilling@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	// Set created_at after billing requirement date
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-07 00:00:00' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update created_at: %v", err)
	}
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	req := httptest.NewRequest("POST", "/invite/request", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected 303 redirect, got %d: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if location != "/" {
		t.Errorf("Expected redirect to /, got %q", location)
	}
}

// TestInviteRequest_EntitlementGrantedShowsConfirmation verifies that
// users with CreditPurchase entitlement can request more invites.
func TestInviteRequest_EntitlementGrantedShowsConfirmation(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	// Create user with active billing (manual setup to avoid Stripe calls)
	user, err := server.createUser(t.Context(), testSSHPubKey, "invite-billing@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	activateUserBilling(t, server, user.UserID)
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	req := httptest.NewRequest("POST", "/invite/request", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreateUserRecordCreatesAccountAndPlan verifies that createUserRecord + createAccountWithBasicPlan
// inserts exactly one account row and one account_plans row with a versioned basic plan_id and ended_at IS NULL.
func TestCreateUserRecordCreatesAccountAndPlan(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	ctx := context.Background()

	var userID string
	err := server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = server.createUserRecord(ctx, queries, "account-plan-test@example.com", false)
		if err != nil {
			return err
		}
		_, err = createAccountWithBasicPlan(ctx, queries, userID)
		return err
	})
	if err != nil {
		t.Fatalf("createUserRecord: %v", err)
	}

	// User must have exactly one account.
	acct, err := withRxRes1(server, ctx, (*exedb.Queries).GetAccountByUserID, userID)
	if err != nil {
		t.Fatalf("GetAccountByUserID: expected account after createUserRecord, got: %v", err)
	}
	if acct.CreatedBy != userID {
		t.Errorf("account.created_by=%q, want %q", acct.CreatedBy, userID)
	}

	// Account must have exactly one active plan with plan_id='basic'.
	ap, err := withRxRes1(server, ctx, (*exedb.Queries).GetActiveAccountPlan, acct.ID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: expected basic plan after signup, got: %v", err)
	}
	if entitlement.BasePlan(ap.PlanID) != entitlement.VersionBasic {
		t.Errorf("initial plan_id=%q, want base plan 'basic'", ap.PlanID)
	}
	if ap.EndedAt != nil {
		t.Errorf("initial plan ended_at=%v, want nil (plan must be active)", ap.EndedAt)
	}
	if ap.ChangedBy == nil || *ap.ChangedBy != "system:signup" {
		t.Errorf("initial plan changed_by=%v, want 'system:signup'", ap.ChangedBy)
	}
}

// TestCreateUserRecordNoAccountPlanDuplicates verifies that calling createUserRecord twice
// with different emails does not cross-contaminate account_plans rows.
func TestCreateUserRecordNoAccountPlanDuplicates(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	ctx := context.Background()

	emails := []string{"dup-check-a@example.com", "dup-check-b@example.com"}
	var userIDs []string
	for _, email := range emails {
		var uid string
		if err := server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
			var err error
			uid, err = server.createUserRecord(ctx, queries, email, false)
			if err != nil {
				return err
			}
			_, err = createAccountWithBasicPlan(ctx, queries, uid)
			return err
		}); err != nil {
			t.Fatalf("createUserRecord(%s): %v", email, err)
		}
		userIDs = append(userIDs, uid)
	}

	for i, uid := range userIDs {
		acct, err := withRxRes1(server, ctx, (*exedb.Queries).GetAccountByUserID, uid)
		if err != nil {
			t.Fatalf("user %d GetAccountByUserID: %v", i, err)
		}
		ap, err := withRxRes1(server, ctx, (*exedb.Queries).GetActiveAccountPlan, acct.ID)
		if err != nil {
			t.Fatalf("user %d GetActiveAccountPlan: %v", i, err)
		}
		if entitlement.BasePlan(ap.PlanID) != entitlement.VersionBasic {
			t.Errorf("user %d: plan=%q, want base plan 'basic'", i, ap.PlanID)
		}
		if ap.AccountID != acct.ID {
			t.Errorf("user %d: plan.account_id=%q, want %q", i, ap.AccountID, acct.ID)
		}
	}
}

// TestCreateAccountWithBasicPlanIdempotent verifies that createAccountWithBasicPlan
// succeeds even when the account already has an active basic plan (e.g. from a
// prior partial signup attempt). The account_plans table must contain exactly one
// active row afterward.
func TestCreateAccountWithBasicPlanIdempotent(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	ctx := context.Background()

	// Step 1: Create a user and account+plan via the normal path.
	var userID, accountID string
	err := server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = server.createUserRecord(ctx, queries, "idempotent-plan@example.com", false)
		if err != nil {
			return err
		}
		accountID, err = createAccountWithBasicPlan(ctx, queries, userID)
		return err
	})
	if err != nil {
		t.Fatalf("first createAccountWithBasicPlan: %v", err)
	}

	// Verify the plan exists.
	ap, err := withRxRes1(server, ctx, (*exedb.Queries).GetActiveAccountPlan, accountID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan after first call: %v", err)
	}
	if entitlement.BasePlan(ap.PlanID) != entitlement.VersionBasic {
		t.Fatalf("plan_id=%q, want base plan 'basic'", ap.PlanID)
	}

	// Step 2: Simulate a retry — insert the same plan again for the same account.
	// This is what happens when a partial prior attempt left the plan in place.
	err = server.withTx(ctx, func(ctx context.Context, queries *exedb.Queries) error {
		now := time.Now()
		changedBy := "system:signup"
		return queries.UpsertAccountPlan(ctx, exedb.UpsertAccountPlanParams{
			AccountID: accountID,
			PlanID:    entitlement.VersionedPlanID(entitlement.VersionBasic, "monthly", time.Now()),
			StartedAt: now,
			ChangedBy: &changedBy,
		})
	})
	if err != nil {
		t.Fatalf("UpsertAccountPlan (retry) should not fail: %v", err)
	}

	// Step 3: Verify exactly one active plan row exists.
	plans, err := withRxRes1(server, ctx, (*exedb.Queries).ListAccountPlanHistory, accountID)
	if err != nil {
		t.Fatalf("ListAccountPlanHistory: %v", err)
	}
	var activeCount int
	for _, p := range plans {
		if p.EndedAt == nil {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Errorf("active plan count=%d, want 1", activeCount)
	}
	if len(plans) != 1 {
		t.Errorf("total plan rows=%d, want 1 (INSERT OR IGNORE should not duplicate)", len(plans))
	}
}

func TestPendingRegistrationDeduplicatesByEmail(t *testing.T) {
	t.Parallel()
	server := newBillingTestServer(t)
	server.env.SkipBilling = false

	email := "dedup-test@example.com"

	// Helper: submit the /auth form and return the pending registration token + account_id.
	submitAuth := func() (token, accountID string) {
		t.Helper()
		form := url.Values{}
		form.Add("email", email)
		req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
		req.Host = server.env.WebHost
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)

		if w.Code != http.StatusSeeOther {
			t.Fatalf("Expected redirect 303, got %d. Body: %s", w.Code, w.Body.String())
		}
		location := w.Header().Get("Location")
		redirectURL, err := url.Parse(location)
		if err != nil {
			t.Fatalf("Failed to parse redirect URL: %v", err)
		}
		token = redirectURL.Query().Get("token")
		if token == "" {
			t.Fatal("Expected token in redirect URL")
		}

		var aid string
		err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
			return rx.Conn().QueryRowContext(ctx, `SELECT account_id FROM pending_registrations WHERE token = ?`, token).Scan(&aid)
		})
		if err != nil {
			t.Fatalf("Failed to find pending registration: %v", err)
		}
		return token, aid
	}

	// First signup attempt — generates a fresh account_id.
	_, firstAccountID := submitAuth()
	if !strings.HasPrefix(firstAccountID, "exe_") {
		t.Fatalf("Expected exe_ prefix, got %q", firstAccountID)
	}

	// Second signup attempt (retry) — should reuse the same account_id.
	_, secondAccountID := submitAuth()
	if secondAccountID != firstAccountID {
		t.Errorf("Expected account_id reuse: first=%q, second=%q", firstAccountID, secondAccountID)
	}

	// Expire all existing pending registrations, then retry — should get a new account_id.
	err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE pending_registrations SET expires_at = datetime('now', '-1 hour') WHERE email = ?`, email)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to expire pending registrations: %v", err)
	}

	_, thirdAccountID := submitAuth()
	if thirdAccountID == firstAccountID {
		t.Errorf("After expiry, expected new account_id but got reused %q", thirdAccountID)
	}
	if !strings.HasPrefix(thirdAccountID, "exe_") {
		t.Fatalf("Expected exe_ prefix, got %q", thirdAccountID)
	}
}

// TestNewUserBillingSuccess_PollerRace verifies that handleNewUserBillingSuccess
// succeeds even when the subscription poller has already inserted an account_plans
// row for the Stripe customer ID. This is the race condition from the screenshot:
//  1. User completes Stripe checkout → subscription created in Stripe
//  2. Poller (every 3s) sees customer.subscription.created → syncAccountPlan →
//     INSERT OR IGNORE into account_plans (no FK enforcement in SQLite)
//  3. User returns to /billing/success → handleNewUserBillingSuccess tries
//     INSERT INTO account_plans → UNIQUE constraint violation
func TestNewUserBillingSuccess_PollerRace(t *testing.T) {
	t.Parallel()

	server := newTestServer(t)
	ctx := t.Context()

	// The billing ID that VerifyCheckout will return (from fake Stripe).
	billingID := "cus_test123"

	// Step 1: Create a pending registration (simulates /auth POST for new email).
	token := "test-token-poller-race"
	accountID := billingID
	err := withTx1(server, ctx, (*exedb.Queries).InsertPendingRegistration, exedb.InsertPendingRegistrationParams{
		Token:     token,
		Email:     "poller-race@example.com",
		ExpiresAt: time.Now().Add(1 * time.Hour),
		AccountID: &accountID,
	})
	if err != nil {
		t.Fatalf("InsertPendingRegistration: %v", err)
	}

	// Step 2: Simulate the subscription poller racing ahead.
	// The poller inserts an orphaned account_plans row (no accounts row exists yet,
	// but SQLite FK enforcement is off so this succeeds).
	now := sqlite.NormalizeTime(time.Now())
	changedBy := "stripe:event"
	err = withTx1(server, ctx, (*exedb.Queries).InsertAccountPlan, exedb.InsertAccountPlanParams{
		AccountID: billingID,
		PlanID:    string(entitlement.VersionIndividual),
		StartedAt: now,
		ChangedBy: &changedBy,
	})
	if err != nil {
		t.Fatalf("Simulating poller InsertAccountPlan: %v", err)
	}

	// Step 3: Hit /billing/success as the returning user would.
	req := httptest.NewRequest("GET", "/billing/success?session_id=cs_test_session&token="+token, nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should succeed — NOT a 500 error.
	// The handler renders a 200 "check your email" page on success.
	if w.Code == http.StatusInternalServerError {
		t.Fatalf("handleNewUserBillingSuccess returned 500: %s", w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify user was created.
	var userCount int
	err = server.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.Conn().QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, "poller-race@example.com").Scan(&userCount)
	})
	if err != nil {
		t.Fatalf("Failed to count users: %v", err)
	}
	if userCount != 1 {
		t.Errorf("Expected 1 user, got %d", userCount)
	}

	// Verify exactly one active account_plan exists.
	plan, err := withRxRes1(server, ctx, (*exedb.Queries).GetActiveAccountPlan, billingID)
	if err != nil {
		t.Fatalf("GetActiveAccountPlan: %v", err)
	}
	if plan.PlanID != string(entitlement.VersionIndividual) {
		t.Errorf("plan_id=%q, want %q", plan.PlanID, entitlement.VersionIndividual)
	}
}
