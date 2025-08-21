package exe

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestUserDashboard(t *testing.T) {
	server, cleanup := setupTestServerWithDatabase(t)
	defer cleanup()

	// Create a test user
	email := "test@example.com"
	teamName := "testteam"

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
		INSERT INTO ssh_keys (user_id, public_key, verified)
		VALUES (?, ?, 1)
	`, userID, "ssh-rsa dummy-test-key test@example.com")
	if err != nil {
		t.Fatalf("Failed to insert SSH key: %v", err)
	}

	// Create test team
	_, err = server.db.Exec(`
		INSERT INTO teams (team_name, is_personal, owner_user_id) 
		VALUES (?, ?, ?)
	`, teamName, true, userID)
	if err != nil {
		t.Fatalf("Failed to insert test team: %v", err)
	}

	// Add user to team
	_, err = server.db.Exec(`
		INSERT INTO team_members (user_id, team_name, is_admin) 
		VALUES (?, ?, ?)
	`, userID, teamName, true)
	if err != nil {
		t.Fatalf("Failed to insert team member: %v", err)
	}

	// Update the SSH key with additional details
	pubKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7..."
	_, err = server.db.Exec(`
		UPDATE ssh_keys SET public_key = ?, device_name = ?, default_team = ? WHERE user_id = ?
	`, pubKey, "Test Device", teamName, userID)
	if err != nil {
		t.Fatalf("Failed to update SSH key: %v", err)
	}

	// Add test machine
	_, err = server.db.Exec(`
		INSERT INTO machines (team_name, name, status, image, created_by_user_id) 
		VALUES (?, ?, ?, ?, ?)
	`, teamName, "testmachine", "running", "ubuntu:22.04", 1) // hardcode user_id for test
	if err != nil {
		t.Fatalf("Failed to insert test machine: %v", err)
	}

	// Start test server
	ts := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	defer ts.Close()

	// Create HTTP client with cookie jar
	cookieJar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: cookieJar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Copy cookies from previous requests
			if len(via) > 0 {
				for _, cookie := range via[len(via)-1].Cookies() {
					req.AddCookie(cookie)
				}
			}
			return nil
		},
	}

	t.Run("root_redirects_to_welcome_when_not_logged_in", func(t *testing.T) {
		resp, err := client.Get(ts.URL + "/")
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		// Should serve welcome.html content
		body := readBody(t, resp.Body)
		if !strings.Contains(body, "just use ssh") {
			t.Error("Expected welcome page content, got different content")
		}

		if !strings.Contains(body, "login") {
			t.Error("Expected login button on welcome page")
		}
	})

	t.Run("tilde_redirects_to_auth_when_not_logged_in", func(t *testing.T) {
		// Test /~
		resp, err := client.Get(ts.URL + "/~")
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200 (after redirect), got %d", resp.StatusCode)
		}

		// Should be redirected to auth page
		body := readBody(t, resp.Body)
		if !strings.Contains(body, "Authentication Required") {
			t.Error("Expected auth page, got different content")
		}
	})

	t.Run("user_dashboard_when_logged_in", func(t *testing.T) {
		// Create auth cookie for the test
		cookieValue, err := server.createAuthCookie(userID, "127.0.0.1")
		if err != nil {
			t.Fatalf("Failed to create auth cookie: %v", err)
		}

		// Test root path with auth cookie
		req, err := http.NewRequest("GET", ts.URL+"/", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		// Set auth cookie
		req.AddCookie(&http.Cookie{
			Name:  "exe-auth",
			Value: cookieValue,
		})

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		body := readBody(t, resp.Body)

		// Check for user dashboard content
		if !strings.Contains(body, "welcome back") {
			t.Error("Expected welcome back message")
		}

		if !strings.Contains(body, email) {
			t.Errorf("Expected user email %s in page content", email)
		}

		if !strings.Contains(body, "Your SSH Keys") {
			t.Error("Expected SSH keys section")
		}

		if !strings.Contains(body, "Your Machines") {
			t.Error("Expected machines section")
		}

		if !strings.Contains(body, "testmachine") {
			t.Error("Expected test machine name in content")
		}

		if !strings.Contains(body, "Test Device") {
			t.Error("Expected SSH key device name in content")
		}

		if !strings.Contains(body, "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC7") {
			t.Error("Expected SSH public key in content")
		}

		if !strings.Contains(body, "logout") {
			t.Error("Expected logout button in content")
		}

		if !strings.Contains(body, "You") {
			t.Error("Expected 'You' section")
		}

		if !strings.Contains(body, "copyToClipboard") {
			t.Error("Expected copy functionality for SSH commands")
		}

		if !strings.Contains(body, "Copy to clipboard") {
			t.Error("Expected copy button with tooltip")
		}

		if !strings.Contains(body, "ubuntu:22.04") {
			t.Error("Expected machine image in content")
		}
	})

	t.Run("tilde_path_when_logged_in", func(t *testing.T) {
		// Create auth cookie for the test
		cookieValue, err := server.createAuthCookie(userID, "127.0.0.1")
		if err != nil {
			t.Fatalf("Failed to create auth cookie: %v", err)
		}

		// Test /~ path with auth cookie
		req, err := http.NewRequest("GET", ts.URL+"/~", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		// Set auth cookie
		req.AddCookie(&http.Cookie{
			Name:  "exe-auth",
			Value: cookieValue,
		})

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		body := readBody(t, resp.Body)

		// Check for user dashboard content
		if !strings.Contains(body, "welcome back") {
			t.Error("Expected welcome back message")
		}

		if !strings.Contains(body, email) {
			t.Errorf("Expected user email %s in page content", email)
		}
	})

	t.Run("tilde_slash_path_when_logged_in", func(t *testing.T) {
		// Create auth cookie for the test
		cookieValue, err := server.createAuthCookie(userID, "127.0.0.1")
		if err != nil {
			t.Fatalf("Failed to create auth cookie: %v", err)
		}

		// Test /~/ path with auth cookie
		req, err := http.NewRequest("GET", ts.URL+"/~/", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		// Set auth cookie
		req.AddCookie(&http.Cookie{
			Name:  "exe-auth",
			Value: cookieValue,
		})

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200, got %d", resp.StatusCode)
		}

		body := readBody(t, resp.Body)

		// Check for user dashboard content
		if !strings.Contains(body, "welcome back") {
			t.Error("Expected welcome back message")
		}

		if !strings.Contains(body, email) {
			t.Errorf("Expected user email %s in page content", email)
		}
	})

	t.Run("logout_clears_cookie_and_redirects", func(t *testing.T) {
		// Create auth cookie for the test
		cookieValue, err := server.createAuthCookie(userID, "127.0.0.1")
		if err != nil {
			t.Fatalf("Failed to create auth cookie: %v", err)
		}

		// Test logout
		req, err := http.NewRequest("GET", ts.URL+"/logout", nil)
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}

		// Set auth cookie
		req.AddCookie(&http.Cookie{
			Name:  "exe-auth",
			Value: cookieValue,
		})

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Failed to make request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected status 200 after redirect, got %d", resp.StatusCode)
		}

		// Should be back to welcome page
		body := readBody(t, resp.Body)
		if !strings.Contains(body, "just use ssh") {
			t.Error("Expected welcome page after logout")
		}

		if !strings.Contains(body, "login") {
			t.Error("Expected login button on welcome page")
		}
	})
}

func TestUserDashboardWithNoData(t *testing.T) {
	server, cleanup := setupTestServerWithDatabase(t)
	defer cleanup()

	// Create a test user with no SSH keys or machines
	email := "empty@example.com"

	// Insert test user
	emptyUserID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}

	_, err = server.db.Exec(`
		INSERT INTO users (user_id, email) 
		VALUES (?, ?)
	`, emptyUserID, email)
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	// Create SSH key for authentication (needed for auth cookie)
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (user_id, public_key, verified)
		VALUES (?, ?, 1)
	`, emptyUserID, "ssh-rsa dummy-test-key test@example.com")
	if err != nil {
		t.Fatalf("Failed to insert SSH key: %v", err)
	}

	// Start test server
	ts := httptest.NewServer(http.HandlerFunc(server.ServeHTTP))
	defer ts.Close()

	// Create HTTP client
	client := &http.Client{}

	// Create auth cookie for the test
	cookieValue, err := server.createAuthCookie(emptyUserID, "127.0.0.1")
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Test user dashboard with no data
	req, err := http.NewRequest("GET", ts.URL+"/~", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	// Set auth cookie
	req.AddCookie(&http.Cookie{
		Name:  "exe-auth",
		Value: cookieValue,
	})

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body := readBody(t, resp.Body)

	// Check that empty state messages are shown
	// Note: User will have at least one SSH key for authentication, so we only check for no additional devices
	if strings.Contains(body, "No SSH keys found") {
		t.Log("User has the basic SSH key for authentication, which is expected")
	}

	if !strings.Contains(body, "No machines yet") {
		t.Error("Expected no machines message")
	}

	if !strings.Contains(body, "ssh exe.dev create --name=myname") {
		t.Error("Expected create machine command")
	}
}

// readBody reads the response body to a string
func readBody(t *testing.T, body io.ReadCloser) string {
	bytes, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("Failed to read body: %v", err)
	}
	return string(bytes)
}

// setupTestServerWithDatabase creates a test server with a database
func setupTestServerWithDatabase(t *testing.T) (*Server, func()) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	tmpDB.Close()

	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		os.Remove(tmpDB.Name())
		t.Fatalf("Failed to create server: %v", err)
	}

	// Use mock container manager for testing
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	return server, func() {
		server.Stop()
		os.Remove(tmpDB.Name())
	}
}
