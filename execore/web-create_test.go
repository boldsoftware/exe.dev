package execore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"exe.dev/exedb"
)

func TestHostnameCheck(t *testing.T) {
	server := newTestServer(t)

	// Test hostname availability check
	reqBody := `{"hostname": "test-hostname"}`
	req := httptest.NewRequest("POST", "/check-hostname", strings.NewReader(reqBody))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response struct {
		Valid     bool   `json:"valid"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}

	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if !response.Valid {
		t.Error("Expected hostname to be valid")
	}
	if !response.Available {
		t.Error("Expected hostname to be available")
	}
	if response.Message != "" {
		t.Errorf("Expected empty message for available hostname, got %q", response.Message)
	}
}

func TestInvalidHostname(t *testing.T) {
	server := newTestServer(t)

	// Test invalid hostname check
	reqBody := `{"hostname": "a"}`
	req := httptest.NewRequest("POST", "/check-hostname", strings.NewReader(reqBody))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response struct {
		Valid     bool   `json:"valid"`
		Available bool   `json:"available"`
		Message   string `json:"message"`
	}

	err := json.Unmarshal(w.Body.Bytes(), &response)
	if err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if response.Valid {
		t.Error("Expected hostname to be invalid")
	}
	if !strings.Contains(response.Message, "nvalid") {
		t.Error("Expected invalid hostname message")
	}
}

func TestInvalidEmail(t *testing.T) {
	server := newTestServer(t)

	// Test invalid email
	form := url.Values{}
	form.Add("email", "invalid-email")

	req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestRunCommandUnauthorized(t *testing.T) {
	server := newTestServer(t)

	// Test command without authentication
	reqBody := `{"command": "share show test-box"}`
	req := httptest.NewRequest("POST", "/cmd", strings.NewReader(reqBody))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}
}

func TestRunCommandNotAllowed(t *testing.T) {
	server := newTestServer(t)

	// Create a user and get auth cookie
	email := "cmd-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Test command not in allowlist
	reqBody := `{"command": "new --name=test"}`
	req := httptest.NewRequest("POST", "/cmd", strings.NewReader(reqBody))
	req.Host = server.env.WebHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var response struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse JSON response: %v", err)
	}

	if response.Success {
		t.Error("Expected command to fail (not in allowlist)")
	}
	if !strings.Contains(response.Error, "not allowed") {
		t.Errorf("Expected 'not allowed' error, got: %s", response.Error)
	}
}

func TestIsCommandAllowed(t *testing.T) {
	tests := []struct {
		command string
		allowed bool
	}{
		{"restart mybox", true},
		{"rm mybox", true},
		{"share show mybox", true},
		{"share add mybox user@example.com", true},
		{"share remove mybox user@example.com", true},
		{"share add-link mybox", true},
		{"share remove-link mybox token123", true},
		{"share set-public mybox", true},
		{"share set-private mybox", true},
		{"ssh-key list", true},
		{"ssh-key add ssh-ed25519 AAAA...", true},
		{"ssh-key remove ssh-ed25519 AAAA...", true},
		{"ssh-key rename old-name new-name", true},
		{"team members", true},
		{"team add user@example.com", true},
		{"team remove user@example.com", true},
		{"integrations add http-proxy --name=test --target=https://example.com --bearer=tok", true},
		{"integrations remove test", true},
		{"integrations attach test vm:mybox", true},
		{"integrations detach test vm:mybox", true},
		{"integrations setup github", true},
		{"integrations list", false},
		{"new --name=test", false},
		{"help", false},
		{"ls", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			result := isCommandAllowed(tt.command)
			if result != tt.allowed {
				t.Errorf("isCommandAllowed(%q) = %v, want %v", tt.command, result, tt.allowed)
			}
		})
	}
}

func TestStartBoxCreationReusesDeletedName(t *testing.T) {
	// Test that startBoxCreation works after a previous creation stream
	// for the same hostname is done. This is the fix for:
	// https://github.com/boldsoftware/exe.dev/issues/167
	server := newTestServer(t)

	email := "reuse-name-test@example.com"
	user, err := server.createUser(t.Context(), testSSHPubKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	hostname := "reuse-me"

	// Simulate a previous creation that completed: create a stream and mark it done.
	cs := server.getOrCreateCreationStream(user.UserID, hostname)
	cs.MarkDone(nil)

	// The old stream should still be in memory (cleanup timer hasn't fired).
	if got := server.getCreationStream(user.UserID, hostname); got == nil {
		t.Fatal("Expected old creation stream to still be present")
	}

	// Now call startBoxCreation again with the same hostname.
	// Before the fix, this returned early because the old (done) stream existed.
	server.startBoxCreation(t.Context(), hostname, "", "exeuntu", user.UserID)

	// After the fix, the old stream is replaced with a new one.
	newCS := server.getCreationStream(user.UserID, hostname)
	if newCS == nil {
		t.Fatal("Expected a new creation stream to be created")
	}
	if newCS == cs {
		t.Fatal("Expected a NEW creation stream, but got the old one")
	}
}

func TestRemoveCreationStreamIfMatchStalePointer(t *testing.T) {
	// Test that removeCreationStreamIfMatch is a no-op when called with a stale pointer.
	// This is the core safety property: an old stream's cleanup timer cannot
	// accidentally remove a replacement stream.
	server := newTestServer(t)

	userID := "user-stale-ptr"
	hostname := "test-box"

	// Create stream A.
	streamA := server.getOrCreateCreationStream(userID, hostname)

	// Replace it with stream B by removing A and creating a new one.
	server.removeCreationStreamIfMatch(userID, hostname, streamA)
	streamB := server.getOrCreateCreationStream(userID, hostname)
	if streamB == streamA {
		t.Fatal("Expected a different stream after replacement")
	}

	// Simulate stream A's cleanup timer firing with the stale pointer.
	server.removeCreationStreamIfMatch(userID, hostname, streamA)

	// Stream B must still be present.
	if got := server.getCreationStream(userID, hostname); got != streamB {
		t.Fatal("removeCreationStreamIfMatch with stale pointer removed the replacement stream")
	}
}

func TestAPICheckoutParams(t *testing.T) {
	server := newTestServer(t)

	// Create a user and get auth cookie
	email := "cp-api-test@example.com"
	user, err := server.createUser(t.Context(), testSSHPubKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Insert checkout params
	token := "test-cp-token-123"
	err = withTx1(server, t.Context(), (*exedb.Queries).InsertCheckoutParams, exedb.InsertCheckoutParamsParams{
		Token:    token,
		UserID:   user.UserID,
		Source:   "new",
		VMName:   "my-restored-vm",
		VMPrompt: "Build a blog with Go",
		VMImage:  "exeuntu",
	})
	if err != nil {
		t.Fatalf("Failed to insert checkout params: %v", err)
	}

	// Fetch checkout params via API
	req := httptest.NewRequest("GET", "/api/checkout-params?token="+token, nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result struct {
		Name   string `json:"name"`
		Prompt string `json:"prompt"`
		Image  string `json:"image"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}
	if result.Name != "my-restored-vm" {
		t.Errorf("Expected name 'my-restored-vm', got %q", result.Name)
	}
	if result.Prompt != "Build a blog with Go" {
		t.Errorf("Expected prompt 'Build a blog with Go', got %q", result.Prompt)
	}
	if result.Image != "exeuntu" {
		t.Errorf("Expected image 'exeuntu', got %q", result.Image)
	}

	// Missing token returns 400
	req = httptest.NewRequest("GET", "/api/checkout-params", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for missing token, got %d", w.Code)
	}

	// Invalid token returns 404
	req = httptest.NewRequest("GET", "/api/checkout-params?token=bogus", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("Expected 404 for invalid token, got %d", w.Code)
	}

	// Unauthenticated returns 401
	req = httptest.NewRequest("GET", "/api/checkout-params?token="+token, nil)
	req.Host = server.env.WebHost
	w = httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 for unauthenticated, got %d", w.Code)
	}
}
