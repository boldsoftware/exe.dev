package exe

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"exe.dev/exedb"
	"exe.dev/exemenu"
	"exe.dev/sqlite"
)

func TestProxyHostnameParsing(t *testing.T) {
	t.Parallel()

	prodServer := &Server{}
	devServer := &Server{devMode: "dev"}

	tests := []struct {
		name        string
		server      *Server
		hostname    string
		expectedBox string
	}{
		{"prod valid exe.dev", prodServer, "test-box.exe.dev", "test-box"},
		{"prod rejects localhost", prodServer, "web.localhost", ""},
		{"prod valid simple", prodServer, "empty.exe.dev", "empty"},
		{"prod invalid domain", prodServer, "invalid.domain.com", ""},
		{"prod rejects dotted box", prodServer, "box.with.dots.exe.dev", ""},
		{"dev valid localhost", devServer, "dev-box.localhost", "dev-box"},
		{"dev rejects exe.dev", devServer, "dev-box.exe.dev", ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.server.parseProxyHostname(test.hostname)
			if result != test.expectedBox {
				t.Fatalf("parseProxyHostname(%q) = %q, want %q", test.hostname, result, test.expectedBox)
			}
		})
	}
}

func TestBoxCreationWithRoute(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Set up test user and alloc
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"
	allocID := "alloc-test-123"

	// Create user with alloc
	err := server.createUser(t.Context(), publicKey, email)
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
	box, err := withRxRes(server, t.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:   "test-box",
			UserID: userID,
		})
	})
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
	mainDomain := server.getMainDomain()

	// Create test user and alloc
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"

	// Create user with alloc
	err := server.createUser(t.Context(), publicKey, email)
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
	box, err := withRxRes(server, t.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:   "web-server",
			UserID: userID,
		})
	})
	if err != nil {
		t.Fatalf("Failed to get box: %v", err)
	}

	// Set a public route
	publicRoute := exedb.Route{
		Port:  80,
		Share: "public",
	}
	box.SetRoute(publicRoute)

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
		{fmt.Sprintf("web-server.%s", mainDomain), "GET", "/", 502, "Failed to proxy request to container"},           // No container running, should fail
		{fmt.Sprintf("web-server.%s", mainDomain), "GET", "/api/status", 502, "Failed to proxy request to container"}, // No container running, should fail
		{fmt.Sprintf("nonexistent.%s", mainDomain), "GET", "/", 404, "Box not found"},
	}

	for _, test := range tests {
		req := createTestRequestForServer(test.method, test.path, test.hostname, server)

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
	err := server.createUser(t.Context(), publicKey, email)
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

	// Test proxy command by calling it directly
	cc := &exemenu.CommandContext{
		PublicKey: publicKey,
		Alloc:     &exemenu.AllocInfo{ID: allocID},
		Args:      []string{boxName},
		Output:    &mockSession{},
		User:      &exemenu.UserInfo{ID: userID},
	}

	// Test showing current proxy configuration (no flags)
	cc.FlagSet = proxyCommandFlags() // Create empty flagset
	err = sshServer.handleProxyCommand(t.Context(), cc)
	if err != nil {
		t.Errorf("Failed to show proxy configuration: %v", err)
	}
	output := cc.Output.(*mockSession).output.String()
	if !strings.Contains(output, "Port: 80") || !strings.Contains(output, "Share: private") {
		t.Errorf("Expected default proxy configuration info, got: %s", output)
	}

	// Test setting proxy to public
	cc.Output = &mockSession{}
	cc.FlagSet = proxyCommandFlags()
	cc.FlagSet.Set("port", "8080")
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleProxyCommand(t.Context(), cc)
	if err != nil {
		t.Errorf("Failed to set proxy configuration: %v", err)
	}
	output = cc.Output.(*mockSession).output.String()
	if !strings.Contains(output, "updated successfully") {
		t.Errorf("Expected success message, got: %s", output)
	}

	// Verify the proxy configuration was updated
	box, err := withRxRes(server, t.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:   boxName,
			UserID: userID,
		})
	})
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
	cc.FlagSet = proxyCommandFlags()
	cc.FlagSet.Set("port", "80")
	cc.FlagSet.Set("private", "true")
	err = sshServer.handleProxyCommand(t.Context(), cc)
	if err == nil {
		t.Fatal("Expected error for nonexistent box")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Unexpected error for nonexistent box: %v", err)
	}
}

