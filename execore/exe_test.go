package execore

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTokenGeneration(t *testing.T) {
	token1 := generateRegistrationToken()
	token2 := generateRegistrationToken()

	if token1 == token2 {
		t.Error("Generated tokens should be unique")
	}

	if len(token1) == 0 {
		t.Error("Token should not be empty")
	}
}

func TestEmailValidation(t *testing.T) {
	tests := []struct {
		email string
		valid bool
	}{
		{"test@example.com", true},
		{"user@domain.co.uk", true},
		{"user.name+tag@example.com", true},
		{"", false},
		{"invalid", false},
		{"@example.com", false},
		{"test@", false},
		{"test@domain", false},
		// Special characters that should be rejected
		{`'"><h<!;Sad@asdasd.asd`, false},
		{`"test@example.com`, false},
		{`<script>@example.com`, false},
		{`test@example.com>`, false},
		{`test;drop@example.com`, false},
		{"test\n@example.com", false},
		{"test\r@example.com", false},
	}

	for _, tt := range tests {
		result := isValidEmail(tt.email)
		if result != tt.valid {
			t.Errorf("isValidEmail(%q) = %v, want %v", tt.email, result, tt.valid)
		}
	}
}

func TestIsBogusEmailDomain(t *testing.T) {
	tests := []struct {
		email string
		bogus bool
	}{
		// Valid domains
		{"user@gmail.com", false},
		{"user@outlook.com", false},
		{"user@company.org", false},
		{"user@university.edu", false},

		// RFC 2606 reserved domains
		{"user@example.com", true},
		{"user@example.net", true},
		{"user@example.org", true},
		{"user@test.com", true},

		// Common typos of popular domains
		{"user@gmail.co", true},
		{"user@gmial.com", true},
		{"user@gmai.com", true},
		{"user@gamil.com", true},
		{"user@gnail.com", true},
		{"user@gmail.con", true},
		{"user@gmail.om", true},
		{"user@hotmail.co", true},
		{"user@hotmal.com", true},
		{"user@hotmial.com", true},
		{"user@outlok.com", true},
		{"user@outloo.com", true},
		{"user@outlook.co", true},
		{"user@yahooo.com", true},
		{"user@yaho.com", true},
		{"user@yahoo.co", true},
		{"user@icloud.co", true},
		{"user@icoud.com", true},

		// Case insensitive (ParseAddress normalizes to lowercase)
		{"user@EXAMPLE.COM", true},
		{"user@Gmail.Co", true},
	}

	for _, tt := range tests {
		result := isBogusEmailDomain(tt.email)
		if result != tt.bogus {
			t.Errorf("isBogusEmailDomain(%q) = %v, want %v", tt.email, result, tt.bogus)
		}
	}
}

