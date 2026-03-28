package execore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/googleoauth"
	"exe.dev/sqlite"
)

// fakeGoogleTokenServer returns an httptest.Server that mimics Google's token endpoint.
// It returns a valid OAuth2 token response with an embedded ID token containing the given email.
func fakeGoogleTokenServer(t *testing.T, email string) *httptest.Server {
	t.Helper()
	// Build a fake JWT id_token (header.payload.signature) — unsigned is fine
	// because ExtractClaims trusts tokens obtained over TLS from the token endpoint.
	claims := map[string]any{
		"sub":            "fake-gaia-id-12345",
		"email":          email,
		"email_verified": true,
	}
	payloadJSON, _ := json.Marshal(claims)
	headerJSON, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	idToken := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(payloadJSON) + ".fakesig"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fake-access-token",
			"token_type":    "Bearer",
			"expires_in":    3600,
			"id_token":      idToken,
			"refresh_token": "fake-refresh-token",
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGoogleOAuthNewUserRedirect verifies that redirect/return_host stored in
// the OAuth state are honored when a new user completes Google OAuth signup.
func TestGoogleOAuthNewUserRedirect(t *testing.T) {
	t.Parallel()
	email := "newgoogleuser@gmail.com"

	tokenServer := fakeGoogleTokenServer(t, email)

	server := newTestServer(t)
	server.googleOAuth = &googleoauth.Client{
		ClientID:          "test-client-id",
		ClientSecret:      "test-client-secret",
		WebBaseURL:        server.httpURL(),
		TestTokenEndpoint: tokenServer.URL,
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	t.Run("redirect_url", func(t *testing.T) {
		state := "test-state-redirect-" + fmt.Sprint(time.Now().UnixNano())
		redirectURL := "/billing/update"
		err := server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			_ = queries.CleanupExpiredOAuthStates(ctx, time.Now())
			return queries.InsertOAuthState(ctx, exedb.InsertOAuthStateParams{
				State:       state,
				Provider:    googleoauth.ProviderName,
				Email:       email,
				IsNewUser:   true,
				RedirectUrl: &redirectURL,
				ExpiresAt:   sqlite.NormalizeTime(time.Now().Add(5 * time.Minute)),
			})
		})
		if err != nil {
			t.Fatalf("insert oauth state: %v", err)
		}

		resp, err := client.Get(server.httpURL() + "/oauth/google/callback?code=fakecode&state=" + url.QueryEscape(state))
		if err != nil {
			t.Fatalf("GET callback: %v", err)
		}
		resp.Body.Close()

		// Should redirect (303 See Other) to the redirect URL.
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", resp.StatusCode)
		}
		location := resp.Header.Get("Location")
		if location != redirectURL {
			t.Fatalf("expected redirect to %q, got %q", redirectURL, location)
		}
	})

	t.Run("return_host", func(t *testing.T) {
		// Use a different email to avoid "user already exists" issues.
		email2 := "newgoogleuser2@gmail.com"
		tokenServer2 := fakeGoogleTokenServer(t, email2)
		server.googleOAuth.TestTokenEndpoint = tokenServer2.URL

		// Use a terminal-style host (xterm subdomain) so redirectAfterAuthWithParams
		// takes the terminal branch which doesn't need DNS resolution.
		state := "test-state-return-host-" + fmt.Sprint(time.Now().UnixNano())
		returnHost := "myvmtest.xterm.exe.cloud"
		redirectURL := "/"
		err := server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			_ = queries.CleanupExpiredOAuthStates(ctx, time.Now())
			return queries.InsertOAuthState(ctx, exedb.InsertOAuthStateParams{
				State:        state,
				Provider:     googleoauth.ProviderName,
				Email:        email2,
				IsNewUser:    true,
				ReturnHost:   &returnHost,
				RedirectUrl:  &redirectURL,
				LoginWithExe: true,
				ExpiresAt:    sqlite.NormalizeTime(time.Now().Add(5 * time.Minute)),
			})
		})
		if err != nil {
			t.Fatalf("insert oauth state: %v", err)
		}

		resp, err := client.Get(server.httpURL() + "/oauth/google/callback?code=fakecode&state=" + url.QueryEscape(state))
		if err != nil {
			t.Fatalf("GET callback: %v", err)
		}
		resp.Body.Close()

		// Should redirect (303 See Other) to the terminal auth flow.
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", resp.StatusCode)
		}
		location := resp.Header.Get("Location")
		if location == "" || location == "/" {
			t.Fatalf("expected redirect to return_host flow, got %q", location)
		}
		// The redirect should contain the terminal auth endpoint.
		if !strings.Contains(location, "__exe.dev/auth") {
			t.Fatalf("expected terminal auth redirect, got %q", location)
		}
		if !strings.Contains(location, "myvmtest.xterm.exe.cloud") {
			t.Fatalf("expected return_host in redirect, got %q", location)
		}
	})

	t.Run("no_redirect_shows_welcome", func(t *testing.T) {
		// Use a different email.
		email3 := "newgoogleuser3@gmail.com"
		tokenServer3 := fakeGoogleTokenServer(t, email3)
		server.googleOAuth.TestTokenEndpoint = tokenServer3.URL

		state := "test-state-no-redirect-" + fmt.Sprint(time.Now().UnixNano())
		err := server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			_ = queries.CleanupExpiredOAuthStates(ctx, time.Now())
			return queries.InsertOAuthState(ctx, exedb.InsertOAuthStateParams{
				State:     state,
				Provider:  googleoauth.ProviderName,
				Email:     email3,
				IsNewUser: true,
				ExpiresAt: sqlite.NormalizeTime(time.Now().Add(5 * time.Minute)),
			})
		})
		if err != nil {
			t.Fatalf("insert oauth state: %v", err)
		}

		resp, err := client.Get(server.httpURL() + "/oauth/google/callback?code=fakecode&state=" + url.QueryEscape(state))
		if err != nil {
			t.Fatalf("GET callback: %v", err)
		}
		resp.Body.Close()

		// Without redirect params, should show the welcome page (200 OK).
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 (welcome page), got %d", resp.StatusCode)
		}
	})
}

