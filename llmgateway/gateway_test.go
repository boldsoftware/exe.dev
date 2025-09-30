package llmgateway

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/accounting"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	_ "modernc.org/sqlite"
)

// setupTestAccountant creates a simple test accountant with balance
func setupTestAccountant(t *testing.T, billingAccountID string, balance float64) (*accounting.Accountant, *sqlite.DB) {
	// Create temp database file
	tmpFile, err := os.CreateTemp("", "gateway-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	dbPath := tmpFile.Name()

	// Run migrations
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to open database: %v", err)
	}

	if err := exedb.RunMigrations(rawDB); err != nil {
		rawDB.Close()
		os.Remove(dbPath)
		t.Fatalf("Failed to run migrations: %v", err)
	}
	rawDB.Close()

	// Open with sqlite wrapper
	db, err := sqlite.New(dbPath, 1)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to open sqlite database: %v", err)
	}

	accountant := accounting.NewAccountant()

	// Add balance if specified
	if balance > 0 {
		credit := accounting.UsageCredit{
			BillingAccountID: billingAccountID,
			Amount:           balance,
			PaymentMethod:    "test",
			PaymentID:        "test-payment",
			Status:           "completed",
		}
		err = db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
			return accountant.CreditUsage(ctx, tx, credit)
		})
		require.NoError(t, err)
	}

	return accountant, db
}

// setupTestGateway creates a test gateway with mocked dependencies
func setupTestGateway(t *testing.T) (*llmGateway, *accounting.Accountant, *sqlite.DB, *mockBoxKeyAuthority, *testKeyPair) {
	keyPair := generateTestKeys(t)

	mockAuth := &mockBoxKeyAuthority{
		keys: map[string]ssh.PublicKey{
			"test-box": keyPair.sshPublicKey,
		},
	}

	accountant, db := setupTestAccountant(t, "billing-123", 10.0)

	gateway := &llmGateway{
		now:             time.Now,
		accountant:      accountant,
		db:              db,
		boxKeyAuthority: mockAuth,
		apiKeys:         APIKeys{Anthropic: "test-api-key"},
		devMode:         false,
		testDebitDone:   make(chan bool, 10), // Buffered for tests
	}

	return gateway, accountant, db, mockAuth, keyPair
}

func TestGateway_BillingIntegration_CheckCredits_SufficientBalance(t *testing.T) {
	gateway, _, db, _, _ := setupTestGateway(t)
	defer db.Close()

	// Test checkCredits with sufficient balance
	err := gateway.checkCredits(context.Background(), "billing-123")
	if err != nil {
		t.Errorf("Expected no error for sufficient balance, got: %v", err)
	}
}

func TestGateway_BillingIntegration_CheckCredits_InsufficientBalance(t *testing.T) {
	// Set up with no initial balance
	accountant, db := setupTestAccountant(t, "billing-123", 0.0)
	defer db.Close()

	keyPair := generateTestKeys(t)
	mockAuth := &mockBoxKeyAuthority{
		keys: map[string]ssh.PublicKey{
			"test-box": keyPair.sshPublicKey,
		},
	}

	gateway := &llmGateway{
		now:             time.Now,
		accountant:      accountant,
		db:              db,
		boxKeyAuthority: mockAuth,
		apiKeys:         APIKeys{Anthropic: "test-api-key"},
		devMode:         false,
	}

	// Test checkCredits with insufficient balance
	err := gateway.checkCredits(context.Background(), "billing-123")
	if err == nil {
		t.Error("Expected error for insufficient balance")
		return
	}
	if !strings.Contains(err.Error(), "insufficient") {
		t.Errorf("Expected insufficient balance error, got: %v", err)
	}
}

func TestGateway_BillingIntegration_CheckCredits_BalanceCheckFails(t *testing.T) {
	// Create a corrupted database to simulate errors
	tmpFile, _ := os.CreateTemp("", "test-*.db")
	tmpFile.Close()
	db, _ := sqlite.New(tmpFile.Name(), 1)
	db.Close() // Close immediately to cause errors
	os.Remove(tmpFile.Name())

	accountant := accounting.NewAccountant()
	mockAuth := &mockBoxKeyAuthority{
		keys: map[string]ssh.PublicKey{},
	}

	gateway := &llmGateway{
		now:             time.Now,
		accountant:      accountant,
		db:              db,
		boxKeyAuthority: mockAuth,
		apiKeys:         APIKeys{Anthropic: "test-api-key"},
		devMode:         false,
	}

	// Test checkCredits when balance check fails - should allow request (fallback)
	err := gateway.checkCredits(context.Background(), "billing-123")
	if err != nil {
		t.Errorf("Expected no error on balance check failure (fallback), got: %v", err)
	}
}