// TestEmailVerificationRequiresPOST tests that email verification requires POST confirmation
func TestEmailVerificationRequiresPOST(t *testing.T) {
	// Create server
	server := newTestServer(t)

	// Create a test user
	email := "test@example.com"
	// Create user with generated user_id
	publicKey := testSSHPubKey
	_, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user : %v", err)
	}

	user, err := server.GetUserByEmail(t.Context(), email)
	if err != nil {
		t.Fatalf("Failed to get user by email: %v", err)
	}

	// Create an email verification token (no verification_code — this is the
	// standard link-based flow, not the app_token code-based flow).
	token := "test-token-" + time.Now().Format("20060102150405")
	expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO email_verifications (token, email, user_id, expires_at)
			VALUES (?, ?, ?, ?)`,
			token, email, user.UserID, expires)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create verification token: %v", err)
	}

	// Test 1: GET request should show form, not complete verification
	req := httptest.NewRequest("GET", "/verify-email?token="+token, nil)
	w := httptest.NewRecorder()
	server.handleEmailVerificationHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET request failed: got status %d, want %d", w.Code, http.StatusOK)
	}

	// Verify token is still valid (not consumed by GET)
	var count int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM email_verifications WHERE token = ?`, token).Scan(&count)
	})
	if err != nil {
		t.Errorf("Error checking token after GET: %v", err)
	}
	if count != 1 {
		t.Errorf("GET request should not consume the verification token, count = %d", count)
	}

	// Test 2: POST request should complete verification
	form := url.Values{}
	form.Add("token", token)
	req = httptest.NewRequest("POST", "/verify-email", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	server.handleEmailVerificationHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST request failed: got status %d, want %d", w.Code, http.StatusOK)
	}

	// Verify token is consumed
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM email_verifications WHERE token = ?`, token).Scan(&count)
	})
	if err != nil || count != 0 {
		t.Error("POST request should consume the verification token")
	}

	// Test 3: Invalid token should show error (401 with branded page)
	req = httptest.NewRequest("GET", "/verify-email?token=invalid", nil)
	w = httptest.NewRecorder()
	server.handleEmailVerificationHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Invalid token should return 401: got status %d", w.Code)
	}
}

// TestHomePageShowsDashboardAfterEmailVerification tests that after completing
// email verification, browsing to "/" shows the dashboard (not the landing page).
func TestHomePageShowsDashboardAfterEmailVerification(t *testing.T) {
	server := newTestServer(t)

	// Create a test user
	email := "test-home@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create an email verification token (no verification_code — this is the
	// standard link-based flow, not the app_token code-based flow).
	token := "test-home-token-" + time.Now().Format("20060102150405")
	expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO email_verifications (token, email, user_id, expires_at)
			VALUES (?, ?, ?, ?)`,
			token, email, user.UserID, expires)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create verification token: %v", err)
	}

	// Use the actual server's HTTP port for real HTTP behavior with cookies
	baseURL := server.httpURL()

	// Create HTTP client with cookie jar
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// POST to verify email
	form := url.Values{}
	form.Add("token", token)
	resp, err := client.PostForm(baseURL+"/verify-email", form)
	if err != nil {
		t.Fatalf("POST /verify-email failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /verify-email returned status %d, want 200", resp.StatusCode)
	}

	// Now browse to "/" with a NEW client (without cookies) - should show landing page
	noJarClient := &http.Client{}
	resp, err = noJarClient.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "ssh") || !strings.Contains(bodyStr, "disk persists") {
		t.Fatalf("GET / without cookies should show landing page, got:\n%s", bodyStr)
	}

	// Now browse to "/" with cookies - should show dashboard, not landing page
	resp, err = client.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / returned status %d, want 200", resp.StatusCode)
	}

	// Check that we see the dashboard, not the landing page
	bodyStr = string(body)
	if strings.Contains(bodyStr, "disk persists") {
		t.Fatalf("GET / after email verification shows landing page instead of dashboard:\n%s", bodyStr)
	}
}

// TestMetricsEndpoint tests that the /metrics endpoint returns Prometheus metrics
func TestMetricsEndpoint(t *testing.T) {
	server := newTestServer(t)
	baseURL := server.httpURL()

	// Make a request to the health endpoint first to trigger HTTP metrics
	healthResp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("Failed to make health request: %v", err)
	}
	healthResp.Body.Close()

	// Make request to metrics endpoint
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("Failed to fetch metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	bodyStr := string(body)

	// Debug: print the actual response
	t.Logf("Metrics response body: %s", bodyStr)

	// Check for expected metrics
	expectedMetrics := []string{
		"http_requests_total",
		"ssh_connections_current", // This should always be present as a gauge
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(bodyStr, metric) {
			t.Errorf("Expected to find metric %s in response", metric)
		}
	}

	// Verify the response is in Prometheus format
	if !strings.Contains(bodyStr, "# HELP") {
		t.Error("Expected Prometheus format with HELP comments")
	}
	if !strings.Contains(bodyStr, "# TYPE") {
		t.Error("Expected Prometheus format with TYPE comments")
	}
}

