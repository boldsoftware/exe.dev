package exe

import (
	"net/http"
	"os"
	"testing"
	"time"
)

func TestBrowserScenario(t *testing.T) {
	// This test simulates browser access using foo.localhost subdomains
	// which work without DNS setup on most systems

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server with HTTP enabled for dev mode on a specific port
	// We use a fixed port for testing to avoid port detection issues
	httpPort := "18088"
	sshPort := "12288"
	server, err := NewServer(":"+httpPort, "", ":"+sshPort, ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

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
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, created_at) 
		VALUES (?, ?, 'medium', 'aws-us-west-2', datetime('now'))`, allocID, userID)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Create a mock container
	containerID := "mock-httptest-container"
	machineName := "httptest"

	// Add container to mock manager
	mockManager.AddContainer(containerID, machineName, userID, allocID)

	// Store container in database using the proper createMachine method
	err = server.createMachine(userID, allocID, machineName, containerID, "")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Update status to running
	_, err = server.db.Exec(`UPDATE machines SET status = 'running' WHERE name = ?`, machineName)
	if err != nil {
		t.Fatalf("Failed to store machine in database: %v", err)
	}

	// Create auth cookie for the test
	cookieValue, err := server.createAuthCookie(userID, "localhost")
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Start HTTP server in background
	serverStarted := make(chan bool)
	go func() {
		serverStarted <- true
		server.Start() // This starts all servers (HTTP, HTTPS, SSH)
	}()

	// Wait for server to start
	<-serverStarted
	time.Sleep(200 * time.Millisecond)

	t.Logf("HTTP server listening on port %s", httpPort)

	// Make HTTP request to simulate browser
	// We'll use the Host header to route to the correct subdomain
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Fix the port on redirects - the server might redirect to port 80/443
			// but we're testing on a custom port
			return http.ErrUseLastResponse // Don't follow redirects
		},
	}

	// Test 1: Request without authentication should redirect to auth
	req, err := http.NewRequest("GET", "http://localhost:"+httpPort+"/", nil)
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

	// Test 2: Request with authentication cookie should proxy
	req2, err := http.NewRequest("GET", "http://localhost:"+httpPort+"/", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req2.Host = "httptest.localhost"
	req2.AddCookie(&http.Cookie{
		Name:  "exe_auth",
		Value: cookieValue,
	})

	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("Failed to make authenticated request: %v", err)
	}
	resp2.Body.Close()

	t.Logf("Authenticated response status: %d", resp2.StatusCode)

	// The mock container should return 502 (Bad Gateway) since it's not really running
	// This is expected behavior when the container exists but isn't responding
	if resp2.StatusCode != http.StatusBadGateway {
		t.Errorf("Expected 502 Bad Gateway (container not responding), got %d", resp2.StatusCode)
	}

	// Test 3: Request to non-existent machine should return 404
	req3, err := http.NewRequest("GET", "http://localhost:"+httpPort+"/", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req3.Host = "nonexistent.localhost"
	req3.AddCookie(&http.Cookie{
		Name:  "exe_auth",
		Value: cookieValue,
	})

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
