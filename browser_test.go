package exe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"

	"exe.dev/sqlite"
)

func TestBrowserScenario(t *testing.T) {
	t.Parallel()
	// This test simulates browser access using foo.localhost subdomains
	// which work without DNS setup on most systems

	// Create server with HTTP enabled for dev mode on a specific port
	server := NewTestServer(t)

	// Use mock container manager for testing
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Generate test user
	email := "test@example.com"
	allocID := "test-alloc"

	// Create user and alloc
	userID, err := server.createTestUserWithID(email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc for the user
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, created_at)
			VALUES (?, ?, 'medium', 'aws-us-west-2', datetime('now'))`, allocID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Create a mock container
	containerID := "mock-httptest-container"
	machineName := "httptest"

	// Add container to mock manager
	mockManager.AddContainer(containerID, machineName, allocID)

	// Store container in database using the proper createMachine method
	err = server.createMachine(t.Context(), userID, allocID, machineName, containerID, "")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Update status to running
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`UPDATE machines SET status = 'running' WHERE name = ?`, machineName)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to store machine in database: %v", err)
	}

	// Create magic secret for authentication
	magicSecret, err := server.createMagicSecret(userID, machineName, "/")
	if err != nil {
		t.Fatalf("Failed to create magic secret: %v", err)
	}

	// Make HTTP request to simulate browser
	// We'll use the Host header to route to the correct subdomain
	jar, _ := cookiejar.New(nil) // Create a cookie jar to maintain cookies
	client := &http.Client{
		Timeout: 5 * time.Second,
		Jar:     jar, // Use cookie jar to maintain cookies across requests
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Fix the port on redirects - the server might redirect to port 80/443
			// but we're testing on a custom port
			return http.ErrUseLastResponse // Don't follow redirects
		},
	}

	baseURL := fmt.Sprintf("http://localhost:%v/", server.httpLn.tcp.Port)

	// Test 1: Request without authentication should redirect to auth
	req, err := http.NewRequest("GET", baseURL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	// Set the Host header to simulate subdomain routing (new format: machinename.localhost)
	req.Host = "httptest.localhost"

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	resp.Body.Close()

	t.Logf("Response status: %d", resp.StatusCode)

	// Expect redirect to auth (302 or 307)
	if resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusFound {
		t.Errorf("Expected redirect to auth, got status %d", resp.StatusCode)
	}

	// Test 2: Get auth cookie through magic auth flow
	// First, request the magic auth URL to get the cookie
	magicReq, err := http.NewRequest("GET", fmt.Sprintf("%s__exe.dev/auth?secret=%s", baseURL, magicSecret), nil)
	if err != nil {
		t.Fatalf("Failed to create magic auth request: %v", err)
	}
	magicReq.Host = "httptest.localhost"

	magicResp, err := client.Do(magicReq)
	if err != nil {
		t.Fatalf("Failed to do magic auth: %v", err)
	}
	magicResp.Body.Close()

	if magicResp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("Expected redirect after magic auth, got %d", magicResp.StatusCode)
	}

	// Now make the actual request with the cookie that was set
	req2, err := http.NewRequest("GET", baseURL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req2.Host = "httptest.localhost"

	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("Failed to make authenticated request: %v", err)
	}
	resp2.Body.Close()

	t.Logf("Authenticated response status: %d", resp2.StatusCode)

	// In test mode, the proxy returns 200 with a test response when SSH credentials are test values
	// This is because we're using testMode which simulates successful proxy
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("Expected 200 OK (test mode proxy simulation), got %d", resp2.StatusCode)
	}

	// Test 3: Request to non-existent machine should return 404
	req3, err := http.NewRequest("GET", baseURL, nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req3.Host = "nonexistent.localhost"
	// Use the same client that has cookies from the magic auth

	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("Failed to make request for non-existent machine: %v", err)
	}
	resp3.Body.Close()

	t.Logf("Non-existent machine response status: %d", resp3.StatusCode)

	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("Expected 404 for non-existent machine, got %d", resp3.StatusCode)
	}
}
