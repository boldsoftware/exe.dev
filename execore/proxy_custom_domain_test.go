package execore

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/exeweb"
	"exe.dev/sqlite"
)

// TestCustomDomainAuthFlow tests the complete auth flow for a box accessed via custom domain
// NOTE: This is vibe coded SLOP. Keeping for reference about auth flow for future improvements coming soon.
func TestCustomDomainAuthFlow(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.magicSecrets = exeweb.NewMagicSecrets()

	// Create test user and box
	publicKey := testSSHPubKey
	email := "test@example.com"

	_, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	user, err := server.getUserByPublicKey(t.Context(), publicKey)
	if err != nil {
		t.Fatalf("Failed to get test user: %v", err)
	}

	// Create a test box with a private route
	boxName := "mybox"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (ctrhost, name, status, image, container_id, created_by_user_id, routes,
			                     ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "fake_ctrhost", boxName, "running", "test-image", "test-container-id", user.UserID, `[
			{
				"name": "default",
				"policy": "private",
				"methods": ["*"],
				"paths": {"prefix": "/"},
				"priority": 1,
				"ports": [80]
			}
		]`, []byte("test-identity-key"), "test-authorized-keys", []byte("test-client-key"), 2222)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert test box: %v", err)
	}

	// Set up CNAME resolution for custom domain
	customDomain := "example.com"
	server.lookupCNAMEFunc = func(ctx context.Context, host string) (string, error) {
		if host == customDomain {
			// Simulate CNAME pointing to mybox.exe.dev (or mybox.exe.cloud in dev)
			return server.env.BoxSub(boxName), nil
		}
		return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	// Step 1: User visits https://example.com/ without authentication
	t.Run("unauthenticated_request_to_custom_domain", func(t *testing.T) {
		// Create initial request to custom domain
		initialURL := "https://example.com/"
		req := createTestRequestForServer("GET", initialURL, customDomain, server)
		req.TLS = &tls.ConnectionState{} // Simulate HTTPS

		// Use httptest.Server to handle redirects properly
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)

		// Should redirect to login
		if recorder.Code != http.StatusTemporaryRedirect {
			t.Fatalf("Expected redirect (307), got %d. Body: %s", recorder.Code, recorder.Body.String())
		}

		location := recorder.Header().Get("Location")
		t.Logf("Step 1 redirect location: %s", location)

		// Step 2: Follow redirect to /__exe.dev/login on the custom domain
		// Parse the redirect to extract the path and query
		if !strings.Contains(location, "/__exe.dev/login") {
			t.Fatalf("Expected redirect to /__exe.dev/login, got: %s", location)
		}

		// Extract the path and query from the escaped URL
		// The location will be something like: https://example.com%3A51108/__exe.dev/login?redirect=...
		// We need to unescape it and parse it
		unescaped, err := url.QueryUnescape(location)
		if err != nil {
			t.Fatalf("Failed to unescape location: %v", err)
		}
		t.Logf("Step 1 unescaped location: %s", unescaped)

		parsedLoc1, err := url.Parse(unescaped)
		if err != nil {
			t.Fatalf("Failed to parse unescaped location: %v", err)
		}

		req2 := createTestRequestForServer("GET", unescaped, customDomain, server)
		req2.TLS = &tls.ConnectionState{}
		req2.URL.Path = parsedLoc1.Path
		req2.URL.RawQuery = parsedLoc1.RawQuery

		recorder2 := httptest.NewRecorder()
		server.ServeHTTP(recorder2, req2)

		// handleProxyLogin should redirect to main domain for auth
		if recorder2.Code != http.StatusTemporaryRedirect {
			t.Fatalf("Expected redirect to main domain (307), got %d. Body: %s", recorder2.Code, recorder2.Body.String())
		}

		location2 := recorder2.Header().Get("Location")
		t.Logf("Step 2 redirect location: %s", location2)

		// Verify it's redirecting to the main web domain (localhost in tests)
		if !strings.Contains(location2, server.env.WebHost) {
			t.Fatalf("Expected redirect to main domain (%s), got: %s", server.env.WebHost, location2)
		}

		// Verify it's going to /auth endpoint
		if !strings.Contains(location2, "/auth?") {
			t.Fatalf("Expected redirect to /auth endpoint, got: %s", location2)
		}

		// Verify redirect and return_host parameters are present
		if !strings.Contains(location2, "redirect=") {
			t.Fatalf("Expected redirect parameter in URL, got: %s", location2)
		}
		if !strings.Contains(location2, "return_host=") {
			t.Fatalf("Expected return_host parameter in URL, got: %s", location2)
		}

		t.Logf("✓ SUCCESS: handleProxyLogin correctly redirects to main domain auth")
	})
}

