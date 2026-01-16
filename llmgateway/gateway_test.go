package llmgateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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

func TestGateway_GzipResponse(t *testing.T) {
	// This test reproduces an issue where gzip-compressed responses from OpenAI
	// were not being decompressed before JSON parsing, resulting in errors like:
	// "openai json decode error: invalid character 'a' looking for beginning of value"
	//
	// The issue occurs because http.DefaultTransport has DisableCompression=false,
	// which means it sends Accept-Encoding: gzip by default. When the upstream
	// server responds with gzip-compressed content, the transport is supposed to
	// decompress it automatically. However, there's a subtle issue: when using
	// httputil.ReverseProxy, the automatic decompression doesn't always work as
	// expected because the proxy copies the response headers including Content-Encoding.

	gateway, _ := setupTestGateway(t)
	gateway.devMode = true
	gateway.apiKeys.OpenAI = "test-openai-key"
	gateway.creditMgr = NewCreditManager(gateway.db)

	// Create a gzip-compressed response body
	jsonResponse := `{"id": "chatcmpl-123", "model": "gpt-4", "usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}}`
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	gzWriter.Write([]byte(jsonResponse))
	gzWriter.Close()
	gzippedData := buf.Bytes()

	t.Run("with Content-Encoding gzip header", func(t *testing.T) {
		// Create a mock response with gzip-compressed body and proper header
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &readCloser{Reader: bytes.NewReader(gzippedData)},
		}
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Set("Content-Encoding", "gzip")

		// Create the accounting transport
		incomingReq := httptest.NewRequest("GET", "/_/gateway/openai/v1/models", nil)
		transport := &accountingTransport{
			RoundTripper: http.DefaultTransport,
			db:           gateway.db,
			apiType:      "openai",
			log:          gateway.log,
			creditMgr:    gateway.creditMgr,
			incomingReq:  incomingReq,
			boxName:      "test-box",
			userID:       "test-user-test-box",
		}

		// This should not return an error - the transport should decompress the gzip data
		err := transport.modifyResponse(resp)
		if err != nil {
			t.Errorf("modifyResponse failed with gzip response: %v", err)
		}
	})

	t.Run("gzip data without Content-Encoding header", func(t *testing.T) {
		// This simulates the case where gzip data arrives without the header
		// (could happen if some middleware strips the header but not the encoding)
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &readCloser{Reader: bytes.NewReader(gzippedData)},
		}
		resp.Header.Set("Content-Type", "application/json")
		// NOTE: No Content-Encoding header!

		incomingReq := httptest.NewRequest("GET", "/_/gateway/openai/v1/models", nil)
		transport := &accountingTransport{
			RoundTripper: http.DefaultTransport,
			db:           gateway.db,
			apiType:      "openai",
			log:          gateway.log,
			creditMgr:    gateway.creditMgr,
			incomingReq:  incomingReq,
			boxName:      "test-box",
			userID:       "test-user-test-box",
		}

		// The code should detect gzip via magic bytes even without the header
		err := transport.modifyResponse(resp)
		if err != nil {
			t.Errorf("modifyResponse failed with gzip data (no header): %v", err)
		}
	})

	t.Run("with non-standard Content-Encoding header value", func(t *testing.T) {
		// Test with different Content-Encoding header formats
		testCases := []string{
			"GZIP",     // uppercase
			"Gzip",     // mixed case
			"gzip, br", // multiple values
			" gzip",    // leading space
			"gzip ",    // trailing space
		}

		for _, encoding := range testCases {
			t.Run(encoding, func(t *testing.T) {
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       &readCloser{Reader: bytes.NewReader(gzippedData)},
				}
				resp.Header.Set("Content-Type", "application/json")
				resp.Header.Set("Content-Encoding", encoding)

				incomingReq := httptest.NewRequest("GET", "/_/gateway/openai/v1/models", nil)
				transport := &accountingTransport{
					RoundTripper: http.DefaultTransport,
					db:           gateway.db,
					apiType:      "openai",
					log:          gateway.log,
					creditMgr:    gateway.creditMgr,
					incomingReq:  incomingReq,
					boxName:      "test-box",
					userID:       "test-user-test-box",
				}

				err := transport.modifyResponse(resp)
				if err != nil {
					t.Errorf("modifyResponse failed with Content-Encoding=%q: %v", encoding, err)
				}
			})
		}
	})
}

