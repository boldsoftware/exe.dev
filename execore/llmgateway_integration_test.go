package execore

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/llmgateway"
	"exe.dev/sqlite"
	"exe.dev/stage"
	"exe.dev/tslog"
)

// setupTestBox creates a user and box in the database for testing
func setupTestBox(t *testing.T, db *sqlite.DB, boxName string) {
	t.Helper()
	err := db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())

		userID := "test-user-" + boxName
		err := queries.InsertUser(ctx, exedb.InsertUserParams{
			UserID:                 userID,
			Email:                  "test@example.com",
			CreatedForLoginWithExe: false,
			Region:                 "pdx",
		})
		if err != nil {
			return fmt.Errorf("insert user: %w", err)
		}

		_, err = queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-ctrhost",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: userID,
			Routes:          nil,
			Region:          "pdx",
		})
		if err != nil {
			return fmt.Errorf("insert box: %w", err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("setupTestBox: %v", err)
	}
}

// These tests cover interactions between Server and llmgateway.llmGateway.
// Authentication is now done via X-Exedev-Box header from Tailscale IPs or dev mode.

func TestLLMGatewayIntegrationAuthFlow(t *testing.T) {
	t.Parallel()
	// Create exe.Server for full integration
	server := newTestServer(t)

	// Create the test box in the database (required since the gateway now fails closed)
	setupTestBox(t, server.db, "test-box")

	// Create gateway in dev mode (allows X-Exedev-Box header from any IP)
	gateway := llmgateway.NewGateway(tslog.Slogger(t), &llmgateway.DBGatewayData{DB: server.db}, llmgateway.APIKeys{Anthropic: "fake-anthropic-api-key"}, stage.Test())

	// Create test HTTP server with the gateway
	testServer := httptest.NewServer(gateway)
	defer testServer.Close()

	// Test successful authentication flow with X-Exedev-Box header in dev mode
	t.Run("successful authentication with X-Exedev-Box header", func(t *testing.T) {
		req, err := http.NewRequest("POST", testServer.URL+"/_/gateway/nonexistent-endpoint", strings.NewReader(`{"message": "test"}`))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Header.Set("X-Exedev-Box", "test-box")
		req.Header.Set("Content-Type", "application/json")

		// Make the request
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		// We expect Not Found (rather than a 5xx, 3xx or 2xx) - means auth succeeded
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected status Not Found, got %d", resp.StatusCode)
		}

		t.Log("Authentication successful - reached proxy implementation")
	})

	// Test authentication failure scenarios
	t.Run("missing X-Exedev-Box header", func(t *testing.T) {
		req, err := http.NewRequest("POST", testServer.URL+"/_/gateway/nonexistent-endpoint", strings.NewReader(`{"message": "test"}`))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		// No X-Exedev-Box header

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected status 401 (Unauthorized), got %d", resp.StatusCode)
		}
	})
}

func TestLLMGatewayNonDevModeRejectsNonTailscale(t *testing.T) {
	t.Parallel()
	// Create exe.Server for full integration
	server := newTestServer(t)

	// Create the test box in the database (required since the gateway now fails closed)
	setupTestBox(t, server.db, "test-box")

	// Create gateway using stage.Prod (danger! danger!)
	// This makes it require a Tailscale IP
	env := stage.Prod()
	gateway := llmgateway.NewGateway(tslog.Slogger(t), &llmgateway.DBGatewayData{DB: server.db}, llmgateway.APIKeys{Anthropic: "fake-anthropic-api-key"}, env)

	// Create test HTTP server with the gateway
	testServer := httptest.NewServer(gateway)
	defer testServer.Close()

	// Test that X-Exedev-Box header is rejected from non-Tailscale IP in non-dev mode
	t.Run("X-Exedev-Box rejected from non-tailscale IP", func(t *testing.T) {
		req, err := http.NewRequest("POST", testServer.URL+"/_/gateway/nonexistent-endpoint", strings.NewReader(`{"message": "test"}`))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Header.Set("X-Exedev-Box", "test-box")
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		// Should be rejected because not from Tailscale IP
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("Expected status 401 (Unauthorized), got %d", resp.StatusCode)
		}
	})
}
