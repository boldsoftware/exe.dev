package exe

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
)

func TestBrowserScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// This test simulates the exact browser scenario
	// Create a full HTTP server and make a real HTTP request to it

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server with HTTP enabled
	server, err := NewServer(":18081", "", ":12223", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}

	// Create GKE manager for testing
	ctx := context.Background()
	gkeManager, err := container.NewGKEManager(ctx, &container.Config{
		ProjectID:            "exe-dev-468515",
		ClusterName:          "exe-cluster",
		ClusterLocation:      "us-central1",
		NamespacePrefix:      "exe-",
		DefaultCPURequest:    "100m",
		DefaultMemoryRequest: "128Mi",
		DefaultStorageSize:   "1Gi",
	})
	if err != nil {
		t.Skipf("Skipping GKE test (no cluster access): %v", err)
	}

	server.containerManager = gkeManager

	// Generate test user
	fingerprint := "test-fingerprint-browser"
	email := "test@example.com"
	teamName := "testteam"

	// Create user and team
	_, err = server.db.Exec(`INSERT INTO users (public_key_fingerprint, email) VALUES (?, ?)`, fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	
	_, err = server.db.Exec(`INSERT INTO teams (name) VALUES (?)`, teamName)
	if err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}
	
	_, err = server.db.Exec(`INSERT INTO team_members (user_fingerprint, team_name, is_admin) VALUES (?, ?, ?)`, fingerprint, teamName, true)
	if err != nil {
		t.Fatalf("Failed to add user to team: %v", err)
	}

	// Use existing running container (delta-dog)
	containerID := "62670fdc-delta-dog-1754833841"

	// Store container in database
	_, err = server.db.Exec(`
		INSERT OR REPLACE INTO machines (team_name, name, container_id, created_by_fingerprint, status)
		VALUES (?, ?, ?, ?, 'running')
	`, teamName, "httptest", containerID, fingerprint)
	if err != nil {
		t.Fatalf("Failed to store machine in database: %v", err)
	}

	// Create auth cookie for the test
	cookieValue, err := server.createAuthCookie(fingerprint, "localhost:18081")
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Start HTTP server in background
	go func() {
		server.ServeHTTP(nil, nil) // This starts the HTTP server
	}()

	// Give server time to start
	time.Sleep(500 * time.Millisecond)

	// Make HTTP request to simulate browser
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	req, err := http.NewRequest("GET", "http://localhost:18081/", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Set Host header to trigger container routing
	req.Host = "httptest.testteam.localhost:18081"

	// Set auth cookie
	req.AddCookie(&http.Cookie{
		Name:  "exe-auth",
		Value: cookieValue,
	})

	t.Log("Making HTTP request to server...")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	t.Logf("Response status: %s", resp.Status)
	t.Logf("Response headers: %v", resp.Header)

	// Read response body
	body := make([]byte, 2048)
	n, _ := resp.Body.Read(body)
	responseBody := string(body[:n])
	t.Logf("Response body: %s", responseBody)

	// Verify response
	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if !strings.Contains(responseBody, "Directory listing") && !strings.Contains(responseBody, "Index of") {
		t.Errorf("Response doesn't look like Python HTTP server output: %s", responseBody)
	}

	server.Stop()
	t.Log("Browser scenario test completed!")
}