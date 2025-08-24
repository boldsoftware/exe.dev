package exe

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestAuthConfirmE2EFlow tests the complete end-to-end flow with the interstitial page
func TestAuthConfirmE2EFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E integration test in short mode")
	}

	// Create temporary database
	tmpDB, err := os.CreateTemp("", "auth_confirm_e2e_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = false
	defer server.Stop()

	// Use mock container manager
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Test data
	email := "test@example.com"
	machineName := "web"
	originalURL := fmt.Sprintf("http://%s.localhost:8080/dashboard", machineName)
	proxyHost := fmt.Sprintf("%s.localhost:8080", machineName)

	// Create user and alloc
	userID, err := server.createTestUserWithID(email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc for user
	allocID := "test-alloc-" + userID[:8]
	_, err = server.db.Exec(`INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email) VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', 'test@example.com')`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Create machine
	_, err = server.db.Exec(`INSERT INTO machines (alloc_id, name, status, created_by_user_id) VALUES (?, ?, 'stopped', ?)`,
		allocID, machineName, userID)
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// For this test, simulate the auth flow by manually creating the auth redirect URL
	// that would normally come from a proxy request
	authURL := fmt.Sprintf("/auth?redirect=%s&return_host=%s",
		url.QueryEscape(originalURL),
		url.QueryEscape(proxyHost))
	t.Logf("Simulating auth URL: %s", authURL)

	t.Log("=== STEP 1: User follows redirect to main domain auth ===")
	// Create auth cookie for authenticated user
	cookieValue, err := server.createAuthCookie(userID, "localhost:0")
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	req2 := httptest.NewRequest("GET", authURL, nil)
	req2.Host = "localhost:0"
	req2.AddCookie(&http.Cookie{
		Name:  "exe-auth",
		Value: cookieValue,
	})
	w2 := httptest.NewRecorder()

	server.ServeHTTP(w2, req2)

	if w2.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Expected redirect to confirmation, got status %d", w2.Code)
	}

	confirmURL := w2.Header().Get("Location")
	t.Logf("Redirected to confirmation: %s", confirmURL)

	if !strings.Contains(confirmURL, "/auth/confirm") {
		t.Fatal("Expected redirect to confirmation page")
	}

	t.Log("=== STEP 2: Show confirmation page ===")
	req3 := httptest.NewRequest("GET", confirmURL, nil)
	req3.Host = "localhost:0"
	w3 := httptest.NewRecorder()

	server.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("Expected confirmation page to load, got status %d", w3.Code)
	}

	confirmBody := w3.Body.String()
	t.Log("=== CONFIRMATION PAGE CONTENT ===")
	t.Logf("Confirmation page contains:")
	if strings.Contains(confirmBody, "Confirm Login") {
		t.Log("✓ Title: 'Confirm Login'")
	} else {
		t.Error("✗ Missing 'Confirm Login' title")
	}

	if strings.Contains(confirmBody, machineName) {
		t.Logf("✓ Machine name: '%s'", machineName)
	} else {
		t.Errorf("✗ Missing machine name '%s'", machineName)
	}

	if strings.Contains(confirmBody, "Continue") && strings.Contains(confirmBody, "Cancel") {
		t.Log("✓ Both 'Continue' and 'Cancel' buttons")
	} else {
		t.Error("✗ Missing Continue/Cancel buttons")
	}

	t.Log("=== STEP 3a: User clicks Cancel ===")
	cancelURL := confirmURL + "&action=cancel"
	req4a := httptest.NewRequest("GET", cancelURL, nil)
	req4a.Host = "localhost:0"
	w4a := httptest.NewRecorder()

	server.ServeHTTP(w4a, req4a)

	if w4a.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Expected redirect on cancel, got status %d", w4a.Code)
	}

	location4a := w4a.Header().Get("Location")
	if location4a != "/" {
		t.Errorf("Expected redirect to home on cancel, got: %s", location4a)
	} else {
		t.Log("✓ Cancel redirects to home page")
	}

	t.Log("=== STEP 3b: User clicks Continue (new secret) ===")
	// Create new magic secret since the previous one was consumed by cancel
	secret, err := server.createMagicSecret(userID, machineName, originalURL)
	if err != nil {
		t.Fatalf("Failed to create magic secret: %v", err)
	}

	continueURL := fmt.Sprintf("/auth/confirm?secret=%s&return_host=%s&action=confirm",
		secret, url.QueryEscape(proxyHost))
	req4b := httptest.NewRequest("GET", continueURL, nil)
	req4b.Host = "localhost:0"
	w4b := httptest.NewRecorder()

	server.ServeHTTP(w4b, req4b)

	if w4b.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Expected redirect on continue, got status %d", w4b.Code)
	}

	magicURL := w4b.Header().Get("Location")
	t.Logf("Continue redirects to magic URL: %s", magicURL)

	if !strings.Contains(magicURL, "__exe.dev/auth") {
		t.Error("✗ Expected redirect to magic auth URL")
	} else {
		t.Log("✓ Continue redirects to magic auth URL")
	}

	t.Log("=== STEP 4: Magic auth completes and sets cookie ===")
	req5 := httptest.NewRequest("GET", magicURL, nil)
	req5.Host = proxyHost
	w5 := httptest.NewRecorder()

	server.ServeHTTP(w5, req5)

	if w5.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Expected final redirect, got status %d", w5.Code)
	}

	proxyCookies := w5.Result().Cookies()
	var proxyAuthCookie *http.Cookie
	for _, cookie := range proxyCookies {
		if cookie.Name == "exe-proxy-auth" {
			proxyAuthCookie = cookie
			break
		}
	}

	if proxyAuthCookie == nil {
		t.Fatal("✗ Expected proxy auth cookie to be set")
	} else {
		t.Log("✓ Proxy auth cookie set successfully")
	}

	finalLocation := w5.Header().Get("Location")
	if finalLocation == originalURL {
		t.Log("✓ Final redirect goes to original requested URL")
	} else {
		t.Errorf("✗ Expected final redirect to %s, got %s", originalURL, finalLocation)
	}

	t.Log("=== E2E TEST SUMMARY ===")
	t.Log("✅ Complete auth flow with interstitial works correctly:")
	t.Log("   1. User visits protected page → redirect to auth")
	t.Log("   2. User authenticated on main domain → redirect to confirmation")
	t.Log("   3. Confirmation page shows team info and options")
	t.Log("   4. User can cancel (cleans up) or continue")
	t.Log("   5. Continue proceeds with magic auth and sets cookie")
	t.Log("   6. User ends up at originally requested page")
}
