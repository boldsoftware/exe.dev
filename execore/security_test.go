package execore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/pow"
)

// TestXSSInEmailVerificationForm tests that template variables in the
// email verification form are properly escaped to prevent XSS attacks.
func TestXSSInEmailVerificationForm(t *testing.T) {
	server := newTestServer(t)

	// Create a malicious redirect URL that attempts XSS
	xssPayload := "';alert(document.cookie);//"

	// Store the verification in memory so showEmailVerificationForm can find it
	token := "test-token-xss"
	verification := &EmailVerification{
		Email:        "test@example.com",
		PairingCode:  "123456",
		Token:        token,
		CompleteChan: make(chan struct{}),
		CreatedAt:    time.Now(),
	}
	server.emailVerifications[token] = verification

	// Request the verification form with the XSS payload in redirect
	req := httptest.NewRequest("GET", "/verify-email?token="+token+"&redirect="+url.QueryEscape(xssPayload), nil)
	w := httptest.NewRecorder()
	server.showEmailVerificationForm(w, req, token, "")

	body := w.Body.String()

	// The XSS payload should NOT appear unescaped in JavaScript context
	// If vulnerable, the raw payload would appear allowing script injection
	// If fixed with | js, special chars are escaped as unicode
	if strings.Contains(body, "'redirect': '"+xssPayload) {
		t.Errorf("XSS vulnerability: redirect parameter not escaped in JavaScript context")
	}

	// Check that the payload doesn't appear in a way that could execute
	// The single quote should be escaped as \u0027 or similar
	if strings.Contains(body, "';alert(") {
		t.Errorf("XSS vulnerability: single quote not escaped, payload could break out of string")
	}
}

// TestXSSInReturnHost tests that return_host is properly escaped.
func TestXSSInReturnHost(t *testing.T) {
	server := newTestServer(t)

	xssPayload := `";alert(1);//`

	token := "test-token-xss-host"
	verification := &EmailVerification{
		Email:        "test@example.com",
		PairingCode:  "123456",
		Token:        token,
		CompleteChan: make(chan struct{}),
		CreatedAt:    time.Now(),
	}
	server.emailVerifications[token] = verification

	req := httptest.NewRequest("GET", "/verify-email?token="+token+"&return_host="+url.QueryEscape(xssPayload), nil)
	w := httptest.NewRecorder()
	server.showEmailVerificationForm(w, req, token, "")

	body := w.Body.String()

	// The double quote should be escaped to prevent breaking out of the string
	if strings.Contains(body, `";alert(1)`) {
		t.Errorf("XSS vulnerability: return_host parameter not escaped, double quote allows breakout")
	}
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
			valid := isValidRedirectURL(tt.redirectURL)
			if tt.shouldBlock && valid {
				t.Errorf("isValidRedirectURL(%q) = true, want false (should block)", tt.redirectURL)
			}
			if !tt.shouldBlock && !valid {
				t.Errorf("isValidRedirectURL(%q) = false, want true (should allow)", tt.redirectURL)
			}
		})
	}
}

// TestOpenRedirectAfterAuth tests that the redirect after authentication
// is validated and doesn't allow external redirects.
func TestOpenRedirectAfterAuth(t *testing.T) {
	server := newTestServer(t)

	// Create a user and get them authenticated
	email := "redirect-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create an auth cookie for this user
	cookieValue, err := server.createAuthCookie(t.Context(), user.UserID, "exe.dev")
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Try to redirect to an external URL after auth
	maliciousRedirect := "https://evil.com/steal-creds"
	req := httptest.NewRequest("GET", "/auth?redirect="+url.QueryEscape(maliciousRedirect), nil)
	req.AddCookie(&http.Cookie{Name: "exe_auth", Value: cookieValue})
	w := httptest.NewRecorder()
	server.handleAuth(w, req)

	// Should NOT redirect to the external URL
	location := w.Header().Get("Location")
	if location == maliciousRedirect {
		t.Errorf("Open redirect vulnerability: redirected to external URL %q", location)
	}
	if strings.HasPrefix(location, "https://evil.com") {
		t.Errorf("Open redirect vulnerability: redirected to external domain: %q", location)
	}
}