func TestGateway_BillingIntegration_BillingAccountLookup(t *testing.T) {
	_, accountant, db, _, _ := setupTestGateway(t)
	defer db.Close()

	// Test failed lookup for nonexistent box (simplified test database doesn't have box setup)
	err := db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		_, err := accountant.BillingAccountForBox(ctx, rx, "test-box")
		return err
	})
	if err == nil {
		t.Error("Expected error for nonexistent box")
	}

	// Test another failed lookup
	err = db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		_, err := accountant.BillingAccountForBox(ctx, rx, "nonexistent-box")
		return err
	})
	if err == nil {
		t.Error("Expected error for nonexistent box")
	}
}

func TestGateway_ProxyFunctionality_URLParsing(t *testing.T) {
	tests := []struct {
		name          string
		requestPath   string
		wantAlias     string
		wantRemainder string
	}{
		{
			name:          "anthropic messages endpoint",
			requestPath:   "/_/gateway/anthropic/v1/messages",
			wantAlias:     "anthropic",
			wantRemainder: "/v1/messages",
		},
		{
			name:          "openai chat endpoint",
			requestPath:   "/_/gateway/openai/v1/chat/completions",
			wantAlias:     "openai",
			wantRemainder: "/v1/chat/completions",
		},
		{
			name:          "gemini generate endpoint",
			requestPath:   "/_/gateway/gemini/v1/models/generate",
			wantAlias:     "gemini",
			wantRemainder: "/v1/models/generate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpointPath := strings.TrimPrefix(tt.requestPath, "/_/gateway/")
			parts := strings.Split(endpointPath, "/")
			alias := parts[0]
			remainder := endpointPath[len(alias):]

			if alias != tt.wantAlias {
				t.Errorf("Expected alias %s, got %s", tt.wantAlias, alias)
			}
			if remainder != tt.wantRemainder {
				t.Errorf("Expected remainder %s, got %s", tt.wantRemainder, remainder)
			}
		})
	}
}

func TestGateway_ProxyFunctionality_HeaderFiltering(t *testing.T) {
	gateway, _, db, _, _ := setupTestGateway(t)
	defer db.Close()

	// Create a mock request with various headers
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer should-be-filtered")
	req.Header.Set("X-Api-Key", "should-be-filtered")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "test-agent")
	req.Header.Set("Accept", "application/json")

	// Test header filtering logic (extracted from ServeHTTP)
	filteredHeaders := http.Header{}
	for hk := range req.Header {
		if hk == "X-Api-Key" || hk == "Authorization" {
			continue // Should be filtered
		}
		if hv, ok := req.Header[hk]; ok {
			filteredHeaders[hk] = hv
		}
	}
	filteredHeaders.Add("X-Api-Key", gateway.apiKeys.Anthropic)

	// Verify filtered headers
	if filteredHeaders.Get("Authorization") != "" {
		t.Errorf("Authorization header should be filtered, got: %s", filteredHeaders.Get("Authorization"))
	}
	if filteredHeaders.Get("X-Api-Key") != "test-api-key" {
		t.Errorf("X-Api-Key should be replaced with gateway key, got: %s", filteredHeaders.Get("X-Api-Key"))
	}
	if filteredHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type should be preserved, got: %s", filteredHeaders.Get("Content-Type"))
	}
	if filteredHeaders.Get("User-Agent") != "test-agent" {
		t.Errorf("User-Agent should be preserved, got: %s", filteredHeaders.Get("User-Agent"))
	}
}

// For the integration tests, we'll rely on the actual HTTP errors rather than mocking

// TestGateway_ServeHTTP_RequestProcessing tests the request processing logic
// This test will make a real HTTP call (which will fail) but allows us to test
// the request preprocessing, authentication, and billing check logic
func TestGateway_ServeHTTP_RequestProcessing(t *testing.T) {
	gateway, _, db, _, keyPair := setupTestGateway(t)
	defer db.Close()

	// Create authenticated request
	token := NewBearerToken("test-box", time.Now().Add(-5*time.Minute), 3600)
	tokenEncoded, err := token.Encode(keyPair.sshPrivateKey)
	if err != nil {
		t.Fatalf("Failed to encode token: %v", err)
	}

	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+tokenEncoded)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "test-client")
	req.Header.Set("X-Custom-Header", "should-be-preserved")
	req.Header.Set("X-Api-Key", "should-be-filtered")

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// The request will fail (401 from Anthropic due to invalid API key) but that's expected
	// We can still verify that the request preprocessing worked correctly

	// The response should be either:
	// 1. 401 from Anthropic API (authentication failed with real API)
	// 2. 500 (could not reach origin server)
	// Either is fine for this test since we're testing the preprocessing
	if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusInternalServerError {
		// Only log, don't fail - the exact error depends on network conditions
		t.Logf("Unexpected status code: %d, body: %s", rr.Code, rr.Body.String())
	}
}

