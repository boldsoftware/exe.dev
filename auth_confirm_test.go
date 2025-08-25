package exe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAuthConfirmInterstitial tests the interstitial confirmation page functionality
func TestAuthConfirmInterstitial(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create server
	server := NewTestServer(t, ":0", ":0")
	server.quietMode = false

	// Use mock container manager
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Create test data
	email := "test@example.com"
	machineName := "machine"
	returnHost := "machine.localhost:8080"
	redirectURL := "http://machine.localhost:8080/"

	// Create user and alloc
	userID, err := server.createTestUserWithID(email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	t.Logf("Created user with ID: %s", userID)

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

	t.Log("Test 1: Missing secret parameter returns error")
	req1 := httptest.NewRequest("GET", "/auth/confirm", nil)
	w1 := httptest.NewRecorder()
	server.ServeHTTP(w1, req1)

	if w1.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for missing secret, got %d", w1.Code)
	}

	t.Log("Test 2: Invalid secret returns error")
	req2 := httptest.NewRequest("GET", "/auth/confirm?secret=invalid&return_host="+returnHost, nil)
	w2 := httptest.NewRecorder()
	server.ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for invalid secret, got %d", w2.Code)
	}

	t.Log("Test 3: Valid secret shows confirmation page")
	// Create a valid magic secret
	secret, err := server.createMagicSecret(userID, machineName, redirectURL)
	if err != nil {
		t.Fatalf("Failed to create magic secret: %v", err)
	}

	req3 := httptest.NewRequest("GET", "/auth/confirm?secret="+secret+"&return_host="+returnHost, nil)
	w3 := httptest.NewRecorder()
	server.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Errorf("Expected 200 OK for valid secret, got %d", w3.Code)
	}

	body3 := w3.Body.String()
	if !strings.Contains(body3, "Confirm Login") {
		t.Error("Confirmation page should contain 'Confirm Login' title")
	}
	if !strings.Contains(body3, machineName) {
		t.Error("Confirmation page should show team name")
	}
	if !strings.Contains(body3, "machine.localhost") {
		t.Error("Confirmation page should show site domain")
	}

	t.Log("Test 4: User confirms - should redirect to magic URL")
	// Create another secret for the confirm test
	secret4, err := server.createMagicSecret(userID, machineName, redirectURL)
	if err != nil {
		t.Fatalf("Failed to create magic secret: %v", err)
	}

	req4 := httptest.NewRequest("GET", "/auth/confirm?secret="+secret4+"&return_host="+returnHost+"&action=confirm", nil)
	w4 := httptest.NewRecorder()
	server.ServeHTTP(w4, req4)

	if w4.Code != http.StatusTemporaryRedirect {
		t.Errorf("Expected 307 redirect for confirm action, got %d", w4.Code)
	}

	location4 := w4.Header().Get("Location")
	if !strings.Contains(location4, "__exe.dev/auth") {
		t.Error("Confirm action should redirect to magic auth URL")
	}
	if !strings.Contains(location4, "secret=") {
		t.Error("Magic URL should contain secret parameter")
	}
	if !strings.Contains(location4, returnHost) {
		t.Error("Magic URL should contain return host")
	}

	t.Log("Test 5: User cancels - should clean up secret and redirect to home")
	// Create another secret for the cancel test
	secret5, err := server.createMagicSecret(userID, machineName, redirectURL)
	if err != nil {
		t.Fatalf("Failed to create magic secret: %v", err)
	}

	req5 := httptest.NewRequest("GET", "/auth/confirm?secret="+secret5+"&return_host="+returnHost+"&action=cancel", nil)
	w5 := httptest.NewRecorder()
	server.ServeHTTP(w5, req5)

	if w5.Code != http.StatusTemporaryRedirect {
		t.Errorf("Expected 307 redirect for cancel action, got %d", w5.Code)
	}

	location5 := w5.Header().Get("Location")
	if location5 != "/" {
		t.Errorf("Cancel action should redirect to home, got: %s", location5)
	}

	// Verify secret was cleaned up
	_, err = server.validateMagicSecret(secret5)
	if err == nil {
		t.Error("Secret should have been cleaned up after cancellation")
	}

	t.Log("Test 6: Missing return_host parameter returns error")
	// Create another secret for this test
	secret6, err := server.createMagicSecret(userID, machineName, redirectURL)
	if err != nil {
		t.Fatalf("Failed to create magic secret: %v", err)
	}

	req6 := httptest.NewRequest("GET", "/auth/confirm?secret="+secret6, nil)
	w6 := httptest.NewRecorder()
	server.ServeHTTP(w6, req6)

	if w6.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 Bad Request for missing return_host, got %d", w6.Code)
	}

	t.Log("✅ All interstitial confirmation tests passed")
}
