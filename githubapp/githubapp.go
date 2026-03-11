// Package githubapp implements GitHub App installation flow for exe.dev.
// It handles generating install URLs, exchanging authorization codes for
// user access tokens, and looking up the authenticated user.
package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	defaultTokenURL = "https://github.com/login/oauth/access_token"
	defaultAPIURL   = "https://api.github.com"
)

// Client holds the GitHub App configuration for the installation flow.
type Client struct {
	ClientID     string
	ClientSecret string
	AppSlug      string
	TokenURL     string // override for testing; defaults to GitHub
	APIURL       string // override for testing; defaults to GitHub
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

// InstallURL returns the GitHub App installation URL with the given state parameter.
func (c *Client) InstallURL(state string) string {
	return fmt.Sprintf("https://github.com/apps/%s/installations/new?state=%s", c.AppSlug, url.QueryEscape(state))
}

// TokenResponse is the result of exchanging an authorization code.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
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
