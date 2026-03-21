// Package githubapp implements GitHub App installation flow for exe.dev.
// It handles generating install URLs, exchanging authorization codes for
// user access tokens, and looking up the authenticated user.
package githubapp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	defaultTokenURL = "https://github.com/login/oauth/access_token"
	defaultAPIURL   = "https://api.github.com"
)

// AuthError indicates an authentication/authorization failure (HTTP 401/403).
type AuthError struct {
	StatusCode int
	Body       string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("auth error (HTTP %d): %s", e.StatusCode, e.Body)
}

// IsAuthError reports whether err is a GitHub authentication failure.
func IsAuthError(err error) bool {
	var ae *AuthError
	return errors.As(err, &ae)
}

// Client holds the GitHub App configuration for the installation flow.
type Client struct {
	ClientID     string
	ClientSecret string
	AppSlug      string
	TokenURL     string // override for testing; defaults to GitHub
	APIURL       string // override for testing; defaults to GitHub

	// For minting installation access tokens (repo-scoped).
	AppID      int64
	PrivateKey *rsa.PrivateKey
}

// Enabled returns true if the GitHub App is fully configured.
func (c *Client) Enabled() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.AppSlug != ""
}

func (c *Client) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return defaultTokenURL
}

func (c *Client) apiURL() string {
	if c.APIURL != "" {
		return c.APIURL
	}
	return defaultAPIURL
}

// InstallURL returns the GitHub App installation URL.
// If state is non-empty, it is included as a query parameter.
func (c *Client) InstallURL(state string) string {
	u := fmt.Sprintf("https://github.com/apps/%s/installations/new", c.AppSlug)
	if state != "" {
		u += "?state=" + url.QueryEscape(state)
	}
	return u
}

// AuthorizeURL returns an OAuth authorization URL that identifies the user.
// Used as a fallback when the app is already installed and the user just
// needs to link their account. The callback will receive code and state
// but NOT installation_id.
func (c *Client) AuthorizeURL(state string) string {
	return fmt.Sprintf("https://github.com/login/oauth/authorize?client_id=%s&state=%s",
		url.QueryEscape(c.ClientID), url.QueryEscape(state))
}

// Installation is a GitHub App installation.
type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
}

// GetUserInstallations returns the installations accessible to the user with
// the given access token. This discovers which accounts have the app installed.
func (c *Client) GetUserInstallations(ctx context.Context, accessToken string) ([]Installation, error) {
	u := c.apiURL() + "/user/installations"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("user installations request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("reading user installations response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user installations returned %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Installations []Installation `json:"installations"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parsing user installations: %w", err)
	}
	return result.Installations, nil
}

// Repository is a GitHub repository accessible through an installation.
type Repository struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// GetInstallationRepositories returns the repositories accessible to the user
// through a specific installation. Uses the user access token.
func (c *Client) GetInstallationRepositories(ctx context.Context, accessToken string, installationID int64) ([]Repository, error) {
	var allRepos []Repository
	page := 1
	for {
		u := fmt.Sprintf("%s/user/installations/%d/repositories?per_page=100&page=%d", c.apiURL(), installationID, page)
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("installation repositories request failed: %w", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, fmt.Errorf("reading installation repositories response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("installation repositories returned %d: %s", resp.StatusCode, body)
		}

		var result struct {
			Repositories []Repository `json:"repositories"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parsing installation repositories: %w", err)
		}
		allRepos = append(allRepos, result.Repositories...)
		if len(result.Repositories) < 100 {
			break
		}
		page++
	}
	return allRepos, nil
}

// TokenResponse is the result of exchanging an authorization code.
type TokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int64  `json:"expires_in"`               // seconds until access token expires
	RefreshTokenExpiresIn int64  `json:"refresh_token_expires_in"` // seconds until refresh token expires
}

// sqliteTimeFmt is the format used by SQLite's CURRENT_TIMESTAMP and datetime().
const sqliteTimeFmt = "2006-01-02 15:04:05"

// AccessTokenExpiresAt returns the access token expiry as a SQLite-compatible
// UTC datetime string, or nil if not provided.
func (tr *TokenResponse) AccessTokenExpiresAt() *string {
	if tr.ExpiresIn <= 0 {
		return nil
	}
	s := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).UTC().Format(sqliteTimeFmt)
	return &s
}

