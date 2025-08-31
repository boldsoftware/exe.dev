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
	// Test default routes creation
	var machine Machine
	routes := machine.getDefaultRoutes()

	if len(routes) != 1 {
		t.Errorf("Expected 1 default route, got %d", len(routes))
	}

	defaultRoute := routes[0]
	if defaultRoute.Name != "default" {
		t.Errorf("Expected route name 'default', got '%s'", defaultRoute.Name)
	}
	if defaultRoute.Priority != 10 {
		t.Errorf("Expected priority 10, got %d", defaultRoute.Priority)
	}
	if len(defaultRoute.Methods) != 1 || defaultRoute.Methods[0] != "*" {
		t.Errorf("Expected methods ['*'], got %v", defaultRoute.Methods)
	}
	if defaultRoute.Paths.Prefix != "/" {
		t.Errorf("Expected path prefix '/', got '%s'", defaultRoute.Paths.Prefix)
	}
	if defaultRoute.Policy != "private" {
		t.Errorf("Expected policy 'private', got '%s'", defaultRoute.Policy)
	}
	expectedPorts := []int{80, 8000, 8080, 8888}
	if len(defaultRoute.Ports) != len(expectedPorts) {
		t.Errorf("Expected %d ports, got %d", len(expectedPorts), len(defaultRoute.Ports))
	} else {
		for i, port := range expectedPorts {
			if defaultRoute.Ports[i] != port {
				t.Errorf("Expected port %d, got %d", port, defaultRoute.Ports[i])
			}
		}
	}
}

func TestMachineRoutes(t *testing.T) {
	t.Parallel()
	machine := Machine{}

	// Test getting routes when none are set (should return defaults)
	routes, err := machine.GetRoutes()
	if err != nil {
		t.Errorf("Error getting default routes: %v", err)
	}
	if len(routes) != 1 {
		t.Errorf("Expected 1 default route, got %d", len(routes))
	}

	// Test setting custom routes
	customRoutes := MachineRoutes{
		{
			Name:     "api",
			Priority: 1,
			Methods:  []string{"GET", "POST"},
			Paths:    PathMatcher{Prefix: "/api/"},
			Policy:   "public",
			Ports:    []int{3000},
		},
		{
			Name:     "default",
			Priority: 10,
			Methods:  []string{"*"},
			Paths:    PathMatcher{Prefix: "/"},
			Policy:   "private",
			Ports:    []int{80},
		},
	}

	err = machine.SetRoutes(customRoutes)
	if err != nil {
		t.Errorf("Error setting routes: %v", err)
	}

	// Test getting the custom routes back
	retrievedRoutes, err := machine.GetRoutes()
	if err != nil {
		t.Errorf("Error getting custom routes: %v", err)
	}
	if len(retrievedRoutes) != 2 {
		t.Errorf("Expected 2 custom routes, got %d", len(retrievedRoutes))
	}

	// Check the API route
	apiRoute := retrievedRoutes[0]
	if apiRoute.Name != "api" {
		t.Errorf("Expected route name 'api', got '%s'", apiRoute.Name)
	}
	if apiRoute.Policy != "public" {
		t.Errorf("Expected policy 'public', got '%s'", apiRoute.Policy)
	}
}

func TestProxyHostnameParsing(t *testing.T) {
	t.Parallel()
	server := &Server{}

	tests := []struct {
		hostname        string
		expectedMachine string
		shouldError     bool
	}{
		{"test-machine.exe.dev", "test-machine", false},
		{"web.localhost", "web", false},
		{"api.exe.dev", "api", false},
		{"empty.exe.dev", "empty", false}, // Valid in new format
		{"invalid.domain.com", "", true},
		{"machine.with.dots.exe.dev", "", true}, // Too many subdomains
		{"just-domain.com", "", true},           // Not exe.dev or localhost
	}

	for _, test := range tests {
		machine, err := server.parseProxyHostname(test.hostname)

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

		if machine != test.expectedMachine {
			t.Errorf("Expected machine '%s', got '%s'", test.expectedMachine, machine)
		}
	}
}

