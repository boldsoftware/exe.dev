package execore

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

// TestLockedOutUserCannotAccessDashboard tests that a locked out user sees
// the "Account Locked" page instead of the dashboard.
func TestLockedOutUserCannotAccessDashboard(t *testing.T) {
	server := newTestServer(t)

	// Create a user
	email := "lockedout-dashboard@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Lock out the user
	err = withTx1(server, t.Context(), (*exedb.Queries).SetUserIsLockedOut, exedb.SetUserIsLockedOutParams{
		IsLockedOut: true,
		UserID:      user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to lock out user: %v", err)
	}

	// Create an auth cookie for this user
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      user.UserID,
		Domain:      "127.0.0.1",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Set the cookie on the jar
	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// Make request to dashboard (/)
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Should return 403 Forbidden
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected 403, got %d", resp.StatusCode)
	}

	// Should show support contact with trace ID
	if !strings.Contains(string(body), "support@exe.dev") {
		t.Error("Response should contain 'support@exe.dev'")
	}
	if !strings.Contains(string(body), "trace:") {
		t.Error("Response should contain a trace ID")
	}
}

// TestLockedOutUserCannotAccessProfile tests that a locked out user cannot
// access the /user profile page.
func TestLockedOutUserCannotAccessProfile(t *testing.T) {
	server := newTestServer(t)

	// Create a user
	email := "lockedout-profile@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Lock out the user
	err = withTx1(server, t.Context(), (*exedb.Queries).SetUserIsLockedOut, exedb.SetUserIsLockedOutParams{
		IsLockedOut: true,
		UserID:      user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to lock out user: %v", err)
	}

	// Create an auth cookie for this user
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      user.UserID,
		Domain:      "127.0.0.1",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Set the cookie on the jar
	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// Make request to /user profile
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/user", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /user: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Should return 403 Forbidden
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("Expected 403, got %d", resp.StatusCode)
	}

	// Should show support contact with trace ID
	if !strings.Contains(string(body), "support@exe.dev") {
		t.Error("Response should contain 'support@exe.dev'")
	}
}

// TestNonLockedOutUserCanAccessDashboard verifies that non-locked-out users
// can still access the dashboard normally.
func TestNonLockedOutUserCanAccessDashboard(t *testing.T) {
	server := newTestServer(t)

	// Create a user (not locked out)
	email := "notlocked-dashboard@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create an auth cookie for this user
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      user.UserID,
		Domain:      "127.0.0.1",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Set the cookie on the jar
	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// Make request to dashboard (/)
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Should return 200 OK
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// Should NOT show lockout message
	if strings.Contains(string(body), "contact support@exe.dev") {
		t.Error("Non-locked-out user should not see lockout message")
	}
}

// TestIsUserLockedOut tests the isUserLockedOut helper function directly.
func TestIsUserLockedOut(t *testing.T) {
	server := newTestServer(t)

	// Create a user
	email := "lockout-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Initially should not be locked out
	isLockedOut, err := server.isUserLockedOut(t.Context(), user.UserID)
	if err != nil {
		t.Fatalf("Failed to check lockout status: %v", err)
	}
	if isLockedOut {
		t.Error("User should not be locked out initially")
	}

	// Lock out the user
	err = withTx1(server, t.Context(), (*exedb.Queries).SetUserIsLockedOut, exedb.SetUserIsLockedOutParams{
		IsLockedOut: true,
		UserID:      user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to lock out user: %v", err)
	}

	// Now should be locked out
	isLockedOut, err = server.isUserLockedOut(t.Context(), user.UserID)
	if err != nil {
		t.Fatalf("Failed to check lockout status: %v", err)
	}
	if !isLockedOut {
		t.Error("User should be locked out after SetUserIsLockedOut")
	}

	// Unlock the user
	err = withTx1(server, t.Context(), (*exedb.Queries).SetUserIsLockedOut, exedb.SetUserIsLockedOutParams{
		IsLockedOut: false,
		UserID:      user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to unlock user: %v", err)
	}

	// Should not be locked out anymore
	isLockedOut, err = server.isUserLockedOut(t.Context(), user.UserID)
	if err != nil {
		t.Fatalf("Failed to check lockout status: %v", err)
	}
	if isLockedOut {
		t.Error("User should not be locked out after being unlocked")
	}
}

// TestLockedOutUserCannotAddNewSSHKey tests that a locked out user cannot
// add a new SSH key via device verification (simulates the "new SSH key" registration flow).
func TestLockedOutUserCannotAddNewSSHKey(t *testing.T) {
	server := newTestServer(t)

	// Create a user with an existing SSH key
	email := "lockedout-newkey@example.com"
	existingPublicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), existingPublicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Lock out the user
	err = withTx1(server, t.Context(), (*exedb.Queries).SetUserIsLockedOut, exedb.SetUserIsLockedOutParams{
		IsLockedOut: true,
		UserID:      user.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to lock out user: %v", err)
	}

	// A different public key (simulating user connecting with a new SSH key)
	newPublicKey := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZh3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZ3qZL test-lockout-newkey"

	// Create a pending SSH key in the database (simulates SSH registration flow)
	token := "test-lockout-device-" + time.Now().Format("20060102150405")
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO pending_ssh_keys (token, public_key, user_email, expires_at)
			VALUES (?, ?, ?, ?)`,
			token, newPublicKey, email, time.Now().Add(24*time.Hour).Format(time.RFC3339))
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create pending SSH key: %v", err)
	}

	// Create an email verification in memory (simulates the SSH session waiting)
	verification := &EmailVerification{
		Email:        email,
		PublicKey:    newPublicKey,
		Token:        token,
		PairingCode:  "123456",
		CompleteChan: make(chan struct{}),
		CreatedAt:    time.Now(),
	}
	server.emailVerifications[token] = verification

	// POST to verify-device to complete the verification
	form := url.Values{}
	form.Add("token", token)
	req := httptest.NewRequest("POST", "/verify-device", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDeviceVerificationHTTP(w, req)

	// Should return 403 Forbidden
	if w.Code != http.StatusForbidden {
		t.Errorf("Expected 403, got %d; body: %s", w.Code, w.Body.String())
	}

	// Should show support contact with trace ID
	body := w.Body.String()
	if !strings.Contains(body, "support@exe.dev") {
		t.Errorf("Response should contain 'support@exe.dev', got: %s", body)
	}

	// Verify the SSH key was NOT added
	var keyCount int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM ssh_keys WHERE public_key = ?`, newPublicKey).Scan(&keyCount)
	})
	if err != nil {
		t.Fatalf("Failed to check SSH key count: %v", err)
	}
	if keyCount != 0 {
		t.Error("SSH key should NOT have been added for locked out user")
	}

	// Verify the verification channel received an error
	select {
	case <-verification.CompleteChan:
		if verification.Err == nil {
			t.Error("Verification should have an error set")
		}
	default:
		t.Error("Verification channel should have been closed")
	}
}