// TestSimplifiedRoutingEndToEnd tests the complete simplified routing flow
func TestSimplifiedRoutingEndToEnd(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)
	mainDomain := server.getMainDomain()

	// Create test user and alloc
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"
	boxName := "test-server"

	// Create user with alloc
	err := server.createUser(t.Context(), publicKey, email)
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
	box, err := withRxRes(server, t.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:   boxName,
			UserID: userID,
		})
	})
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
	req := createTestRequestForServer("GET", "/", fmt.Sprintf("%s.%s", boxName, mainDomain), server)
	w := httptest.NewRecorder()
	server.handleProxyRequest(w, req)

	// Should redirect for private access without auth
	if w.Code != 307 {
		t.Errorf("Expected status 307 for private route without auth, got %d", w.Code)
	}

	// Test 3: Use proxy command to set public access on port 8080
	sshServer := &SSHServer{server: server}
	cc := &exemenu.CommandContext{
		PublicKey: publicKey,
		Alloc:     &exemenu.AllocInfo{ID: allocID},
		Args:      []string{boxName},
		Output:    &mockSession{},
		User:      &exemenu.UserInfo{ID: userID},
	}

	// Set to public port 8080
	cc.FlagSet = proxyCommandFlags()
	cc.FlagSet.Set("port", "8080")
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleProxyCommand(t.Context(), cc)
	if err != nil {
		t.Fatalf("Failed to set route: %v", err)
	}

	// Test 4: Verify the route was updated
	box, err = withRxRes(server, t.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:   boxName,
			UserID: userID,
		})
	})
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

	// Test 5: Test proxy request with public route (should fail since no container is running)
	req2 := createTestRequestForServer("GET", "/api/test", fmt.Sprintf("%s.%s", boxName, mainDomain), server)
	w2 := httptest.NewRecorder()
	server.handleProxyRequest(w2, req2)

	// Should fail since no actual container is running to proxy to
	if w2.Code != 502 {
		t.Errorf("Expected status 502 for public route with no running container, got %d", w2.Code)
	}

	// Check that it returns appropriate error message
	body := w2.Body.String()
	if !strings.Contains(body, "Failed to proxy request to container") {
		t.Errorf("Expected response to contain error message, got: %s", body)
	}

	// Test 6: Test invalid port (should fail)
	cc.Output = &mockSession{}
	cc.FlagSet = proxyCommandFlags()
	cc.FlagSet.Set("port", "99999")
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleProxyCommand(t.Context(), cc)
	if err == nil {
		t.Fatal("Expected error for invalid port 99999")
	}
	if !strings.Contains(err.Error(), "must be a valid port number") {
		t.Errorf("Unexpected error for invalid port: %v", err)
	}

	// Test 7: Test missing --private/--public when --port is specified (should fail)
	cc.Output = &mockSession{}
	cc.FlagSet = proxyCommandFlags()
	cc.FlagSet.Set("port", "3000")
	err = sshServer.handleProxyCommand(t.Context(), cc)
	if err == nil {
		t.Fatal("Expected error when --port is specified without --private or --public")
	}
	if !strings.Contains(err.Error(), "either --private or --public is required") {
		t.Errorf("Unexpected error when --port is specified without share: %v", err)
	}

	// Test 8: Test missing --port when --public is specified (should fail)
	cc.Output = &mockSession{}
	cc.FlagSet = proxyCommandFlags()
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleProxyCommand(t.Context(), cc)
	if err == nil {
		t.Fatal("Expected error when --public is specified without --port")
	}
	if !strings.Contains(err.Error(), "--port is required") {
		t.Errorf("Unexpected error when --public is specified without --port: %v", err)
	}

	// Test 9: Test both --private and --public specified (should fail)
	cc.Output = &mockSession{}
	cc.FlagSet = proxyCommandFlags()
	cc.FlagSet.Set("port", "80")
	cc.FlagSet.Set("private", "true")
	cc.FlagSet.Set("public", "true")
	err = sshServer.handleProxyCommand(t.Context(), cc)
	if err == nil {
		t.Fatal("Expected error when both --private and --public are specified")
	}
	if !strings.Contains(err.Error(), "cannot specify both") {
		t.Errorf("Unexpected error when both --private and --public are specified: %v", err)
	}
}
