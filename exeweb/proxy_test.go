package exeweb

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"exe.dev/email"
	"exe.dev/stage"

	"github.com/prometheus/client_golang/prometheus"
	prometheusclient "github.com/prometheus/client_model/go"
)

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
			ps := &ProxyServer{ProxyHTTPPort: tc.serverPort}

			result := ps.isDefaultServerPort(tc.testPort)
			if result != tc.expected {
				t.Errorf("Expected %v for port %d (server on %d), got %v\nComment: %s",
					tc.expected, tc.testPort, tc.serverPort, result, tc.comment)
			} else {
				t.Logf("✓ %s: port=%d serverPort=%d -> %v", tc.comment, tc.testPort, tc.serverPort, result)
			}
		})
	}

	// Test case where httpPort is 0
	t.Run("nil httpLn", func(t *testing.T) {
		ps := &ProxyServer{ProxyHTTPPort: 0}
		// Should only return true for 443
		if !ps.isDefaultServerPort(443) {
			t.Error("Expected true for port 443 even with nil httpLn")
		}
		if ps.isDefaultServerPort(8080) {
			t.Error("Expected false for port 8080 with nil httpLn")
		}
	})
}

// TestOpenRedirectInAuthFlow tests that redirect URLs are validated
// to prevent open redirect attacks.
func TestOpenRedirectInAuthFlow(t *testing.T) {
	tests := []struct {
		name        string
		redirectURL string
		shouldBlock bool
	}{
		{"relative path", "/dashboard", false},
		{"relative path with query", "/box?id=123", false},
		{"absolute external URL", "https://evil.com/phish", true},
		{"protocol-relative URL", "//evil.com/phish", true},
		{"javascript URL", "javascript:alert(1)", true},
		{"data URL", "data:text/html,<script>alert(1)</script>", true},
		{"external with subdomain trick", "https://exe.dev.evil.com", true},
		{"empty string", "", true},
		{"relative path without leading slash", "dashboard", true},
		{"path traversal attempt", "/../evil.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			valid := IsValidRedirectURL(tt.redirectURL)
			if tt.shouldBlock && valid {
				t.Errorf("isValidRedirectURL(%q) = true, want false (should block)", tt.redirectURL)
			}
			if !tt.shouldBlock && !valid {
				t.Errorf("isValidRedirectURL(%q) = false, want true (should allow)", tt.redirectURL)
			}
		})
	}
}

