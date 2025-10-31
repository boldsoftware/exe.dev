package execore

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"exe.dev/exedb"
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
	server := newTestServer(t)

	// Set up test user
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"

	// Create user
	_, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
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

	// Create test box with userID and ctrhost
	server.createTestBox(t, userID, "test-ctrhost", "test-box", "container123", "ubuntu")

	// Retrieve the box and check its route
	box, err := withRxRes(server, t.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            "test-box",
			CreatedByUserID: userID,
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
	server := newTestServer(t)
	mainDomain := server.getMainDomain()

	// Create test user
	publicKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDtest..."
	email := "test@example.com"

	// Create user
	_, err := server.createUser(t.Context(), publicKey, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Get userID for box creation
	var userID string
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT user_id FROM users WHERE email = ?`, email).Scan(&userID)
	})
	if err != nil {
		t.Fatalf("Failed to get user ID: %v", err)
	}

	// Create a test box with userID and ctrhost
	server.createTestBox(t, userID, "test-ctrhost", "web-server", "container123", "nginx")

	// Get the box and set it to public
	box, err := withRxRes(server, t.Context(), func(ctx context.Context, queries *exedb.Queries) (exedb.Box, error) {
		return queries.BoxWithOwnerNamed(ctx, exedb.BoxWithOwnerNamedParams{
			Name:            "web-server",
			CreatedByUserID: userID,
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
		_, err := tx.Exec(`UPDATE boxes SET routes = ? WHERE name = ? AND created_by_user_id = ?`,
			*box.Routes, "web-server", userID)
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
		{fmt.Sprintf("web-server.%s", mainDomain), "GET", "/", 502, "Bad Gateway"},           // No container running, should fail
		{fmt.Sprintf("web-server.%s", mainDomain), "GET", "/api/status", 502, "Bad Gateway"}, // No container running, should fail
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
