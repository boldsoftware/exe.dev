package exe

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestProxyMagicAuthFlow tests the complete proxy magic authentication flow
// to debug the infinite redirect issue
func TestProxyMagicAuthFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database
	tmpDB, err := os.CreateTemp("", "proxy_magic_auth_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server with quiet mode disabled to see debug logs
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = false // Enable logging for debugging
	defer server.Stop()

	// Use mock container manager
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Create test data
	email := "test@example.com"
	teamName := "testteam"
	machineName := "testmachine"
	containerID := "test-container-123"

	// Create user and team
	userID, err := server.createTestUserWithID(email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	t.Logf("Created user with ID: %s", userID)

	_, err = server.db.Exec(`INSERT INTO teams (team_name) VALUES (?)`, teamName)
	if err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	_, err = server.db.Exec(`INSERT INTO team_members (user_id, team_name, is_admin) VALUES (?, ?, ?)`, userID, teamName, true)
	if err != nil {
		t.Fatalf("Failed to add user to team: %v", err)
	}

	// Create machine
	err = server.createMachine(userID, teamName, machineName, containerID, "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Add container to mock manager
	mockManager.AddContainer(containerID, machineName, userID, teamName)

	// Step 1: Simulate initial request to machine.team.localhost (no auth cookie)
	t.Logf("Step 1: Initial request to proxy subdomain without auth")
	req1 := httptest.NewRequest("GET", "http://testmachine.testteam.localhost:8080/", nil)
	req1.Host = "testmachine.testteam.localhost:8080"
	w1 := httptest.NewRecorder()

	server.ServeHTTP(w1, req1)

	t.Logf("Step 1 Response: Status=%d", w1.Code)
	for k, v := range w1.Header() {
		t.Logf("Step 1 Header: %s = %v", k, v)
	}

	// Should redirect to main auth
	if w1.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Expected redirect (307), got %d", w1.Code)
	}

	location1 := w1.Header().Get("Location")
	t.Logf("Step 1 Redirect to: %s", location1)

	if !strings.Contains(location1, "localhost") {
		t.Fatalf("Expected redirect to localhost for auth, got: %s", location1)
	}

	// Step 2: Follow redirect to main auth (simulate user doing auth)
	t.Logf("Step 2: Follow redirect to main auth domain")
	req2 := httptest.NewRequest("GET", location1, nil)
	if strings.HasPrefix(location1, "/") {
		req2.Host = "localhost:8080" // Simulate main domain
	}
	w2 := httptest.NewRecorder()

	// First create an auth cookie for the user (simulate successful login)
	authCookieValue, err := server.createAuthCookie(userID, "localhost")
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}
	t.Logf("Created auth cookie: %s...", authCookieValue[:10])

	// Add the auth cookie to the request
	req2.AddCookie(&http.Cookie{Name: "exe-auth", Value: authCookieValue})

	server.ServeHTTP(w2, req2)

	t.Logf("Step 2 Response: Status=%d", w2.Code)
	for k, v := range w2.Header() {
		t.Logf("Step 2 Header: %s = %v", k, v)
	}

	// Should redirect back with magic secret
	if w2.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Expected redirect (307), got %d", w2.Code)
	}

	location2 := w2.Header().Get("Location")
	t.Logf("Step 2 Redirect to: %s", location2)

	if !strings.Contains(location2, "/auth/confirm") {
		t.Fatalf("Expected redirect to confirmation page, got: %s", location2)
	}

	if !strings.Contains(location2, "secret=") {
		t.Fatalf("Expected secret parameter in confirmation URL, got: %s", location2)
	}

	// Step 2b: Simulate user clicking "Continue" on confirmation page
	t.Logf("Step 2b: User confirms login")
	confirmLocation := location2 + "&action=confirm"
	req2b := httptest.NewRequest("GET", confirmLocation, nil)
	req2b.Host = "localhost:0"
	w2b := httptest.NewRecorder()

	server.ServeHTTP(w2b, req2b)

	t.Logf("Step 2b Response: Status=%d", w2b.Code)
	for k, v := range w2b.Header() {
		t.Logf("Step 2b Header: %s = %v", k, v)
	}

	location2b := w2b.Header().Get("Location")
	t.Logf("Step 2b Redirect to: %s", location2b)

	if !strings.Contains(location2b, "__exe.dev/auth") {
		t.Fatalf("Expected redirect to magic auth URL after confirmation, got: %s", location2b)
	}

	if !strings.Contains(location2b, "secret=") {
		t.Fatalf("Expected secret parameter in magic auth URL, got: %s", location2b)
	}

	// Step 3: Follow magic auth redirect
	t.Logf("Step 3: Follow magic auth redirect")
	req3 := httptest.NewRequest("GET", location2b, nil)
	req3.Host = "testmachine.testteam.localhost:8080"
	w3 := httptest.NewRecorder()

	server.ServeHTTP(w3, req3)

	t.Logf("Step 3 Response: Status=%d", w3.Code)
	for k, v := range w3.Header() {
		t.Logf("Step 3 Header: %s = %v", k, v)
	}

	// Should set proxy auth cookie and redirect to original page
	if w3.Code != http.StatusTemporaryRedirect {
		t.Fatalf("Expected redirect (307), got %d", w3.Code)
	}

	// Check for proxy auth cookie
	proxyCookieSet := false
	for _, cookie := range w3.Result().Cookies() {
		if cookie.Name == "exe-proxy-auth" {
			proxyCookieSet = true
			t.Logf("Step 3 Proxy cookie set: %s...", cookie.Value[:10])
			break
		}
	}

	if !proxyCookieSet {
		t.Fatalf("Expected proxy auth cookie to be set")
	}

	location3 := w3.Header().Get("Location")
	t.Logf("Step 3 Redirect to: %s", location3)

	// Step 4: Final request with proxy auth cookie should succeed
	t.Logf("Step 4: Final request with proxy auth cookie")
	req4 := httptest.NewRequest("GET", "/", nil)
	req4.Host = "testmachine.testteam.localhost:8080"

	// Copy the proxy auth cookie from step 3
	for _, cookie := range w3.Result().Cookies() {
		if cookie.Name == "exe-proxy-auth" {
			req4.AddCookie(cookie)
			break
		}
	}

	w4 := httptest.NewRecorder()

	// Debug: Check cookie is being sent
	for _, cookie := range req4.Cookies() {
		t.Logf("Step 4 Request Cookie: %s = %s...", cookie.Name, cookie.Value[:10])
	}

	// Debug: Check what's in the database
	var cookieCount int
	server.db.QueryRow(`SELECT COUNT(*) FROM auth_cookies WHERE domain = 'localhost'`).Scan(&cookieCount)
	t.Logf("Step 4 Debug: %d cookies in database for domain 'localhost'", cookieCount)

	// Check if this specific cookie exists
	proxyCookieValue := ""
	for _, cookie := range req4.Cookies() {
		if cookie.Name == "exe-proxy-auth" {
			proxyCookieValue = cookie.Value
			break
		}
	}

	var dbUserID string
	err = server.db.QueryRow(`SELECT user_id FROM auth_cookies WHERE cookie_value = ? AND domain = 'localhost'`, proxyCookieValue).Scan(&dbUserID)
	if err != nil {
		t.Logf("Step 4 Debug: Cookie lookup failed: %v", err)
	} else {
		t.Logf("Step 4 Debug: Cookie found in DB for user: %s", dbUserID)
	}

	// Check team access
	var teamAccessCount int
	server.db.QueryRow(`SELECT COUNT(*) FROM team_members WHERE user_id = ? AND team_name = ?`, dbUserID, teamName).Scan(&teamAccessCount)
	t.Logf("Step 4 Debug: User %s has %d team memberships for %s", dbUserID, teamAccessCount, teamName)

	server.ServeHTTP(w4, req4)

	t.Logf("Step 4 Response: Status=%d", w4.Code)

	// Should not redirect anymore - should serve content or proxy to container
	if w4.Code == http.StatusTemporaryRedirect {
		location4 := w4.Header().Get("Location")
		t.Fatalf("Got unexpected redirect in step 4 - infinite loop detected. Location: %s", location4)
	}

	// Success! No infinite redirect
	t.Logf("✅ Magic auth flow completed successfully without infinite redirect")
}