// TestGoogleOAuthExistingUserRedirect verifies that redirect/return_host stored in
// the OAuth state are honored when an existing user logs in via Google OAuth.
func TestGoogleOAuthExistingUserRedirect(t *testing.T) {
	t.Parallel()
	email := "existinggoogleuser@gmail.com"

	tokenServer := fakeGoogleTokenServer(t, email)

	server := newTestServer(t)
	server.googleOAuth = &googleoauth.Client{
		ClientID:          "test-client-id",
		ClientSecret:      "test-client-secret",
		WebBaseURL:        server.httpURL(),
		TestTokenEndpoint: tokenServer.URL,
	}

	// Create an existing user.
	userID := createTestUser(t, server, email)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	t.Run("redirect_url", func(t *testing.T) {
		state := "test-existing-redirect-" + fmt.Sprint(time.Now().UnixNano())
		redirectURL := "/billing/update"
		err := server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			_ = queries.CleanupExpiredOAuthStates(ctx, time.Now())
			return queries.InsertOAuthState(ctx, exedb.InsertOAuthStateParams{
				State:       state,
				Provider:    googleoauth.ProviderName,
				Email:       email,
				UserID:      &userID,
				IsNewUser:   false,
				RedirectUrl: &redirectURL,
				ExpiresAt:   sqlite.NormalizeTime(time.Now().Add(5 * time.Minute)),
			})
		})
		if err != nil {
			t.Fatalf("insert oauth state: %v", err)
		}

		resp, err := client.Get(server.httpURL() + "/oauth/google/callback?code=fakecode&state=" + url.QueryEscape(state))
		if err != nil {
			t.Fatalf("GET callback: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("expected 303, got %d", resp.StatusCode)
		}
		location := resp.Header.Get("Location")
		if location != redirectURL {
			t.Fatalf("expected redirect to %q, got %q", redirectURL, location)
		}
	})

	t.Run("no_redirect_goes_home", func(t *testing.T) {
		state := "test-existing-no-redirect-" + fmt.Sprint(time.Now().UnixNano())
		err := server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
			_ = queries.CleanupExpiredOAuthStates(ctx, time.Now())
			return queries.InsertOAuthState(ctx, exedb.InsertOAuthStateParams{
				State:     state,
				Provider:  googleoauth.ProviderName,
				Email:     email,
				UserID:    &userID,
				IsNewUser: false,
				ExpiresAt: sqlite.NormalizeTime(time.Now().Add(5 * time.Minute)),
			})
		})
		if err != nil {
			t.Fatalf("insert oauth state: %v", err)
		}

		resp, err := client.Get(server.httpURL() + "/oauth/google/callback?code=fakecode&state=" + url.QueryEscape(state))
		if err != nil {
			t.Fatalf("GET callback: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("expected 307, got %d", resp.StatusCode)
		}
		location := resp.Header.Get("Location")
		if location != "/" {
			t.Fatalf("expected redirect to /, got %q", location)
		}
	})
}
