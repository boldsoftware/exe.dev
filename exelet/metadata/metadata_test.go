package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"exe.dev/tracing"
)

// mockInstanceLookup is a simple mock for testing
type mockInstanceLookup struct{}

func (m *mockInstanceLookup) GetInstanceByIP(ctx context.Context, ip string) (id, name string, err error) {
	return "test-id", "test-box", nil
}

func TestMetadataService404(t *testing.T) {
	log := slog.Default()

	svc, err := NewService(log, &mockInstanceLookup{}, "http://localhost:8080", "127.0.0.1:18080", []string{".int.exe.cloud"}, "", false, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Create test server using the service's handler setup
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", svc.handleRoot)
	mux.HandleFunc("/gateway/llm/", svc.handleGatewayProxy)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{
			name:       "root returns 200",
			path:       "/",
			wantStatus: http.StatusOK,
		},
		{
			name:       "unknown path returns 404",
			path:       "/does-not-exist",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "another unknown path returns 404",
			path:       "/foo/bar/baz",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "gateway llm ready returns 200 or proxy error",
			path:       "/gateway/llm/ready",
			wantStatus: http.StatusBadGateway, // proxy will fail since there's no backend
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get(ts.URL + tt.path)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("got status %d, want %d, body: %s", resp.StatusCode, tt.wantStatus, string(body))
			}
		})
	}
}

func TestMetadataServiceRootResponse(t *testing.T) {
	log := slog.Default()

	svc, err := NewService(log, &mockInstanceLookup{}, "http://localhost:8080", "127.0.0.1:18080", []string{".int.exe.cloud"}, "", false, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", svc.handleRoot)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var meta MetadataResponse
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if meta.Name != "test-box" {
		t.Errorf("got name %q, want %q", meta.Name, "test-box")
	}
}

func TestMetadataServiceLoggingMiddleware(t *testing.T) {
	// Capture log output using our tracing handler
	var buf bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&buf, nil)
	tracingHandler := tracing.NewHandler(jsonHandler)
	log := slog.New(tracingHandler)

	svc, err := NewService(log, &mockInstanceLookup{}, "http://localhost:8080", "127.0.0.1:18080", []string{".int.exe.cloud"}, "", false, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Create a request using the middleware directly (not starting the actual server)
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", svc.handleRoot)
	handler := svc.loggerMiddleware(mux)

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", w.Code, http.StatusOK)
	}

	// Parse log output (should be a single JSON line)
	var logEntry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("failed to parse log output: %v\nLog output: %s", err, buf.String())
	}

	// Verify trace_id is present
	if _, ok := logEntry["trace_id"]; !ok {
		t.Errorf("trace_id not found in log. Log: %v", logEntry)
	}

	// Verify log_type is present
	if logType, ok := logEntry["log_type"]; !ok {
		t.Errorf("log_type not found in log. Log: %v", logEntry)
	} else if logType != "http_request" {
		t.Errorf("expected log_type=http_request, got %v", logType)
	}

	// Verify method is present
	if method, ok := logEntry["method"]; !ok {
		t.Errorf("method not found in log. Log: %v", logEntry)
	} else if method != "GET" {
		t.Errorf("expected method=GET, got %v", method)
	}

	// Verify uri is present
	if uri, ok := logEntry["uri"]; !ok {
		t.Errorf("uri not found in log. Log: %v", logEntry)
	} else if uri != "/" {
		t.Errorf("expected uri=/, got %v", uri)
	}

	// Verify remote_ip is present
	if remoteIP, ok := logEntry["remote_ip"]; !ok {
		t.Errorf("remote_ip not found in log. Log: %v", logEntry)
	} else if remoteIP != "10.0.0.1" {
		t.Errorf("expected remote_ip=10.0.0.1, got %v", remoteIP)
	}

	// Verify vm_name is present (from mockInstanceLookup)
	if vmName, ok := logEntry["vm_name"]; !ok {
		t.Errorf("vm_name not found in log. Log: %v", logEntry)
	} else if vmName != "test-box" {
		t.Errorf("expected vm_name=test-box, got %v", vmName)
	}
}

