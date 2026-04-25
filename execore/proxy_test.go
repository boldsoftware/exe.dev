package execore

import (
	"context"
	"errors"
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

func TestExeproxTarget(t *testing.T) {
	server := newTestServer(t)
	server.exeproxAddress = "exeprox.exe.xyz"

	for _, test := range []struct {
		host   string
		url    string
		lookup string
		cname  string
		want   string
	}{
		{
			host: "example.com",
			url:  "https://example.com/",
			want: "https://exeprox.exe.xyz/?exedev_host=example.com",
		},
		{
			host: "example.com",
			url:  "https://example.com/page",
			want: "https://exeprox.exe.xyz/page?exedev_host=example.com",
		},
		{
			host: "example.com",
			url:  "https://example.com/?param=val",
			want: "https://exeprox.exe.xyz/?exedev_host=example.com&param=val",
		},
		{
			host: "example.com",
			url:  "https://example.com/page?param=val",
			want: "https://exeprox.exe.xyz/page?exedev_host=example.com&param=val",
		},
		{
			host:   "example.com",
			url:    "https://example.com/",
			lookup: "www.example.com",
			cname:  "example." + server.env.BoxHost,
			want:   "https://www.example.com/",
		},
		{
			host:   "example.com",
			url:    "https://example.com/page",
			lookup: "www.example.com",
			cname:  "example." + server.env.BoxHost,
			want:   "https://www.example.com/page",
		},
		{
			host:   "example.com",
			url:    "https://example.com/?param=val",
			lookup: "www.example.com",
			cname:  "example." + server.env.BoxHost,
			want:   "https://www.example.com/?param=val",
		},
		{
			host:   "example.com",
			url:    "https://example.com/page?param=val",
			lookup: "www.example.com",
			cname:  "example." + server.env.BoxHost,
			want:   "https://www.example.com/page?param=val",
		},
		{
			host:   "www.example.com",
			url:    "https://www.example.com/",
			lookup: "www.example.com", // should not be used
			cname:  "example." + server.env.BoxHost,
			want:   "https://exeprox.exe.xyz/?exedev_host=www.example.com",
		},
		{
			host: "example.com:8080",
			url:  "https://example.com:8080/",
			want: "https://exeprox.exe.xyz/?exedev_host=example.com%3A8080",
		},
		{
			host:   "example.com:8080",
			url:    "https://example.com:8080/",
			lookup: "www.example.com",
			cname:  "example." + server.env.BoxHost,
			want:   "https://www.example.com:8080/",
		},
		{
			host:   "example.com",
			url:    "https://example.com/",
			lookup: "www.example.com",
			cname:  "www.example.com", // dnsresolver.LookupCNAME will return argument if there is no CNAME
			want:   "https://exeprox.exe.xyz/?exedev_host=example.com",
		},
	} {
		server.lookupCNAMEFunc = func(ctx context.Context, host string) (string, error) {
			if test.lookup != "" && host == test.lookup {
				return test.cname, nil
			}
			return "", errors.New("no such CNAME")
		}

		reqURL, err := url.Parse(test.url)
		if err != nil {
			t.Errorf("url.Parse(%q) failed: %v", test.url, err)
			continue
		}
		got := server.exeproxTarget(t.Context(), "https", test.host, reqURL)
		if got != test.want {
			t.Errorf("exeproxTarget(%q, %q) = %q, want %q", test.host, reqURL, got, test.want)
		}
	}
}
