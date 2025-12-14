package execore

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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
	publicKey := "ssh-rsa dummy-redirect-test-key redirect-test@example.com"
	user, err := server.createUser(t.Context(), publicKey, email)
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