func TestStripExeDevAuth(t *testing.T) {
	t.Parallel()

	makeBasicAuth := func(user, pass string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}

	tests := []struct {
		name    string
		auth    string
		strip   bool
		comment string
	}{
		{
			name:    "bearer_exe0",
			auth:    "Bearer exe0.payload.sig",
			strip:   true,
			comment: "exe token bearer auth should be stripped",
		},
		{
			name:    "bearer_exe0_prefix_only",
			auth:    "Bearer exe0.",
			strip:   true,
			comment: "exe token prefix with empty body should be stripped",
		},
		{
			name:    "bearer_non_exe0",
			auth:    "Bearer abc.def.ghi",
			strip:   false,
			comment: "non-exe bearer auth should be forwarded",
		},
		{
			name:    "bearer_jwt",
			auth:    "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig",
			strip:   false,
			comment: "JWT bearer auth should be forwarded",
		},
		{
			name:    "bearer_mixed_case_scheme",
			auth:    "bEaReR exe0.payload.sig",
			strip:   true,
			comment: "bearer scheme is case-insensitive",
		},
		{
			name:    "basic_exe0_password",
			auth:    makeBasicAuth("anyuser", "exe0.payload.sig"),
			strip:   true,
			comment: "exe token basic password should be stripped",
		},
		{
			name:    "basic_non_exe0_password",
			auth:    makeBasicAuth("anyuser", "hunter2"),
			strip:   false,
			comment: "non-exe basic auth should be forwarded",
		},
		{
			name:    "bearer_exeapp",
			auth:    "Bearer exeapp_dGVzdHRva2VuZGF0YQ",
			strip:   true,
			comment: "app token bearer auth should be stripped",
		},
		{
			name:    "bearer_exeapp_prefix_only",
			auth:    "Bearer exeapp_",
			strip:   true,
			comment: "app token prefix with empty body should be stripped",
		},
		{
			name:    "basic_exeapp_password",
			auth:    makeBasicAuth("anyuser", "exeapp_dGVzdHRva2VuZGF0YQ"),
			strip:   true,
			comment: "app token basic password should be stripped",
		},
		{
			name:    "basic_mixed_case_scheme",
			auth:    "bAsIc " + base64.StdEncoding.EncodeToString([]byte("user:exe0.payload.sig")),
			strip:   true,
			comment: "basic scheme is case-insensitive",
		},
		{
			name:    "basic_invalid_base64",
			auth:    "Basic !!!notbase64!!!",
			strip:   false,
			comment: "invalid basic header should not be stripped",
		},
		{
			name:    "bearer_exe1",
			auth:    "Bearer exe1.opaque-token-value",
			strip:   true,
			comment: "exe1 token bearer auth should be stripped",
		},
		{
			name:    "bearer_exe1_prefix_only",
			auth:    "Bearer exe1.",
			strip:   true,
			comment: "exe1 token prefix with empty body should be stripped",
		},
		{
			name:    "basic_exe1_password",
			auth:    makeBasicAuth("anyuser", "exe1.opaque-token-value"),
			strip:   true,
			comment: "exe1 token basic password should be stripped",
		},
		{
			name:    "digest_scheme",
			auth:    `Digest username="admin", realm="test"`,
			strip:   false,
			comment: "non-bearer/basic schemes should be forwarded",
		},
		{
			name:    "no_auth",
			auth:    "",
			strip:   false,
			comment: "no Authorization header should remain absent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := make(http.Header)
			if tt.auth != "" {
				h.Set("Authorization", tt.auth)
			}
			req := &http.Request{Header: h}
			stripExeDevAuth(req)

			got := req.Header.Get("Authorization")
			if tt.strip {
				if got != "" {
					t.Fatalf("expected Authorization to be stripped; got %q (%s)", got, tt.comment)
				}
				return
			}
			if tt.auth == "" {
				if got != "" {
					t.Fatalf("expected Authorization to be absent; got %q (%s)", got, tt.comment)
				}
				return
			}
			if got != tt.auth {
				t.Fatalf("expected Authorization preserved as %q; got %q (%s)", tt.auth, got, tt.comment)
			}
		})
	}
}

