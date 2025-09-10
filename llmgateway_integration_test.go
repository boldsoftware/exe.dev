package exe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	if err := server.createUserWithAlloc(context.Background(), publicKeyStr, "test@example.com"); err != nil {
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
	box, err := server.getBoxByName(context.Background(), boxName)
	if err != nil {
		t.Fatalf("Failed to get box: %v", err)
	}

	// Parse the SSH server identity key to create bearer token
	signer, err := ssh.ParsePrivateKey([]byte(*box.SSHServerIdentityKey))
	if err != nil {
		t.Fatalf("Failed to parse SSH server identity key: %v", err)
	}

	// Generate bearer token using box's SSH server identity key
	startTime := time.Now()
	duration := 10 * time.Minute
	token, err := llmgateway.NewBearerToken(boxName, startTime, duration, signer)
	if err != nil {
		t.Fatalf("Failed to create bearer token: %v", err)
	}

	// Encode token for Authorization header
	tokenEncoded, err := token.Encode()
	if err != nil {
		t.Fatalf("Failed to encode bearer token: %v", err)
	}

	// Create mock accountant with positive balance
	mockAcct := &mockAccountant{
		balance: 10.0, // Sufficient balance
	}

	// Use the exe.Server as boxKeyAuthority (it implements the interface)
	gateway := llmgateway.NewGateway(mockAcct, server)

	// Create test HTTP server with the gateway
	testServer := httptest.NewServer(gateway)
	defer testServer.Close()

	// Test successful authentication flow
	t.Run("successful authentication", func(t *testing.T) {
		// Create HTTP request with bearer token
		req, err := http.NewRequest("POST", testServer.URL+"/claude", strings.NewReader(`{"message": "test"}`))
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

		// Should get "not implemented" response (501) since proxy isn't implemented yet
		if resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("Expected status 501 (Not Implemented), got %d", resp.StatusCode)
		}

		// This means authentication succeeded and we reached the proxy logic
		t.Log("Authentication successful - reached proxy implementation")
	})

	// Test authentication failure scenarios
	t.Run("missing authorization header", func(t *testing.T) {
		req, err := http.NewRequest("POST", testServer.URL+"/claude", strings.NewReader(`{"message": "test"}`))
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
		req, err := http.NewRequest("POST", testServer.URL+"/claude", strings.NewReader(`{"message": "test"}`))
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
		}
		negativeGateway := llmgateway.NewGateway(negativeBalanceAcct, server)
		negativeServer := httptest.NewServer(negativeGateway)
		defer negativeServer.Close()

		req, err := http.NewRequest("POST", negativeServer.URL+"/claude", strings.NewReader(`{"message": "test"}`))
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

		if resp.StatusCode != http.StatusPaymentRequired {
			t.Errorf("Expected status 402 (Payment Required), got %d", resp.StatusCode)
		}
	})
}

// mockAccountant implements Accountant for testing
type mockAccountant struct {
	balance      float64
	balanceErr   error
	usageDebits  []llmgateway.UsageDebit
	usageCredits []llmgateway.UsageCredit
}

func (m *mockAccountant) GetUserBalance(ctx context.Context, billingAccountID string) (float64, error) {
	if m.balanceErr != nil {
		return 0, m.balanceErr
	}
	return m.balance, nil
}

func (m *mockAccountant) DebitUsage(ctx context.Context, debit llmgateway.UsageDebit) error {
	m.usageDebits = append(m.usageDebits, debit)
	return nil
}

func (m *mockAccountant) CreditUsage(ctx context.Context, credit llmgateway.UsageCredit) error {
	m.usageCredits = append(m.usageCredits, credit)
	return nil
}

func (m *mockAccountant) HasNewUserCredits(ctx context.Context, billingAccountID string) (bool, any) {
	return false, nil
}

func (m *mockAccountant) ApplyNewUserCredits(ctx context.Context, billingAccountID string) any {
	return nil
}
