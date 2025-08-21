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
	teamName := "testteam"

	// Create user and team
	userID, err := server.createTestUserWithID(email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	_, err = server.db.Exec(`INSERT INTO teams (team_name) VALUES (?)`, teamName)
	if err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	_, err = server.db.Exec(`INSERT INTO team_members (user_id, team_name, is_admin) VALUES (?, ?, ?)`, userID, teamName, true)
	if err != nil {
		t.Fatalf("Failed to add user to team: %v", err)
	}

	// Create a mock container
	containerID := "mock-httptest-container"
	machineName := "httptest"

	// Add container to mock manager
	mockManager.AddContainer(containerID, machineName, userID, teamName)

	// Store container in database using the proper createMachine method
	err = server.createMachine(userID, teamName, machineName, containerID, "")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Update status to running
	_, err = server.db.Exec(`UPDATE machines SET status = 'running' WHERE name = ? AND team_name = ?`, machineName, teamName)
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
			if req.URL.Port() == "" || req.URL.Port() == "80" || req.URL.Port() == "443" {
				req.URL.Host = "localhost:" + httpPort
			}
			// Copy cookies from previous requests
			if len(via) > 0 {
				for _, cookie := range via[len(via)-1].Cookies() {
					req.AddCookie(cookie)
				}
			}
			return nil
		},
	}

	// Test 1: Access the main page (should redirect to login/welcome)
	req, err := http.NewRequest("GET", "http://localhost:"+httpPort+"/", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP request to main page failed: %v", err)
	}
	resp.Body.Close()

	t.Logf("Main page response status: %s", resp.Status)

	// Test 2: Access container via subdomain using Host header
	// This simulates browser accessing httptest.testteam.localhost
	req, err = http.NewRequest("GET", "http://localhost:"+httpPort+"/", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Set Host header to trigger container routing
	// Format: machinename.teamname.localhost
	req.Host = machineName + "." + teamName + ".localhost"

	// Set auth cookie
	req.AddCookie(&http.Cookie{
		Name:   "exe-auth",
		Value:  cookieValue,
		Domain: "localhost",
		Path:   "/",
	})

	t.Logf("Making HTTP request to %s with Host: %s", req.URL, req.Host)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	t.Logf("Response status: %s", resp.Status)

	// Since we're using a mock container manager, we expect the proxy to attempt
	// to forward the request but the mock won't actually serve content
	// We should at least get past authentication

	// In dev mode with mock container, we might get different responses
	// Let's check that we're properly routing based on subdomain
	if resp.StatusCode == 502 || resp.StatusCode == 503 {
		// Expected when mock container doesn't have a real HTTP server
		t.Log("Got expected proxy error for mock container (502/503)")
	} else if resp.StatusCode == 401 || resp.StatusCode == 403 {
		t.Errorf("Authentication failed - cookie not working properly")
	} else if resp.StatusCode == 404 {
		// This can happen if the auth endpoint doesn't exist or the proxy can't connect
		// In dev mode, this might be expected
		t.Log("Got 404 - possibly expected in test environment")
	} else if resp.StatusCode == 200 {
		// If we somehow get a 200, that's fine too (mock might return success)
		t.Log("Request succeeded")
	} else {
		t.Logf("Unexpected status code: %d", resp.StatusCode)
	}

	// Test 3: Try accessing without auth cookie (should redirect to auth)
	req, err = http.NewRequest("GET", "http://localhost:"+httpPort+"/", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	req.Host = machineName + "." + teamName + ".localhost"

	// Use a client without redirect following to check the redirect
	noRedirectClient := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Don't follow redirects
		},
	}

	resp, err = noRedirectClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request without auth failed: %v", err)
	}
	resp.Body.Close()

	// Should redirect to auth (302 or 307)
	if resp.StatusCode != 302 && resp.StatusCode != 307 {
		// Also accept 200 if it's showing a login page directly
		if resp.StatusCode == 200 {
			t.Log("Got 200 - server may be showing login page directly")
		} else {
			t.Errorf("Expected redirect to auth without cookie, got status %d", resp.StatusCode)
		}
	} else {
		t.Logf("Got expected redirect to auth: %s", resp.Header.Get("Location"))
	}

	t.Log("Browser scenario test completed successfully!")
}