func TestNonProxyRedirect(t *testing.T) {
	t.Parallel()

	testEnv := stage.Test()

	testCases := []struct {
		name   string
		url    string
		expect string
	}{
		{
			name:   "exe.new redirects to /new",
			url:    "http://exe.new",
			expect: "http://" + testEnv.WebHost + "/new",
		},
		{
			name:   "exe.new with path redirects with idea shortname",
			url:    "http://exe.new/foo",
			expect: "http://" + testEnv.WebHost + "/new?idea=foo",
		},
		{
			name:   "exe.new with port redirects",
			url:    "http://exe.new:443",
			expect: "http://" + testEnv.WebHost + "/new",
		},
		{
			name:   "exe.new/moltbot redirects with idea (alias)",
			url:    "http://exe.new/moltbot",
			expect: "http://" + testEnv.WebHost + "/new?idea=openclaw",
		},
		{
			name:   "exe.new/clawdbot redirects with idea (alias)",
			url:    "http://exe.new/clawdbot",
			expect: "http://" + testEnv.WebHost + "/new?idea=openclaw",
		},
		{
			name:   "exe.new/moltbot with invite passes through invite",
			url:    "http://exe.new/moltbot?invite=TESTCODE",
			expect: "http://" + testEnv.WebHost + "/new?idea=openclaw&invite=TESTCODE",
		},
		{
			name:   "exe.new/clawdbot with invite passes through invite",
			url:    "http://exe.new/clawdbot?invite=TESTCODE",
			expect: "http://" + testEnv.WebHost + "/new?idea=openclaw&invite=TESTCODE",
		},
		{
			name:   "exe.new/openclaw redirects with idea",
			url:    "http://exe.new/openclaw",
			expect: "http://" + testEnv.WebHost + "/new?idea=openclaw",
		},
		{
			name:   "exe.new/openclaw with invite passes through invite",
			url:    "http://exe.new/openclaw?invite=TESTCODE",
			expect: "http://" + testEnv.WebHost + "/new?idea=openclaw&invite=TESTCODE",
		},
		{
			name:   "exe.new with invite but no prompt",
			url:    "http://exe.new/?invite=TESTCODE",
			expect: "http://" + testEnv.WebHost + "/new?invite=TESTCODE",
		},
		{
			name:   "WebHost does not redirect",
			url:    "http://" + testEnv.WebHost + "/new",
			expect: "",
		},
		{
			name:   "other domain does not redirect",
			url:    "http://other.test",
			expect: "",
		},
	}

	for _, tc := range testCases {
		r, err := http.NewRequest("GET", tc.url, bytes.NewReader(nil))
		if err != nil {
			t.Errorf("http.NewRequest(%q) failed: %v", tc.url, err)
			continue
		}
		target := NonProxyRedirect(&testEnv, r)
		if target != tc.expect {
			t.Errorf(`%s: NonProxyRedirect("test", %q) = %q, want %q`, tc.name, tc.url, target, tc.expect)
		}
	}
}

// TestIsProxyRequest tests the IsProxyRequest function
// with comprehensive cases.
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
			name:     "xterm subdomain",
			env:      stage.Test(),
			host:     "mybox.xterm.exe.cloud:8080",
			expected: false,
			comment:  "Should recognize xterm",
		},
		{
			name:     "shelley subdomain",
			env:      stage.Test(),
			host:     "mybox.shelley.exe.cloud:8080",
			expected: true,
			comment:  "Shelley subdomain is a proxy request (proxies to port 9999)",
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
			host:     "mybox.exe.xyz",
			expected: true,
			comment:  "Should recognize *.exe.xyz (BoxHost) pattern in production",
		},
		{
			name:     "prod subdomain with server port",
			env:      stage.Prod(),
			host:     "mybox.exe.xyz:443",
			expected: true,
			comment:  "Should recognize *.exe.xyz even with server port",
		},
		{
			name:     "blog subdomain should proxy",
			env:      stage.Prod(),
			host:     "blog.exe.dev",
			expected: true,
			comment:  "blog.exe.dev is served from a VM even though it's under WebHost",
		},
		{
			name:     "prod WebHost subdomain should not be proxy",
			env:      stage.Prod(),
			host:     "mybox.exe.dev",
			expected: false,
			comment:  "Subdomains of WebHost (exe.dev) should not be proxy requests",
		},
		{
			name:     "exe.dev alone in prod mode",
			env:      stage.Prod(),
			host:     "exe.dev",
			expected: false,
			comment:  "Plain exe.dev should not be proxy request",
		},

		// Cross-mode cases: requests to "foreign" box domains go to proxy (which will 404)
		{
			name:     "prod BoxHost in dev mode",
			env:      stage.Test(),
			host:     "mybox.exe.xyz",
			expected: true,
			comment:  "Prod BoxHost subdomains are proxied in dev (not excluded)",
		},
		{
			name:     "dev BoxHost in prod mode",
			env:      stage.Prod(),
			host:     "mybox.exe.cloud",
			expected: true,
			comment:  "Dev BoxHost subdomains are proxied in prod (not excluded)",
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
			result := IsProxyRequest(&tc.env, "", tc.host)
			if result != tc.expected {
				t.Errorf("IsProxyRequest(%q, %q, %q) = %t, want %t\nComment %s", tc.env.String(), "", tc.host, result, tc.expected, tc.comment)
			} else {
				t.Logf("✓ %s: host=%q stage=%s -> %v", tc.comment, tc.host, tc.env.String(), result)
			}
		})
	}
}

