package llmgateway

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/llmpricing"
	"exe.dev/sqlite"
	"exe.dev/stage"
	"exe.dev/tslog"
	sloghttp "github.com/samber/slog-http"
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
			Region:                 "pdx",
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
			Region:          "pdx",
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
	dbPath := filepath.Join(t.TempDir(), "gateway_test.db")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("Failed to copy template database: %v", err)
	}

	// Open with sqlite wrapper
	db, err := sqlite.New(dbPath, 1)
	if err != nil {
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
		data:          &DBGatewayData{db},
		apiKeys:       APIKeys{Anthropic: "test-api-key"},
		env:           stage.Test(),
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
	gateway.env.GatewayDev = false // Production mode - requires Tailscale IP

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
	gateway.env.GatewayDev = false // Production mode

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
	gateway.apiKeys.OpenAI = "test-openai-key"
	gateway.creditMgr = NewCreditManager(gateway.data)

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
			provider:     llmpricing.ProviderOpenAI,
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
			provider:     llmpricing.ProviderOpenAI,
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
					provider:     llmpricing.ProviderOpenAI,
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
	gateway.apiKeys.OpenAI = "test-openai-key"
	gateway.creditMgr = NewCreditManager(gateway.data)

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
		provider:     llmpricing.ProviderOpenAI,
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
	gateway.apiKeys.OpenAI = "test-openai-key"
	gateway.creditMgr = NewCreditManager(gateway.data)

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
		provider:     llmpricing.ProviderOpenAI,
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

// TestGateway_FireworksModelsEndpoint tests that the /inference/v1/models endpoint works
// correctly. This endpoint returns a list of models without usage data,
// so the gateway should pass through the response without error.
func TestGateway_FireworksModelsEndpoint(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.apiKeys.Fireworks = "test-fireworks-key"
	gateway.creditMgr = NewCreditManager(gateway.data)

	// This is what Fireworks' /inference/v1/models endpoint returns (no usage field)
	modelsResponse := `{"data":[{"id":"accounts/fireworks/models/qwen3-vl-30b-a3b-instruct","object":"model","owned_by":"fireworks","created":1759959171}]}`

	mockFireworks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(modelsResponse))
	}))
	defer mockFireworks.Close()

	mockURL, _ := url.Parse(mockFireworks.URL)

	// The path after the gateway strips the prefix is /inference/v1/models
	incomingReq := httptest.NewRequest("GET", "/inference/v1/models", nil)
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderFireworks,
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

	// The proxy should succeed - no usage data is fine for /inference/v1/models
	if rr.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify the response body is the models list
	if !strings.Contains(rr.Body.String(), "qwen3-vl-30b-a3b-instruct") {
		t.Errorf("Expected response to contain qwen3-vl-30b-a3b-instruct, got: %s", rr.Body.String())
	}
}