func TestRouteMatching(t *testing.T) {
	t.Parallel()
	server := &Server{}

	routes := MachineRoutes{
		{
			Name:     "api",
			Priority: 1,
			Methods:  []string{"GET", "POST"},
			Paths:    PathMatcher{Prefix: "/api/"},
			Policy:   "public",
			Ports:    []int{3000},
		},
		{
			Name:     "admin",
			Priority: 5,
			Methods:  []string{"*"},
			Paths:    PathMatcher{Prefix: "/admin/"},
			Policy:   "private",
			Ports:    []int{80},
		},
		{
			Name:     "default",
			Priority: 10,
			Methods:  []string{"*"},
			Paths:    PathMatcher{Prefix: "/"},
			Policy:   "private",
			Ports:    []int{80},
		},
	}

	tests := []struct {
		method        string
		path          string
		expectedRoute string
	}{
		{"GET", "/api/users", "api"},
		{"POST", "/api/create", "api"},
		{"DELETE", "/api/delete", "default"}, // DELETE not in api methods
		{"GET", "/admin/dashboard", "admin"},
		{"POST", "/admin/settings", "admin"},
		{"GET", "/home", "default"},
		{"POST", "/upload", "default"},
	}

	for _, test := range tests {
		req := httptest.NewRequest(test.method, test.path, nil)
		matchingRoute := server.findMatchingRoute(routes, req)

		if matchingRoute == nil {
			t.Errorf("No route matched for %s %s", test.method, test.path)
			continue
		}

		if matchingRoute.Name != test.expectedRoute {
			t.Errorf("Expected route '%s' for %s %s, got '%s'", test.expectedRoute, test.method, test.path, matchingRoute.Name)
		}
	}
}

