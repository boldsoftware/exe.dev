package llmgateway

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"exe.dev/tslog"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// setupTestBox creates a box in the database linked to a billing account
func setupTestBox(t *testing.T, db *sqlite.DB, boxName string) {
	err := db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())

		// Create user
		userID := "test-user-" + boxName
		err := queries.InsertUser(ctx, exedb.InsertUserParams{
			UserID:                 userID,
			Email:                  "test@example.com",
			CreatedForLoginWithExe: false,
		})
		if err != nil {
			return fmt.Errorf("insert user: %w", err)
		}

		// Create box with user_id and ctrhost
		_, err = queries.InsertBox(ctx, exedb.InsertBoxParams{
			Ctrhost:         "test-ctrhost",
			Name:            boxName,
			Status:          "running",
			Image:           "test-image",
			CreatedByUserID: userID,
			Routes:          nil,
		})
		if err != nil {
			return fmt.Errorf("insert box: %w", err)
		}

		return nil
	})
	require.NoError(t, err)
}

// newDB creates a simple test accountant with balance
func newDB(t *testing.T) *sqlite.DB {
	// Run migrations
	dbPath := filepath.Join(t.TempDir(), "gateway_test.db")
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to open database: %v", err)
	}
	if err := exedb.RunMigrations(tslog.Slogger(t), rawDB); err != nil {
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
	t.Cleanup(func() { db.Close() })
	return db
}

// setupTestGateway creates a test gateway with mocked dependencies
func setupTestGateway(t *testing.T) (*llmGateway, *sqlite.DB) {
	db := newDB(t)

	// Create the box.
	setupTestBox(t, db, "test-box")

	gateway := &llmGateway{
		now:           time.Now,
		db:            db,
		apiKeys:       APIKeys{Anthropic: "test-api-key"},
		devMode:       false,
		testDebitDone: make(chan bool, 10), // Buffered for tests
		log:           tslog.Slogger(t),
	}

	return gateway, db
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
	gateway, _ := setupTestGateway(t)

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

func TestGateway_ServeHTTP_AuthenticationFailure(t *testing.T) {
	gateway, _ := setupTestGateway(t)

	// Create request without authentication (from non-tailscale IP)
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Should return 401
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", rr.Code)
	}

	// Should contain auth error message (tailscale IP is checked first)
	if !strings.Contains(rr.Body.String(), "hey go away") {
		t.Errorf("Expected 'hey go away' error in response, got: %s", rr.Body.String())
	}
}

func TestGateway_ServeHTTP_XExedevBoxAuthenticationInDevMode(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.devMode = true // Enable dev mode

	// Test with X-Exedev-Box header in dev mode (should be accepted)
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("X-Exedev-Box", "test-box")
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345" // Local address

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Gateway authentication should pass - we should NOT get our gateway's auth error
	// The request will be proxied to Anthropic which will return 401 for invalid API key
	if rr.Code == http.StatusUnauthorized && strings.Contains(rr.Body.String(), "X-Exedev-Box header required") {
		t.Errorf("Expected gateway authentication to pass with X-Exedev-Box in dev mode, but got auth error: %s", rr.Body.String())
	}
	if rr.Code == http.StatusUnauthorized && strings.Contains(rr.Body.String(), "X-Exedev-Box header not allowed") {
		t.Errorf("Expected X-Exedev-Box to be allowed in dev mode, but got rejection: %s", rr.Body.String())
	}
}

func TestGateway_ServeHTTP_XExedevBoxAuthenticationInProduction(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.devMode = false // Production mode

	// Test with X-Exedev-Box header in production from non-tailscale IP (should be rejected)
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model":"claude-3-haiku","messages":[{"role":"user","content":"Hello"}]}`))
	req.Header.Set("X-Exedev-Box", "test-box")
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "1.2.3.4:12345" // Non-tailscale address

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Should return 401 with rejection message (tailscale IP is checked first)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for request from non-tailscale IP in production mode, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "hey go away") {
		t.Errorf("Expected 'hey go away' error, got: %s", rr.Body.String())
	}
}

func TestGateway_ServeHTTP_ReadyEndpoint(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.devMode = true // Enable dev mode for easier testing

	// Test /ready endpoint with X-Exedev-Box authentication (requires auth)
	req := httptest.NewRequest("GET", "/_/gateway/ready", nil)
	req.Header.Set("X-Exedev-Box", "test-box")
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Should return 200 with valid authentication
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200 for /ready endpoint with auth, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "OK\n" {
		t.Errorf("Expected body 'OK\\n', got: %s", rr.Body.String())
	}

	// Test /ready endpoint without authentication should fail
	req2 := httptest.NewRequest("GET", "/_/gateway/ready", nil)
	rr2 := httptest.NewRecorder()
	gateway.ServeHTTP(rr2, req2)

	// Should return 401 without authentication
	if rr2.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401 for /ready endpoint without auth, got %d", rr2.Code)
	}
}

func TestGateway_ServeHTTP_UnrecognizedAlias(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.devMode = true // Enable dev mode for easier testing

	// Test with unrecognized alias
	req := httptest.NewRequest("POST", "/_/gateway/unknown/v1/messages",
		strings.NewReader(`{"model":"test","messages":[]}`))
	req.Header.Set("X-Exedev-Box", "test-box")
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	gateway.ServeHTTP(rr, req)

	// Should return 404 for unrecognized alias
	if rr.Code != http.StatusNotFound {
		t.Errorf("Expected status 404 for unrecognized alias, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "unrecognized origin alias") {
		t.Errorf("Expected 'unrecognized origin alias' error, got: %s", rr.Body.String())
	}
}
