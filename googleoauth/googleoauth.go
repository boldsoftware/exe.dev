// Package googleoauth implements Google OAuth2 protocol logic for exe.dev.
// It handles OAuth config, state generation, ID token extraction, and
// authorization URL construction. The package is stateless — orchestration
// (user creation, cookie setting, redirects) lives in execore.
package googleoauth

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	emailpkg "exe.dev/email"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	// ProviderName is the value stored in auth_provider columns.
	ProviderName = "google"

	// StateExpiry is how long an OAuth state token is valid.
	StateExpiry = 10 * time.Minute
)

// Client holds the Google OAuth2 configuration.
type Client struct {
	ClientID     string
	ClientSecret string
	WebBaseURL   string // e.g. "https://exe.dev"
}

// Enabled returns true if Google OAuth credentials are configured.
func (c *Client) Enabled() bool {
	return c.ClientID != "" && c.ClientSecret != ""
}

// OAuth2Config returns the oauth2.Config for Google.
func (c *Client) OAuth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     c.ClientID,
		ClientSecret: c.ClientSecret,
		RedirectURL:  c.WebBaseURL + "/oauth/google/callback",
		Scopes:       []string{"openid", "email"},
		Endpoint:     google.Endpoint,
	}
}

// IDTokenClaims holds the claims we care about from a Google ID token.
type IDTokenClaims struct {
	Sub           string `json:"sub"` // GAIA ID
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

// ExtractClaims gets user claims from the OAuth2 token. It first tries the
// ID token embedded in the response, then falls back to the userinfo endpoint.
func ExtractClaims(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (*IDTokenClaims, error) {
	claims, err := extractIDToken(token)
	if err == nil && claims.Email != "" {
		return claims, nil
	}

	// ID token missing or lacks email — fetch from userinfo endpoint.
	return fetchUserInfo(ctx, cfg, token)
}

// extractIDToken parses the ID token from an OAuth2 token response.
// We trust the token because we got it directly from Google's token endpoint
// over TLS using our client credentials — no signature verification needed.
func extractIDToken(token *oauth2.Token) (*IDTokenClaims, error) {
	idTokenStr, ok := token.Extra("id_token").(string)
	if !ok || idTokenStr == "" {
		return nil, errors.New("no id_token in oauth response")
	}

	parts := strings.SplitN(idTokenStr, ".", 3)
	if len(parts) < 2 {
		return nil, errors.New("malformed id_token")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode id_token payload: %w", err)
	}

	var claims IDTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse id_token claims: %w", err)
	}

	if claims.Sub == "" {
		return nil, errors.New("id_token missing sub claim")
	}

	return &claims, nil
}

// fetchUserInfo calls Google's userinfo endpoint to get email and sub.
func fetchUserInfo(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (*IDTokenClaims, error) {
	client := cfg.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("userinfo returned %d: %s", resp.StatusCode, body)
	}

	var claims IDTokenClaims
	if err := json.NewDecoder(resp.Body).Decode(&claims); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo response: %w", err)
	}
	if claims.Sub == "" {
		return nil, errors.New("userinfo missing sub")
	}
	if claims.Email == "" {
		return nil, errors.New("userinfo missing email")
	}
	// Google's userinfo uses "email_verified" same as ID token.
	return &claims, nil
}

// GenerateState creates a cryptographically random OAuth state string.
func GenerateState() (string, error) {
	stateBytes := make([]byte, 32)
	if _, err := crand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate oauth state: %w", err)
	}
	return base64.URLEncoding.EncodeToString(stateBytes), nil
}

// AuthURL builds a Google OAuth authorization URL with the given state and email hint.
func (c *Client) AuthURL(state, emailHint string) string {
	cfg := c.OAuth2Config()
	return cfg.AuthCodeURL(state,
		oauth2.SetAuthURLParam("login_hint", emailHint),
	)
}

// ShouldUse determines if Google OAuth should be used for the given auth context.
// Parameters:
//   - email: the user's email address
//   - isNewUser: whether this is a new registration
//   - userAuthProvider: the user's auth_provider value (empty if not set or new user)
//   - inviteAuthProvider: the team invite's auth_provider value (empty if not applicable)
func (c *Client) ShouldUse(email string, isNewUser bool, userAuthProvider, inviteAuthProvider string) bool {
	if !c.Enabled() {
		return false
	}

	// Existing user with auth_provider='google'
	if userAuthProvider == ProviderName {
		return true
	}

	// Team invite with auth_provider='google'
	if inviteAuthProvider == ProviderName {
		return true
	}

	// Gmail address (new or existing)
	if emailpkg.IsGmailAddress(email) {
		return true
	}

	return false
}