// TestSitemapEndpoint tests that /sitemap.xml returns a valid sitemap with the home page and docs.
func TestSitemapEndpoint(t *testing.T) {
	server := newTestServer(t)
	baseURL := server.httpURL()

	resp, err := http.Get(baseURL + "/sitemap.xml")
	if err != nil {
		t.Fatalf("Failed to fetch sitemap: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/xml") {
		t.Errorf("Expected Content-Type application/xml, got %s", contentType)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read sitemap response: %v", err)
	}

	// Parse XML to validate structure
	type sitemapURL struct {
		Loc string `xml:"loc"`
	}
	type urlset struct {
		XMLName xml.Name     `xml:"urlset"`
		URLs    []sitemapURL `xml:"url"`
	}

	var sitemap urlset
	if err := xml.Unmarshal(body, &sitemap); err != nil {
		t.Fatalf("Failed to parse sitemap XML: %v", err)
	}

	if len(sitemap.URLs) == 0 {
		t.Fatal("Sitemap contains no URLs")
	}

	// Check that home page is included
	hasHomePage := false
	hasDocsPage := false
	for _, u := range sitemap.URLs {
		if u.Loc == "https://localhost/" {
			hasHomePage = true
		}
		if strings.Contains(u.Loc, "/docs/") {
			hasDocsPage = true
		}
	}

	if !hasHomePage {
		t.Error("Missing home page URL in sitemap")
	}
	if !hasDocsPage {
		t.Error("No docs pages found in sitemap")
	}
}

// TestRobotsTxtEndpoint tests that /robots.txt returns a valid robots.txt with sitemap reference.
func TestRobotsTxtEndpoint(t *testing.T) {
	server := newTestServer(t)
	baseURL := server.httpURL()

	resp, err := http.Get(baseURL + "/robots.txt")
	if err != nil {
		t.Fatalf("Failed to fetch robots.txt: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/plain") {
		t.Errorf("Expected Content-Type text/plain, got %s", contentType)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read robots.txt response: %v", err)
	}

	bodyStr := string(body)

	if !strings.Contains(bodyStr, "User-agent: *") {
		t.Error("Missing User-agent directive")
	}
	if !strings.Contains(bodyStr, "Allow: /") {
		t.Error("Missing Allow directive")
	}
	if !strings.Contains(bodyStr, "Sitemap: https://localhost/sitemap.xml") {
		t.Error("Missing or incorrect Sitemap directive")
	}
}

func TestPricingRedirect(t *testing.T) {
	server := newTestServer(t)
	baseURL := server.httpURL()

	// Create client that doesn't follow redirects
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Request /pricing
	resp, err := client.Get(baseURL + "/pricing")
	if err != nil {
		t.Fatalf("Failed to fetch /pricing: %v", err)
	}
	defer resp.Body.Close()

	// Check we get a 307 redirect
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("Expected status %d, got %d", http.StatusTemporaryRedirect, resp.StatusCode)
	}

	// Check the Location header
	location := resp.Header.Get("Location")
	if location != "/docs/pricing" {
		t.Errorf("Expected Location /docs/pricing, got %s", location)
	}

	// Verify /docs/pricing returns 200
	resp2, err := http.Get(baseURL + location)
	if err != nil {
		t.Fatalf("Failed to fetch /docs/pricing: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("Expected /docs/pricing status 200, got %d", resp2.StatusCode)
	}
}

// TestHTTPMetricsInstrumentation tests that HTTP requests are being instrumented
func TestHTTPMetricsInstrumentation(t *testing.T) {
	server := newTestServer(t)
	baseURL := server.httpURL()

	// Make a request to the health endpoint
	resp, err := http.Get(baseURL + "/health")
	if err != nil {
		t.Fatalf("Failed to make health check request: %v", err)
	}
	resp.Body.Close()

	// Now fetch metrics to see if the request was recorded
	resp, err = http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("Failed to fetch metrics: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read metrics response: %v", err)
	}

	bodyStr := string(body)

	// Check that we have HTTP request metrics
	if !strings.Contains(bodyStr, "http_requests_total") {
		t.Error("Expected to find http_requests_total metric")
	}
}

// createTestBox is a test helper that generates SSH keys and stores box info in database
func (s *Server) createTestBox(t *testing.T, userID, ctrhost, name, containerID, image string) {
	// Generate SSH keys for testing
	sshKeys, err := container.GenerateContainerSSHKeys()
	if err != nil {
		t.Fatalf("failed to generate SSH keys: %v", err)
	}

	id, err := s.preCreateBox(t.Context(), preCreateBoxOptions{
		userID:        userID,
		ctrhost:       ctrhost,
		name:          name,
		image:         image,
		noShard:       false,
		region:        "pdx",
		allocatedCPUs: 2,
	})
	if err != nil {
		t.Fatalf("failed to create box with test SSH keys: %v", err)
	}

	err = s.updateBoxWithContainer(t.Context(), id, containerID, "root", sshKeys, s.piperdPort)
	if err != nil {
		t.Fatalf("failed to update box with container ID: %v", err)
	}
}

// TestMetricsEndpointProtection tests that /metrics is protected by IP restrictions
func TestMetricsEndpointProtection(t *testing.T) {
	// Test the requireLocalAccess decorator directly

	// Create a simple test handler
	testHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}

	// Test request from non-localhost, non-Tailscale IP should be denied
	t.Run("external_ip_denied", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345" // Simulate external IP
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("Expected status 404 for external IP, got %d", w.Code)
		}
	})

	// Test request from localhost should be allowed
	t.Run("localhost_allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "127.0.0.1:12345" // Localhost IP
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for localhost, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "success" {
			t.Errorf("Expected 'success' in response body, got: %s", body)
		}
	})

	// Test request from IPv6 localhost should be allowed
	t.Run("localhost_ipv6_allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "[::1]:12345" // IPv6 localhost
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for IPv6 localhost, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "success" {
			t.Errorf("Expected 'success' in response body, got: %s", body)
		}
	})

	// Test request from Tailscale IP should be allowed
	t.Run("tailscale_ip_allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "100.64.1.1:12345" // Tailscale IP range
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected status 200 for Tailscale IP, got %d", w.Code)
		}
		body := w.Body.String()
		if body != "success" {
			t.Errorf("Expected 'success' in response body, got: %s", body)
		}
	})

	// Test malformed RemoteAddr
	t.Run("malformed_remote_addr", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "invalid-ip" // Malformed IP
		w := httptest.NewRecorder()

		requireLocalAccess(testHandler)(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("Expected status 500 for malformed IP, got %d", w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "remoteaddr check") {
			t.Errorf("Expected 'remoteaddr check' error in response body, got: %s", body)
		}
	})
}

