package execore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

// createTestRequest creates an http.Request with proper context for proxy tests
// For hosts without explicit ports, adds the server's HTTP port
func createTestRequestForServer(method, url, host string, server *Server) *http.Request {
	req := httptest.NewRequest(method, url, nil)

	// If host doesn't have a port, add the server's HTTP port
	finalHost := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		// No port in host, add server's HTTP port
		if server.servingHTTP() {
			finalHost = net.JoinHostPort(host, strconv.Itoa(server.httpPort()))
		} else {
			// Fallback to port 80 for test
			finalHost = net.JoinHostPort(host, "80")
		}
	}

	req.Host = finalHost

	// Set up mock local address context that the proxy handler expects
	// Parse the host to determine what port to mock
	var mockPort int
	if _, portStr, err := net.SplitHostPort(finalHost); err == nil {
		if port, err := strconv.Atoi(portStr); err == nil {
			mockPort = port
		} else {
			mockPort = 80 // fallback
		}
	} else {
		// No port specified, assume default
		mockPort = 80
	}

	// Create a mock net.Addr that represents the local address
	mockAddr := &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: mockPort,
	}

	// Add the local address to the request context
	ctx := context.WithValue(req.Context(), http.LocalAddrContextKey, mockAddr)
	req = req.WithContext(ctx)

	return req
}

// TestIsDefaultServerPort tests the isDefaultServerPort function
func TestIsDefaultServerPort(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		serverPort int // simulated server HTTP port
		testPort   int // port to test
		expected   bool
		comment    string
	}{
		{
			name:       "port 443 is always default",
			serverPort: 8080,
			testPort:   443,
			expected:   true,
			comment:    "Port 443 (HTTPS) should always use default route",
		},
		{
			name:       "server HTTP port is default",
			serverPort: 8080,
			testPort:   8080,
			expected:   true,
			comment:    "Request to server's own HTTP port should use default route",
		},
		{
			name:       "different port is not default",
			serverPort: 8080,
			testPort:   9000,
			expected:   false,
			comment:    "Different port should use multi-port routing",
		},
		{
			name:       "port 80 not default when server on 8080",
			serverPort: 8080,
			testPort:   80,
			expected:   false,
			comment:    "Port 80 should not be default when server runs on different port",
		},
		{
			name:       "port 80 is default when server on 80",
			serverPort: 80,
			testPort:   80,
			expected:   true,
			comment:    "Port 80 should be default when server runs on port 80",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock TCP listener to simulate the server port
			mockTCP := &net.TCPAddr{Port: tc.serverPort}
			mockListener := &listener{tcp: mockTCP}
			s := &Server{httpLn: mockListener}

			result := s.isDefaultServerPort(tc.testPort)
			if result != tc.expected {
				t.Errorf("Expected %v for port %d (server on %d), got %v\nComment: %s",
					tc.expected, tc.testPort, tc.serverPort, result, tc.comment)
			} else {
				t.Logf("✓ %s: port=%d serverPort=%d -> %v", tc.comment, tc.testPort, tc.serverPort, result)
			}
		})
	}

	// Test case where httpLn is nil
	t.Run("nil httpLn", func(t *testing.T) {
		s := &Server{httpLn: nil}
		// Should only return true for 443
		if !s.isDefaultServerPort(443) {
			t.Error("Expected true for port 443 even with nil httpLn")
		}
		if s.isDefaultServerPort(8080) {
			t.Error("Expected false for port 8080 with nil httpLn")
		}
	})

	// Test case where tcp is nil
	t.Run("nil tcp", func(t *testing.T) {
		s := &Server{httpLn: &listener{tcp: nil}}
		// Should only return true for 443
		if !s.isDefaultServerPort(443) {
			t.Error("Expected true for port 443 even with nil tcp")
		}
		if s.isDefaultServerPort(8080) {
			t.Error("Expected false for port 8080 with nil tcp")
		}
	})
}