// TestCountingConn tests that the countingConn wrapper
// correctly tracks bytes read and written.
func TestCountingConn(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	metrics := NewHTTPMetrics(registry)

	// Create a pipe to simulate a connection
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Wrap the client side with countingConn
	wrapped := &countingConn{Conn: client, metrics: metrics}

	// Write some data through the wrapped connection
	testData := []byte("hello, world!")
	writeDone := make(chan struct{})
	go func() {
		wrapped.Write(testData)
		close(writeDone)
	}()

	// Read from the server side
	buf := make([]byte, len(testData))
	_, err := io.ReadFull(server, buf)
	if err != nil {
		t.Fatalf("failed to read from server: %v", err)
	}
	if !bytes.Equal(buf, testData) {
		t.Fatalf("data mismatch: got %q, want %q", buf, testData)
	}

	// Wait for the write goroutine to complete
	<-writeDone

	// Now write from server and read through wrapped connection
	responseData := []byte("response data")
	go func() {
		server.Write(responseData)
	}()

	buf = make([]byte, len(responseData))
	_, err = io.ReadFull(wrapped, buf)
	if err != nil {
		t.Fatalf("failed to read from wrapped: %v", err)
	}
	if !bytes.Equal(buf, responseData) {
		t.Fatalf("data mismatch: got %q, want %q", buf, responseData)
	}

	// Verify metrics were recorded
	// "out" is bytes written through the wrapped connection (to the backend)
	// "in" is bytes read through the wrapped connection (from the backend)
	outMetric := getCounterValue(t, metrics.ProxyBytesTotal.WithLabelValues("out"))
	inMetric := getCounterValue(t, metrics.ProxyBytesTotal.WithLabelValues("in"))

	if outMetric != float64(len(testData)) {
		t.Errorf("out bytes: got %v, want %v", outMetric, len(testData))
	}
	if inMetric != float64(len(responseData)) {
		t.Errorf("in bytes: got %v, want %v", inMetric, len(responseData))
	}
}

func getCounterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	counter.Collect(ch)
	m := <-ch
	metric := &prometheusclient.Metric{}
	m.Write(metric)
	return metric.Counter.GetValue()
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

func TestClearExeDevHeaders(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "https://box.exe.dev/", nil)
	req.Header.Set("X-ExeDev-UserID", "spoofed-user")
	req.Header.Set("X-ExeDev-Email", "spoofed@example.com")
	req.Header.Add("X-ExeDev-Email", "second@example.com")
	req.Header.Set("X-ExeDev-Arbitrary", "arbitrary-value")
	req.Header.Set("X-ExeDev-Future-Header", "future-value")
	req.Header.Set("x-exedev-lowercase", "lowercase-value") // test case insensitivity
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Custom-Header", "custom-value")

	clearExeDevHeaders(req)

	if got := req.Header.Get("X-ExeDev-UserID"); got != "" {
		t.Fatalf("expected user header cleared, got %q", got)
	}
	if got := req.Header.Get("X-ExeDev-Email"); got != "" {
		t.Fatalf("expected email header cleared, got %q", got)
	}
	if got := req.Header.Get("X-ExeDev-Arbitrary"); got != "" {
		t.Fatalf("expected arbitrary X-ExeDev header cleared, got %q", got)
	}
	if got := req.Header.Get("X-ExeDev-Future-Header"); got != "" {
		t.Fatalf("expected future X-ExeDev header cleared, got %q", got)
	}
	if got := req.Header.Get("x-exedev-lowercase"); got != "" {
		t.Fatalf("expected lowercase X-ExeDev header cleared, got %q", got)
	}
	if got := req.Header.Get("X-Forwarded-Proto"); got != "https" {
		t.Fatalf("expected unrelated headers untouched, got %q", got)
	}
	if got := req.Header.Get("X-Custom-Header"); got != "custom-value" {
		t.Fatalf("expected other X- headers untouched, got %q", got)
	}
}