// TestGateway_GzipWithClientAcceptEncoding tests the full proxy flow when
// the client explicitly sends Accept-Encoding: gzip. This reproduces the
// actual bug where clients inside VMs would send Accept-Encoding: gzip,
// OpenAI would respond with gzip-compressed data, but the proxy would
// fail to decompress it before JSON parsing.
func TestGateway_GzipWithClientAcceptEncoding(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.devMode = true
	gateway.apiKeys.OpenAI = "test-openai-key"
	gateway.creditMgr = NewCreditManager(gateway.db)

	// Create a mock "OpenAI" server that returns gzip when client requests it
	jsonResponse := `{"id": "chatcmpl-123", "model": "gpt-4", "usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}}`
	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if client accepts gzip (this is what OpenAI does)
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			var buf bytes.Buffer
			gzWriter := gzip.NewWriter(&buf)
			gzWriter.Write([]byte(jsonResponse))
			gzWriter.Close()

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusOK)
			w.Write(buf.Bytes())
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(jsonResponse))
		}
	}))
	defer mockOpenAI.Close()

	// Parse the mock server URL
	mockURL, _ := url.Parse(mockOpenAI.URL)

	// Create a test request WITH Accept-Encoding: gzip (simulating curl --compressed)
	incomingReq := httptest.NewRequest("GET", "/_/gateway/openai/v1/models", nil)
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Accept-Encoding", "gzip") // This is the key!
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	// Create a custom transport that points to our mock server
	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           gateway.db,
		apiType:      "openai",
		log:          gateway.log,
		creditMgr:    gateway.creditMgr,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user-test-box",
	}

	// Create a reverse proxy that points to our mock server
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = mockURL.Host
			r.Out.Host = mockURL.Host
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	// Make the request through the proxy
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)

	// The proxy should succeed without errors
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// The response body should be readable (either compressed or decompressed)
	body := rr.Body.Bytes()
	if len(body) == 0 {
		t.Error("Expected non-empty response body")
	}
}

// TestGateway_OpenAIModelsEndpoint tests that the /v1/models endpoint works
// correctly. This endpoint returns a list of models without usage data,
// so the gateway should pass through the response without error.
func TestGateway_OpenAIModelsEndpoint(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.devMode = true
	gateway.apiKeys.OpenAI = "test-openai-key"
	gateway.creditMgr = NewCreditManager(gateway.db)

	// This is what OpenAI's /v1/models endpoint actually returns (no usage field)
	modelsResponse := `{"object": "list", "data": [{"id": "gpt-4", "object": "model", "owned_by": "openai"}]}`

	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(modelsResponse))
	}))
	defer mockOpenAI.Close()

	mockURL, _ := url.Parse(mockOpenAI.URL)

	// The path after the gateway strips the prefix is /v1/models
	incomingReq := httptest.NewRequest("GET", "/v1/models", nil)
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           gateway.db,
		apiType:      "openai",
		log:          gateway.log,
		creditMgr:    gateway.creditMgr,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user-test-box",
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = mockURL.Host
			r.Out.Host = mockURL.Host
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)

	// The proxy should succeed - no usage data is fine for /v1/models
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify the response body is the models list
	if !strings.Contains(rr.Body.String(), "gpt-4") {
		t.Errorf("Expected response to contain gpt-4, got: %s", rr.Body.String())
	}
}

// TestGateway_OpenAIMissingUsageOnOtherEndpoints tests that endpoints other than
// /v1/models fail if they return a response without usage data.
func TestGateway_OpenAIMissingUsageOnOtherEndpoints(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.devMode = true
	gateway.apiKeys.OpenAI = "test-openai-key"
	gateway.creditMgr = NewCreditManager(gateway.db)

	// Response without usage data (which would be unexpected for chat completions)
	badResponse := `{"id": "chatcmpl-123", "object": "chat.completion", "choices": []}`

	mockOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(badResponse))
	}))
	defer mockOpenAI.Close()

	mockURL, _ := url.Parse(mockOpenAI.URL)

	// Test with chat completions endpoint - should fail without usage
	incomingReq := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           gateway.db,
		apiType:      "openai",
		log:          gateway.log,
		creditMgr:    gateway.creditMgr,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user-test-box",
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = mockURL.Host
			r.Out.Host = mockURL.Host
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "openai api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, incomingReq)

	// The proxy should fail - missing usage on /v1/chat/completions is an error
	if rr.Code != http.StatusBadGateway {
		t.Errorf("Expected status 502 (BadGateway), got %d: %s", rr.Code, rr.Body.String())
	}

	// Error message should mention the path
	if !strings.Contains(rr.Body.String(), "/v1/chat/completions") {
		t.Errorf("Expected error to mention path, got: %s", rr.Body.String())
	}
}

// readCloser wraps a Reader to implement io.ReadCloser
type readCloser struct {
	*bytes.Reader
}

func (rc *readCloser) Close() error {
	return nil
}
