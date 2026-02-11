package execore

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMobileHome(t *testing.T) {
	server := newTestServer(t)
	req := httptest.NewRequest("GET", "/m", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("GET /m returned status %d, want %d", w.Code, http.StatusOK)
	}
	reject := "Internal server error"
	if bytes.Contains(w.Body.Bytes(), []byte(reject)) {
		t.Errorf("response included unexpected string %q", reject)
	}
}

func TestMobileHostnameCheck(t *testing.T) {
	server := newTestServer(t)

	// Test hostname availability check
	reqBody := `{"hostname": "test-hostname"}`
	req := httptest.NewRequest("POST", "/m/check-hostname", strings.NewReader(reqBody))
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

func TestMobileVMListUnauthorized(t *testing.T) {
	server := newTestServer(t)

	// Test VM list without authentication
	req := httptest.NewRequest("GET", "/m/home", nil)
	req.Host = server.env.WebHost
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	// Should redirect to /m
	if w.Code != http.StatusSeeOther {
		t.Errorf("Expected status 303, got %d", w.Code)
	}

	location := w.Header().Get("Location")
	if location != "/m" {
		t.Errorf("Expected redirect to /m, got %s", location)
	}
}

func TestMobileInvalidHostname(t *testing.T) {
	server := newTestServer(t)

	// Test invalid hostname check
	reqBody := `{"hostname": "a"}`
	req := httptest.NewRequest("POST", "/m/check-hostname", strings.NewReader(reqBody))
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

func TestMobileInvalidEmail(t *testing.T) {
	server := newTestServer(t)

	// Test invalid email
	form := url.Values{}
	form.Add("email", "invalid-email")

	req := httptest.NewRequest("POST", "/m/email-auth", strings.NewReader(form.Encode()))
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
	req := httptest.NewRequest("POST", "/m/cmd", strings.NewReader(reqBody))
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
	req := httptest.NewRequest("POST", "/m/cmd", strings.NewReader(reqBody))
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

func TestDashboardWaitsForCreatingBox(t *testing.T) {
	// Test that the dashboard busy-waits for boxes that are being created.
	// This tests the fix for the race where redirect happens before DB insert.
	server := newTestServer(t)

	// Create a user and get auth cookie
	email := "creating-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, server.env.WebHost)
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Create a creation stream (simulating startBoxCreation before the goroutine inserts into DB)
	hostname := "my-new-vm"
	cs := server.getOrCreateCreationStream(user.UserID, hostname)

	// Start a goroutine that will insert the box after a short delay
	// This simulates preCreateBox running in the background goroutine
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, err := server.preCreateBox(t.Context(), preCreateBoxOptions{
			userID:        user.UserID,
			ctrhost:       "test-exelet",
			name:          hostname,
			image:         "exeuntu",
			noShard:       true,
			region:        "pdx",
			allocatedCPUs: 2,
		})
		if err != nil {
			t.Errorf("preCreateBox failed: %v", err)
		}
		cs.MarkDone(nil)
	}()

	// Load the dashboard - it should wait for the box to appear
	start := time.Now()
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = server.env.WebHost
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	elapsed := time.Since(start)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", w.Code)
	}

	// Should have waited at least 200ms for the box to appear
	if elapsed < 150*time.Millisecond {
		t.Errorf("Dashboard returned too quickly (%v), expected to wait for box creation", elapsed)
	}

	body := w.Body.String()

	// The dashboard should show the hostname
	if !strings.Contains(body, hostname) {
		t.Errorf("Dashboard should contain hostname %q but it doesn't", hostname)
	}

	// The dashboard should show "creating" status (the box was inserted with status="creating")
	if !strings.Contains(body, `class="status-dot creating"`) {
		t.Errorf("Dashboard should contain creating status dot")
	}
}
