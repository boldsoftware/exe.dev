package execore

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/llmgateway"
	"exe.dev/tslog"
)

// These tests cover interactions between Server and llmgateway.llmGateway.
// Authentication is now done via X-Exedev-Box header from Tailscale IPs or dev mode.

func TestLLMGatewayIntegrationAuthFlow(t *testing.T) {
	// Create exe.Server for full integration
	server := newTestServer(t)

	// Create gateway in dev mode (allows X-Exedev-Box header from any IP)
	gateway := llmgateway.NewGateway(tslog.Slogger(t), server.db, llmgateway.APIKeys{Anthropic: "fake-anthropic-api-key"}, true)

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
	// Create exe.Server for full integration
	server := newTestServer(t)

	// Create gateway NOT in dev mode (requires Tailscale IP)
	gateway := llmgateway.NewGateway(tslog.Slogger(t), server.db, llmgateway.APIKeys{Anthropic: "fake-anthropic-api-key"}, false)

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
