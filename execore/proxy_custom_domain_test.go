package execore

import (
	"context"
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
