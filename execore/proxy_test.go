package execore

import (
	"context"
	"crypto/tls"
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

	"exe.dev/stage"
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

// TestIsProxyRequest tests the isProxyRequest function with comprehensive cases
func TestIsProxyRequest(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		env      stage.Env
		host     string
		expected bool
		comment  string
	}{
		// Box:port format cases
		{
			name:     "invalid box:port (bad port)",
			env:      stage.Test(),
			host:     "mybox:abc",
			expected: false,
			comment:  "Should reject non-numeric ports",
		},
		{
			name:     "localhost:port should not be proxy",
			env:      stage.Test(),
			host:     "localhost:8080",
			expected: false,
			comment:  "localhost with port is the main domain, not a proxy request",
		},
		{
			name:     "exe.dev:port should not be proxy",
			env:      stage.Prod(),
			host:     "exe.dev:443",
			expected: false,
			comment:  "exe.dev with port is the main domain, not a proxy request",
		},

		// Subdomain format cases (dev mode)
		{
			name:     "dev subdomain format",
			env:      stage.Test(),
			host:     "mybox.exe.cloud",
			expected: true,
			comment:  "Should recognize *.exe.cloud pattern in dev mode",
		},
		{
			name:     "dev subdomain with server port",
			env:      stage.Test(),
			host:     "mybox.exe.cloud:8080",
			expected: true,
			comment:  "Should recognize *.exe.cloud even with server port",
		},
		{
			name:     "localhost alone in dev mode",
			env:      stage.Test(),
			host:     "localhost",
			expected: false,
			comment:  "Plain localhost should not be proxy request",
		},
		{
			name:     "deep subdomain in dev mode",
			env:      stage.Test(),
			host:     "box.team.exe.cloud",
			expected: true,
			comment:  "Should work with deeper subdomains",
		},

		// Subdomain format cases (production mode)
		{
			name:     "prod subdomain format",
			env:      stage.Prod(),
			host:     "mybox.exe.dev",
			expected: true,
			comment:  "Should recognize *.exe.dev pattern in production",
		},
		{
			name:     "prod subdomain with server port",
			env:      stage.Prod(),
			host:     "mybox.exe.dev:443",
			expected: true,
			comment:  "Should recognize *.exe.dev even with server port",
		},
		{
			name:     "exe.dev alone in prod mode",
			env:      stage.Prod(),
			host:     "exe.dev",
			expected: false,
			comment:  "Plain exe.dev should not be proxy request",
		},

		// Cross-mode cases (testing flexibility)
		{
			name:     "prod domain in dev mode",
			env:      stage.Test(),
			host:     "mybox.exe.dev",
			expected: true,
			comment:  "Should still work with production domain in dev mode for flexibility",
		},
		{
			name:     "dev domain in prod mode",
			env:      stage.Prod(),
			host:     "mybox.exe.cloud",
			expected: true,
			comment:  "Should still work with dev domain in production for flexibility",
		},

		// Edge cases
		{
			name:     "empty host",
			env:      stage.Test(),
			host:     "",
			expected: false,
			comment:  "Empty host should not be proxy request",
		},
		{
			name:     "just colon",
			env:      stage.Test(),
			host:     ":",
			expected: false,
			comment:  "Invalid format should be rejected",
		},
		{
			name:     "box with multiple colons",
			env:      stage.Test(),
			host:     "my:box:8080",
			expected: false,
			comment:  "Multiple colons should be rejected for box:port format",
		},
		{
			name:     "other domain",
			env:      stage.Test(),
			host:     "example.com",
			expected: true,
			comment:  "Other domains should be proxy requests",
		},
		{
			name:     "subdomain of other domain",
			env:      stage.Test(),
			host:     "mybox.example.com",
			expected: true,
			comment:  "Subdomains of other domains should be proxy requests",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{env: tc.env}
			result := s.isProxyRequest(tc.host)
			if result != tc.expected {
				t.Errorf("Expected %v for host %q (stage=%v), got %v\nComment: %s",
					tc.expected, tc.host, tc.env.String(), result, tc.comment)
			} else {
				t.Logf("✓ %s: host=%q stage=%s -> %v", tc.comment, tc.host, tc.env.String(), result)
			}
		})
	}
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
		for i := 0; i < 3; i++ {
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
	for i := 0; i < 3; i++ {
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

func TestSetForwardedHeaders(t *testing.T) {
	t.Parallel()

	t.Run("https request populates headers", func(t *testing.T) {
		incoming := httptest.NewRequest(http.MethodGet, "https://box.exe.dev/", nil)
		incoming.Host = "box.exe.dev"
		incoming.RemoteAddr = "203.0.113.5:45678"
		incoming.TLS = &tls.ConnectionState{}

		outgoing := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/", nil)

		setForwardedHeaders(outgoing, incoming)

		if got := outgoing.Header.Get("X-Forwarded-Proto"); got != "https" {
			t.Fatalf("expected proto https, got %q", got)
		}
		if got := outgoing.Header.Get("X-Forwarded-Host"); got != "box.exe.dev" {
			t.Fatalf("expected forwarded host box.exe.dev, got %q", got)
		}
		if got := outgoing.Header.Get("X-Forwarded-For"); got != "203.0.113.5" {
			t.Fatalf("expected forwarded for 203.0.113.5, got %q", got)
		}
	})

	t.Run("appends existing xff and preserves host port", func(t *testing.T) {
		incoming := httptest.NewRequest(http.MethodGet, "http://app.exe.dev/resource", nil)
		incoming.Host = "app.exe.dev:8443"
		incoming.RemoteAddr = "198.51.100.7:4444"
		incoming.Header.Set("X-Forwarded-For", "10.0.0.1")

		outgoing := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5000/", nil)

		setForwardedHeaders(outgoing, incoming)

		if got := outgoing.Header.Get("X-Forwarded-Proto"); got != "http" {
			t.Fatalf("expected proto http, got %q", got)
		}
		if got := outgoing.Header.Get("X-Forwarded-Host"); got != "app.exe.dev:8443" {
			t.Fatalf("expected forwarded host app.exe.dev:8443, got %q", got)
		}
		if got := outgoing.Header.Get("X-Forwarded-For"); got != "10.0.0.1, 198.51.100.7" {
			t.Fatalf("expected forwarded for '10.0.0.1, 198.51.100.7', got %q", got)
		}
	})
}