// TestGateway_OpenAIMissingUsageOnOtherEndpoints tests that endpoints other than
// /v1/models fail if they return a response without usage data.
func TestGateway_OpenAIMissingUsageOnOtherEndpoints(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.apiKeys.OpenAI = "test-openai-key"
	gateway.creditMgr = NewCreditManager(gateway.data)

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
		provider:     llmpricing.ProviderOpenAI,
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

func TestIsBlockedEndpoint(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Blocked endpoints (per-image pricing)
		{"/v1/images/generations", true},
		{"/v1/images/edits", true},

		// Allowed endpoints
		{"/v1/chat/completions", false},
		{"/v1/completions", false},
		{"/v1/models", false},
		{"/v1/embeddings", false},
		{"/v1/audio/transcriptions", false},
		{"/inference/v1/chat/completions", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isBlockedEndpoint(tt.path)
			if got != tt.want {
				t.Errorf("isBlockedEndpoint(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsFreeEndpoint(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// OpenAI models endpoints (free)
		{"/v1/models", true},
		{"/v1/models/gpt-4", true},
		{"/v1/models/gpt-4-turbo-preview", true},

		// Fireworks models endpoints (free)
		{"/inference/v1/models", true},
		{"/inference/v1/models/accounts/fireworks/models/llama-v3", true},

		// Non-free endpoints
		{"/v1/chat/completions", false},
		{"/v1/completions", false},
		{"/v1/embeddings", false},
		{"/v1/images/generations", false},
		{"/v1/audio/transcriptions", false},
		{"/inference/v1/chat/completions", false},

		// Edge cases
		{"/v2/models", false},
		{"/models", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := isFreeEndpoint(tt.path)
			if got != tt.want {
				t.Errorf("isFreeEndpoint(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestParseShelleyVersion(t *testing.T) {
	tests := []struct {
		userAgent string
		want      string
	}{
		{"Shelley/abcd1234", "abcd1234"},
		{"Shelley/abcd1234 other-stuff", "abcd1234"},
		{"Shelley/", ""},
		{"Mozilla/5.0", ""},
		{"", ""},
		{"shelley/abcd1234", ""}, // case-sensitive
		{"Shelley/v1.0.0-beta", "v1.0.0-beta"},
		{"Shelley/abc123 (Linux; x86_64)", "abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.userAgent, func(t *testing.T) {
			got := parseShelleyVersion(tt.userAgent)
			if got != tt.want {
				t.Errorf("parseShelleyVersion(%q) = %q, want %q", tt.userAgent, got, tt.want)
			}
		})
	}
}

func TestExtractModelFromRequest(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantModel string
		wantErr   bool
	}{
		{
			name:      "anthropic request",
			body:      `{"model": "claude-3-haiku-20240307", "messages": [{"role": "user", "content": "Hello"}]}`,
			wantModel: "claude-3-haiku-20240307",
		},
		{
			name:      "openai request",
			body:      `{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hello"}]}`,
			wantModel: "gpt-4o",
		},
		{
			name:      "fireworks request",
			body:      `{"model": "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct", "messages": []}`,
			wantModel: "accounts/fireworks/models/qwen3-coder-480b-a35b-instruct",
		},
		{
			name:      "empty body",
			body:      "",
			wantModel: "",
		},
		{
			name:      "no model field",
			body:      `{"messages": [{"role": "user", "content": "Hello"}]}`,
			wantModel: "",
		},
		{
			name:      "invalid json",
			body:      `{invalid json`,
			wantModel: "",
			wantErr:   true, // Invalid JSON now returns an error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest("POST", "/v1/messages", body)
			gotModel, gotBytes, err := extractModelFromRequest(req)
			if (err != nil) != tt.wantErr {
				t.Errorf("extractModelFromRequest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotModel != tt.wantModel {
				t.Errorf("extractModelFromRequest() model = %q, want %q", gotModel, tt.wantModel)
			}
			// Verify body was preserved for replay
			if tt.body != "" && string(gotBytes) != tt.body {
				t.Errorf("extractModelFromRequest() body not preserved: got %q, want %q", string(gotBytes), tt.body)
			}
		})
	}
}

func TestGateway_UnknownModelRejected(t *testing.T) {
	db := newDB(t)
	setupTestBox(t, db, "test-box")

	gateway := &llmGateway{
		now:     time.Now,
		data:    &DBGatewayData{db},
		apiKeys: APIKeys{Anthropic: "test-key", OpenAI: "test-key", Fireworks: "test-key"},
		env:     stage.Test(),
		log:     tslog.Slogger(t),
	}

	tests := []struct {
		name  string
		path  string
		body  string
		model string
	}{
		{
			name:  "unknown anthropic model rejected",
			path:  "/_/gateway/anthropic/v1/messages",
			body:  `{"model": "unknown-model-xyz", "messages": []}`,
			model: "unknown-model-xyz",
		},
		{
			name:  "unknown openai model rejected",
			path:  "/_/gateway/openai/v1/chat/completions",
			body:  `{"model": "gpt-99", "messages": []}`,
			model: "gpt-99",
		},
		{
			name:  "unknown fireworks model rejected",
			path:  "/_/gateway/fireworks/inference/v1/chat/completions",
			body:  `{"model": "accounts/fireworks/models/unknown-model", "messages": []}`,
			model: "accounts/fireworks/models/unknown-model",
		},
		{
			name: "empty model rejected for anthropic",
			path: "/_/gateway/anthropic/v1/messages",
			body: `{"messages": []}`,
		},
		{
			name: "empty model rejected for openai",
			path: "/_/gateway/openai/v1/chat/completions",
			body: `{"messages": []}`,
		},
		{
			name: "empty model rejected for fireworks",
			path: "/_/gateway/fireworks/inference/v1/chat/completions",
			body: `{"messages": []}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
			req.Header.Set("X-Exedev-Box", "test-box")
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "100.100.100.100:12345" // Tailscale IP

			w := httptest.NewRecorder()
			gateway.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("got status %d, want 400 for unknown model", w.Code)
			}
			body := w.Body.String()
			if tt.model != "" {
				if !strings.Contains(body, tt.model) {
					t.Errorf("response body %q should mention the model %q", body, tt.model)
				}
			} else {
				if !strings.Contains(body, "missing required") {
					t.Errorf("response body %q should mention missing required model", body)
				}
			}
		})
	}
}

func TestGateway_CostHeaders(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.apiKeys.Anthropic = "test-anthropic-key"
	gateway.creditMgr = NewCreditManager(gateway.data)

	// Create a mock Anthropic server that returns usage info
	jsonResponse := `{
		"id": "msg_123",
		"model": "claude-sonnet-4-20250514",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens": 25
		}
	}`
	mockAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(jsonResponse))
	}))
	defer mockAnthropic.Close()

	mockURL, _ := url.Parse(mockAnthropic.URL)

	// Create a test request
	incomingReq := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(`{"model": "claude-sonnet-4-20250514", "messages": []}`))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	// Create the accounting transport
	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderAnthropic,
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

	if rr.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	// Check that cost header is present
	costHeader := rr.Header().Get("Exedev-Gateway-Cost")
	if costHeader == "" {
		t.Error("missing Exedev-Gateway-Cost header")
	} else {
		t.Logf("Exedev-Gateway-Cost: %s", costHeader)
	}
}

func TestSSEStreamingNotBuffered(t *testing.T) {
	// Create a backend that sends SSE events with delays
	eventDelayMs := 50
	eventCount := 5
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		for i := range eventCount {
			fmt.Fprintf(w, "data: event %d\n\n", i)
			flusher.Flush()
			time.Sleep(time.Duration(eventDelayMs) * time.Millisecond)
		}
	}))
	defer backend.Close()

	// Simulate the gateway's proxy setup with pipe wrapping (like in modifyResponse)
	backendURL, _ := url.Parse(backend.URL)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		revProxy := &httputil.ReverseProxy{
			FlushInterval: -1, // This is the key setting we're testing
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(backendURL)
			},
			ModifyResponse: func(resp *http.Response) error {
				if resp.Header.Get("Content-Type") != "text/event-stream" {
					return nil
				}

				// Wrap the body with an io.Pipe like the gateway does for accounting
				body := resp.Body
				bodyReader, bodyWriter := io.Pipe()
				resp.Body = bodyReader

				go func() {
					scanner := bufio.NewScanner(body)
					for scanner.Scan() {
						line := scanner.Text()
						fmt.Fprintln(bodyWriter, line)
					}
					bodyWriter.Close()
				}()

				return nil
			},
		}
		revProxy.ServeHTTP(w, r)
	}))
	defer proxy.Close()

	// Measure the timing of events received from the proxy
	resp, err := http.Get(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var timestamps []time.Time
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			timestamps = append(timestamps, time.Now())
		}
	}

	if len(timestamps) != eventCount {
		t.Fatalf("expected %d events, got %d", eventCount, len(timestamps))
	}

	// Check that events arrived with at least some delay between them
	// (not all at once due to buffering)
	minExpectedDelayMs := eventDelayMs / 2 // Allow some slack
	for i := 1; i < len(timestamps); i++ {
		delay := timestamps[i].Sub(timestamps[i-1])
		if delay < time.Duration(minExpectedDelayMs)*time.Millisecond {
			t.Errorf("event %d arrived too quickly after event %d: %v (expected at least %dms)",
				i, i-1, delay, minExpectedDelayMs)
		}
	}
}

// TestGateway_SSE_SlogAttributes verifies that the sloghttp middleware log line
// for SSE streaming requests includes all the expected LLM attributes (model,
// tokens, cost, etc.). This tests the full flow: sloghttp middleware wraps the
// gateway handler which proxies to a mock Anthropic server.
func TestGateway_SSE_SlogAttributes(t *testing.T) {
	gateway, _ := setupTestGateway(t)
	gateway.apiKeys.Anthropic = "test-anthropic-key"
	gateway.creditMgr = NewCreditManager(gateway.data)

	// Build a realistic Anthropic SSE stream
	var sseBuf bytes.Buffer
	sseBuf.WriteString(`data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_test123","type":"message","role":"assistant","content":[],"usage":{"input_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":10,"output_tokens":3}}}` + "\n\n")
	sseBuf.WriteString(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}` + "\n\n")
	sseBuf.WriteString(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":10,"output_tokens":25}}` + "\n\n")
	sseBuf.WriteString(`data: {"type":"message_stop"}` + "\n\n")

	mockAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write(sseBuf.Bytes())
	}))
	defer mockAnthropic.Close()

	mockURL, _ := url.Parse(mockAnthropic.URL)

	// Set up accounting transport pointing at mock
	reqBody := `{"model":"claude-sonnet-4-5-20250929","messages":[{"role":"user","content":"Hello"}],"stream":true}`
	incomingReq := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(reqBody))
	incomingReq.Header.Set("X-Exedev-Box", "test-box")
	incomingReq.Header.Set("Content-Type", "application/json")
	incomingReq.RemoteAddr = "127.0.0.1:12345"

	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		provider:     llmpricing.ProviderAnthropic,
		log:          gateway.log,
		creditMgr:    gateway.creditMgr,
		incomingReq:  incomingReq,
		boxName:      "test-box",
		userID:       "test-user-test-box",
	}

	proxy := &httputil.ReverseProxy{
		FlushInterval: -1,
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
	transport.WaitAndAddSSEAttributes()

	require.Equal(t, http.StatusOK, rr.Code)

	// Verify the sseUsage was stored with correct values
	require.NotNil(t, transport.sseUsage, "sseUsage should be stored")
	require.Equal(t, "claude-sonnet-4-5-20250929", transport.sseUsage.Model)
	require.Equal(t, "msg_test123", transport.sseUsage.MessageID)
	require.Equal(t, uint64(50), transport.sseUsage.Usage.InputTokens)
	require.Equal(t, uint64(25), transport.sseUsage.Usage.OutputTokens)
	require.Equal(t, uint64(10), transport.sseUsage.Usage.CacheReadInputTokens)
	require.Greater(t, transport.sseUsage.Usage.CostUSD, 0.0, "cost should be > 0")

	// Verify the SSE body was streamed through.
	require.Contains(t, rr.Body.String(), "Hello")

	// Verify cost calculation
	expectedMicroCents := uint64(50)*300 + uint64(25)*1500 + uint64(10)*30
	expectedUSD := float64(expectedMicroCents) / 100_000_000
	require.InDelta(t, expectedUSD, transport.sseUsage.Usage.CostUSD, 0.0000001)
}

// TestGateway_SSE_SlogMiddlewareIntegration verifies that when the gateway handles
// an SSE streaming request wrapped by sloghttp middleware, the final log line
// includes the LLM-specific attributes (model, tokens, cost).
// This is the end-to-end test for the "200: OK" log line.
func TestGateway_SSE_SlogMiddlewareIntegration(t *testing.T) {
	// Build a realistic Anthropic SSE stream
	var sseBuf bytes.Buffer
	sseBuf.WriteString(`data: {"type":"message_start","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_integ123","type":"message","role":"assistant","content":[],"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":20,"output_tokens":3}}}` + "\n\n")
	sseBuf.WriteString(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi there"}}` + "\n\n")
	sseBuf.WriteString(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":20,"output_tokens":40}}` + "\n\n")
	sseBuf.WriteString(`data: {"type":"message_stop"}` + "\n\n")

	mockAnthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		w.Write(sseBuf.Bytes())
	}))
	defer mockAnthropic.Close()

	mockURL, _ := url.Parse(mockAnthropic.URL)

	db := newDB(t)
	setupTestBox(t, db, "test-box")

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	// Create a gateway handler that uses our mock server
	gatewayHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gw := &llmGateway{
			now:       time.Now,
			data:      &DBGatewayData{db},
			apiKeys:   APIKeys{Anthropic: "test-key"},
			env:       stage.Test(),
			log:       logger,
			creditMgr: NewCreditManager(&DBGatewayData{db}),
		}

		boxName := r.Header.Get("X-Exedev-Box")
		r.Header.Del("X-Exedev-Box")
		sloghttp.AddCustomAttributes(r, slog.String("request_type", "gateway"))

		// Parse model from body
		model, bodyBytes, _ := extractModelFromRequest(r)
		if bodyBytes != nil {
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			r.ContentLength = int64(len(bodyBytes))
		}
		if model != "" {
			sloghttp.AddCustomAttributes(r, slog.String("requested_model", model))
		}

		userID := "test-user-test-box"
		gw.creditMgr.CheckAndRefreshCredit(r.Context(), userID) //nolint:errcheck

		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/_/gateway/anthropic")

		transport := &accountingTransport{
			RoundTripper: http.DefaultTransport,
			provider:     llmpricing.ProviderAnthropic,
			log:          logger,
			creditMgr:    gw.creditMgr,
			incomingReq:  r,
			boxName:      boxName,
			userID:       userID,
		}
		proxy := &httputil.ReverseProxy{
			FlushInterval: -1,
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.Out.URL.Scheme = "http"
				pr.Out.URL.Host = mockURL.Host
				pr.Out.Host = mockURL.Host
			},
			Transport:      transport,
			ModifyResponse: transport.modifyResponse,
		}
		proxy.ServeHTTP(w, r)
		transport.WaitAndAddSSEAttributes()
	})

	// Wrap with sloghttp middleware (same as production)
	slogConfig := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
		WithUserAgent:    true,
	}
	wrapped := sloghttp.NewWithConfig(logger, slogConfig)(gatewayHandler)

	reqBody := `{"model":"claude-sonnet-4-5-20250929","messages":[{"role":"user","content":"Hello"}],"stream":true}`
	req := httptest.NewRequest("POST", "/_/gateway/anthropic/v1/messages",
		strings.NewReader(reqBody))
	req.Header.Set("X-Exedev-Box", "test-box")
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"

	rr := httptest.NewRecorder()
	wrapped.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	// Parse the sloghttp log output — find the "200: OK" line
	logLines := bytes.Split(logBuf.Bytes(), []byte("\n"))
	var foundLogLine map[string]any
	for _, line := range logLines {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if msg, ok := entry["msg"].(string); ok && msg == "200: OK" {
			foundLogLine = entry
			break
		}
	}

	if foundLogLine == nil {
		t.Logf("All log lines:\n%s", logBuf.String())
		t.Fatal("no '200: OK' log line found from sloghttp middleware")
	}

	t.Logf("200: OK log line: %v", foundLogLine)

	// Verify LLM-specific attributes are present in the log line
	require.Equal(t, "claude-sonnet-4-5-20250929", foundLogLine["llm_model"],
		"log line should include llm_model")
	require.Equal(t, "test-box", foundLogLine["vm_name"],
		"log line should include vm_name")
	require.Equal(t, "test-user-test-box", foundLogLine["user_id"],
		"log line should include user_id")
	require.Equal(t, "gateway", foundLogLine["request_type"],
		"log line should include request_type=gateway")

	// Token counts (JSON numbers are float64)
	require.Equal(t, float64(100), foundLogLine["input_tokens"],
		"log line should include input_tokens")
	require.Equal(t, float64(40), foundLogLine["output_tokens"],
		"log line should include output_tokens")

	// Cost should be > 0
	costUSD, ok := foundLogLine["cost_usd"].(float64)
	require.True(t, ok, "cost_usd should be a float64")
	require.Greater(t, costUSD, 0.0, "cost_usd should be > 0")
}