// TestWebAuthFlowCreatesNewUser tests that the web auth flow creates a new user if they don't exist
func TestWebAuthFlowCreatesNewUser(t *testing.T) {
	server := newTestServer(t)

	email := "newuser@example.com"

	// Verify user doesn't exist yet
	userID, err := server.GetUserIDByEmail(context.Background(), email)
	if err == nil {
		t.Fatalf("User should not exist yet, but got user ID: %s", userID)
	}

	// Submit email for authentication
	form := url.Values{}
	form.Add("email", email)
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/auth", server.httpPort()),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("Failed to POST /auth: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("POST /auth: Expected status 200, got %d, body: %s", resp.StatusCode, string(body))
	}

	// Verify the response doesn't contain the error message about SSH signup
	if strings.Contains(string(body), "Please sign up first using SSH") {
		t.Errorf("Response should not tell user to sign up via SSH: %s", string(body))
	}

	// Verify user was created
	userID, err = server.GetUserIDByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("User should exist after web auth, but got error: %v", err)
	}

	if userID == "" {
		t.Fatal("User ID should not be empty")
	}

	// Verify user exists (allocations are now part of user creation)
	user, err := withRxRes1(server, context.Background(), (*exedb.Queries).GetUserWithDetails, userID)
	if err != nil {
		t.Fatalf("Failed to get user details: %v", err)
	}

	if user.UserID == "" {
		t.Fatal("User should exist")
	}
}

