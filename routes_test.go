package exe

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"exe.dev/sqlite"
)

func TestRouteStructs(t *testing.T) {
	t.Parallel()
	// Test default route creation
	var box Box
	route := box.getDefaultRoute()

	if route.Port != 80 {
		t.Errorf("Expected default port 80, got %d", route.Port)
	}
	if route.Share != "private" {
		t.Errorf("Expected default share 'private', got '%s'", route.Share)
	}
}

func TestBoxRoute(t *testing.T) {
	t.Parallel()
	box := Box{}

	// Test getting route when none are set (should return defaults)
	route := box.GetRoute()
	if route.Port != 80 {
		t.Errorf("Expected default port 80, got %d", route.Port)
	}
	if route.Share != "private" {
		t.Errorf("Expected default share 'private', got '%s'", route.Share)
	}

	// Test setting custom route
	customRoute := Route{
		Port:  3000,
		Share: "public",
	}

	err := box.SetRoute(customRoute)
	if err != nil {
		t.Errorf("Error setting route: %v", err)
	}

	// Test getting the custom route back
	retrievedRoute := box.GetRoute()
	if retrievedRoute.Port != 3000 {
		t.Errorf("Expected port 3000, got %d", retrievedRoute.Port)
	}
	if retrievedRoute.Share != "public" {
		t.Errorf("Expected share 'public', got '%s'", retrievedRoute.Share)
	}
}

func TestProxyHostnameParsing(t *testing.T) {
	t.Parallel()
	server := &Server{}

	tests := []struct {
		hostname    string
		expectedBox string
		shouldError bool
	}{
		{"test-box.exe.dev", "test-box", false},
		{"web.localhost", "web", false},
		{"api.exe.dev", "api", false},
		{"empty.exe.dev", "empty", false}, // Valid in new format
		{"invalid.domain.com", "", true},
		{"box.with.dots.exe.dev", "", true}, // Too many subdomains
		{"just-domain.com", "", true},       // Not exe.dev or localhost
	}

	for _, test := range tests {
		box, err := server.parseProxyHostname(test.hostname)

		if test.shouldError {
			if err == nil {
				t.Errorf("Expected error for hostname '%s', got none", test.hostname)
			}
			continue
		}

		if err != nil {
			t.Errorf("Unexpected error for hostname '%s': %v", test.hostname, err)
			continue
		}

		if box != test.expectedBox {
			t.Errorf("Expected box '%s', got '%s'", test.expectedBox, box)
		}
	}
}

// TestRouteMatching is no longer needed since we have simplified routing
// All requests to a box go to the same port with the same sharing setting

