package execore

import (
	"context"
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