// TestBasicUserCreatedForLoginWithExe tests that a user created during the
// login-with-exe flow (with login_with_exe form field) has the flag set.
func TestBasicUserCreatedForLoginWithExe(t *testing.T) {
	server := newTestServer(t)

	email := "basicuser@example.com"

	// Submit email for authentication WITH login_with_exe=1 (simulating login-with-exe)
	form := url.Values{}
	form.Add("email", email)
	form.Add("login_with_exe", "1")
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/auth", server.httpPort()),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("Failed to POST /auth: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("POST /auth: Expected status 200, got %d", resp.StatusCode)
	}

	// Verify user was created with the flag set
	user, err := server.GetUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("User should exist after web auth, but got error: %v", err)
	}

	if !user.CreatedForLoginWithExe {
		t.Fatal("User created with login_with_exe=1 should have CreatedForLoginWithExe=true")
	}
}

// TestNormalUserCreatedWithoutFlag tests that a user created during normal
// web auth (without login_with_exe) does NOT have the login-with-exe flag set.
func TestNormalUserCreatedWithoutFlag(t *testing.T) {
	server := newTestServer(t)

	email := "normaluser@example.com"

	// Submit email for authentication WITHOUT return_host (normal web auth)
	form := url.Values{}
	form.Add("email", email)
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/auth", server.httpPort()),
		"application/x-www-form-urlencoded",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		t.Fatalf("Failed to POST /auth: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("POST /auth: Expected status 200, got %d", resp.StatusCode)
	}

	// Verify user was created WITHOUT the flag
	user, err := server.GetUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("User should exist after web auth, but got error: %v", err)
	}

	if user.CreatedForLoginWithExe {
		t.Fatal("User created without login_with_exe should have CreatedForLoginWithExe=false")
	}
}

// TestNewsletterSubscription tests that POST /newsletter-subscribe sets the flag on the user.
func TestNewsletterSubscription(t *testing.T) {
	server := newTestServer(t)

	// Create a user
	var userID string
	err := server.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = server.createUserRecord(ctx, queries, "newsletter@example.com", false)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Verify default is false
	user, err := server.GetUserByEmail(context.Background(), "newsletter@example.com")
	if err != nil {
		t.Fatalf("Failed to get user: %v", err)
	}
	if user.NewsletterSubscribed {
		t.Fatal("New user should not be subscribed to newsletter by default")
	}

	// Create authenticated client
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      userID,
		Domain:      "127.0.0.1",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}
	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// POST /newsletter-subscribe without auth should fail
	resp, err := http.Post(
		fmt.Sprintf("http://127.0.0.1:%d/newsletter-subscribe", server.httpPort()),
		"application/x-www-form-urlencoded",
		nil,
	)
	if err != nil {
		t.Fatalf("Failed to POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Expected 401 without auth, got %d", resp.StatusCode)
	}

	// POST /newsletter-subscribe with auth should succeed
	resp, err = client.Post(
		fmt.Sprintf("http://127.0.0.1:%d/newsletter-subscribe", server.httpPort()),
		"application/x-www-form-urlencoded",
		nil,
	)
	if err != nil {
		t.Fatalf("Failed to POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Expected 204, got %d", resp.StatusCode)
	}

	// Verify user is now subscribed
	user, err = server.GetUserByEmail(context.Background(), "newsletter@example.com")
	if err != nil {
		t.Fatalf("Failed to get user: %v", err)
	}
	if !user.NewsletterSubscribed {
		t.Fatal("User should be subscribed after POST /newsletter-subscribe")
	}

	// POST /newsletter-subscribe with subscribed=0 should unsubscribe
	resp, err = client.Post(
		fmt.Sprintf("http://127.0.0.1:%d/newsletter-subscribe", server.httpPort()),
		"application/x-www-form-urlencoded",
		strings.NewReader("subscribed=0"),
	)
	if err != nil {
		t.Fatalf("Failed to POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("Expected 204, got %d", resp.StatusCode)
	}

	// Verify user is now unsubscribed
	user, err = server.GetUserByEmail(context.Background(), "newsletter@example.com")
	if err != nil {
		t.Fatalf("Failed to get user: %v", err)
	}
	if user.NewsletterSubscribed {
		t.Fatal("User should be unsubscribed after POST with subscribed=0")
	}
}

