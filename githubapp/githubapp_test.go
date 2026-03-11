package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
