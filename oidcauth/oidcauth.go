// Package oidcauth implements Generic OIDC authentication for exe.dev.
// It supports any OIDC-compliant identity provider (Okta, Azure AD, etc.)
// by performing standard OIDC discovery and token exchange.
package oidcauth

import (
	"context"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const (
	// ProviderName is the value stored in auth_provider columns.
	ProviderName = "oidc"

	// StateExpiry is how long an OAuth state token is valid.
	StateExpiry = 10 * time.Minute
)

// DiscoveryDoc represents an OIDC discovery document from /.well-known/openid-configuration.
type DiscoveryDoc struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// ProviderConfig holds the configuration for a team's OIDC provider.
type ProviderConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	AuthURL      string // from discovery
	TokenURL     string // from discovery
	UserinfoURL  string // from discovery
	RedirectURL  string // our callback URL
}

// IDTokenClaims holds the claims we care about from an OIDC ID token.
type IDTokenClaims struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"` // bool or string depending on provider
}

// IsEmailVerified returns whether the email_verified claim is truthy.
// Some providers return a bool, others return a string "true".
// A nil value (claim absent from the token) is treated as verified,
// because providers like Okta omit the claim rather than setting it.
func (c *IDTokenClaims) IsEmailVerified() bool {
	switch v := c.EmailVerified.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	}
	return c.EmailVerified == nil
}

// Discover fetches the OIDC discovery document from the given issuer URL.
// Returns the parsed discovery document or an error.
func Discover(ctx context.Context, issuerURL string) (*DiscoveryDoc, error) {
	wellKnown := strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"

	req, err := http.NewRequestWithContext(ctx, "GET", wellKnown, nil)
	if err != nil {
		return nil, fmt.Errorf("create discovery request: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("discovery returned %d: %s", resp.StatusCode, body)
	}

	var doc DiscoveryDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode discovery document: %w", err)
	}

	if doc.AuthorizationEndpoint == "" {
		return nil, errors.New("discovery document missing authorization_endpoint")
	}
	if doc.TokenEndpoint == "" {
		return nil, errors.New("discovery document missing token_endpoint")
	}

	return &doc, nil
}

// OAuth2Config returns an oauth2.Config for this provider.
func (p *ProviderConfig) OAuth2Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		RedirectURL:  p.RedirectURL,
		Scopes:       []string{"openid", "email"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  p.AuthURL,
			TokenURL: p.TokenURL,
		},
	}
}

// BuildAuthURL builds an authorization URL with the given state and email hint.
func (p *ProviderConfig) BuildAuthURL(state, emailHint string) string {
	cfg := p.OAuth2Config()
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("login_hint", emailHint),
	}
	return cfg.AuthCodeURL(state, opts...)
}

// Exchange exchanges an authorization code for an OAuth2 token.
func (p *ProviderConfig) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	cfg := p.OAuth2Config()
	return cfg.Exchange(ctx, code)
}

// ExtractClaims extracts user claims from the OAuth2 token.
// Tries the ID token first, then falls back to the userinfo endpoint.
func (p *ProviderConfig) ExtractClaims(ctx context.Context, token *oauth2.Token) (*IDTokenClaims, error) {
	claims, err := extractIDToken(token)
	if err == nil && claims.Email != "" {
		return claims, nil
	}

	if p.UserinfoURL == "" {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("no userinfo endpoint and id_token lacks email")
	}

	return fetchUserInfo(ctx, p.OAuth2Config(), token, p.UserinfoURL)
}

// extractIDToken parses the ID token from an OAuth2 token response.
// We trust the token because we got it directly from the IdP's token endpoint
// over TLS using our client credentials.
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
		return nil, fmt.Errorf("decode id_token payload: %w", err)
	}

	var claims IDTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse id_token claims: %w", err)
	}

	if claims.Sub == "" {
		return nil, errors.New("id_token missing sub claim")
	}

	return &claims, nil
}

// fetchUserInfo calls the provider's userinfo endpoint.
func fetchUserInfo(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token, userinfoURL string) (*IDTokenClaims, error) {
	client := cfg.Client(ctx, token)
	resp, err := client.Get(userinfoURL)
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
		return nil, fmt.Errorf("decode userinfo response: %w", err)
	}
	if claims.Sub == "" {
		return nil, errors.New("userinfo missing sub")
	}
	if claims.Email == "" {
		return nil, errors.New("userinfo missing email")
	}
	return &claims, nil
}

// GenerateState creates a cryptographically random OAuth state string.
func GenerateState() (string, error) {
	stateBytes := make([]byte, 32)
	if _, err := crand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("generate oauth state: %w", err)
	}
	return base64.URLEncoding.EncodeToString(stateBytes), nil
}

// isLoopback returns whether the given hostname is a loopback address.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// TestConnectivity verifies that the OIDC provider is reachable and properly configured.
// It runs discovery and validates the required endpoints are present.
func TestConnectivity(ctx context.Context, issuerURL string) (*DiscoveryDoc, error) {
	doc, err := Discover(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}

	// Validate the issuer matches what we expect
	normalizedIssuer := strings.TrimRight(issuerURL, "/")
	normalizedDocIssuer := strings.TrimRight(doc.Issuer, "/")
	if !strings.EqualFold(normalizedIssuer, normalizedDocIssuer) {
		return nil, fmt.Errorf("issuer mismatch: expected %q, got %q", normalizedIssuer, doc.Issuer)
	}

	// Validate URLs are HTTPS (exempt loopback addresses per RFC 8252 §8.3).
	for _, u := range []struct{ name, val string }{
		{"authorization_endpoint", doc.AuthorizationEndpoint},
		{"token_endpoint", doc.TokenEndpoint},
	} {
		parsed, err := url.Parse(u.val)
		if err != nil {
			return nil, fmt.Errorf("invalid %s URL: %w", u.name, err)
		}
		if parsed.Scheme != "https" && !isLoopback(parsed.Hostname()) {
			return nil, fmt.Errorf("%s must use HTTPS, got %q", u.name, u.val)
		}
	}

	return doc, nil
}
