package ghuser

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	_ "modernc.org/sqlite"
)

type Client struct {
	token      string
	db         *sql.DB
	stmt       *sql.Stmt
	httpClient *http.Client
}

type githubUserInfo struct {
	Login       string    `json:"login"`
	Email       string    `json:"email"`
	CreatedAt   time.Time `json:"created_at"`
	PublicRepos int       `json:"public_repos"`
}

type githubUserKey struct {
	Key string `json:"key"`
}

// githubUser fetches user info from GitHub API by user ID.
func (c *Client) githubUser(ctx context.Context, userID int64) (*githubUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.github.com/user/%d", userID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 401:
		return nil, fmt.Errorf("invalid GitHub token %q", c.token)
	case 429:
		return nil, fmt.Errorf("GitHub API rate limit exceeded")
	case 200:
		// OK, continued below
	default:
		return nil, fmt.Errorf("GitHub API error: %s", resp.Status)
	}

	var user githubUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode GitHub response: %w", err)
	}

	return &user, nil
}

// githubUserHasKey reports whether the provided login currently publishes trimmedKey as one of their SSH public keys.
func (c *Client) githubUserHasKey(ctx context.Context, login, trimmedKey string) (bool, error) {
	if login == "" {
		return false, fmt.Errorf("empty GitHub login")
	}
	trimmed := strings.TrimSpace(trimmedKey)
	if trimmed == "" {
		return false, fmt.Errorf("empty public key")
	}

	encodedLogin := url.PathEscape(login)
	baseURL := fmt.Sprintf("https://api.github.com/users/%s/keys", encodedLogin)
	// In theory this is paginated but if you have > 100 keys...fine, it doesn't always work for you.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 401:
		return false, fmt.Errorf("invalid GitHub token %q", c.token)
	case 429:
		return false, fmt.Errorf("GitHub API rate limit exceeded")
	case 404:
		return false, nil
	case 200:
		// OK, continued below
	default:
		return false, fmt.Errorf("GitHub API error: %s", resp.Status)
	}

	var keys []githubUserKey
	decodeErr := json.NewDecoder(resp.Body).Decode(&keys)
	if decodeErr != nil {
		return false, fmt.Errorf("failed to decode GitHub keys response: %w", decodeErr)
	}

	for _, k := range keys {
		if strings.TrimSpace(k.Key) == trimmed {
			return true, nil
		}
	}
	return false, nil
}

// New creates a new GitHub user client.
// token is the GitHub API token to use (required, because rate limiting is too severe otherwise).
// The dbPath should be a sqlite3 database with schema:
//
//	CREATE TABLE key_userid (keyHash BLOB PRIMARY KEY, userID INTEGER) WITHOUT ROWID;
//
// where keyHash is SHA-256(bytes.TrimSpace(ssh.MarshalAuthorizedKey(pk)))[:16]
// The makefile has targets for cleaning, downloading, and deploying the db.
func New(token, dbPath string) (*Client, error) {
	if token == "" {
		return nil, fmt.Errorf("no GitHub token provided")
	}
	if dbPath == "" {
		return nil, fmt.Errorf("no database path provided")
	}

	c := &Client{token: token, httpClient: &http.Client{Timeout: 10 * time.Second}}

	// Validate token with GitHub API by fetching a known user
	// TODO: maybe restore this...or maybe not.
	// It is disabled because it adds ~300ms and a GitHub dependency to startup time.
	// But it would be nice to catch invalid tokens earlier in dev mode.
	//
	// _, err := c.githubUser(context.Background(), 67496) // @josharian
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to validate GitHub token: %w", err)
	// }

	// Open database
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Check row count
	var count int64
	err = db.QueryRow("SELECT COUNT(*) FROM key_userid").Scan(&count)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to query database: %w", err)
	}
	if count < 20_000_000 {
		db.Close()
		return nil, fmt.Errorf("database has only %d rows, expected at least 20 million", count)
	}

	// Prepare statement for lookups
	stmt, err := db.Prepare("SELECT userID FROM key_userid WHERE keyHash = ?")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to prepare statement: %w", err)
	}

	c.db = db
	c.stmt = stmt
	return c, nil
}

// Close releases resources.
func (c *Client) Close() error {
	if c.stmt != nil {
		c.stmt.Close()
		c.stmt = nil
	}
	if c.db != nil {
		err := c.db.Close()
		c.db = nil
		return err
	}
	return nil
}

type Info struct {
	// IsGitHubUser indicates whether the public key is recognized as belonging to a GitHub user.
	IsGitHubUser bool
	// Email is the GitHub-validated email address for the user.
	// Excludes GitHub-proxied addresses.
	Email string
	// CreditOK indicates whether we trust this user to pay us.
	// Simple proxy: account is not super new, and they have a repo.
	CreditOK bool
}