// TestBasicUserDashboardRedirectsToProfile tests that a basic user (no SSH keys,
// no boxes, created_for_login_with_exe=1) is redirected from / to /user.
func TestBasicUserDashboardRedirectsToProfile(t *testing.T) {
	server := newTestServer(t)

	// Create a basic user directly (with the flag set, no SSH keys, no boxes)
	var userID string
	err := server.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = server.createUserRecord(ctx, queries, "basicuser-redirect@example.com", true)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create basic user: %v", err)
	}

	// Create an auth cookie for this user
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Don't follow redirects
		},
	}

	// Create auth cookie - domain must match what baseDomain returns for the request host
	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      userID,
		Domain:      "127.0.0.1", // baseDomain returns this for 127.0.0.1:port
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Set the cookie on the jar
	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// Make request to dashboard (/)
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /: %v", err)
	}
	resp.Body.Close()

	// Should redirect to /user
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("Expected redirect (307), got %d", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location != "/user" {
		t.Errorf("Expected redirect to /user, got %s", location)
	}
}

// TestBasicUserProfileShowsWhatIsExe tests that a basic user sees the
// "What is exe?" section on their profile page.
func TestBasicUserProfileShowsWhatIsExe(t *testing.T) {
	server := newTestServer(t)

	// Create a basic user directly (with the flag set, no SSH keys, no boxes)
	var userID string
	err := server.withTx(context.Background(), func(ctx context.Context, queries *exedb.Queries) error {
		var err error
		userID, err = server.createUserRecord(ctx, queries, "basicuser-profile@example.com", true)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create basic user: %v", err)
	}

	// Create an auth cookie for this user
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Create auth cookie - domain must match what baseDomain returns for the request host
	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      userID,
		Domain:      "127.0.0.1", // baseDomain returns this for 127.0.0.1:port
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Set the cookie on the jar
	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// Make request to profile page (/user)
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/user", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /user: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// Should contain the "What is exe?" section
	if !strings.Contains(string(body), "What is exe?") {
		t.Error("Profile page should contain 'What is exe?' section for basic users")
	}
	if !strings.Contains(string(body), "exe.dev is a hosting service") {
		t.Error("Profile page should contain explanation text for basic users")
	}

	// Should NOT show SSH Keys section for basic users
	if strings.Contains(string(body), "SSH Keys") {
		t.Error("Profile page should NOT show SSH Keys section for basic users")
	}
}

// TestNormalUserProfileShowsSSHKeys tests that a normal user (with SSH key)
// sees the SSH Keys section on their profile page.
func TestNormalUserProfileShowsSSHKeys(t *testing.T) {
	server := newTestServer(t)

	// Create a normal user (with SSH key)
	email := "normaluser-profile@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create an auth cookie for this user
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Create auth cookie
	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      user.UserID,
		Domain:      "127.0.0.1",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Set the cookie on the jar
	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// Make request to profile page (/user)
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/user", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /user: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// Should show SSH Keys section for normal users
	if !strings.Contains(string(body), "SSH Keys") {
		t.Error("Profile page should show SSH Keys section for normal users")
	}

	// Should NOT show "What is exe?" section for normal users
	if strings.Contains(string(body), "What is exe?") {
		t.Error("Profile page should NOT show 'What is exe?' section for normal users")
	}
}

// TestNormalUserDashboardShowsAllTabs tests that a normal user (with SSH key)
// can access the dashboard and sees all tabs.
func TestNormalUserDashboardShowsAllTabs(t *testing.T) {
	server := newTestServer(t)

	// Create a normal user (with SSH key)
	email := "normaluser-dashboard@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create an auth cookie for this user
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Create auth cookie - domain must match what baseDomain returns for the request host
	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      user.UserID,
		Domain:      "127.0.0.1", // baseDomain returns this for 127.0.0.1:port
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Set the cookie on the jar
	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// Make request to dashboard (/)
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d", resp.StatusCode)
	}

	// Should show all tabs (VMs, Shell, Profile)
	if !strings.Contains(string(body), "VMs") {
		t.Error("Dashboard should show VMs tab for normal users")
	}
	if !strings.Contains(string(body), "Shell") {
		t.Error("Dashboard should show Shell tab for normal users")
	}
	if !strings.Contains(string(body), "Profile") {
		t.Error("Dashboard should show Profile tab for normal users")
	}
}