// mockProxyData is a minimal ProxyData + AppTokenValidator for unit tests.
type mockProxyData struct {
	appTokens map[string]string // token -> userID
	cookies   map[string]CookieData
}

func (m *mockProxyData) BoxInfo(ctx context.Context, boxName string) (BoxData, bool, error) {
	return BoxData{}, false, nil
}

func (m *mockProxyData) CookieInfo(ctx context.Context, cookieValue, domain string) (CookieData, bool, error) {
	key := cookieValue + "@" + domain
	if cd, ok := m.cookies[key]; ok {
		return cd, true, nil
	}
	return CookieData{}, false, nil
}

func (m *mockProxyData) UserInfo(ctx context.Context, userID string) (UserData, bool, error) {
	return UserData{UserID: userID}, true, nil
}

func (m *mockProxyData) IsUserLockedOut(ctx context.Context, userID string) (bool, error) {
	return false, nil
}

func (m *mockProxyData) UserHasExeSudo(ctx context.Context, userID string) (bool, error) {
	return false, nil
}

func (m *mockProxyData) CreateAuthCookie(ctx context.Context, userID, domain string) (string, error) {
	return "", nil
}
func (m *mockProxyData) DeleteAuthCookie(ctx context.Context, cookieValue string) error { return nil }
func (m *mockProxyData) UsedCookie(ctx context.Context, cookieValue string)             {}
func (m *mockProxyData) HasUserAccessToBox(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	return false, nil
}

func (m *mockProxyData) IsBoxSharedWithUserTeam(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	return false, nil
}

func (m *mockProxyData) IsBoxShelleySharedWithTeamMember(ctx context.Context, boxID int, boxName, userID string) (bool, error) {
	return false, nil
}

func (m *mockProxyData) CheckShareLink(ctx context.Context, boxID int, boxName, userID, shareToken string) (bool, error) {
	return false, nil
}

func (m *mockProxyData) ValidateMagicSecret(ctx context.Context, secret string) (string, string, string, error) {
	return "", "", "", nil
}

func (m *mockProxyData) GetSSHKeyByFingerprint(ctx context.Context, fingerprint string) (string, string, error) {
	return "", "", nil
}
func (m *mockProxyData) HLLNoteEvents(ctx context.Context, userID string, events []string) {}
func (m *mockProxyData) CheckAndIncrementEmailQuota(ctx context.Context, userID string) error {
	return nil
}

func (m *mockProxyData) SendEmail(ctx context.Context, req email.SendRequest) error {
	return nil
}

func (m *mockProxyData) CheckAndDebitVMEmailCredit(ctx context.Context, boxID int) error {
	return nil
}

func (m *mockProxyData) ResolveExe1Token(ctx context.Context, exe1Token string) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (m *mockProxyData) GetPushTokensByUserID(ctx context.Context, userID string) ([]PushTokenData, error) {
	return nil, nil
}

func (m *mockProxyData) DeletePushToken(ctx context.Context, token, userID string) error {
	return nil
}

func (m *mockProxyData) ValidateAppToken(ctx context.Context, token string) (string, error) {
	if uid, ok := m.appTokens[token]; ok {
		return uid, nil
	}
	return "", fmt.Errorf("invalid token")
}

