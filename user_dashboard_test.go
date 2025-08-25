package exe

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserDashboard(t *testing.T) {
	t.Parallel()

	server := NewTestServer(t, ":0", ":0")

	// Create a test user
	email := "test@example.com"
	allocID := "test-alloc"

	// Insert test user
	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}

	_, err = server.db.Exec(`
		INSERT INTO users (user_id, email)
		VALUES (?, ?)
	`, userID, email)
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	// Create SSH key for user
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (user_id, public_key)
		VALUES (?, ?)
	`, userID, "ssh-rsa dummy-test-key test@example.com")
	if err != nil {
		t.Fatalf("Failed to insert SSH key: %v", err)
	}

	// Create alloc for the user
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, created_at)
		VALUES (?, ?, 'medium', 'aws-us-west-2', datetime('now'))
	`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to insert test alloc: %v", err)
	}

	// Create a test machine
	machineName := "testmachine"
	_, err = server.db.Exec(`
		INSERT INTO machines (alloc_id, name, status, image, created_by_user_id)
		VALUES (?, ?, ?, ?, ?)
	`, allocID, machineName, "stopped", "ubuntu:22.04", userID)
	if err != nil {
		t.Fatalf("Failed to insert test machine: %v", err)
	}

	// Create test server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Call handleUserDashboard directly with userID
		server.handleUserDashboard(w, r, userID)
	}))
	defer ts.Close()

	// Create HTTP client with cookies
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Make request to dashboard
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("Failed to GET dashboard: %v", err)
	}
	defer resp.Body.Close()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response: %v", err)
	}
	bodyStr := string(body)

	// Check response
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	// Check that user email appears
	if !strings.Contains(bodyStr, email) {
		t.Errorf("Expected to find user email %s in dashboard", email)
	}

	// Check that machine name appears
	if !strings.Contains(bodyStr, machineName) {
		t.Errorf("Expected to find machine name %s in dashboard", machineName)
	}

	// Check that the page has expected elements (title contains EXE.DEV)
	if !strings.Contains(bodyStr, "EXE.DEV") {
		t.Logf("Response body (first 500 chars): %s", bodyStr[:min(500, len(bodyStr))])
		t.Errorf("Expected to find 'EXE.DEV' in page title")
	}

	// Check for welcome message or machines section
	if !strings.Contains(bodyStr, "welcome") && !strings.Contains(bodyStr, "Machines") && !strings.Contains(bodyStr, "machines") {
		t.Errorf("Expected to find welcome message or machines section")
	}
}
