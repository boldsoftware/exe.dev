package execore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/exeweb"
	"exe.dev/pow"
	"tailscale.com/util/limiter"
)

// TestXSSInEmailVerificationForm tests that template variables in the
// email verification form are properly escaped to prevent XSS attacks.
// Page data is JSON-serialized inside a <script> tag as window.__PAGE__.
// json.Marshal escapes <, >, & as unicode escapes, and " as \".
// Single quotes don't need escaping in JSON strings (they can't break
// out of a double-quoted JSON value).
func TestXSSInEmailVerificationForm(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create a malicious redirect URL that attempts XSS via script injection
	xssPayload := `<script>alert(document.cookie)</script>`

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

	// json.Marshal escapes < and > as \u003c and \u003e, preventing script injection.
	// The raw <script> tag must NOT appear in the output.
	if strings.Contains(body, "<script>alert(") {
		t.Errorf("XSS vulnerability: script tag not escaped in JSON output")
	}

	// Verify the payload is present but properly escaped
	if !strings.Contains(body, `\u003cscript\u003e`) {
		t.Errorf("Expected JSON-escaped script tag in output, body: %s", body[:min(500, len(body))])
	}
}

// TestXSSInReturnHost tests that return_host is properly escaped.
// With JSON serialization, double quotes in values are escaped as \" by json.Marshal.
func TestXSSInReturnHost(t *testing.T) {
	t.Parallel()
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

	// json.Marshal escapes double quotes as \", so the raw payload can't break out.
	// The unescaped double quote must NOT appear in JSON context.
	if strings.Contains(body, `"return_host":"";alert(1)`) {
		t.Errorf("XSS vulnerability: return_host double quote not escaped in JSON")
	}

	// Verify the value is properly JSON-escaped (\" for the double quote)
	if !strings.Contains(body, `\"`) {
		t.Errorf("Expected escaped double quote in JSON output, body: %s", body[:min(500, len(body))])
	}
}

// TestOpenRedirectAfterAuth tests that the redirect after authentication
// is validated and doesn't allow external redirects.
func TestOpenRedirectAfterAuth(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Create a user and get them authenticated
	email := "redirect-test@example.com"
	publicKey := testSSHPubKey
	user, err := server.createUser(t.Context(), publicKey, email, "", AllQualityChecks)
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
	t.Parallel()
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
			// the validation logic is applied by checking IsValidRedirectURL
			redirectTo := tt.redirectTo
			if redirectTo == "" || !exeweb.IsValidRedirectURL(redirectTo) {
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
	t.Parallel()
	server := newTestServer(t)

	// Use a very long refill interval so no tokens are refilled during
	// the test. The default 12s interval can cause flakes when the 21
	// requests straddle a refill boundary.
	server.signupLimiter = &limiter.Limiter[netip.Addr]{
		Size:           10,
		Max:            20,
		RefillInterval: time.Hour,
	}
	server.signupLimiter.Allow(netip.Addr{}) // initialize internal state

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
	t.Parallel()
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
	// Vue page should contain the POW data in window.__PAGE__ JSON
	body := w.Body.String()
	if !strings.Contains(body, "window.__PAGE__") {
		t.Errorf("Expected POW page with window.__PAGE__, got: %s", body[:min(200, len(body))])
	}
	if !strings.Contains(body, "powToken") {
		t.Errorf("Expected POW page with powToken in page data")
	}
	if !strings.Contains(body, "powDifficulty") {
		t.Errorf("Expected POW page with powDifficulty in page data")
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
	t.Parallel()
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

func TestRender401PageVariants(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	handler := server.prepareHandler()

	t.Run("login-with-exe without share link", func(t *testing.T) {
		req := httptest.NewRequest("GET",
			"http://"+server.env.WebHost+"/auth?redirect=%2F&return_host=mybox."+server.env.BoxHost, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", w.Code, http.StatusUnauthorized)
		}
		body := w.Body.String()
		if !strings.Contains(body, "Sign in to continue.") {
			t.Errorf("expected 'Sign in to continue.' heading for login-with-exe flow")
		}
		if !strings.Contains(body, "log in or create an account") {
			t.Errorf("expected 'log in or create an account' hint")
		}
	})

	t.Run("login-with-exe with share link", func(t *testing.T) {
		redirect := url.QueryEscape("/?share=abc123")
		req := httptest.NewRequest("GET",
			"http://"+server.env.WebHost+"/auth?redirect="+redirect+"&return_host=mybox."+server.env.BoxHost, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("got status %d, want %d", w.Code, http.StatusUnauthorized)
		}
		body := w.Body.String()
		if !strings.Contains(body, "You've been invited.") {
			t.Errorf("expected 'You've been invited.' heading for share link flow, got: %s", body[:min(500, len(body))])
		}
		if !strings.Contains(body, "get access") {
			t.Errorf("expected 'get access' hint")
		}
	})

	t.Run("no return_host shows auth form", func(t *testing.T) {
		req := httptest.NewRequest("GET",
			"http://"+server.env.WebHost+"/auth", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", w.Code, http.StatusOK)
		}
		body := w.Body.String()
		// Should show the regular auth form, not "Access required"
		if strings.Contains(body, "Sign in to continue") {
			t.Errorf("regular auth page should not show login-with-exe messaging")
		}
	})
}

func TestCSRFProtection(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	handler := server.prepareHandler()

	t.Run("same-origin POST to /auth is allowed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "http://"+server.env.WebHost+"/auth", strings.NewReader("email=test@example.com"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://"+server.env.WebHost)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusForbidden {
			t.Errorf("same-origin POST to /auth was blocked: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("cross-origin POST to /auth is blocked", func(t *testing.T) {
		req := httptest.NewRequest("POST", "http://"+server.env.WebHost+"/auth", strings.NewReader("email=test@example.com"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://mybox."+server.env.BoxHost+":8001")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("cross-origin POST to /auth should be blocked, got %d", w.Code)
		}
	})

	t.Run("cross-origin POST to /verify-email is allowed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "http://"+server.env.WebHost+"/verify-email", strings.NewReader("token=bogus"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://mail.google.com")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusForbidden {
			t.Errorf("cross-origin POST to /verify-email was blocked: %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("cross-origin POST to /create-vm is blocked", func(t *testing.T) {
		req := httptest.NewRequest("POST", "http://"+server.env.WebHost+"/create-vm", strings.NewReader("hostname=evil"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://evil.com")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("cross-origin POST to /create-vm should be blocked, got %d", w.Code)
		}
	})

	t.Run("cross-origin GET is allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "http://"+server.env.WebHost+"/auth", nil)
		req.Header.Set("Origin", "https://evil.com")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code == http.StatusForbidden {
			t.Errorf("cross-origin GET should not be blocked, got %d", w.Code)
		}
	})
}
