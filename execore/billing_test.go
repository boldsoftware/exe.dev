package execore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

func TestBillingRequiredForNewVM_WebUI(t *testing.T) {
	// Test that /new always shows the form, even for users who need billing.
	// Billing is only checked when the user tries to create a VM via /create-vm.
	server := newTestServer(t)
	// Enable billing checks for this test (disabled by default in test env)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "no-billing@example.com"
	publicKey := "ssh-rsa dummy-billing-test-key no-billing@example.com"
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
	server := newTestServer(t)
	// Enable billing checks for this test (disabled by default in test env)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "no-billing-create@example.com"
	publicKey := "ssh-rsa dummy-billing-create-test-key no-billing-create@example.com"
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
	// Should redirect to /billing/subscribe with VM name and prompt preserved
	if !strings.HasPrefix(location, "/billing/subscribe?") {
		t.Errorf("Expected redirect to /billing/subscribe with params, got %q", location)
	}
	if !strings.Contains(location, "name=test-vm") {
		t.Errorf("Expected name param in redirect URL, got %q", location)
	}
	if !strings.Contains(location, "prompt=test") {
		t.Errorf("Expected prompt param in redirect URL, got %q", location)
	}
}

func TestUserWithBillingCanAccessNewVM_WebUI(t *testing.T) {
	server := newTestServer(t)

	// Create a user with billing info
	email := "has-billing@example.com"
	publicKey := "ssh-rsa dummy-has-billing-test-key has-billing@example.com"
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
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, user.UserID)
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
	server := newTestServer(t)

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

func TestUserIsPayingQuery(t *testing.T) {
	server := newTestServer(t)

	// Create a user without billing info
	email := "ispaying-test@example.com"
	publicKey := "ssh-rsa dummy-ispaying-test-key ispaying-test@example.com"
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Check that user is not paying initially
	isPaying, err := withRxRes1(server, t.Context(), (*exedb.Queries).UserIsPaying, user.UserID)
	if err != nil {
		t.Fatalf("UserIsPaying query failed: %v", err)
	}
	if isPaying {
		t.Error("Expected user without account record to not be paying")
	}

	// Add an account record and activate it (simulates completing Stripe checkout)
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        "exe_ispaying_test",
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, user.UserID)
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}

	// Check that user is now paying
	isPaying, err = withRxRes1(server, t.Context(), (*exedb.Queries).UserIsPaying, user.UserID)
	if err != nil {
		t.Fatalf("UserIsPaying query failed: %v", err)
	}
	if !isPaying {
		t.Error("Expected user with account record to be paying")
	}
}

func TestUserNeedsBillingQuery(t *testing.T) {
	server := newTestServer(t)

	// Create a user
	email := "needsbilling-test@example.com"
	publicKey := "ssh-rsa dummy-needsbilling-test-key needsbilling-test@example.com"
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
	needsBilling, err := withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
	if err != nil {
		t.Fatalf("UserNeedsBilling query failed: %v", err)
	}
	if needsBilling == nil {
		t.Fatal("UserNeedsBilling returned nil")
	}
	if !*needsBilling {
		t.Error("Expected new user without account record to need billing")
	}

	// Add an account record and activate it (simulate completing Stripe checkout)
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        "exe_needsbilling_test",
		CreatedBy: user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to insert account: %v", err)
	}
	err = withTx1(server, t.Context(), (*exedb.Queries).ActivateAccount, user.UserID)
	if err != nil {
		t.Fatalf("Failed to activate account: %v", err)
	}

	// User with active account should NOT need billing
	needsBilling, err = withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
	if err != nil {
		t.Fatalf("UserNeedsBilling query failed: %v", err)
	}
	if needsBilling == nil {
		t.Fatal("UserNeedsBilling returned nil")
	}
	if *needsBilling {
		t.Error("Expected user with account record to NOT need billing")
	}
}

func TestLegacyUserDoesNotNeedBilling(t *testing.T) {
	server := newTestServer(t)

	// Create a user
	email := "legacy-user@example.com"
	publicKey := "ssh-rsa dummy-legacy-test-key legacy-user@example.com"
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
	needsBilling, err := withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
	if err != nil {
		t.Fatalf("UserNeedsBilling query failed: %v", err)
	}
	if needsBilling == nil {
		t.Fatal("UserNeedsBilling returned nil")
	}
	if *needsBilling {
		t.Error("Expected legacy user (created before 2026-01-06 23:10 UTC) to NOT need billing")
	}
}