func TestMetadataServiceEmailProxyHandler(t *testing.T) {
	log := slog.Default()

	// Start a mock backend that receives the proxied request
	var receivedBoxHeader string
	var receivedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBoxHeader = r.Header.Get("X-Exedev-Box")
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true}`))
	}))
	defer backend.Close()

	svc, err := NewService(log, &mockInstanceLookup{}, backend.URL, "127.0.0.1:18080", []string{".int.exe.cloud"}, "", false, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Test the handler directly using httptest.NewRequest
	t.Run("POST email send proxies with correct headers", func(t *testing.T) {
		body := bytes.NewBufferString(`{"to":"test@example.com","subject":"Test","body":"Hello"}`)
		req := httptest.NewRequest("POST", "/gateway/email/send", body)
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "10.0.0.1:12345"
		w := httptest.NewRecorder()

		svc.handleEmailProxy(w, req)

		// Expect 200 from the mock backend
		if w.Code != http.StatusOK {
			t.Errorf("got status %d, want %d, body: %s", w.Code, http.StatusOK, w.Body.String())
		}

		// Verify the proxy set the correct headers
		if receivedBoxHeader != "test-box" {
			t.Errorf("got X-Exedev-Box header %q, want %q", receivedBoxHeader, "test-box")
		}

		// Verify the path was rewritten correctly
		if receivedPath != "/_/gateway/email/send" {
			t.Errorf("got path %q, want %q", receivedPath, "/_/gateway/email/send")
		}
	})
}

func TestMetadataServiceEmailProxyNoBox(t *testing.T) {
	log := slog.Default()

	// Mock that returns empty box name (box not found)
	emptyLookup := &emptyInstanceLookup{}

	svc, err := NewService(log, emptyLookup, "http://localhost:8080", "127.0.0.1:18080", []string{".int.exe.cloud"}, "", false, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	body := bytes.NewBufferString(`{"to":"test@example.com","subject":"Test","body":"Hello"}`)
	req := httptest.NewRequest("POST", "/gateway/email/send", body)
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.1:12345"
	w := httptest.NewRecorder()

	svc.handleEmailProxy(w, req)

	// Expect Forbidden because no box was found for the IP
	if w.Code != http.StatusForbidden {
		t.Errorf("got status %d, want %d, body: %s", w.Code, http.StatusForbidden, w.Body.String())
	}
}

type emptyInstanceLookup struct{}

func (m *emptyInstanceLookup) GetInstanceByIP(ctx context.Context, ip string) (id, name string, err error) {
	return "", "", nil // No box found
}

func TestMetadataServiceTraceIDIsUnique(t *testing.T) {
	seen := make(map[string]bool)

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	svc, err := NewService(log, &mockInstanceLookup{}, "http://localhost:8080", "127.0.0.1:18080", []string{".int.exe.cloud"}, "", false, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	// Create an inner handler that captures trace_id
	captureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := tracing.TraceIDFromContext(r.Context())
		if seen[traceID] {
			t.Errorf("loggerMiddleware generated duplicate trace_id: %s", traceID)
		}
		seen[traceID] = true
		svc.handleRoot(w, r)
	})
	testHandler := svc.loggerMiddleware(captureHandler)

	for range 100 {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		testHandler.ServeHTTP(w, req)
	}

	if len(seen) != 100 {
		t.Errorf("expected 100 unique trace_ids, got %d", len(seen))
	}
}

func TestIntegrationHostName(t *testing.T) {
	svc := &Service{integrationHostSuffixes: []string{".int.exe.cloud"}}
	tests := []struct {
		host     string
		wantName string
		wantOK   bool
	}{
		{"myproxy.int.exe.cloud", "myproxy", true},
		{"myproxy.int.exe.cloud:80", "myproxy", true},
		{"my-proxy.int.exe.cloud", "my-proxy", true},
		{"test123.int.exe.cloud:8080", "test123", true},
		{".int.exe.cloud", "", false},     // empty name
		{"int.exe.cloud", "", false},      // no subdomain
		{"example.com", "", false},        // wrong domain
		{"169.254.169.254", "", false},    // IP address
		{"169.254.169.254:80", "", false}, // IP with port
		{"", "", false},                   // empty
	}
	for _, tt := range tests {
		gotName, gotOK := svc.integrationHostName(tt.host)
		if gotName != tt.wantName || gotOK != tt.wantOK {
			t.Errorf("integrationHostName(%q) = (%q, %v), want (%q, %v)",
				tt.host, gotName, gotOK, tt.wantName, tt.wantOK)
		}
	}
}

func TestIntegrationHostNameMultipleSuffixes(t *testing.T) {
	svc := &Service{integrationHostSuffixes: []string{".int.exe.xyz", ".int.exe.cloud"}}
	tests := []struct {
		host     string
		wantName string
		wantOK   bool
	}{
		{"myproxy.int.exe.xyz", "myproxy", true},
		{"myproxy.int.exe.xyz:443", "myproxy", true},
		{"myproxy.int.exe.cloud", "myproxy", true},    // legacy suffix still works
		{"myproxy.int.exe.cloud:80", "myproxy", true}, // legacy with port
		{"example.com", "", false},
	}
	for _, tt := range tests {
		gotName, gotOK := svc.integrationHostName(tt.host)
		if gotName != tt.wantName || gotOK != tt.wantOK {
			t.Errorf("integrationHostName(%q) = (%q, %v), want (%q, %v)",
				tt.host, gotName, gotOK, tt.wantName, tt.wantOK)
		}
	}
}

func TestMetadataServiceIntegrationProxy(t *testing.T) {
	// Fake exed that serves /_/integration-config with the generic proxy format.
	fakeExed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_/integration-config" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("vm_name") == "" {
			t.Error("expected vm_name query parameter")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"target":  "https://httpbin.org/anything",
			"headers": map[string]string{"X-Custom-Auth": "secret123"},
		})
	}))
	defer fakeExed.Close()

	log := slog.Default()
	// Use gatewayDev=true because we're running in a test environment where
	// outbound connections go through private interfaces.
	svc, err := NewService(log, &mockInstanceLookup{}, fakeExed.URL, "127.0.0.1:0", []string{".int.exe.cloud"}, "", true, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("failed to start service: %v", err)
	}
	defer svc.Stop(context.Background())

	// This test verifies the handler logic (config lookup, routing, type dispatch)
	// and the actual proxy to httpbin.org (public IP, passes dial guard).
	req := httptest.NewRequest("GET", "http://myproxy.int.exe.cloud/some/path", nil)
	req.Host = "myproxy.int.exe.cloud"
	req.RemoteAddr = "10.42.0.2:12345"
	rr := httptest.NewRecorder()
	svc.server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify the response is from httpbin and includes our injected header.
	var result map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err == nil {
		if headers, ok := result["headers"].(map[string]any); ok {
			if got := headers["X-Custom-Auth"]; got != "secret123" {
				t.Errorf("expected X-Custom-Auth=secret123, got %v", got)
			}
		}
	}
}

func TestMetadataServiceIntegrationProxyPathFilter(t *testing.T) {
	// Fake exed that serves /_/integration-config with allowed_path_prefixes.
	fakeExed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_/integration-config" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok":                    true,
			"target":                "https://github.com",
			"basic_auth":            map[string]string{"user": "x-access-token", "pass": "test-token"},
			"allowed_path_prefixes": []string{"/owner/repo"},
		})
	}))
	defer fakeExed.Close()

	log := slog.Default()
	svc, err := NewService(log, &mockInstanceLookup{}, fakeExed.URL, "127.0.0.1:0", []string{".int.exe.cloud"}, "", true, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("failed to start service: %v", err)
	}
	defer svc.Stop(context.Background())

	// Paths that should be blocked (403) by our filter.
	blocked := []string{
		"/",
		"/other/path",
		"/owner",
		"/owner/repo",
		"/owner/repo/anything",
		"/owner/repo-other",
		"/owner/repo-other.git/info/refs",
	}
	for _, path := range blocked {
		t.Run("blocked:"+path, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://myproxy.int.exe.cloud"+path, nil)
			req.Host = "myproxy.int.exe.cloud"
			req.RemoteAddr = "10.42.0.2:12345"
			rr := httptest.NewRecorder()
			svc.server.Handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Errorf("path %s: got %d, want 403: %s", path, rr.Code, rr.Body.String())
			}
		})
	}

	// Git paths that match a configured repo should NOT be blocked by our filter.
	// They'll be proxied to github.com and may get various responses from
	// upstream, but crucially not our path-filter 403.
	const ourForbiddenMsg = "path does not match any configured repository"
	allowed := []string{"/owner/repo.git/info/refs", "/owner/repo.git/git-upload-pack"}
	for _, path := range allowed {
		t.Run("allowed:"+path, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://myproxy.int.exe.cloud"+path, nil)
			req.Host = "myproxy.int.exe.cloud"
			req.RemoteAddr = "10.42.0.2:12345"
			rr := httptest.NewRecorder()
			svc.server.Handler.ServeHTTP(rr, req)

			body := rr.Body.String()
			if strings.Contains(body, ourForbiddenMsg) {
				t.Errorf("path %s: blocked by path filter (should have been proxied)", path)
			}
		})
	}
}

func TestMetadataServiceIntegrationProxyNotFound(t *testing.T) {
	// Fake exed returns ok=false (integration not found).
	fakeExed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"ok": false})
	}))
	defer fakeExed.Close()

	log := slog.Default()
	svc, err := NewService(log, &mockInstanceLookup{}, fakeExed.URL, "127.0.0.1:0", []string{".int.exe.cloud"}, "", false, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("failed to start service: %v", err)
	}
	defer svc.Stop(context.Background())

	req := httptest.NewRequest("GET", "http://myproxy.int.exe.cloud/", nil)
	req.Host = "myproxy.int.exe.cloud"
	req.RemoteAddr = "10.42.0.2:12345"
	rr := httptest.NewRecorder()
	svc.server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 for not-found integration, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMetadataServiceIntegrationProxyNoBox(t *testing.T) {
	log := slog.Default()
	lookup := &failingInstanceLookup{}
	svc, err := NewService(log, lookup, "http://localhost:9999", "127.0.0.1:0", []string{".int.exe.cloud"}, "", false, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("failed to start service: %v", err)
	}
	defer svc.Stop(context.Background())

	req := httptest.NewRequest("GET", "http://myproxy.int.exe.cloud/", nil)
	req.Host = "myproxy.int.exe.cloud"
	req.RemoteAddr = "10.42.0.99:12345"
	rr := httptest.NewRecorder()
	svc.server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 for unknown box, got %d: %s", rr.Code, rr.Body.String())
	}
}

// failingInstanceLookup always returns an error.
type failingInstanceLookup struct{}

func (f *failingInstanceLookup) GetInstanceByIP(ctx context.Context, ip string) (id, name string, err error) {
	return "", "", context.DeadlineExceeded
}

func TestPathMatchesPrefixes(t *testing.T) {
	prefixes := []string{"/owner/repo", "/org/other-repo"}

	allowed := []string{
		"/owner/repo.git",
		"/owner/repo.git/",
		"/owner/repo.git/info/refs",
		"/owner/repo.git/git-upload-pack",
		"/owner/repo.git/git-receive-pack",
		"/org/other-repo.git",
		"/org/other-repo.git/info/refs",
	}
	for _, path := range allowed {
		if !pathMatchesPrefixes(path, prefixes) {
			t.Errorf("pathMatchesPrefixes(%q, ...) = false, want true", path)
		}
	}

	blocked := []string{
		"/",
		"/owner",
		"/owner/",
		"/owner/repo",
		"/owner/repo/",
		"/owner/repo/anything",
		"/owner/repo-other",
		"/owner/repo-other.git/info/refs",
		"/other/repo",
		"/owner/repoextra",
		"/owner/repo.gitfoo",
	}
	for _, path := range blocked {
		if pathMatchesPrefixes(path, prefixes) {
			t.Errorf("pathMatchesPrefixes(%q, ...) = true, want false", path)
		}
	}
}

func TestPathMatchesPrefixesEmpty(t *testing.T) {
	// Empty prefixes should match nothing.
	if pathMatchesPrefixes("/anything", nil) {
		t.Error("nil prefixes should match nothing")
	}
	if pathMatchesPrefixes("/anything", []string{}) {
		t.Error("empty prefixes should match nothing")
	}
}

func TestIsValidIntegrationName(t *testing.T) {
	good := []string{"a", "myproxy", "my-proxy", "a1", "test-123"}
	for _, name := range good {
		if !isValidIntegrationName(name) {
			t.Errorf("isValidIntegrationName(%q) = false, want true", name)
		}
	}

	bad := []string{
		"", "-start", "end-", "UPPER", "has space", "has.dot",
		"has_underscore", "a/b",
		string(make([]byte, 64)), // 64 chars
	}
	for _, name := range bad {
		if isValidIntegrationName(name) {
			t.Errorf("isValidIntegrationName(%q) = true, want false", name)
		}
	}
}

func TestIsPrivateIP(t *testing.T) {
	private := []string{
		"127.0.0.1",       // loopback
		"10.0.0.1",        // RFC1918
		"192.168.1.1",     // RFC1918
		"172.16.0.1",      // RFC1918
		"169.254.169.254", // link-local
		"100.100.100.100", // CGNAT/Tailscale
		"0.0.0.0",         // unspecified
	}
	for _, s := range private {
		ip := netip.MustParseAddr(s)
		if !isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%s) = false, want true", s)
		}
	}

	public := []string{
		"8.8.8.8",
		"1.1.1.1",
		"52.0.0.1",
	}
	for _, s := range public {
		ip := netip.MustParseAddr(s)
		if isPrivateIP(ip) {
			t.Errorf("isPrivateIP(%s) = true, want false", s)
		}
	}
}

func TestCheckLocalAddr(t *testing.T) {
	prodService := &Service{gatewayDev: false}
	devService := &Service{gatewayDev: true}

	tests := []struct {
		name     string
		localIP  string
		wantProd bool // true = error expected in production
		wantDev  bool // true = error expected in dev
	}{
		{"loopback", "127.0.0.1", false, true},
		{"private-192", "192.168.1.50", false, false},
		{"private-10", "10.0.0.5", false, false},
		{"private-172", "172.16.0.1", false, false},
		{"tailscale", "100.100.100.100", true, false},
		{"link-local", "169.254.1.1", false, false},
		{"public", "8.8.8.8", false, false},
		{"public-other", "52.1.2.3", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := netip.MustParseAddr(tt.localIP)
			conn := &fakeConn{localIP: ip}

			errProd := prodService.checkLocalAddr(conn)
			if (errProd != nil) != tt.wantProd {
				t.Errorf("prod: checkLocalAddr(%s) error=%v, wantErr=%v", tt.localIP, errProd, tt.wantProd)
			}

			errDev := devService.checkLocalAddr(conn)
			if (errDev != nil) != tt.wantDev {
				t.Errorf("dev: checkLocalAddr(%s) error=%v, wantErr=%v", tt.localIP, errDev, tt.wantDev)
			}
		})
	}
}

// fakeConn implements net.Conn with a controllable LocalAddr.
type fakeConn struct {
	net.Conn
	localIP netip.Addr
}

func (f *fakeConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: f.localIP.AsSlice(), Port: 12345}
}

func TestMetadataServiceGatewayIntegration(t *testing.T) {
	// Fake exed that serves /_/integration-config with a gateway_path response,
	// and handles the forwarded gateway request.
	var gotBoxHeader string
	var gotPath string
	fakeExed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_/integration-config":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"ok":           true,
				"gateway_path": "/_/gateway/push/send",
			})
		case "/_/gateway/push/send":
			gotBoxHeader = r.Header.Get("X-Exedev-Box")
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"success": true, "sent": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeExed.Close()

	log := slog.Default()
	svc, err := NewService(log, &mockInstanceLookup{}, fakeExed.URL, "127.0.0.1:0", []string{".int.exe.cloud"}, "", true, nil)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("failed to start service: %v", err)
	}
	defer svc.Stop(context.Background())

	// Send a POST to notify.int.exe.cloud/ which should be forwarded to exed.
	reqBody := `{"title":"Test","body":"Hello"}`
	req := httptest.NewRequest("POST", "http://notify.int.exe.cloud/", strings.NewReader(reqBody))
	req.Host = "notify.int.exe.cloud"
	req.RemoteAddr = "10.42.0.2:12345"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	svc.server.Handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify the request was forwarded with the correct box header.
	if gotBoxHeader != "test-box" {
		t.Errorf("expected X-Exedev-Box=test-box, got %q", gotBoxHeader)
	}
	if gotPath != "/_/gateway/push/send" {
		t.Errorf("expected path /_/gateway/push/send, got %q", gotPath)
	}

	// Verify the response was forwarded back.
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}
}