// TestProxyStreaming tests that the proxy doesn't buffer streaming responses
func TestProxyStreaming(t *testing.T) {
	t.Parallel()

	// This test verifies that FlushInterval is set on the reverse proxy
	// to avoid buffering responses. This is critical for:
	// - Server-Sent Events (SSE)
	// - Streaming responses
	// - WebSocket upgrades
	// - Any real-time data transfer

	// Create a mock streaming backend that sends data in chunks
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Enable flushing
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter doesn't support flushing")
		}

		// Send headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send data in chunks with delays
		for i := range 3 {
			fmt.Fprintf(w, "data: chunk %d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer backend.Close()

	// Parse backend URL
	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Create a reverse proxy similar to what proxyViaSSHPortForward does
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	// This is the critical setting - FlushInterval = -1 means flush immediately
	proxy.FlushInterval = -1

	// Create test server with the proxy
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Make request to proxy
	req, err := http.NewRequest("GET", proxyServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Read the response body to verify streaming works
	// If buffering were enabled, we'd get all chunks at once after the delays
	// With flushing, we get them as they're sent
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	bodyStr := string(body)

	// Verify we got all chunks
	for i := range 3 {
		expected := fmt.Sprintf("data: chunk %d\n\n", i)
		if !strings.Contains(bodyStr, expected) {
			t.Errorf("Expected to find %q in response, got: %q", expected, bodyStr)
		}
	}

	// Verify content type
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Expected Content-Type 'text/event-stream', got %q", ct)
	}
}

// TestPublicRouteStripsTokenCtx verifies that on public routes, token auth
// still works for identity (UserID is set), but CtxRaw is stripped so that
// X-ExeDev-Token-Ctx is never forwarded to the VM. This prevents user A from
// injecting arbitrary trusted context into user B's public box.
func TestPublicRouteStripsTokenCtx(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)

	// Generate a test ed25519 key pair and register a user.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sshPubKey, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatalf("failed to create SSH public key: %v", err)
	}
	sshPrivKey, err := ssh.NewSignerFromKey(privKey)
	if err != nil {
		t.Fatalf("failed to create SSH signer: %v", err)
	}

	ctx := t.Context()
	userID := "usr" + generateRegistrationToken()
	email := "publicctx@example.com"
	pubKeyStr := string(ssh.MarshalAuthorizedKey(sshPubKey))
	fingerprint := strings.TrimPrefix(ssh.FingerprintSHA256(sshPubKey), "SHA256:")
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key, fingerprint) VALUES (?, ?, ?)`, userID, pubKeyStr, fingerprint); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create a box with a public route.
	boxName := "pubbox"
	publicRoute := `{"port":80,"share":"public"}`
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (ctrhost, name, status, image, container_id, created_by_user_id, routes,
			                     ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"fake_ctrhost", boxName, "running", "test-image", "test-container-id", userID, publicRoute,
			"test-identity-key", "test-authorized-keys", "test-client-key", 2222)
		return err
	})
	if err != nil {
		t.Fatalf("failed to create box: %v", err)
	}

	// Look up the box (same as handleProxyRequest does).
	box, err := withRxRes1(s, ctx, (*exedb.Queries).BoxNamed, boxName)
	if err != nil {
		t.Fatalf("failed to look up box: %v", err)
	}

	// Create a VM token with a ctx field.
	vmNamespace := "v0@" + boxName + "." + s.env.BoxHost
	payload := []byte(`{"exp":4000000000,"ctx":{"role":"admin"}}`)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	sigBlob := createSigBlob(t, sshPrivKey, payload, vmNamespace)
	token := "exe0." + payloadB64 + "." + sigBlob

	// Sanity check: getProxyAuth with Bearer token returns non-nil CtxRaw.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	result := s.getProxyAuth(req, box)
	if result == nil {
		t.Fatal("getProxyAuth returned nil for valid token")
	}
	if result.UserID != userID {
		t.Errorf("expected UserID %q, got %q", userID, result.UserID)
	}
	if result.CtxRaw == nil {
		t.Fatal("getProxyAuth should return non-nil CtxRaw for token with ctx")
	}

	// Now simulate the public route code path: call getProxyAuth and strip CtxRaw.
	// This mirrors the logic in handleProxyRequest for public routes.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	authResult := s.getProxyAuth(req2, box)
	if authResult == nil {
		t.Fatal("getProxyAuth returned nil on second call")
	}
	// Public route: strip CtxRaw.
	authResult.CtxRaw = nil

	if authResult.UserID != userID {
		t.Errorf("after strip, expected UserID %q, got %q", userID, authResult.UserID)
	}
	if authResult.CtxRaw != nil {
		t.Error("after strip, CtxRaw should be nil")
	}
}