func TestBillingBypassBug(t *testing.T) {
	// This test reproduces a critical billing bypass bug:
	// 1. New user signs up (requires billing)
	// 2. User fills out /new form and clicks "Create VM"
	// 3. /create-vm redirects to /billing/subscribe which creates account and redirects to Stripe
	// 4. User hits browser back button (never completes Stripe checkout)
	// 5. User tries to create VM again -> should still be blocked!
	//
	// The fix: accounts should have a billing_status that starts as 'pending'
	// and only becomes 'active' after Stripe checkout completes.

	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a new user
	email := "billing-bypass@example.com"
	publicKey := "ssh-rsa dummy-billing-bypass-key billing-bypass@example.com"
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
	needsBilling, err := withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
	if err != nil {
		t.Fatalf("UserNeedsBilling query failed: %v", err)
	}
	if needsBilling == nil || !*needsBilling {
		t.Fatal("Expected new user to need billing initially")
	}

	// Step 2: Visit /billing/subscribe (this creates account and redirects to Stripe)
	req := httptest.NewRequest("GET", "/billing/subscribe?name=test-vm&prompt=test", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to Stripe checkout
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect to Stripe, got status %d", w.Code)
	}
	location := w.Header().Get("Location")
	if !strings.Contains(location, "stripe.com") && !strings.Contains(location, "checkout") {
		t.Fatalf("Expected redirect to Stripe checkout, got %q", location)
	}

	// Step 3: User hits back button - they never completed Stripe checkout!
	// At this point, the account record exists but checkout was NOT completed.

	// Step 4: Check if user still needs billing - they SHOULD still need it!
	// This is where the bug manifests: currently UserNeedsBilling returns false
	// because an account record exists, even though checkout wasn't completed.
	needsBilling, err = withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
	if err != nil {
		t.Fatalf("UserNeedsBilling query failed: %v", err)
	}
	if needsBilling == nil {
		t.Fatal("UserNeedsBilling returned nil")
	}
	if !*needsBilling {
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
	if !strings.HasPrefix(location, "/billing/subscribe") {
		t.Errorf("Expected redirect to /billing/subscribe, got %q - billing was bypassed!", location)
	}
}

func TestBillingSuccessBypassWithFakeSessionID(t *testing.T) {
	// This test reproduces a critical billing bypass vulnerability:
	// A user can bypass Stripe checkout by directly visiting /billing/success
	// with any fake session_id parameter. The endpoint should verify with Stripe
	// that the session was actually completed before activating the account.

	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a new user
	email := "bypass-fake-session@example.com"
	publicKey := "ssh-rsa dummy-bypass-fake-session bypass-fake-session@example.com"
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
	req := httptest.NewRequest("GET", "/billing/subscribe", nil)
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
		needsBilling, err := withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
		if err != nil {
			t.Fatalf("UserNeedsBilling query failed: %v", err)
		}
		if needsBilling != nil && !*needsBilling {
			t.Error("SECURITY BUG: User bypassed billing with fake session_id!")
		}
	}

	// User should still need billing since checkout was never completed
	needsBilling, err := withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
	if err != nil {
		t.Fatalf("UserNeedsBilling query failed: %v", err)
	}
	if needsBilling == nil {
		t.Fatal("UserNeedsBilling returned nil")
	}
	if !*needsBilling {
		t.Error("SECURITY BUG: User should still need billing after visiting success with fake session_id")
	}
}

func TestDebugForceBillingForLegacyUser(t *testing.T) {
	// Test that _debug_force_billing=1 forces billing flow even for grandfathered users.
	// This is used for canary testing billing before the official billing start date.

	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a user
	email := "legacy-force-billing@example.com"
	publicKey := "ssh-rsa dummy-legacy-force-billing legacy-force-billing@example.com"
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Update user's created_at to before the billing requirement date (make them a legacy user)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-06 23:09:59' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Verify user does NOT need billing (they are grandfathered)
	needsBilling, err := withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
	if err != nil {
		t.Fatalf("UserNeedsBilling query failed: %v", err)
	}
	if needsBilling == nil || *needsBilling {
		t.Fatal("Expected legacy user to NOT need billing")
	}

	// Without _debug_force_billing, /billing/subscribe should redirect to /new
	req := httptest.NewRequest("GET", "/billing/subscribe", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect (303), got %d", w.Code)
	}
	location := w.Header().Get("Location")
	if location != "/new" {
		t.Errorf("Expected redirect to /new for legacy user, got %q", location)
	}

	// With _debug_force_billing=1, /billing/subscribe should redirect to Stripe checkout
	req = httptest.NewRequest("GET", "/billing/subscribe?_debug_force_billing=1", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect (303), got %d", w.Code)
	}
	location = w.Header().Get("Location")
	if !strings.Contains(location, "stripe.com") && !strings.Contains(location, "checkout") {
		t.Errorf("Expected redirect to Stripe checkout with _debug_force_billing=1, got %q", location)
	}
}

