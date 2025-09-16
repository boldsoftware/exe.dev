package exe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"exe.dev/accounting"
	"exe.dev/exedb"
	"exe.dev/llmgateway"
	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

// These tests cover interactions between Server and llmgateway.llmGateway.
// Real implementations are used for all dependencies except Accountant,
// which does not have a concrete implementation yet.

func TestLLMGatewayFullIntegrationAuthFlow(t *testing.T) {
	// Create exe.Server for full integration
	server := NewTestServer(t)

	// Create a test user and alloc
	publicKeyStr := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDummy-test-key test@example.com"
	if err := server.createUser(context.Background(), publicKeyStr, "test@example.com"); err != nil {
		t.Fatalf("Failed to create user with alloc: %v", err)
	}

	// Get the user to find their alloc
	var userID, allocID string
	err := server.db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT u.user_id, a.alloc_id FROM users u JOIN allocs a ON u.user_id = a.user_id WHERE u.email = ?`, "test@example.com").Scan(&userID, &allocID)
	})
	if err != nil {
		t.Fatalf("Failed to get user and alloc: %v", err)
	}

	// Create a box with SSH keys using the server's helper
	boxName := "llmgateway-test-box"
	containerID := "container-123"
	image := "ubuntu:latest"
	server.createTestBox(t, userID, allocID, boxName, containerID, image)

	// Get the box to extract its SSH server identity key for token creation
	key, err := withRxRes(server, t.Context(), func(ctx context.Context, queries *exedb.Queries) ([]byte, error) {
		return queries.SSHKeyForBoxNamed(ctx, boxName)
	})
	if err != nil {
		t.Fatalf("Failed to get user's SSH key: %v", err)
	}

	// Parse the SSH server identity key to create bearer token
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatalf("Failed to parse SSH server identity key: %v", err)
	}

	// Generate bearer token using box's SSH server identity key
	startTime := time.Now()
	ttlSec := 10 * 60 * 60
	token := llmgateway.NewBearerToken(boxName, startTime, ttlSec)

	// Encode token for Authorization header
	tokenEncoded, err := token.Encode(signer)
	if err != nil {
		t.Fatalf("Failed to encode bearer token: %v", err)
	}

	// Create mock accountant with positive balance
	mockAcct := &mockAccountant{
		balance: 10.0, // Sufficient balance
	}

	// Use the exe.Server as boxKeyAuthority (it implements the interface)
	gateway := llmgateway.NewGateway(mockAcct, server, "fake-anthropic-api-key")

	// Create test HTTP server with the gateway
	testServer := httptest.NewServer(gateway)
	defer testServer.Close()

	// Test successful authentication flow
	t.Run("successful authentication", func(t *testing.T) {
		// Create HTTP request with bearer token
		req, err := http.NewRequest("POST", testServer.URL+"/_/gateway/nonexistent-endpoint", strings.NewReader(`{"message": "test"}`))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tokenEncoded)
		req.Header.Set("Content-Type", "application/json")

		// Make the request
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		// We expect Not Found (rather than a 5xx, 3xx or 2xx)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected status Not Found, got %d", resp.StatusCode)
		}

		// This means authentication succeeded and we reached the proxy logic
		t.Log("Authentication successful - reached proxy implementation")
	})

	// Test authentication failure scenarios
	t.Run("missing authorization header", func(t *testing.T) {
		req, err := http.NewRequest("POST", testServer.URL+"/_/gateway/nonexistent-endpoint", strings.NewReader(`{"message": "test"}`))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		// No Authorization header

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

	t.Run("invalid bearer token", func(t *testing.T) {
		req, err := http.NewRequest("POST", testServer.URL+"/_/gateway/nonexistent-endpoint", strings.NewReader(`{"message": "test"}`))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer invalid-token")

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

	t.Run("insufficient balance", func(t *testing.T) {
		// Create gateway with negative balance
		negativeBalanceAcct := &mockAccountant{
			balance: -1.0, // Insufficient balance
			billingAccountForBox: map[string]string{
				boxName: boxName + "-billing-account-id",
			},
		}
		negativeGateway := llmgateway.NewGateway(negativeBalanceAcct, server, "fake-anthropic-api-key")
		negativeServer := httptest.NewServer(negativeGateway)
		defer negativeServer.Close()

		req, err := http.NewRequest("POST", negativeServer.URL+"/_/gateway/anthropic", strings.NewReader(`{"message": "test"}`))
		if err != nil {
			t.Fatalf("Failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tokenEncoded)

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusPaymentRequired {
			t.Errorf("Expected status 402 (Payment Required), got %d\n%s", resp.StatusCode, string(body))
		}
	})
}

// mockAccountant implements Accountant for testing
type mockAccountant struct {
	balance              float64
	balanceErr           error
	usageDebits          []accounting.UsageDebit
	usageCredits         []accounting.UsageCredit
	billingAccountForBox map[string]string
}

var _ accounting.Accountant = &mockAccountant{}

// BillingAccountForBox implements accounting.Accountant.
func (m *mockAccountant) BillingAccountForBox(ctx context.Context, boxName string) (string, error) {
	if m.billingAccountForBox == nil {
		return "", fmt.Errorf("no billing accounts defined in this mockAccountant")
	}
	return m.billingAccountForBox[boxName], nil
}

func (m *mockAccountant) GetBalance(ctx context.Context, billingAccountID string) (float64, error) {
	if m.balanceErr != nil {
		return 0, m.balanceErr
	}
	return m.balance, nil
}

func (m *mockAccountant) DebitUsage(ctx context.Context, debit accounting.UsageDebit) error {
	m.usageDebits = append(m.usageDebits, debit)
	return nil
}

func (m *mockAccountant) CreditUsage(ctx context.Context, credit accounting.UsageCredit) error {
	m.usageCredits = append(m.usageCredits, credit)
	return nil
}

func (m *mockAccountant) HasNewUserCredits(ctx context.Context, billingAccountID string) (bool, any) {
	return false, nil
}

func (m *mockAccountant) ApplyNewUserCredits(ctx context.Context, billingAccountID string) any {
	return nil
}
