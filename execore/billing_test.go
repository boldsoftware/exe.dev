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
	server := newTestServer(t)
	// Enable billing checks for this test (disabled by default in test env)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "no-billing@example.com"
	publicKey := "ssh-rsa dummy-billing-test-key no-billing@example.com"
	user, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Request /new - should redirect to billing subscribe page
	req := httptest.NewRequest("GET", "/new", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected status 303, got %d", w.Code)
	}

	location := w.Header().Get("Location")
	if location != "/billing/subscribe" {
		t.Errorf("Expected redirect to /billing/subscribe, got %q", location)
	}
}

func TestBillingRequiredForCreateVM_WebUI(t *testing.T) {
	server := newTestServer(t)
	// Enable billing checks for this test (disabled by default in test env)
	server.env.SkipBilling = false

	// Create a user without billing info
	email := "no-billing-create@example.com"
	publicKey := "ssh-rsa dummy-billing-create-test-key no-billing-create@example.com"
	user, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// POST to /create-vm - should redirect to billing subscribe page
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
	if location != "/billing/subscribe" {
		t.Errorf("Expected redirect to /billing/subscribe, got %q", location)
	}
}

func TestUserWithBillingCanAccessNewVM_WebUI(t *testing.T) {
	server := newTestServer(t)

	// Create a user with billing info
	email := "has-billing@example.com"
	publicKey := "ssh-rsa dummy-has-billing-test-key has-billing@example.com"
	user, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add an account record for this user and activate it (simulates completed Stripe checkout)
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertAccount, exedb.InsertAccountParams{
		ID:        "acct_test123",
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
	user, err := server.createUser(t.Context(), publicKey, email)
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
		ID:        "acct_ispaying_test",
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

	// Create a user (will be created with current timestamp = 2026-01-03)
	email := "needsbilling-test@example.com"
	publicKey := "ssh-rsa dummy-needsbilling-test-key needsbilling-test@example.com"
	user, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// New user without account record should need billing
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
		ID:        "acct_needsbilling_test",
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
	user, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Update user's created_at to before the billing requirement date (2026-01-02)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Conn().ExecContext(ctx, `UPDATE users SET created_at = '2026-01-02 23:59:59' WHERE user_id = ?`, user.UserID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update user created_at: %v", err)
	}

	// Legacy user (created before 2026-01-03) should NOT need billing even without an account
	needsBilling, err := withRxRes1(server, t.Context(), (*exedb.Queries).UserNeedsBilling, user.UserID)
	if err != nil {
		t.Fatalf("UserNeedsBilling query failed: %v", err)
	}
	if needsBilling == nil {
		t.Fatal("UserNeedsBilling returned nil")
	}
	if *needsBilling {
		t.Error("Expected legacy user (created before 2026-01-03) to NOT need billing")
	}
}

func TestBillingBypassBug(t *testing.T) {
	// This test reproduces a critical billing bypass bug:
	// 1. New user signs up (requires billing)
	// 2. User clicks "New" -> redirected to /billing/subscribe
	// 3. /billing/subscribe creates account record and redirects to Stripe checkout
	// 4. User hits browser back button (never completes Stripe checkout)
	// 5. User clicks "New" again -> BUG: allowed to create VM without paying!
	//
	// The fix: accounts should have a billing_status that starts as 'pending'
	// and only becomes 'active' after Stripe checkout completes.

	server := newTestServer(t)
	server.env.SkipBilling = false

	// Create a new user (will require billing since created after 2026-01-03)
	email := "billing-bypass@example.com"
	publicKey := "ssh-rsa dummy-billing-bypass-key billing-bypass@example.com"
	user, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
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
	req := httptest.NewRequest("GET", "/billing/subscribe", nil)
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

	// Step 5: Try to access /new - should still redirect to billing
	req = httptest.NewRequest("GET", "/new", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect (303), got %d - user bypassed billing!", w.Code)
	}
	location = w.Header().Get("Location")
	if location != "/billing/subscribe" {
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

	// Create a new user (will require billing since created after 2026-01-03)
	email := "bypass-fake-session@example.com"
	publicKey := "ssh-rsa dummy-bypass-fake-session bypass-fake-session@example.com"
	user, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
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