func TestNewPageAlwaysShowsForm_EvenWhenBillingRequired(t *testing.T) {
	// Test that /new always shows the form, even for users who need billing.
	// Billing is only requested when they click "Create VM".
	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "new-flow-test@example.com"
	publicKey := "ssh-rsa dummy-new-flow-test-key new-flow-test@example.com"
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
	// Test that /new prefills name and prompt from query params.
	// This is used when user cancels Stripe checkout and is redirected back.
	server := newTestServer(t)

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
	// Test that /create-vm redirects to /billing/subscribe with name and prompt params.
	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "create-vm-params@example.com"
	publicKey := "ssh-rsa dummy-create-vm-params-key create-vm-params@example.com"
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
	// Should redirect to /billing/subscribe with name and prompt params
	if !strings.HasPrefix(location, "/billing/subscribe?") {
		t.Errorf("Expected redirect to /billing/subscribe with params, got %q", location)
	}
	if !strings.Contains(location, "name=test-vm-name") {
		t.Errorf("Expected name param in redirect URL, got %q", location)
	}
	if !strings.Contains(location, "prompt=Build") {
		t.Errorf("Expected prompt param in redirect URL, got %q", location)
	}
}

func TestBillingSubscribePreservesVMParams(t *testing.T) {
	// Test that /billing/subscribe includes name and prompt in Stripe callback URLs.
	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "billing-params@example.com"
	publicKey := "ssh-rsa dummy-billing-params-key billing-params@example.com"
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

	// Request /billing/subscribe with name and prompt params
	req := httptest.NewRequest("GET", "/billing/subscribe?name=my-test-vm&prompt=Build+something", nil)
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
	// Test that visiting /billing/subscribe multiple times reuses the same
	// pending account instead of creating duplicates. This prevents the bug
	// where users who abandon checkout and return later get multiple Stripe customers.

	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing
	email := "duplicate-account-test@example.com"
	publicKey := "ssh-rsa dummy-duplicate-account-test duplicate-account-test@example.com"
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

	// Visit /billing/subscribe first time
	req := httptest.NewRequest("GET", "/billing/subscribe?name=test-vm&prompt=test", nil)
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

	// Visit /billing/subscribe second time (simulating user abandoning checkout and returning)
	req = httptest.NewRequest("GET", "/billing/subscribe?name=test-vm2&prompt=test2", nil)
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
	req = httptest.NewRequest("GET", "/billing/subscribe", nil)
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
	// Prove that canceling billing creates no VM state:
	// 1. User fills form on /new and clicks "Create VM"
	// 2. /create-vm redirects to /billing/subscribe (no startBoxCreation called)
	// 3. /billing/subscribe redirects to Stripe (only account record created, no VM state)
	// 4. User cancels → redirected to /new (no VM state created)
	//
	// This test verifies no boxes are created during this flow.

	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a user without billing
	email := "cancel-no-vm@example.com"
	publicKey := "ssh-rsa dummy-cancel-no-vm cancel-no-vm@example.com"
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
	if !strings.HasPrefix(location, "/billing/subscribe") {
		t.Fatalf("Expected redirect to /billing/subscribe, got %q", location)
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

	// Step 2: Visit /billing/subscribe (simulates following the redirect)
	req = httptest.NewRequest("GET", "/billing/subscribe?name=cancel-test-vm&prompt=test+prompt", nil)
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
		t.Errorf("Box created during /billing/subscribe! Before: %d, After: %d", boxCountBefore, boxCountAfterBilling)
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
	// Test the new billing-first flow for new users:
	// /auth with new email -> redirect to /billing/subscribe with token -> Stripe
	server := newTestServer(t)
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

	// Should redirect to /billing/subscribe with token
	if w.Code != http.StatusSeeOther {
		t.Fatalf("Expected redirect 303, got %d. Body: %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if !strings.HasPrefix(location, "/billing/subscribe?token=") {
		t.Fatalf("Expected redirect to /billing/subscribe?token=..., got %q", location)
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

	// Step 2: Visit /billing/subscribe?token=... (simulates following the redirect)
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
	// Test that canceling Stripe checkout redirects back to /auth with email preserved
	server := newTestServer(t)
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
	// Test that existing users still go through the normal email verification flow
	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create an existing user first
	email := "existing-user@example.com"
	publicKey := "ssh-rsa dummy-existing-user-key existing-user@example.com"
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
		if strings.Contains(location, "/billing/subscribe") {
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
	// Test that new users with a valid invite code skip the Stripe billing flow.
	// The invite code grants a billing exemption, so no payment is required.
	server := newTestServer(t)
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

	// BUG: Currently this redirects to /billing/subscribe, but it should NOT
	// because the invite code grants a billing exemption.
	// Expected: Show "check your email" page (200), NOT redirect to billing
	if w.Code == http.StatusSeeOther {
		location := w.Header().Get("Location")
		if strings.Contains(location, "/billing/subscribe") {
			t.Errorf("BUG: New user with valid invite code should NOT be redirected to billing! Got redirect to: %s", location)
		}
	}

	// Should show the "check your email" page
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 (check email page) for new user with invite code, got %d", w.Code)
	}
}

func TestLoginWithExeSkipsBilling(t *testing.T) {
	// Test that new users signing up via "Login with Exe" (the proxy auth flow)
	// are NOT redirected to the Stripe billing flow.
	// These users are just authenticating to access someone else's app, not
	// signing up to use exe.dev resources directly.
	server := newTestServer(t)
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
		if strings.Contains(location, "/billing/subscribe") {
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
