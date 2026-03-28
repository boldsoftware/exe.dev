package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestEnabled(t *testing.T) {
	c := &Client{}
	if c.Enabled() {
		t.Fatal("expected disabled with empty fields")
	}
	c.ClientID = "test"
	if c.Enabled() {
		t.Fatal("expected disabled with only ClientID")
	}
	c.ClientSecret = "secret"
	if c.Enabled() {
		t.Fatal("expected disabled without AppSlug")
	}
	c.AppSlug = "my-app"
	if !c.Enabled() {
		t.Fatal("expected enabled with all fields set")
	}
}

func TestInstallURL(t *testing.T) {
	c := &Client{AppSlug: "my-app"}
	got := c.InstallURL("abc123")
	want := "https://github.com/apps/my-app/installations/new?state=abc123"
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestExchangeCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("expected Accept: application/json")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "test-client" {
			t.Errorf("expected client_id=test-client, got %s", r.Form.Get("client_id"))
		}
		if r.Form.Get("client_secret") != "test-secret" {
			t.Errorf("expected client_secret=test-secret, got %s", r.Form.Get("client_secret"))
		}
		if r.Form.Get("code") != "auth-code-123" {
			t.Errorf("expected code=auth-code-123, got %s", r.Form.Get("code"))
		}
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "ghu_test_token",
			RefreshToken: "ghr_test_refresh",
			TokenType:    "bearer",
		})
	}))
	defer srv.Close()

	c := &Client{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		TokenURL:     srv.URL,
	}
	resp, err := c.ExchangeCode(context.Background(), "auth-code-123")
	if err != nil {
		t.Fatal(err)
	}
	if resp.AccessToken != "ghu_test_token" {
		t.Errorf("expected ghu_test_token, got %s", resp.AccessToken)
	}
	if resp.RefreshToken != "ghr_test_refresh" {
		t.Errorf("expected ghr_test_refresh, got %s", resp.RefreshToken)
	}
}

func TestExchangeCodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"error": "bad_verification_code",
		})
	}))
	defer srv.Close()

	c := &Client{ClientID: "test", ClientSecret: "secret", TokenURL: srv.URL}
	_, err := c.ExchangeCode(context.Background(), "bad-code")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %s", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(struct {
			Login string `json:"login"`
		}{Login: "octocat"})
	}))
	defer srv.Close()

	c := &Client{ClientID: "test", ClientSecret: "secret", APIURL: srv.URL}
	login, err := c.GetUser(context.Background(), "test-token")
	if err != nil {
		t.Fatal(err)
	}
	if login != "octocat" {
		t.Errorf("expected octocat, got %s", login)
	}
}

func TestGetUserError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	c := &Client{ClientID: "test", ClientSecret: "secret", APIURL: srv.URL}
	_, err := c.GetUser(context.Background(), "bad-token")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRefreshUserToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("expected Accept: application/json")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") != "test-client" {
			t.Errorf("expected client_id=test-client, got %s", r.Form.Get("client_id"))
		}
		if r.Form.Get("client_secret") != "test-secret" {
			t.Errorf("expected client_secret=test-secret, got %s", r.Form.Get("client_secret"))
		}
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type=refresh_token, got %s", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "ghr_test_refresh" {
			t.Errorf("expected refresh_token=ghr_test_refresh, got %s", r.Form.Get("refresh_token"))
		}
		json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:           "ghu_new_token",
			RefreshToken:          "ghr_new_refresh",
			TokenType:             "bearer",
			ExpiresIn:             28800,
			RefreshTokenExpiresIn: 15552000,
		})
	}))
	defer srv.Close()

	c := &Client{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		TokenURL:     srv.URL,
	}
	resp, err := c.RefreshUserToken(context.Background(), "ghr_test_refresh")
	if err != nil {
		t.Fatal(err)
	}
	if resp.AccessToken != "ghu_new_token" {
		t.Errorf("expected ghu_new_token, got %s", resp.AccessToken)
	}
	if resp.RefreshToken != "ghr_new_refresh" {
		t.Errorf("expected ghr_new_refresh, got %s", resp.RefreshToken)
	}
	if resp.ExpiresIn != 28800 {
		t.Errorf("expected expires_in=28800, got %d", resp.ExpiresIn)
	}
	if resp.RefreshTokenExpiresIn != 15552000 {
		t.Errorf("expected refresh_token_expires_in=15552000, got %d", resp.RefreshTokenExpiresIn)
	}
}

func TestRefreshUserTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"error": "bad_refresh_token",
		})
	}))
	defer srv.Close()

	c := &Client{ClientID: "test", ClientSecret: "secret", TokenURL: srv.URL}
	_, err := c.RefreshUserToken(context.Background(), "bad-refresh-token")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTokenResponseExpiryHelpers(t *testing.T) {
	tr := &TokenResponse{
		ExpiresIn:             28800,
		RefreshTokenExpiresIn: 15552000,
	}

	at := tr.AccessTokenExpiresAt()
	if at == nil {
		t.Fatal("expected non-nil AccessTokenExpiresAt")
	}

	rt := tr.RefreshTokenExpiresAt()
	if rt == nil {
		t.Fatal("expected non-nil RefreshTokenExpiresAt")
	}

	// With zero values, should return nil.
	tr2 := &TokenResponse{}
	if tr2.AccessTokenExpiresAt() != nil {
		t.Error("expected nil AccessTokenExpiresAt for zero ExpiresIn")
	}
	if tr2.RefreshTokenExpiresAt() != nil {
		t.Error("expected nil RefreshTokenExpiresAt for zero RefreshTokenExpiresIn")
	}
}

func TestInstallationTokensEnabled(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		appID   int64
		key     *rsa.PrivateKey
		enabled bool
	}{
		{"both zero", 0, nil, false},
		{"only AppID", 42, nil, false},
		{"only PrivateKey", 0, key, false},
		{"both set", 42, key, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{AppID: tt.appID, PrivateKey: tt.key}
			if got := c.InstallationTokensEnabled(); got != tt.enabled {
				t.Errorf("got %v, want %v", got, tt.enabled)
			}
		})
	}
}

func TestMintInstallationToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify JWT on all requests.
		auth := r.Header.Get("Authorization")
		if auth == "" {
			t.Fatal("missing Authorization header")
		}
		tokenStr := auth[len("Bearer "):]
		parsed, err := jwt.Parse(tokenStr, func(token *jwt.Token) (any, error) {
			return &key.PublicKey, nil
		})
		if err != nil {
			t.Fatalf("failed to parse JWT: %v", err)
		}
		iss, _ := parsed.Claims.GetIssuer()
		if iss != "42" {
			t.Errorf("expected iss=42, got %s", iss)
		}

		switch {
		case r.Method == "GET" && r.URL.Path == "/app/installations/12345":
			// Return installation with all permissions granted.
			json.NewEncoder(w).Encode(map[string]any{
				"permissions": map[string]string{
					"actions":       "write",
					"checks":        "read",
					"contents":      "write",
					"issues":        "write",
					"metadata":      "read",
					"pull_requests": "write",
					"statuses":      "read",
					"workflows":     "write",
				},
			})

		case r.Method == "POST" && r.URL.Path == "/app/installations/12345/access_tokens":
			// Verify body.
			body, _ := io.ReadAll(r.Body)
			var reqBody map[string]any
			json.Unmarshal(body, &reqBody)
			repos, ok := reqBody["repositories"].([]any)
			if !ok || len(repos) != 1 || repos[0] != "empty" {
				t.Errorf("unexpected repositories: %v", reqBody["repositories"])
			}

			// Verify permissions include contents, issues, pull_requests, and metadata.
			perms, ok := reqBody["permissions"].(map[string]any)
			if !ok {
				t.Fatal("missing permissions in request body")
			}
			wantPerms := map[string]string{
				"actions":       "write",
				"checks":        "read",
				"contents":      "write",
				"issues":        "write",
				"metadata":      "read",
				"pull_requests": "write",
				"statuses":      "read",
				"workflows":     "write",
			}
			for k, v := range wantPerms {
				if perms[k] != v {
					t.Errorf("expected %s=%s, got %v", k, v, perms[k])
				}
			}
			if len(perms) != len(wantPerms) {
				t.Errorf("expected %d permissions, got %d: %v", len(wantPerms), len(perms), perms)
			}

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_mock_installation_token",
				"expires_at": "2099-01-01T00:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{AppID: 42, PrivateKey: key, APIURL: srv.URL}
	iat, err := c.MintInstallationToken(context.Background(), 12345, []string{"philz/empty"})
	if err != nil {
		t.Fatal(err)
	}
	if iat.Token != "ghs_mock_installation_token" {
		t.Errorf("expected ghs_mock_installation_token, got %s", iat.Token)
	}
}