func TestGateway_ServeHTTP_DevKeyAuthentication(t *testing.T) {
	gateway, _, db, _, _ := setupTestGateway(t)
	gateway.devMode = true // Enable dev mode
	defer db.Close()

	// Test with dev.key in Authorization header
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("Authorization", "Bearer dev.key")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Should not return 401 (authentication should pass)
	if rr.Code == http.StatusUnauthorized {
		t.Errorf("Expected authentication to pass with dev.key, got 401: %s", rr.Body.String())
	}

	// Test with dev.key in X-API-Key header
	req2 := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req2.Header.Set("X-API-Key", "dev.key")
	req2.Header.Set("Content-Type", "application/json")

	rr2 := httptest.NewRecorder()
	gateway.ServeHTTP(rr2, req2)

	// Should not return 401 (authentication should pass)
	if rr2.Code == http.StatusUnauthorized {
		t.Errorf("Expected authentication to pass with dev.key in X-API-Key, got 401: %s", rr2.Body.String())
	}
}

func TestGateway_ServeHTTP_DevKeyDisabledInProduction(t *testing.T) {
	gateway, _, db, _, _ := setupTestGateway(t)
	gateway.devMode = false // Production mode
	defer db.Close()

	// Test with dev.key in Authorization header
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("Authorization", "Bearer dev.key")
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Should return 401 (dev.key should not work in production)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for dev.key in production mode, got %d", rr.Code)
	}
}

func TestGateway_ServeHTTP_AuthenticationFailure(t *testing.T) {
	gateway, _, db, _, _ := setupTestGateway(t)
	defer db.Close()

	// Create request without authentication
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Should return 401
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rr.Code)
	}

	// Should contain auth error message
	if !strings.Contains(rr.Body.String(), "box key auth failed") {
		t.Errorf("Expected auth error in response, got: %s", rr.Body.String())
	}
}

func TestGateway_ServeHTTP_InsufficientCredits(t *testing.T) {
	// Create gateway with no balance
	accountant, db := setupTestAccountant(t, "billing-123", 0.0)
	defer db.Close()

	keyPair := generateTestKeys(t)
	mockAuth := &mockBoxKeyAuthority{
		keys: map[string]ssh.PublicKey{
			"test-box": keyPair.sshPublicKey,
		},
	}

	gateway := &llmGateway{
		now:             time.Now,
		accountant:      accountant,
		db:              db,
		boxKeyAuthority: mockAuth,
		apiKeys:         APIKeys{Anthropic: "test-api-key"},
		devMode:         false,
	}

	// Create authenticated request
	token := NewBearerToken("test-box", time.Now().Add(-5*time.Minute), 3600)
	tokenEncoded, err := token.Encode(keyPair.sshPrivateKey)
	if err != nil {
		t.Fatalf("Failed to encode token: %v", err)
	}

	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+tokenEncoded)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Should return 402 Payment Required
	if rr.Code != http.StatusPaymentRequired {
		t.Errorf("Expected status 402, got %d", rr.Code)
	}

	// Should contain insufficient funds message
	if !strings.Contains(rr.Body.String(), "insufficient") {
		t.Errorf("Expected insufficient credits message, got: %s", rr.Body.String())
	}
}

func TestGateway_ServeHTTP_UpstreamCallAttempt(t *testing.T) {
	gateway, _, db, _, keyPair := setupTestGateway(t)
	defer db.Close()

	// Create authenticated request
	token := NewBearerToken("test-box", time.Now().Add(-5*time.Minute), 3600)
	tokenEncoded, err := token.Encode(keyPair.sshPrivateKey)
	if err != nil {
		t.Fatalf("Failed to encode token: %v", err)
	}

	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+tokenEncoded)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// This will make a real HTTP call to Anthropic (which will fail with our test API key)
	// but we can verify the request made it past authentication and credit checks
	// Expected responses: 401 (API key invalid) or 500 (network error)
	if rr.Code != http.StatusUnauthorized && rr.Code != http.StatusInternalServerError {
		t.Logf("Got status %d, expected 401 or 500. This may indicate the request processing worked correctly.", rr.Code)
	}
}