func TestGetProxyAuth_AppTokenInCookie(t *testing.T) {
	t.Parallel()

	const (
		testUserID = "user-123"
		testToken  = AppTokenPrefix + "test_token_value"
		boxName    = "mybox"
	)

	mock := &mockProxyData{
		appTokens: map[string]string{testToken: testUserID},
		cookies:   map[string]CookieData{},
	}

	testEnv := stage.Test()
	ps := &ProxyServer{
		Data:           mock,
		Lg:             slog.Default(),
		Env:            &testEnv,
		ProxyHTTPSPort: 443,
	}

	t.Run("app_token_in_cookie_authenticates", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://mybox.exe.xyz/", nil)
		req.Host = "mybox.exe.xyz"
		req.TLS = &tls.ConnectionState{}
		req.AddCookie(&http.Cookie{Name: ProxyAuthCookieName(443), Value: testToken})

		result := ps.GetProxyAuth(req, boxName)
		if result == nil {
			t.Fatal("expected auth result from app token in cookie, got nil")
		}
		if result.UserID != testUserID {
			t.Fatalf("expected userID %q, got %q", testUserID, result.UserID)
		}
	})

	t.Run("invalid_app_token_in_cookie_rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://mybox.exe.xyz/", nil)
		req.Host = "mybox.exe.xyz"
		req.TLS = &tls.ConnectionState{}
		req.AddCookie(&http.Cookie{Name: ProxyAuthCookieName(443), Value: AppTokenPrefix + "bad_token"})

		result := ps.GetProxyAuth(req, boxName)
		if result != nil {
			t.Fatalf("expected nil for invalid app token in cookie, got userID=%q", result.UserID)
		}
	})

	t.Run("non_app_token_cookie_not_treated_as_app_token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://mybox.exe.xyz/", nil)
		req.Host = "mybox.exe.xyz"
		req.TLS = &tls.ConnectionState{}
		req.AddCookie(&http.Cookie{Name: ProxyAuthCookieName(443), Value: "regular-cookie-value"})

		result := ps.GetProxyAuth(req, boxName)
		if result != nil {
			t.Fatalf("expected nil for regular cookie value, got userID=%q", result.UserID)
		}
	})

	t.Run("bearer_still_takes_priority_over_cookie", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://mybox.exe.xyz/", nil)
		req.Host = "mybox.exe.xyz"
		req.TLS = &tls.ConnectionState{}
		req.Header.Set("Authorization", "Bearer "+testToken)
		// Also set a cookie with a different (invalid) app token.
		req.AddCookie(&http.Cookie{Name: ProxyAuthCookieName(443), Value: AppTokenPrefix + "other"})

		result := ps.GetProxyAuth(req, boxName)
		if result == nil {
			t.Fatal("expected auth result from bearer, got nil")
		}
		if result.UserID != testUserID {
			t.Fatalf("expected userID %q from bearer, got %q", testUserID, result.UserID)
		}
	})

	t.Run("db_cookie_takes_priority_over_app_token_cookie", func(t *testing.T) {
		// Add a valid DB cookie.
		dbCookieValue := "db-cookie-123"
		mockWithCookie := &mockProxyData{
			appTokens: map[string]string{testToken: testUserID},
			cookies: map[string]CookieData{
				dbCookieValue + "@mybox.exe.xyz": {
					CookieValue: dbCookieValue,
					Domain:      "mybox.exe.xyz",
					UserID:      "db-user-456",
					ExpiresAt:   time.Now().Add(time.Hour),
				},
			},
		}
		testEnvCookie := stage.Test()
		psWithCookie := &ProxyServer{
			Data:           mockWithCookie,
			Lg:             slog.Default(),
			Env:            &testEnvCookie,
			ProxyHTTPSPort: 443,
		}

		req := httptest.NewRequest(http.MethodGet, "https://mybox.exe.xyz/", nil)
		req.Host = "mybox.exe.xyz"
		req.TLS = &tls.ConnectionState{}
		req.AddCookie(&http.Cookie{Name: ProxyAuthCookieName(443), Value: dbCookieValue})

		result := psWithCookie.GetProxyAuth(req, boxName)
		if result == nil {
			t.Fatal("expected auth result from DB cookie, got nil")
		}
		if result.UserID != "db-user-456" {
			t.Fatalf("expected userID %q from DB cookie, got %q", "db-user-456", result.UserID)
		}
	})

	t.Run("app_token_cookie_with_port", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://mybox.exe.xyz:8080/", nil)
		req.Host = "mybox.exe.xyz:8080"
		req.AddCookie(&http.Cookie{Name: ProxyAuthCookieName(8080), Value: testToken})

		result := ps.GetProxyAuth(req, boxName)
		if result == nil {
			t.Fatal("expected auth result from app token in port-specific cookie, got nil")
		}
		if result.UserID != testUserID {
			t.Fatalf("expected userID %q, got %q", testUserID, result.UserID)
		}
	})
}

