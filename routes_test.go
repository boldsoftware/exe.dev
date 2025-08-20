package exe

import (
	"database/sql"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRouteStructs(t *testing.T) {
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
	server := &Server{}

	tests := []struct {
		hostname        string
		expectedMachine string
		expectedTeam    string
		shouldError     bool
	}{
		{"test-machine.my-team.exe.dev", "test-machine", "my-team", false},
		{"web.production.localhost", "web", "production", false},
		{"api.staging.exe.dev", "api", "staging", false},
		{"invalid.domain.com", "", "", true},
		{"no-team.exe.dev", "", "", true},
		{"too.many.parts.team.exe.dev", "", "", true},
	}

	for _, test := range tests {
		machine, team, err := server.parseProxyHostname(test.hostname)

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
		if team != test.expectedTeam {
			t.Errorf("Expected team '%s', got '%s'", test.expectedTeam, team)
		}
	}
}

func TestRouteMatching(t *testing.T) {
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
	// Create temporary database
	dbFile := "/tmp/test_routes.db"
	defer os.Remove(dbFile)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE machines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			image TEXT,
			container_id TEXT,
			created_by_fingerprint TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_started_at DATETIME,
			docker_host TEXT,
			routes TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create machines table: %v", err)
	}

	server := &Server{db: db}

	// Test creating a machine
	err = server.createMachine("test-fingerprint", "test-team", "test-machine", "container123", "ubuntu")
	if err != nil {
		t.Errorf("Failed to create machine: %v", err)
	}

	// Retrieve the machine and check its routes
	machine, err := server.getMachineByName("test-team", "test-machine")
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
	// Create temporary database
	dbFile := "/tmp/test_proxy.db"
	defer os.Remove(dbFile)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE machines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			image TEXT,
			container_id TEXT,
			created_by_fingerprint TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_started_at DATETIME,
			docker_host TEXT,
			routes TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create machines table: %v", err)
	}

	server := &Server{db: db, testMode: true}

	// Create a test machine with custom routes
	err = server.createMachine("test-fingerprint", "test-team", "web-server", "container123", "nginx")
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Get the machine and add a public API route
	machine, err := server.getMachineByName("test-team", "web-server")
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
	_, err = db.Exec(`UPDATE machines SET routes = ? WHERE name = ? AND team_name = ?`,
		*machine.Routes, "web-server", "test-team")
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
		{"web-server.test-team.exe.dev", "GET", "/api/public/status", 200, "public-api"},
		{"web-server.test-team.exe.dev", "GET", "/private/admin", 307, "auth?redirect="}, // Should redirect to auth for private route
		{"nonexistent.test-team.exe.dev", "GET", "/", 404, "Machine not found"},
		{"web-server.nonexistent-team.exe.dev", "GET", "/", 404, "Machine not found"},
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
	// Create temporary database
	dbFile := "/tmp/test_route_commands.db"
	defer os.Remove(dbFile)

	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}
	defer db.Close()

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE teams (
			name TEXT PRIMARY KEY,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE users (
			public_key_fingerprint TEXT PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE team_members (
			user_fingerprint TEXT NOT NULL,
			team_name TEXT NOT NULL,
			is_admin BOOLEAN NOT NULL DEFAULT FALSE,
			joined_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_fingerprint, team_name)
		);
		CREATE TABLE machines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			team_name TEXT NOT NULL,
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			image TEXT,
			container_id TEXT,
			created_by_fingerprint TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_started_at DATETIME,
			docker_host TEXT,
			routes TEXT
		);
	`)
	if err != nil {
		t.Fatalf("Failed to create tables: %v", err)
	}

	// Create a test server
	server := &Server{
		db:       db,
		testMode: true,
		devMode:  "local",
	}

	// Create test user and team
	fingerprint := "test-fingerprint"
	email := "test@example.com"
	teamName := "test-team"
	machineName := "web-server"

	_, err = db.Exec(`INSERT INTO users (public_key_fingerprint, email) VALUES (?, ?)`, fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	_, err = db.Exec(`INSERT INTO teams (name) VALUES (?)`, teamName)
	if err != nil {
		t.Fatalf("Failed to create team: %v", err)
	}

	_, err = db.Exec(`INSERT INTO team_members (user_fingerprint, team_name, is_admin) VALUES (?, ?, TRUE)`, fingerprint, teamName)
	if err != nil {
		t.Fatalf("Failed to add user to team: %v", err)
	}

	// Create a test machine
	err = server.createMachine(fingerprint, teamName, machineName, "container123", "nginx")
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
			server.handleRouteCommand(sess, fingerprint, teamName, test.args)
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