func TestBoxCreationWithRoute(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Set up test user and alloc
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"
	allocID := "alloc-test-123"

	// Create user with alloc
	err := server.createUserWithAlloc(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user with alloc: %v", err)
	}

	// Test creating a box
	// Get userID for box creation
	var userID string
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, email).Scan(&userID)
	})
	if err != nil {
		t.Fatalf("Failed to get user ID: %v", err)
	}

	// Get allocID for this user
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT alloc_id FROM allocs WHERE user_id = ?`, userID).Scan(&allocID)
	})
	if err != nil {
		t.Fatalf("Failed to get alloc ID: %v", err)
	}

	server.createTestBox(t, userID, allocID, "test-box", "container123", "ubuntu")

	// Retrieve the box and check its route
	box, err := server.getBoxByName(t.Context(), "test-box")
	if err != nil {
		t.Errorf("Failed to get box: %v", err)
	}

	if box.Routes == nil {
		t.Error("Box routes should not be nil")
	} else {
		route := box.GetRoute()
		if route.Port != 80 {
			t.Errorf("Expected default port 80, got %d", route.Port)
		}
		if route.Share != "private" {
			t.Errorf("Expected default share 'private', got '%s'", route.Share)
		}
	}
}

func TestHandleProxyRequest(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test user and alloc
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"

	// Create user with alloc
	err := server.createUserWithAlloc(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user with alloc: %v", err)
	}

	// Get userID and allocID for box creation
	var userID, allocID string
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		if err := rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, email).Scan(&userID); err != nil {
			return err
		}
		return rx.QueryRow(`SELECT alloc_id FROM allocs WHERE user_id = ?`, userID).Scan(&allocID)
	})
	if err != nil {
		t.Fatalf("Failed to get user and alloc IDs: %v", err)
	}

	// Create a test box
	server.createTestBox(t, userID, allocID, "web-server", "container123", "nginx")

	// Get the box and set it to public
	box, err := server.getBoxByName(t.Context(), "web-server")
	if err != nil {
		t.Fatalf("Failed to get box: %v", err)
	}

	// Set a public route
	publicRoute := Route{
		Port:  80,
		Share: "public",
	}
	err = box.SetRoute(publicRoute)
	if err != nil {
		t.Fatalf("Failed to set route: %v", err)
	}

	// Update the box in the database
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`UPDATE boxes SET routes = ? WHERE name = ? AND alloc_id = ?`,
			*box.Routes, "web-server", allocID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update box route: %v", err)
	}

	tests := []struct {
		hostname       string
		method         string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{"web-server.exe.dev", "GET", "/", 200, "port: 80"},                      // Public route should work
		{"web-server.exe.dev", "GET", "/api/status", 200, "port: 80"},             // All paths go to same port
		{"nonexistent.exe.dev", "GET", "/", 404, "Box not found"},
	}

	for _, test := range tests {
		req := httptest.NewRequest(test.method, test.path, nil)
		req.Host = test.hostname

		w := httptest.NewRecorder()
		server.handleProxyRequest(w, req)

		if w.Code != test.expectedStatus {
			t.Errorf("Expected status %d for %s %s %s, got %d",
				test.expectedStatus, test.hostname, test.method, test.path, w.Code)
		}

		// Check response body or Location header depending on status
		if w.Code == 307 {
			// For redirects, check the Location header
			location := w.Header().Get("Location")
			if !strings.Contains(location, test.expectedBody) {
				t.Errorf("Expected Location header to contain '%s' for %s %s %s, got: %s",
					test.expectedBody, test.hostname, test.method, test.path, location)
			}
		} else {
			// For other responses, check the body
			body := w.Body.String()
			if !strings.Contains(body, test.expectedBody) {
				t.Errorf("Expected body to contain '%s' for %s %s %s, got: %s",
					test.expectedBody, test.hostname, test.method, test.path, body)
			}
		}
	}
}

// TestRouteSorting is no longer needed since we have simplified routing
// All requests go to the same port with the same sharing setting

// mockSession implements io.Writer for testing
type mockSession struct {
	output strings.Builder
}

func (m *mockSession) Write(p []byte) (n int, err error) {
	return m.output.Write(p)
}

func TestRouteCommandEndToEnd(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test user and alloc
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"
	boxName := "web-server"

	// Create user with alloc
	err := server.createUserWithAlloc(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user with alloc: %v", err)
	}

	// Get userID and allocID for box creation
	var userID, allocID string
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		if err := rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, email).Scan(&userID); err != nil {
			return err
		}
		return rx.QueryRow(`SELECT alloc_id FROM allocs WHERE user_id = ?`, userID).Scan(&allocID)
	})
	if err != nil {
		t.Fatalf("Failed to get user and alloc IDs: %v", err)
	}

	// Create a test box
	server.createTestBox(t, userID, allocID, boxName, "container123", "nginx")

	// Create SSH server for testing
	sshServer := &SSHServer{server: server}

	// Test route command by calling it directly
	cc := &CommandContext{
		PublicKey: publicKey,
		Alloc:     &Alloc{AllocID: allocID},
		Args:      []string{boxName},
		Output:    &mockSession{},
	}

	// Test showing current route (no flags)
	cc.FlagSet = routeCommandFlags() // Create empty flagset
	err = sshServer.handleRouteCommand(t.Context(), cc)
	if err != nil {
		t.Errorf("Failed to show route: %v", err)
	}
	output := cc.Output.(*mockSession).output.String()
	if !strings.Contains(output, "Port: 80") || !strings.Contains(output, "Share: private") {
		t.Errorf("Expected default route info, got: %s", output)
	}

	// Test setting route to public
	cc.Output = &mockSession{}
	cc.FlagSet = routeCommandFlags()
	cc.FlagSet.Set("port", "8080")
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleRouteCommand(t.Context(), cc)
	if err != nil {
		t.Errorf("Failed to set route: %v", err)
	}
	output = cc.Output.(*mockSession).output.String()
	if !strings.Contains(output, "updated successfully") {
		t.Errorf("Expected success message, got: %s", output)
	}

	// Verify the route was updated
	box, err := server.getBoxByName(t.Context(), boxName)
	if err != nil {
		t.Errorf("Failed to get box: %v", err)
	}
	route := box.GetRoute()
	if route.Port != 8080 {
		t.Errorf("Expected port 8080, got %d", route.Port)
	}
	if route.Share != "public" {
		t.Errorf("Expected share 'public', got '%s'", route.Share)
	}

	// Test error on nonexistent box
	cc.Output = &mockSession{}
	cc.Args = []string{"nonexistent"}
	cc.FlagSet = routeCommandFlags()
	cc.FlagSet.Set("port", "80")
	cc.FlagSet.Set("private", "true")
	err = sshServer.handleRouteCommand(t.Context(), cc)
	if err == nil {
		t.Error("Expected error for nonexistent box")
	}
}

// TestSimplifiedRoutingEndToEnd tests the complete simplified routing flow
func TestSimplifiedRoutingEndToEnd(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test user and alloc
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"
	boxName := "test-server"

	// Create user with alloc
	err := server.createUserWithAlloc(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user with alloc: %v", err)
	}

	// Get userID and allocID
	var userID, allocID string
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		if err := rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, email).Scan(&userID); err != nil {
			return err
		}
		return rx.QueryRow(`SELECT alloc_id FROM allocs WHERE user_id = ?`, userID).Scan(&allocID)
	})
	if err != nil {
		t.Fatalf("Failed to get user and alloc IDs: %v", err)
	}

	// Create a test box
	server.createTestBox(t, userID, allocID, boxName, "container123", "nginx")

	// Test 1: Verify default routing (private, port 80)
	box, err := server.getBoxByName(t.Context(), boxName)
	if err != nil {
		t.Fatalf("Failed to get box: %v", err)
	}

	route := box.GetRoute()
	if route.Port != 80 {
		t.Errorf("Expected default port 80, got %d", route.Port)
	}
	if route.Share != "private" {
		t.Errorf("Expected default share 'private', got '%s'", route.Share)
	}

	// Test 2: Test proxy request with private route (should redirect to auth)
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = boxName + ".exe.dev"
	w := httptest.NewRecorder()
	server.handleProxyRequest(w, req)

	// Should redirect for private access without auth
	if w.Code != 307 {
		t.Errorf("Expected status 307 for private route without auth, got %d", w.Code)
	}

	// Test 3: Use route command to set public access on port 8080
	sshServer := &SSHServer{server: server}
	cc := &CommandContext{
		PublicKey: publicKey,
		Alloc:     &Alloc{AllocID: allocID},
		Args:      []string{boxName},
		Output:    &mockSession{},
	}

	// Set to public port 8080
	cc.FlagSet = routeCommandFlags()
	cc.FlagSet.Set("port", "8080")
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleRouteCommand(t.Context(), cc)
	if err != nil {
		t.Fatalf("Failed to set route: %v", err)
	}

	// Test 4: Verify the route was updated
	box, err = server.getBoxByName(t.Context(), boxName)
	if err != nil {
		t.Fatalf("Failed to get updated box: %v", err)
	}

	updatedRoute := box.GetRoute()
	if updatedRoute.Port != 8080 {
		t.Errorf("Expected updated port 8080, got %d", updatedRoute.Port)
	}
	if updatedRoute.Share != "public" {
		t.Errorf("Expected updated share 'public', got '%s'", updatedRoute.Share)
	}

	// Test 5: Test proxy request with public route (should work)
	req2 := httptest.NewRequest("GET", "/api/test", nil)
	req2.Host = boxName + ".exe.dev"
	w2 := httptest.NewRecorder()
	server.handleProxyRequest(w2, req2)

	// Should work for public access
	if w2.Code != 200 {
		t.Errorf("Expected status 200 for public route, got %d", w2.Code)
	}

	// Check that it proxied to the right port
	body := w2.Body.String()
	if !strings.Contains(body, "port: 8080") {
		t.Errorf("Expected response to contain 'port: 8080', got: %s", body)
	}

	// Test 6: Test invalid port (should fail)
	cc.Output = &mockSession{}
	cc.FlagSet = routeCommandFlags()
	cc.FlagSet.Set("port", "99999")
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleRouteCommand(t.Context(), cc)
	if err == nil {
		t.Error("Expected error for invalid port 99999")
	}

	// Test 7: Test missing --private/--public when --port is specified (should fail)
	cc.Output = &mockSession{}
	cc.FlagSet = routeCommandFlags()
	cc.FlagSet.Set("port", "3000")
	err = sshServer.handleRouteCommand(t.Context(), cc)
	if err == nil {
		t.Error("Expected error when --port is specified without --private or --public")
	}

	// Test 8: Test missing --port when --public is specified (should fail)
	cc.Output = &mockSession{}
	cc.FlagSet = routeCommandFlags()
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleRouteCommand(t.Context(), cc)
	if err == nil {
		t.Error("Expected error when --public is specified without --port")
	}

	// Test 9: Test both --private and --public specified (should fail)
	cc.Output = &mockSession{}
	cc.FlagSet = routeCommandFlags()
	cc.FlagSet.Set("port", "80")
	cc.FlagSet.Set("private", "true")
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleRouteCommand(t.Context(), cc)
	if err == nil {
		t.Error("Expected error when both --private and --public are specified")
	}
}