// TestDashboardShareModalFromURLParams verifies that the dashboard renders
// an inline script to auto-open the share modal when share_vm and share_email
// query params are present.
func TestDashboardShareModalFromURLParams(t *testing.T) {
	server := newTestServer(t)

	// Create a user with SSH key (normal user).
	user, err := server.createUser(t.Context(), testSSHPubKey, "sharemodal@test.dev", AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	cookieValue := generateRegistrationToken()
	err = withTx1(server, context.Background(), (*exedb.Queries).InsertAuthCookie, exedb.InsertAuthCookieParams{
		CookieValue: cookieValue,
		UserID:      user.UserID,
		Domain:      "127.0.0.1",
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	baseURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", server.httpPort()))
	jar.SetCookies(baseURL, []*http.Cookie{
		{Name: "exe-auth", Value: cookieValue},
	})

	// Request the dashboard with share_vm and share_email params.
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/?share_vm=mybox&share_email=someone@test.dev", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d", resp.StatusCode)
	}

	bodyStr := string(body)

	// The template should render a hidden div with data attributes and an inline script.
	if !strings.Contains(bodyStr, `data-vm="mybox"`) {
		t.Error("Dashboard should contain data-vm attribute when share_vm param is set")
	}
	if !strings.Contains(bodyStr, `data-email="someone@test.dev"`) {
		t.Error("Dashboard should contain data-email attribute when share_email param is set")
	}
	if !strings.Contains(bodyStr, "cmdModal.open") {
		t.Error("Dashboard should contain inline script to open share modal")
	}

	// Without share params, the hidden div should not be present.
	resp2, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", server.httpPort()))
	if err != nil {
		t.Fatalf("Failed to GET /: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if strings.Contains(string(body2), "share-request") {
		t.Error("Dashboard should NOT contain share-request div without share params")
	}
}

// TestIsExeletNotFoundError tests detection of "not found" errors from exelet
func TestIsExeletNotFoundError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "regular error",
			err:      fmt.Errorf("something went wrong"),
			expected: false,
		},
		{
			name:     "grpc NotFound",
			err:      status.Error(codes.NotFound, "not found: instance vm123"),
			expected: true,
		},
		{
			name:     "grpc FailedPrecondition",
			err:      status.Error(codes.FailedPrecondition, "some other error"),
			expected: false,
		},
		{
			name:     "grpc Internal error",
			err:      status.Error(codes.Internal, "internal server error"),
			expected: false,
		},
		{
			name:     "grpc Unavailable error",
			err:      status.Error(codes.Unavailable, "service unavailable"),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isExeletNotFoundError(tt.err)
			if result != tt.expected {
				t.Errorf("isExeletNotFoundError(%v) = %v, want %v", tt.err, result, tt.expected)
			}
		})
	}
}