// TestLockoutStopsUserBoxes tests that locking out a user stops all their running boxes.
func TestLockoutStopsUserBoxes(t *testing.T) {
	server := newTestServer(t)
	ctx := t.Context()

	// Create a user
	user, err := server.createUser(ctx, testSSHPubKey, "lockout-boxes@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Insert two running boxes and one stopped box for this user
	err = server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		if _, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            "lockout-running1",
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		}); err != nil {
			return err
		}
		if _, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            "lockout-running2",
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		}); err != nil {
			return err
		}
		if _, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-host",
			Name:            "lockout-stopped",
			Status:          "stopped",
			Image:           "test-image",
			CreatedByUserID: user.UserID,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to create test boxes: %v", err)
	}

	// Lock out the user via the debug toggle endpoint
	form := url.Values{}
	form.Set("user_id", user.UserID)
	form.Set("lockout", "1")
	req := httptest.NewRequest("POST", "/debug/users/toggle-lockout", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.handleDebugToggleLockout(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200 from toggle-lockout, got %d: %s", w.Code, w.Body.String())
	}

	// Verify all boxes are now stopped
	boxes, err := withRxRes1(server, ctx, (*exedb.Queries).BoxesForUser, user.UserID)
	if err != nil {
		t.Fatalf("Failed to list boxes: %v", err)
	}
	if len(boxes) != 3 {
		t.Fatalf("Expected 3 boxes, got %d", len(boxes))
	}
	for _, box := range boxes {
		if box.Status != "stopped" {
			t.Errorf("Box %q should be stopped, got %q", box.Name, box.Status)
		}
	}
}

// TestDebugVMList tests that /debug/vmlist returns container IDs excluding
// locked-out users, and supports host filtering.
func TestDebugVMList(t *testing.T) {
	server := newTestServer(t)
	ctx := t.Context()

	normalUser, err := server.createUser(ctx, testSSHPubKey, "normal-user@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create normal user: %v", err)
	}
	lockedUser, err := server.createUser(ctx, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBb locked@example.com", "locked-user@example.com", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create locked user: %v", err)
	}

	normalCID := "ctr-normal-abc"
	lockedCID := "ctr-locked-xyz"
	otherHostCID := "ctr-other-host"
	err = server.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		normalBoxID, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "tcp://host1:9080",
			Name:            "normal-box",
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: normalUser.UserID,
		})
		if err != nil {
			return err
		}
		if err := queries.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ContainerID: &normalCID,
			Status:      "running",
			ID:          int(normalBoxID),
		}); err != nil {
			return err
		}
		lockedBoxID, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "tcp://host1:9080",
			Name:            "locked-box",
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: lockedUser.UserID,
		})
		if err != nil {
			return err
		}
		if err := queries.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ContainerID: &lockedCID,
			Status:      "running",
			ID:          int(lockedBoxID),
		}); err != nil {
			return err
		}
		otherBoxID, err := queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "tcp://host2:9080",
			Name:            "other-host-box",
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: normalUser.UserID,
		})
		if err != nil {
			return err
		}
		return queries.UpdateBoxContainerAndStatus(ctx, exedb.UpdateBoxContainerAndStatusParams{
			ContainerID: &otherHostCID,
			Status:      "running",
			ID:          int(otherBoxID),
		})
	})
	if err != nil {
		t.Fatalf("Failed to create test boxes: %v", err)
	}

	err = withTx1(server, ctx, (*exedb.Queries).SetUserIsLockedOut, exedb.SetUserIsLockedOutParams{
		IsLockedOut: true,
		UserID:      lockedUser.UserID,
	})
	if err != nil {
		t.Fatalf("Failed to lock out user: %v", err)
	}

	// /debug/vmlist returns all non-locked-out container IDs
	req := httptest.NewRequest("GET", "/debug/vmlist", nil)
	w := httptest.NewRecorder()
	server.handleDebugVMList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("Expected 2 container IDs, got %d: %v", len(lines), lines)
	}

	// With host filter, only that host's VMs
	req = httptest.NewRequest("GET", "/debug/vmlist?host=tcp://host1:9080", nil)
	w = httptest.NewRecorder()
	server.handleDebugVMList(w, req)

	lines = strings.Split(strings.TrimSpace(w.Body.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("Expected 1 container ID for host1, got %d: %v", len(lines), lines)
	}
	if lines[0] != normalCID {
		t.Errorf("Expected %q, got %q", normalCID, lines[0])
	}
}