// TestProxyMagicAuthUnauthorized tests that authenticated users without team access get 403
func TestProxyMagicAuthUnauthorized(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temporary database
	tmpDB, err := os.CreateTemp("", "proxy_unauthorized_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = false
	defer server.Stop()

	// Use mock container manager
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Create test data - user and TWO teams
	email := "test@example.com"
	userTeam := "userteam"   // User has access to this team
	otherTeam := "otherteam" // User does NOT have access to this team
	machineName := "testmachine"
	containerID := "test-container-123"

	// Create user
	userID, err := server.createTestUserWithID(email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}
	t.Logf("Created user with ID: %s", userID)

	// Create both teams
	_, err = server.db.Exec(`INSERT INTO teams (team_name) VALUES (?)`, userTeam)
	if err != nil {
		t.Fatalf("Failed to create user team: %v", err)
	}
	_, err = server.db.Exec(`INSERT INTO teams (team_name) VALUES (?)`, otherTeam)
	if err != nil {
		t.Fatalf("Failed to create other team: %v", err)
	}

	// Add user ONLY to userTeam, NOT to otherTeam
	_, err = server.db.Exec(`INSERT INTO team_members (user_id, team_name, is_admin) VALUES (?, ?, ?)`, userID, userTeam, true)
	if err != nil {
		t.Fatalf("Failed to add user to team: %v", err)
	}

	// Create machine in otherTeam (which user doesn't have access to)
	err = server.createMachine(userID, otherTeam, machineName, containerID, "ubuntu:22.04")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Add container to mock manager
	mockManager.AddContainer(containerID, machineName, userID, otherTeam)

	// Create proxy auth cookie directly (simulating user went through magic auth)
	proxyCookieValue, err := server.createAuthCookie(userID, "testmachine.otherteam.localhost")
	if err != nil {
		t.Fatalf("Failed to create proxy auth cookie: %v", err)
	}

	// Try to access machine in team they don't belong to
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "testmachine.otherteam.localhost:8080"
	req.AddCookie(&http.Cookie{Name: "exe-proxy-auth", Value: proxyCookieValue})

	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)

	t.Logf("Response Status: %d", w.Code)
	t.Logf("Response Body: %s", w.Body.String())

	// Should get 403 Forbidden, not a redirect
	if w.Code != http.StatusForbidden {
		t.Fatalf("Expected 403 Forbidden for unauthorized user, got %d", w.Code)
	}

	if !strings.Contains(w.Body.String(), "Forbidden") {
		t.Fatalf("Expected 'Forbidden' message in response body")
	}

	t.Logf("✅ Unauthorized user correctly received 403 Forbidden instead of redirect loop")
}
