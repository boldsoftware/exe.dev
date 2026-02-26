package oidcauth

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"golang.org/x/oauth2"
)

func TestIsEmailVerified(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{"bool true", true, true},
		{"bool false", false, false},
		{"string true", "true", true},
		{"string True", "True", true},
		{"string false", "false", false},
		{"nil", nil, false},
		{"int", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &IDTokenClaims{EmailVerified: tt.value}
			if got := c.IsEmailVerified(); got != tt.want {
				t.Errorf("IsEmailVerified() = %v, want %v", got, tt.want)
			}
		})
	}
}

func makeIDToken(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payloadBytes, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	sig := base64.RawURLEncoding.EncodeToString([]byte("fakesig"))
	return header + "." + payload + "." + sig
}

func TestExtractIDToken_Valid(t *testing.T) {
	idToken := makeIDToken(map[string]any{
		"sub":            "user-123",
		"email":          "alice@example.com",
		"email_verified": true,
	})

	token := (&oauth2.Token{}).WithExtra(map[string]any{
		"id_token": idToken,
	})

	claims, err := extractIDToken(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claims.Sub != "user-123" {
		t.Errorf("Sub = %q, want %q", claims.Sub, "user-123")
	}
	if claims.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", claims.Email, "alice@example.com")
	}
	if !claims.IsEmailVerified() {
		t.Error("expected email_verified to be true")
	}
}

func TestExtractIDToken_MissingIDToken(t *testing.T) {
	token := (&oauth2.Token{}).WithExtra(map[string]any{})

	_, err := extractIDToken(token)
	if err == nil {
		t.Fatal("expected error for missing id_token")
	}
	if err.Error() != "no id_token in oauth response" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExtractIDToken_MissingSub(t *testing.T) {
	idToken := makeIDToken(map[string]any{
		"email": "alice@example.com",
	})

	token := (&oauth2.Token{}).WithExtra(map[string]any{
		"id_token": idToken,
	})

	_, err := extractIDToken(token)
	if err == nil {
		t.Fatal("expected error for missing sub")
	}
	if err.Error() != "id_token missing sub claim" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerateState(t *testing.T) {
	state, err := GenerateState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state == "" {
		t.Error("expected non-empty state")
	}
	// 32 bytes -> 44 chars in base64
	if len(state) < 40 {
		t.Errorf("state too short: %d chars", len(state))
	}

	// Generate another and verify they're different (randomness check)
	state2, err := GenerateState()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state == state2 {
		t.Error("two generated states should not be identical")
	}
}