func TestMintInstallationTokenPartialPermissions(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/app/installations/99999":
			// Installation with only contents and metadata.
			json.NewEncoder(w).Encode(map[string]any{
				"permissions": map[string]string{
					"contents": "write",
					"metadata": "read",
				},
			})

		case r.Method == "POST" && r.URL.Path == "/app/installations/99999/access_tokens":
			body, _ := io.ReadAll(r.Body)
			var reqBody map[string]any
			json.Unmarshal(body, &reqBody)

			perms, ok := reqBody["permissions"].(map[string]any)
			if !ok {
				t.Fatal("missing permissions in request body")
			}
			// Should only request the two permissions that are granted.
			if len(perms) != 2 {
				t.Errorf("expected 2 permissions, got %d: %v", len(perms), perms)
			}
			if perms["contents"] != "write" {
				t.Errorf("expected contents=write, got %v", perms["contents"])
			}
			if perms["metadata"] != "read" {
				t.Errorf("expected metadata=read, got %v", perms["metadata"])
			}

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_partial_token",
				"expires_at": "2099-01-01T00:00:00Z",
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{AppID: 42, PrivateKey: key, APIURL: srv.URL}
	iat, err := c.MintInstallationToken(context.Background(), 99999, []string{"owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if iat.Token != "ghs_partial_token" {
		t.Errorf("expected ghs_partial_token, got %s", iat.Token)
	}
}

func TestMintInstallationTokenNoContents(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Installation with no contents permission.
		json.NewEncoder(w).Encode(map[string]any{
			"permissions": map[string]string{
				"metadata": "read",
			},
		})
	}))
	defer srv.Close()

	c := &Client{AppID: 42, PrivateKey: key, APIURL: srv.URL}
	_, err = c.MintInstallationToken(context.Background(), 99999, []string{"owner/repo"})
	if err == nil {
		t.Fatal("expected error when contents permission is missing")
	}
	if !strings.Contains(err.Error(), "contents permission") {
		t.Errorf("expected error about contents permission, got: %v", err)
	}
}

func TestMintInstallationTokenDowngradePermission(t *testing.T) {
	// If installation grants contents:read but we want contents:write,
	// we should request contents:read.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/app/installations/88888":
			json.NewEncoder(w).Encode(map[string]any{
				"permissions": map[string]string{
					"contents": "read",
					"metadata": "read",
					"issues":   "read", // we want write but only read is granted
				},
			})

		case r.Method == "POST" && r.URL.Path == "/app/installations/88888/access_tokens":
			body, _ := io.ReadAll(r.Body)
			var reqBody map[string]any
			json.Unmarshal(body, &reqBody)

			perms := reqBody["permissions"].(map[string]any)
			if perms["contents"] != "read" {
				t.Errorf("expected contents=read (downgraded), got %v", perms["contents"])
			}
			if perms["issues"] != "read" {
				t.Errorf("expected issues=read (downgraded), got %v", perms["issues"])
			}

			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_downgraded_token",
				"expires_at": "2099-01-01T00:00:00Z",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{AppID: 42, PrivateKey: key, APIURL: srv.URL}
	iat, err := c.MintInstallationToken(context.Background(), 88888, []string{"owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if iat.Token != "ghs_downgraded_token" {
		t.Errorf("expected ghs_downgraded_token, got %s", iat.Token)
	}
}

func TestMintInstallationTokenError(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()

	c := &Client{AppID: 42, PrivateKey: key, APIURL: srv.URL}
	_, err = c.MintInstallationToken(context.Background(), 12345, []string{"philz/empty"})
	if err == nil {
		t.Fatal("expected error")
	}
}