// Info retrieves information about the GitHub user associated with the given public key.
//
// IMPORTANT: When making decisions based on the public key,
// you must be VERY careful to ensure that the public key used here
// is the public key that the user actually used to authenticate (thereby actually proving ownership of the corresponding private key).
//
// https://groups.google.com/g/golang-announce/c/-nPEi39gI4Q/m/cGVPJCqdAQAJ (a security fix announcement) explains the issue well:
//
// We have tagged version v0.31.0 of golang.org/x/crypto in order to address a security issue.
// x/crypto/ssh: misuse of ServerConfig.PublicKeyCallback may cause authorization bypass
// Applications and libraries which misuse the ServerConfig.PublicKeyCallback callback may be susceptible to an authorization bypass.
// The documentation for ServerConfig.PublicKeyCallback says that "A call to this function does not guarantee that the key offered is in fact used to authenticate." Specifically, the SSH protocol allows clients to inquire about whether a public key is acceptable before proving control of the corresponding private key. PublicKeyCallback may be called with multiple keys, and the order in which the keys were provided cannot be used to infer which key the client successfully authenticated with, if any. Some applications, which store the key(s) passed to PublicKeyCallback (or derived information) and make security relevant determinations based on it once the connection is established, may make incorrect assumptions.
// For example, an attacker may send public keys A and B, and then authenticate with A. PublicKeyCallback would be called only twice, first with A and then with B. A vulnerable application may then make authorization decisions based on key B for which the attacker does not actually control the private key.
// Since this API is widely misused, as a partial mitigation golang.org/x/cry...@v0.31.0 enforces the property that, when successfully authenticating via public key, the last key passed to ServerConfig.PublicKeyCallback will be the key used to authenticate the connection. PublicKeyCallback will now be called multiple times with the same key, if necessary. Note that the client may still not control the last key passed to PublicKeyCallback if the connection is then authenticated with a different method, such as PasswordCallback, KeyboardInteractiveCallback, or NoClientAuth.
// Users should be using the Extensions field of the Permissions return value from the various authentication callbacks to record data associated with the authentication attempt instead of referencing external state. Once the connection is established the state corresponding to the successful authentication attempt can be retrieved via the ServerConn.Permissions field. Note that some third-party libraries misuse the Permissions type by sharing it across authentication attempts; users of third-party libraries should refer to the relevant projects for guidance.
//
// See also https://github.com/gliderlabs/ssh/issues/242
func (c *Client) InfoKey(ctx context.Context, pubKey ssh.PublicKey) (Info, error) {
	if c == nil {
		return Info{}, fmt.Errorf("nil ghuser.Client")
	}
	if ctx == nil {
		return Info{}, fmt.Errorf("nil context")
	}
	authorizedKey := ssh.MarshalAuthorizedKey(pubKey)
	return c.InfoString(ctx, string(authorizedKey))
}

func (c *Client) InfoString(ctx context.Context, pubKey string) (Info, error) {
	if c == nil {
		return Info{}, fmt.Errorf("nil ghuser.Client")
	}
	if ctx == nil {
		return Info{}, fmt.Errorf("nil context")
	}
	// Calculate key hash
	trimmed := strings.TrimSpace(pubKey)
	hash := sha256.Sum256([]byte(trimmed))
	dbKey := hash[:16]
	return c.info(ctx, dbKey, trimmed)
}

func (c *Client) info(ctx context.Context, dbKey []byte, trimmedKey string) (Info, error) {
	return Info{}, fmt.Errorf("GitHub integration not complete yet")

	if c.db == nil || c.stmt == nil {
		return Info{}, fmt.Errorf("client not initialized")
	}

	var info Info

	// Look up user ID in database
	// Note: modernc.org/sqlite requires string for binary comparison
	var userID int64
	err := c.stmt.QueryRow(string(dbKey)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return info, nil // IsGitHubUser is false
	}
	if err != nil {
		return Info{}, fmt.Errorf("database query failed: %w", err)
	}

	user, err := c.githubUser(ctx, userID)
	if err != nil {
		return Info{}, fmt.Errorf("GitHub API request failed: %w", err)
	}

	valid, err := c.githubUserHasKey(ctx, user.Login, trimmedKey)
	if err != nil {
		return Info{}, fmt.Errorf("GitHub API request failed: %w", err)
	}

	if !valid {
		return info, nil
	}

	info.IsGitHubUser = true
	if !isGHProxyEmail(user.Email) {
		info.Email = user.Email
	}
	info.CreditOK = user.CreatedAt.Before(userCreationCutoff) && user.PublicRepos > 0

	return info, nil
}

// Jan 1, 2025 cutoff for user creation to determine CreditOK
var userCreationCutoff = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func isGHProxyEmail(email string) bool {
	return strings.Contains(email, "users.noreply.github.com")
}