func TestRelativeRedirect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "path only", url: "http://example.com/foo/bar", want: "/foo/bar"},
		{name: "path with query", url: "http://example.com/foo?x=1&y=2", want: "/foo?x=1&y=2"},
		{name: "root path", url: "http://example.com/", want: "/"},
		{name: "empty path", url: "http://example.com", want: ""},
		{name: "query only", url: "http://example.com?x=1", want: "?x=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.url)
			if err != nil {
				t.Fatal(err)
			}
			got := RelativeRedirect(u)
			if got != tt.want {
				t.Errorf("RelativeRedirect(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestCookieDomainRe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "basic domain", input: "name=val; Domain=.example.com; Path=/", want: "name=val; Path=/"},
		{name: "domain last", input: "name=val; Path=/; Domain=.example.com", want: "name=val; Path=/"},
		{name: "mixed case", input: "name=val; DOMAIN=.example.com; Path=/", want: "name=val; Path=/"},
		{name: "space around equals", input: "name=val; Domain = .example.com; Path=/", want: "name=val; Path=/"},
		{name: "space before equals", input: "name=val; Domain =.example.com; Path=/", want: "name=val; Path=/"},
		{name: "no domain", input: "name=val; Path=/", want: "name=val; Path=/"},
		{name: "domain only attr", input: "name=val; Domain=.example.com", want: "name=val"},
		{name: "extra whitespace", input: "name=val;  domain=.foo.bar; Path=/", want: "name=val; Path=/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cookieDomainRe.ReplaceAllString(tt.input, "")
			if got != tt.want {
				t.Errorf("cookieDomainRe.ReplaceAllString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStripSetCookieDomain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		host      string
		boxHost   string
		setCookie string
		want      string
	}{
		{
			name:      "shared domain strips domain attr",
			host:      "myvm.exe.xyz",
			boxHost:   "exe.xyz",
			setCookie: "sid=abc; Domain=.exe.xyz; Path=/",
			want:      "sid=abc; Path=/",
		},
		{
			name:      "custom domain preserves domain attr",
			host:      "app.example.com",
			boxHost:   "exe.xyz",
			setCookie: "sid=abc; Domain=.example.com; Path=/",
			want:      "sid=abc; Domain=.example.com; Path=/",
		},
		{
			name:      "custom domain preserves all cookies",
			host:      "mysite.org",
			boxHost:   "exe.xyz",
			setCookie: "token=xyz; Domain=.mysite.org; Secure",
			want:      "token=xyz; Domain=.mysite.org; Secure",
		},
		{
			name:      "shared domain with port strips domain attr",
			host:      "myvm.exe.xyz:8080",
			boxHost:   "exe.xyz",
			setCookie: "sid=abc; Domain=.exe.xyz; Path=/",
			want:      "sid=abc; Path=/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			resp.Header.Set("Set-Cookie", tt.setCookie)
			stripSetCookieDomain(tt.host, tt.boxHost, resp)
			got := resp.Header.Get("Set-Cookie")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