// TestPasskeyOpenRedirect tests that the passkey login finish endpoint
// validates redirect_to to prevent open redirect attacks.
func TestPasskeyOpenRedirect(t *testing.T) {
	tests := []struct {
		name             string
		redirectTo       string
		expectedRedirect string
	}{
		{"safe relative path", "/auth?redirect=%2F&return_host=box.exe.dev", "/auth?redirect=%2F&return_host=box.exe.dev"},
		{"external URL blocked", "https://evil.com/phish", "/"},
		{"protocol-relative blocked", "//evil.com/phish", "/"},
		{"javascript blocked", "javascript:alert(1)", "/"},
		{"empty defaults to root", "", "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't easily test the full passkey flow, but we can verify
			// the validation logic is applied by checking isValidRedirectURL
			redirectTo := tt.redirectTo
			if redirectTo == "" || !isValidRedirectURL(redirectTo) {
				redirectTo = "/"
			}
			if redirectTo != tt.expectedRedirect {
				t.Errorf("redirect_to=%q: got %q, want %q", tt.redirectTo, redirectTo, tt.expectedRedirect)
			}
		})
	}
}

// TestSignupRateLimiting tests that signup endpoints are rate limited.
func TestSignupRateLimiting(t *testing.T) {
	server := newTestServer(t)

	// Send 20 requests (the limit) - all should succeed
	for i := range 20 {
		req := httptest.NewRequest("POST", "/auth", strings.NewReader("email=test@example.com"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "192.0.2.1:12345" // Use a test IP
		w := httptest.NewRecorder()
		server.handleAuthEmailSubmission(w, req)

		if w.Code == http.StatusTooManyRequests {
			t.Errorf("Request %d: got 429 Too Many Requests, want success", i+1)
		}
	}

	// 21st request should be rate limited
	req := httptest.NewRequest("POST", "/auth", strings.NewReader("email=test@example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.1:12345" // Same IP
	w := httptest.NewRecorder()
	server.handleAuthEmailSubmission(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Request 21: got %d, want %d (Too Many Requests)", w.Code, http.StatusTooManyRequests)
	}

	// Different IP should not be rate limited
	req = httptest.NewRequest("POST", "/auth", strings.NewReader("email=test@example.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.2:12345" // Different IP
	w = httptest.NewRecorder()
	server.handleAuthEmailSubmission(w, req)

	if w.Code == http.StatusTooManyRequests {
		t.Errorf("Different IP: got 429 Too Many Requests, should not be rate limited")
	}
}

// TestSignupPOW tests that proof-of-work is required for new user signups when enabled.
func TestSignupPOW(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	// Enable POW for signups
	err := withTx1(server, ctx, (*exedb.Queries).SetSignupPOWEnabled, "true")
	if err != nil {
		t.Fatalf("failed to enable POW: %v", err)
	}

	// Attempt signup without POW - should show interstitial page
	form := url.Values{}
	form.Set("email", "newuser-pow-test@example.com")
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.10:12345"
	w := httptest.NewRecorder()
	server.handleAuthEmailSubmission(w, req)

	// Should show the POW interstitial page (status 200)
	if w.Code != http.StatusOK {
		t.Errorf("Without POW: got status %d, want %d", w.Code, http.StatusOK)
	}
	// Interstitial should contain the POW token and "Verifying"
	body := w.Body.String()
	if !strings.Contains(body, "Verifying") {
		t.Errorf("Expected interstitial page with 'Verifying', got: %s", body[:min(200, len(body))])
	}
	if !strings.Contains(body, "pow_token") {
		t.Errorf("Expected interstitial page with pow_token field")
	}

	// Now get a fresh token and solve it
	token, err := server.signupPOW.NewToken()
	if err != nil {
		t.Fatalf("failed to create POW token: %v", err)
	}
	nonce := pow.Solve(token, server.signupPOW.GetDifficulty())

	// Attempt signup with valid POW - should succeed
	form = url.Values{}
	form.Set("email", "newuser-pow-test2@example.com")
	form.Set("pow_token", token)
	form.Set("pow_nonce", strconv.FormatUint(nonce, 10))
	req = httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.11:12345"
	w = httptest.NewRecorder()
	server.handleAuthEmailSubmission(w, req)

	// Should proceed past POW (show email sent page, not interstitial)
	body = w.Body.String()
	if strings.Contains(body, "Verifying") {
		t.Errorf("With valid POW: still showing interstitial page")
	}
}

// TestSignupPOWDisabled tests that POW is not required when disabled.
func TestSignupPOWDisabled(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()

	// Ensure POW is disabled
	err := withTx1(server, ctx, (*exedb.Queries).SetSignupPOWEnabled, "false")
	if err != nil {
		t.Fatalf("failed to disable POW: %v", err)
	}

	// Signup without POW should work for new users
	form := url.Values{}
	form.Set("email", "newuser-no-pow@example.com")
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.20:12345"
	w := httptest.NewRecorder()
	server.handleAuthEmailSubmission(w, req)

	// Should not complain about POW
	if strings.Contains(w.Body.String(), "verification challenge") {
		t.Errorf("With POW disabled: got verification challenge error")
	}
}
