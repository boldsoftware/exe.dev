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

func TestHTTPProxyEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

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
	fingerprint := "test-fingerprint-12345"
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

	// Create test container with Python HTTP server
	t.Log("Creating test container...")
	containerReq := &container.CreateContainerRequest{
		UserID:   fingerprint,
		Name:     "httptest",
		TeamName: teamName,
		Image:    "python:3.9-slim",
	}

	testContainer, err := gkeManager.CreateContainer(ctx, containerReq)
	if err != nil {
		t.Fatalf("Failed to create container: %v", err)
	}

	// Store container ID in database
	_, err = server.db.Exec(`
		INSERT INTO machines (team_name, name, container_id, created_by_fingerprint, status)
		VALUES (?, ?, ?, ?, 'running')
	`, teamName, "httptest", testContainer.ID, fingerprint)
	if err != nil {
		t.Fatalf("Failed to store machine in database: %v", err)
	}

	// Wait for container to be running
	t.Log("Waiting for container to be running...")
	timeout := time.Now().Add(3 * time.Minute)
	for time.Now().Before(timeout) {
		cont, err := gkeManager.GetContainer(ctx, fingerprint, testContainer.ID)
		if err == nil && cont.Status == container.StatusRunning {
			t.Log("Container is running!")
			break
		}
		t.Logf("Container status: %v, waiting...", cont.Status)
		time.Sleep(5 * time.Second)
	}

	// Start Python HTTP server in the container
	t.Log("Starting Python HTTP server in container...")
	go func() {
		err := gkeManager.ExecuteInContainer(
			context.Background(),
			fingerprint,
			testContainer.ID,
			[]string{"python3", "-m", "http.server", "80"},
			nil,
			os.Stdout,
			os.Stderr,
		)
		if err != nil {
			t.Logf("HTTP server error: %v", err)
		}
	}()

	// Give the server time to start
	time.Sleep(10 * time.Second)

	// Test the HTTP server is running inside the container
	t.Log("Testing HTTP server is accessible inside container...")
	var output strings.Builder
	err = gkeManager.ExecuteInContainer(
		ctx,
		fingerprint,
		testContainer.ID,
		[]string{"python3", "-c", "import urllib.request; print(urllib.request.urlopen('http://localhost:80').read().decode())"},
		nil,
		&output,
		os.Stderr,
	)
	if err != nil {
		t.Logf("Failed to test internal HTTP server: %v", err)
	} else {
		t.Logf("Internal HTTP server response: %s", output.String())
	}

	// Now test the HTTP proxy functionality
	t.Log("Testing HTTP proxy through exe.dev...")

	// Create containerTransport directly to test it
	transport := &containerTransport{
		gkeManager:  gkeManager,
		userID:      fingerprint,
		containerID: testContainer.ID,
		targetPort:  "80",
	}

	// Create a test HTTP request
	req, err := http.NewRequest("GET", "http://httptest.testteam.localhost/", nil)
	if err != nil {
		t.Fatalf("Failed to create HTTP request: %v", err)
	}

	// Test the transport directly
	t.Log("Testing containerTransport.RoundTrip...")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("HTTP proxy failed: %v", err)
	}
	defer resp.Body.Close()

	t.Logf("HTTP proxy response status: %s", resp.Status)

	// Read response body
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	responseBody := string(body[:n])
	t.Logf("HTTP proxy response body: %s", responseBody)

	// Verify we got a valid HTTP response
	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if !strings.Contains(responseBody, "Directory listing") && !strings.Contains(responseBody, "Index of") {
		t.Errorf("Response doesn't look like Python HTTP server output: %s", responseBody)
	}

	// Clean up - delete the container
	t.Log("Cleaning up test container...")
	// Note: In a real implementation, we'd implement DeleteContainer
	// For now, just let it be cleaned up by the test environment

	t.Log("HTTP proxy end-to-end test completed successfully!")
}