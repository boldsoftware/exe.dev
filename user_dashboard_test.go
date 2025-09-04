package exe

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"exe.dev/sqlite"
)

func TestUserDashboard(t *testing.T) {
	t.Parallel()

	server := NewTestServer(t)

	// Create a test user
	email := "test@example.com"
	allocID := "test-alloc"

	// Insert test user
	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}

	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO users (user_id, email)
			VALUES (?, ?)
		`, userID, email)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	// Create SSH key for user
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO ssh_keys (user_id, public_key)
			VALUES (?, ?)
		`, userID, "ssh-rsa dummy-test-key test@example.com")
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert SSH key: %v", err)
	}

	// Create alloc for the user
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, ctrhost, created_at)
			VALUES (?, ?, 'medium', 'aws-us-west-2', 'local', datetime('now'))
		`, allocID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert test alloc: %v", err)
	}

	// Create a test box
	boxName := "testbox"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (alloc_id, name, status, image, created_by_user_id)
			VALUES (?, ?, ?, ?, ?)
		`, allocID, boxName, "stopped", "ubuntu:22.04", userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert test box: %v", err)
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

	// Check that box name appears
	if !strings.Contains(bodyStr, boxName) {
		t.Errorf("Expected to find box name %s in dashboard", boxName)
	}

	// Check that the page has expected elements (title contains EXE.DEV)
	if !strings.Contains(bodyStr, "EXE.DEV") {
		t.Logf("Response body (first 500 chars): %s", bodyStr[:min(500, len(bodyStr))])
		t.Errorf("Expected to find 'EXE.DEV' in page title")
	}

	// Check for welcome message or boxes section
	if !strings.Contains(bodyStr, "welcome") && !strings.Contains(bodyStr, "Boxes") && !strings.Contains(bodyStr, "boxes") {
		t.Errorf("Expected to find welcome message or boxes section")
	}
}