func TestMachineCreationWithRoutes(t *testing.T) {
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

	// Test creating a machine
	// Get userID for machine creation
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

	err = server.createMachine(t.Context(), userID, allocID, "test-machine", "container123", "ubuntu")
	if err != nil {
		t.Errorf("Failed to create machine: %v", err)
	}

	// Retrieve the machine and check its routes
	machine, err := server.getMachineByName(t.Context(), "test-machine")
	if err != nil {
		t.Errorf("Failed to get machine: %v", err)
	}

	if machine.Routes == nil {
		t.Error("Machine routes should not be nil")
	} else {
		routes, err := machine.GetRoutes()
		if err != nil {
			t.Errorf("Failed to parse machine routes: %v", err)
		}

		if len(routes) != 1 {
			t.Errorf("Expected 1 default route, got %d", len(routes))
		} else {
			defaultRoute := routes[0]
			if defaultRoute.Name != "default" {
				t.Errorf("Expected default route name 'default', got '%s'", defaultRoute.Name)
			}
			if defaultRoute.Policy != "private" {
				t.Errorf("Expected default policy 'private', got '%s'", defaultRoute.Policy)
			}
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

	// Get userID and allocID for machine creation
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

	// Create a test machine with custom routes
	err = server.createMachine(t.Context(), userID, allocID, "web-server", "container123", "nginx")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Get the machine and add a public API route
	machine, err := server.getMachineByName(t.Context(), "web-server")
	if err != nil {
		t.Fatalf("Failed to get machine: %v", err)
	}

	routes, err := machine.GetRoutes()
	if err != nil {
		t.Fatalf("Failed to get routes: %v", err)
	}

	// Add a public API route
	publicRoute := Route{
		Name:     "public-api",
		Priority: 1,
		Methods:  []string{"GET"},
		Paths:    PathMatcher{Prefix: "/api/public/"},
		Policy:   "public",
		Ports:    []int{80},
	}
	routes = append(routes, publicRoute)

	err = machine.SetRoutes(routes)
	if err != nil {
		t.Fatalf("Failed to set routes: %v", err)
	}

	// Update the machine in the database
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`UPDATE machines SET routes = ? WHERE name = ? AND alloc_id = ?`,
			*machine.Routes, "web-server", allocID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to update machine routes: %v", err)
	}

	tests := []struct {
		hostname       string
		method         string
		path           string
		expectedStatus int
		expectedBody   string
	}{
		{"web-server.exe.dev", "GET", "/api/public/status", 200, "public-api"},
		{"web-server.exe.dev", "GET", "/private/admin", 307, "auth?redirect="}, // Should redirect to auth for private route
		{"nonexistent.exe.dev", "GET", "/", 404, "Machine not found"},
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

func TestRouteSorting(t *testing.T) {
	t.Parallel()
	server := &Server{}

	// Create routes with different priorities
	routes := MachineRoutes{
		{
			Name:     "low-priority",
			Priority: 100,
			Methods:  []string{"*"},
			Paths:    PathMatcher{Prefix: "/"},
			Policy:   "private",
			Ports:    []int{80},
		},
		{
			Name:     "high-priority",
			Priority: 1,
			Methods:  []string{"*"},
			Paths:    PathMatcher{Prefix: "/api/"},
			Policy:   "public",
			Ports:    []int{80},
		},
		{
			Name:     "medium-priority",
			Priority: 50,
			Methods:  []string{"GET"},
			Paths:    PathMatcher{Prefix: "/static/"},
			Policy:   "public",
			Ports:    []int{80},
		},
	}

	// Test that higher priority route (lower number) matches first
	req := httptest.NewRequest("GET", "/api/test", nil)
	matchingRoute := server.findMatchingRoute(routes, req)

	if matchingRoute == nil {
		t.Error("No route matched")
	} else if matchingRoute.Name != "high-priority" {
		t.Errorf("Expected 'high-priority' route to match first, got '%s'", matchingRoute.Name)
	}

	// Test that routes are actually sorted by priority
	req2 := httptest.NewRequest("POST", "/api/create", nil)
	matchingRoute2 := server.findMatchingRoute(routes, req2)

	if matchingRoute2 == nil {
		t.Error("No route matched for POST request")
	} else if matchingRoute2.Name != "high-priority" {
		t.Errorf("Expected 'high-priority' route to match for POST, got '%s'", matchingRoute2.Name)
	}
}

// mockSession implements io.Writer for testing
type mockSession struct {
	output strings.Builder
}

func (m *mockSession) Write(p []byte) (n int, err error) {
	return m.output.Write(p)
}

func TestRouteCommandsEndToEnd(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create test user and alloc
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"
	machineName := "web-server"

	// Create user with alloc
	err := server.createUserWithAlloc(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user with alloc: %v", err)
	}

	// Get userID and allocID for machine creation
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

	// Create a test machine
	err = server.createMachine(t.Context(), userID, allocID, machineName, "container123", "nginx")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Test route commands as string arrays (simulating SSH command parsing)
	tests := []struct {
		name        string
		args        []string
		expected    []string // strings that should be in the output
		notExpected []string // strings that should NOT be in the output
	}{
		{
			name:     "list initial routes",
			args:     []string{machineName, "list"},
			expected: []string{"Routes for machine", "default", "priority 10", "private"},
		},
		{
			name:     "add public API route",
			args:     []string{machineName, "add", "--name=public-api", "--priority=1", "--methods=GET,POST", "--prefix=/api/public", "--policy=public", "--ports=3000"},
			expected: []string{"Route 'public-api' added successfully"},
		},
		{
			name:     "list routes after adding",
			args:     []string{machineName, "list"},
			expected: []string{"public-api", "priority 1", "public", "default", "priority 10", "private"},
		},
		{
			name:     "add route with defaults",
			args:     []string{machineName, "add"},
			expected: []string{"added successfully"},
		},
		{
			name:     "remove public-api route",
			args:     []string{machineName, "remove", "public-api"},
			expected: []string{"Route 'public-api' removed successfully"},
		},
		{
			name:        "list routes after removing",
			args:        []string{machineName, "list"},
			expected:    []string{"default"},
			notExpected: []string{"public-api"},
		},
		{
			name:     "error on nonexistent machine",
			args:     []string{"nonexistent", "list"},
			expected: []string{"not found"},
		},
		{
			name:     "error on duplicate route name",
			args:     []string{machineName, "add", "--name=default"},
			expected: []string{"already exists"},
		},
	}

	// Run tests
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := &mockSession{}
			server.handleRouteCommand(t.Context(), sess, publicKey, allocID, test.args)
			output := sess.output.String()

			// Check expected strings
			for _, expected := range test.expected {
				if !strings.Contains(output, expected) {
					t.Errorf("Expected output to contain '%s', got: %s", expected, output)
				}
			}

			// Check strings that should NOT be present
			for _, notExpected := range test.notExpected {
				if strings.Contains(output, notExpected) {
					t.Errorf("Expected output to NOT contain '%s', got: %s", notExpected, output)
				}
			}
		})
	}
}
