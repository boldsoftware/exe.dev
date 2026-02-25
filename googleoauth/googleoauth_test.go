package googleoauth

import (
	"testing"

	"golang.org/x/oauth2"
)

func TestEnabled(t *testing.T) {
	c := &Client{}
	if c.Enabled() {
		t.Fatal("empty client should not be enabled")
	}
	c = &Client{ClientID: "id", ClientSecret: "secret"}
	if !c.Enabled() {
		t.Fatal("configured client should be enabled")
	}
}

func TestShouldUse(t *testing.T) {
	c := &Client{ClientID: "id", ClientSecret: "secret"}

	tests := []struct {
		name               string
		email              string
		isNew              bool
		userAuthProvider   string
		inviteAuthProvider string
		want               bool
	}{
		{"gmail new user", "user@gmail.com", true, "", "", true},
		{"gmail existing user", "user@gmail.com", false, "", "", true},
		{"googlemail", "user@googlemail.com", true, "", "", true},
		{"google.com workspace", "user@google.com", false, "", "", false},
		{"non-gmail new", "user@example.com", true, "", "", false},
		{"non-gmail existing", "user@example.com", false, "", "", false},
		{"user auth provider google", "user@corp.com", false, "google", "", true},
		{"invite auth provider google", "user@corp.com", true, "", "google", true},
		{"both providers", "user@corp.com", false, "google", "google", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.ShouldUse(tt.email, tt.isNew, tt.userAuthProvider, tt.inviteAuthProvider)
			if got != tt.want {
				t.Errorf("ShouldUse(%q, %v, %q, %q) = %v, want %v",
					tt.email, tt.isNew, tt.userAuthProvider, tt.inviteAuthProvider, got, tt.want)
			}
		})
	}
}

func TestShouldUseDisabled(t *testing.T) {
	c := &Client{} // no credentials
	if c.ShouldUse("user@gmail.com", true, "", "") {
		t.Fatal("disabled client should never return true")
	}
}

func TestGenerateState(t *testing.T) {
	s1, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	s2, err := GenerateState()
	if err != nil {
		t.Fatal(err)
	}
	if s1 == s2 {
		t.Fatal("states should be unique")
	}
	if len(s1) < 40 {
		t.Fatalf("state too short: %q", s1)
	}
}

func TestExtractIDToken(t *testing.T) {
	// Test with empty token - should fail (no id_token extra)
	tok := &oauth2.Token{}
	_, err := extractIDToken(tok)
	if err == nil {
		t.Fatal("expected error for token without id_token")
	}
}

func TestOAuth2Config(t *testing.T) {
	c := &Client{
		ClientID:     "test-id",
		ClientSecret: "test-secret",
		WebBaseURL:   "https://exe.dev",
	}
	cfg := c.OAuth2Config()
	if cfg.ClientID != "test-id" {
		t.Fatalf("unexpected client ID: %s", cfg.ClientID)
	}
	if cfg.RedirectURL != "https://exe.dev/oauth/google/callback" {
		t.Fatalf("unexpected redirect URL: %s", cfg.RedirectURL)
	}
}

func TestAuthURL(t *testing.T) {
	c := &Client{
		ClientID:     "test-id",
		ClientSecret: "test-secret",
		WebBaseURL:   "https://exe.dev",
	}
	u := c.AuthURL("test-state", "user@gmail.com")
	if u == "" {
		t.Fatal("empty auth URL")
	}
}