// RefreshTokenExpiresAt returns the refresh token expiry as a SQLite-compatible
// UTC datetime string, or nil if not provided.
func (tr *TokenResponse) RefreshTokenExpiresAt() *string {
	if tr.RefreshTokenExpiresIn <= 0 {
		return nil
	}
	s := time.Now().Add(time.Duration(tr.RefreshTokenExpiresIn) * time.Second).UTC().Format(sqliteTimeFmt)
	return &s
}

// ExchangeCode exchanges an authorization code for a user access token.
func (c *Client) ExchangeCode(ctx context.Context, code string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"code":          {code},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.tokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange returned %d: %s", resp.StatusCode, body)
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parsing token response: %w", err)
	}

	// Check for error in response body (GitHub returns 200 with error field).
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return nil, fmt.Errorf("token exchange error: %s", errResp.Error)
	}

	if tr.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in response")
	}
	return &tr, nil
}

// GetUser returns the GitHub login for the given access token.
func (c *Client) GetUser(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.apiURL()+"/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("user request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", fmt.Errorf("reading user response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", &AuthError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("user request returned %d: %s", resp.StatusCode, body)
	}

	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return "", fmt.Errorf("parsing user response: %w", err)
	}
	if u.Login == "" {
		return "", fmt.Errorf("empty login in user response")
	}
	return u.Login, nil
}

// RefreshUserToken exchanges a refresh token for a new access token.
func (c *Client) RefreshUserToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	data := url.Values{
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.tokenURL(), strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("reading token refresh response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh returned %d: %s", resp.StatusCode, body)
	}

	var tr TokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parsing token refresh response: %w", err)
	}

	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return nil, fmt.Errorf("token refresh error: %s", errResp.Error)
	}

	if tr.AccessToken == "" {
		return nil, fmt.Errorf("empty access token in refresh response")
	}
	return &tr, nil
}

// GetInstallationAccount returns the login of the account (user or org) where
// the given installation is installed. Requires App JWT authentication.
func (c *Client) GetInstallationAccount(ctx context.Context, installationID int64) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    fmt.Sprintf("%d", c.AppID),
		IssuedAt:  jwt.NewNumericDate(now.Add(-10 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(c.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}

	u := fmt.Sprintf("%s/app/installations/%d", c.apiURL(), installationID)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+signed)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("installation lookup failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", fmt.Errorf("reading installation response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("installation lookup returned %d: %s", resp.StatusCode, body)
	}

	var inst struct {
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &inst); err != nil {
		return "", fmt.Errorf("parsing installation response: %w", err)
	}
	if inst.Account.Login == "" {
		return "", fmt.Errorf("empty account login in installation response")
	}
	return inst.Account.Login, nil
}

// InstallationTokensEnabled reports whether the client can mint installation access tokens.
func (c *Client) InstallationTokensEnabled() bool {
	return c.AppID != 0 && c.PrivateKey != nil
}

// InstallationAccessToken is a short-lived token scoped to specific repositories.
type InstallationAccessToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// MintInstallationToken creates a new installation access token scoped to the given repositories.
// Each entry in repoFullNames should be "owner/repo"; only the repo name (after the slash) is sent to GitHub.
func (c *Client) MintInstallationToken(ctx context.Context, installationID int64, repoFullNames []string) (*InstallationAccessToken, error) {
	var repoNames []string
	for _, fullName := range repoFullNames {
		_, repoName, ok := strings.Cut(fullName, "/")
		if !ok || repoName == "" {
			return nil, fmt.Errorf("invalid repository name %q: expected owner/repo", fullName)
		}
		repoNames = append(repoNames, repoName)
	}
	if len(repoNames) == 0 {
		return nil, fmt.Errorf("at least one repository is required")
	}

	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    fmt.Sprintf("%d", c.AppID),
		IssuedAt:  jwt.NewNumericDate(now.Add(-10 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(c.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("signing JWT: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"repositories": repoNames,
		"permissions": map[string]string{
			"contents": "write",
		},
	})
	if err != nil {
		return nil, err
	}

	u := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.apiURL(), installationID)
	req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+signed)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("installation token request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("reading installation token response: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("installation token request returned %d: %s", resp.StatusCode, respBody)
	}

	var iat InstallationAccessToken
	if err := json.Unmarshal(respBody, &iat); err != nil {
		return nil, fmt.Errorf("parsing installation token response: %w", err)
	}
	if iat.Token == "" {
		return nil, fmt.Errorf("empty token in installation token response")
	}
	return &iat, nil
}