// TestCustomDomainReturnHostValidation tests that redirectAfterAuth validates
// return_host via CNAME resolution, preventing redirects to arbitrary domains.
func TestCustomDomainReturnHostValidation(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	server.magicSecrets = exeweb.NewMagicSecrets()

	// Create test user
	publicKey := testSSHPubKey
	email := "returnhost-test@example.com"

	_, err := server.createUser(t.Context(), publicKey, email, AllQualityChecks)
	if err != nil {
		t.Fatalf("Failed to create test user: %v", err)
	}

	user, err := server.getUserByPublicKey(t.Context(), publicKey)
	if err != nil {
		t.Fatalf("Failed to get test user: %v", err)
	}

	// Create a test box
	boxName := "custombox"
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO boxes (ctrhost, name, status, image, container_id, created_by_user_id, routes,
			                     ssh_server_identity_key, ssh_authorized_keys, ssh_client_private_key, ssh_port)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, "fake_ctrhost", boxName, "running", "test-image", "test-container-id", user.UserID, `[
			{
				"name": "default",
				"policy": "private",
				"methods": ["*"],
				"paths": {"prefix": "/"},
				"priority": 1,
				"ports": [80]
			}
		]`, []byte("test-identity-key"), "test-authorized-keys", []byte("test-client-key"), 2222)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to insert test box: %v", err)
	}

	validCustomDomain := "myapp.example.com"
	invalidCustomDomain := "evil.attacker.com"

	// Set up CNAME resolution - only validCustomDomain points to our box
	server.lookupCNAMEFunc = func(ctx context.Context, host string) (string, error) {
		if host == validCustomDomain {
			return server.env.BoxSub(boxName), nil
		}
		return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}

	// Helper to create a verification token with redirect info stored in DB
	createToken := func(t *testing.T, redirectURL, returnHost string) string {
		token := "test-token-" + t.Name()
		expires := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
		err := server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
			_, err := tx.Exec(`
				INSERT INTO email_verifications (token, email, user_id, expires_at, verification_code, redirect_url, return_host)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				token, email, user.UserID, expires, "123456", redirectURL, returnHost)
			return err
		})
		if err != nil {
			t.Fatalf("Failed to create verification token: %v", err)
		}
		return token
	}

	t.Run("valid_custom_domain_return_host", func(t *testing.T) {
		// Create token with redirect info stored in DB (no longer passed via form)
		token := createToken(t, "/dashboard", validCustomDomain)

		form := url.Values{}
		form.Set("token", token)

		req := httptest.NewRequest("POST", "/verify-email", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = server.env.WebHost

		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)

		// Should redirect to /auth/confirm with the valid return_host
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("Expected 303 redirect, got %d. Body: %s", recorder.Code, recorder.Body.String())
		}

		location := recorder.Header().Get("Location")
		if !strings.Contains(location, "/auth/confirm") {
			t.Fatalf("Expected redirect to /auth/confirm, got: %s", location)
		}
		if !strings.Contains(location, "return_host=") {
			t.Fatalf("Expected return_host in redirect, got: %s", location)
		}
		t.Logf("Valid custom domain accepted: %s", location)
	})

	t.Run("invalid_custom_domain_return_host_rejected", func(t *testing.T) {
		// Create token with invalid return_host stored in DB
		token := createToken(t, "/steal-cookies", invalidCustomDomain)

		form := url.Values{}
		form.Set("token", token)

		req := httptest.NewRequest("POST", "/verify-email", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = server.env.WebHost

		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)

		// Should reject with 400 Bad Request since CNAME doesn't resolve to our domain
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("Expected 400 Bad Request for invalid return_host, got %d. Body: %s", recorder.Code, recorder.Body.String())
		}
		t.Logf("Invalid custom domain rejected with status %d", recorder.Code)
	})

	t.Run("subdomain_return_host_works", func(t *testing.T) {
		boxSubdomain := server.env.BoxSub(boxName)
		// Create token with box subdomain as return_host stored in DB
		token := createToken(t, "/", boxSubdomain)

		form := url.Values{}
		form.Set("token", token)

		req := httptest.NewRequest("POST", "/verify-email", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = server.env.WebHost

		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)

		// Should redirect to /auth/confirm
		if recorder.Code != http.StatusSeeOther {
			t.Fatalf("Expected 303 redirect for box subdomain, got %d. Body: %s", recorder.Code, recorder.Body.String())
		}

		location := recorder.Header().Get("Location")
		if !strings.Contains(location, "/auth/confirm") {
			t.Fatalf("Expected redirect to /auth/confirm, got: %s", location)
		}
		t.Logf("Box subdomain accepted: %s", location)
	})
}
