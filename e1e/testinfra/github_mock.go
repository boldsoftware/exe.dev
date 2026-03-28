package testinfra

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// TestGitHubAppPrivateKeyPEM is a test RSA private key for E2E tests.
// It is used as EXE_GITHUB_APP_PRIVATE_KEY so exed can mint installation tokens
// against the mock GitHub server. Not a real key.
const TestGitHubAppPrivateKeyPEM = `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEAsBz+ZTc5k4NrBUqrQ3VdUvceOmR6OmhyNfegbxniKHcJfGqL
/xqLNyXISYtx+rN6oTdnCDrmsqEF/qrlZGXCD8oapbLBKgaItB/SLq82M8R9YKh2
v6VOyNdVR5qS+CIfZswkDGYj/KvqKfabw1N+ZStAORPOaZzcygOn5LmLCKTrpIkG
HKLoXGYA8M7jjjekykDPM5vTSRP6znbLRuM0aiXO2KGyYwAJdBnqi0H4lWgblWyO
FQ/+INsFrMUYSZ+sdgYxXGVZ7PPK/PGSya4Wt//Vm7dVGBOlE+86rkj92rgF7BiW
psWTmjccmcv0gSWcQEdCpXqyP6ndYKjK5OqsBwIDAQABAoIBAA7PmwPY7YcR/R6I
aKrXSsfyPG3D8XuXORoZmNNL8l2Q1UPMLVK0LfniiMH9CWyAfgZ86ejvrmMrcGoG
5hyknWBFxCpXRDBMNS1g17fq+4ksmmOJbdZkPDuSNuI7H7N6h/i8Blp8l8SYkkYa
yH+LnNk8fL3QHc97MusfL5ZOyS1d/UX3BGBGJStrm/4aUV0yp+depgY/Uu9y+6tA
3hkRdiH89rNkNptm4G2t0ZD06nGKS0b/GCQqER/UBW84JhTTWo3T1FrAQ1JO1RI1
NqomycAxLwlFbaX0035rlR11RVNpS1cX0Mgk7mfcVg8hfkams94UJ5M0mlY4239Y
lt4JVMECgYEA80zulhC5nefsR+Ja4Q7aTk6DuS2oRPscdGBLmV/rjSwx3SfDJNS/
/mb0UCzNNFoyQ2SkE+r9oOW/Sn6szq2wSASL4Tb1e99gV3V0lDCM7TTFvBJ5G6FQ
h4meUaRws/PFQTXEYI4syvCYcNfR5mEWPs3g0CMvoQ9ClDjGQ4jyz3cCgYEAuU5I
HexSRPnMoYx/Sxuv9pPPVxUa3q21UDl0KoxEQSOJ03cHEuV75N1B4M2XhL6Hkpum
nE9QHdzg0HN8+yRUycYknkf3Gb1J+0sco/ZPX2bjQpyZcRl0ejqP6ikdLitPDXMX
9yb9gx8r/Iud6Y4YWgw7to5vhZXYVVpN4wk+y/ECgYEAgkAEVmg7xrJbhxxCBMxb
yKI12JP9yngYgqDut/xm8Rvg0gGwce5Hnp1lW+qwLL/Aut2NDXC3OUTlxK7OOpM3
lUaB0B8JCrjKLegechsPxwmCdi35kfYpU3Y0QIblIyF0z3VGXV6f0kE9iuOvZkNB
knvLSAIeRH4T6Z//XDZbrv8CgYA87r7MCB9tSu682GQrIGmWHTh6nBf/zQLn5FyM
eR8ghD0X6fXLguZgdVjqQPBn1/bggIoir/naN/08zhz0wBeZWaxE18krD5E6LpK2
X5Ht/vkPuErEY+hnIMad6vMLcXZHJ+djf9CwwxlFq+s7F1xuj8M63k9Rj9pZBp7B
3xJlIQKBgQC8D9TeyBAiSVFYwC0YXtJOWbA4c0LCYGIGYdioivVYbNiMoV9n95+b
4lhqAhUppl21Sp76/CvgJq5YXqIdOnN7x/ZRhrHdupmUYNEHPvjKnp71W0y2JrhP
Wt0NuQbI9rRbjIvnE7dgEB5cpvILFiQR/ya+ejWcSmgNci6ZtWui2A==
-----END RSA PRIVATE KEY-----`

// TestGitHubAppID is the fake App ID used in E2E tests.
const TestGitHubAppID = "999999"

// MockInstallation represents a GitHub App installation in the mock server.
type MockInstallation struct {
	ID    int64
	Login string
}

// MockGitHubServer is a fake GitHub server for e2e tests.
// It serves the token exchange and user API endpoints.
//
// The access token returned is "ghu_<code>" (derived from the OAuth code).
// The /user/installations endpoint checks the token against per-token
// overrides (set via SetInstallationsForToken). Tokens without overrides
// get the default installations (one personal installation).
//
// The /user endpoint returns per-token user overrides (set via
// SetUserForToken). Tokens without overrides return "testghuser".
// A token can be configured to return 401 by calling SetUserForToken
// with an empty login.
type MockGitHubServer struct {
	Server *httptest.Server

	mu                   sync.Mutex
	defaultInstallations []MockInstallation
	tokenInstallations   map[string]*[]MockInstallation // token -> installations (pointer for mutability)
	tokenUsers           map[string]string              // token -> login (empty = 401)

	// Token rotation tracking (enabled via EnableTokenRotation).
	tokenRotation        bool
	validRefreshTokens   map[string]bool   // refresh token -> valid?
	refreshToAccessToken map[string]string // refresh token -> access token that was issued with it
	tokenExpiresIn       map[string]int64  // access token -> custom expires_in (for per-token override)
	refreshCounter       int               // monotonic counter for unique refresh tokens
}

// NewMockGitHubServer creates and starts a mock GitHub server.
// By default it returns one installation: {ID: 12345, Login: "testghuser"}.
func NewMockGitHubServer() *MockGitHubServer {
	m := &MockGitHubServer{
		defaultInstallations: []MockInstallation{
			{ID: 12345, Login: "testghuser"},
		},
		tokenInstallations:   make(map[string]*[]MockInstallation),
		tokenUsers:           make(map[string]string),
		validRefreshTokens:   make(map[string]bool),
		refreshToAccessToken: make(map[string]string),
		tokenExpiresIn:       make(map[string]int64),
	}

	mux := http.NewServeMux()

	// Token exchange: access_token is "ghu_<code>" so tests can predict it
	// and configure per-token installations before the callback.
	// Also handles refresh_token grant type for token renewal.
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.FormValue("grant_type") == "refresh_token" {
			oldRefresh := r.FormValue("refresh_token")

			m.mu.Lock()
			rotation := m.tokenRotation
			if rotation {
				if !m.validRefreshTokens[oldRefresh] {
					m.mu.Unlock()
					// Return an OAuth error response (200 with error field),
					// matching GitHub's real behavior for revoked refresh tokens.
					json.NewEncoder(w).Encode(map[string]any{
						"error":             "bad_refresh_token",
						"error_description": "The refresh token is invalid (already rotated)",
					})
					return
				}
				// Invalidate the old refresh token.
				delete(m.validRefreshTokens, oldRefresh)
				// Mint a new unique refresh token and mark it valid.
				m.refreshCounter++
				newRefresh := fmt.Sprintf("ghr_rotated_%d", m.refreshCounter)
				m.validRefreshTokens[newRefresh] = true
				newAccessToken := "ghu_refreshed_" + newRefresh
				// Propagate per-token installations: if the original access
				// token (associated with the old refresh token) had per-token
				// installations, copy them to the new access token.
				if origAccess, ok := m.refreshToAccessToken[oldRefresh]; ok {
					if instPtr, ok := m.tokenInstallations[origAccess]; ok {
						m.tokenInstallations[newAccessToken] = instPtr
					}
				}
				m.refreshToAccessToken[newRefresh] = newAccessToken
				delete(m.refreshToAccessToken, oldRefresh)
				m.mu.Unlock()

				json.NewEncoder(w).Encode(map[string]any{
					"access_token":             newAccessToken,
					"refresh_token":            newRefresh,
					"token_type":               "bearer",
					"expires_in":               28800,
					"refresh_token_expires_in": 15552000,
				})
				return
			}
			m.mu.Unlock()

			// Non-rotation mode: deterministic tokens for simpler tests.
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":             "ghu_refreshed_" + oldRefresh,
				"refresh_token":            "ghr_refreshed_" + oldRefresh,
				"token_type":               "bearer",
				"expires_in":               28800,
				"refresh_token_expires_in": 15552000,
			})
			return
		}

		code := r.FormValue("code")
		token := "ghu_" + code

		m.mu.Lock()
		expiresIn := int64(28800)
		if ei, ok := m.tokenExpiresIn[token]; ok {
			expiresIn = ei
		}
		refreshToken := "ghr_mock_refresh"
		if m.tokenRotation {
			m.refreshCounter++
			refreshToken = fmt.Sprintf("ghr_rotated_%d", m.refreshCounter)
			m.validRefreshTokens[refreshToken] = true
			m.refreshToAccessToken[refreshToken] = token
		}
		m.mu.Unlock()

		json.NewEncoder(w).Encode(map[string]any{
			"access_token":             token,
			"refresh_token":            refreshToken,
			"token_type":               "bearer",
			"expires_in":               expiresIn,
			"refresh_token_expires_in": 15552000,
		})
	})

	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

		m.mu.Lock()
		login, hasOverride := m.tokenUsers[token]
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if hasOverride {
			if login == "" {
				// Simulate 401 Bad credentials (wrong account).
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]any{
					"message":           "Bad credentials",
					"documentation_url": "https://docs.github.com/rest",
					"status":            "401",
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"login": login,
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"login": "testghuser",
		})
	})

	// User installations endpoint — returns per-token or default installations.
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")

		m.mu.Lock()
		var installs []MockInstallation
		if ptr, ok := m.tokenInstallations[token]; ok {
			installs = *ptr
		} else {
			installs = m.defaultInstallations
		}
		result := make([]map[string]any, len(installs))
		for i, inst := range installs {
			result[i] = map[string]any{
				"id": inst.ID,
				"account": map[string]any{
					"login": inst.Login,
				},
			}
		}
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"total_count":   len(result),
			"installations": result,
		})
	})

	// Installation lookup endpoint — searches all known installations.
	mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.PathValue("id")

		m.mu.Lock()
		var found *MockInstallation
		for _, inst := range m.defaultInstallations {
			if fmt.Sprintf("%d", inst.ID) == idStr {
				found = &inst
				break
			}
		}
		if found == nil {
			for _, ptr := range m.tokenInstallations {
				for _, inst := range *ptr {
					if fmt.Sprintf("%d", inst.ID) == idStr {
						found = &inst
						break
					}
				}
				if found != nil {
					break
				}
			}
		}
		m.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if found == nil {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{
				"message":           "Not Found",
				"documentation_url": "https://docs.github.com/rest",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": found.ID,
			"account": map[string]any{
				"login": found.Login,
			},
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
	})

	// Installation access token endpoint for GitHub integration token minting.
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_mock_installation_token",
			"expires_at": "2099-01-01T00:00:00Z",
		})
	})

	m.Server = httptest.NewServer(mux)
	return m
}

// EnableTokenRotation turns on strict refresh token rotation tracking.
// When enabled:
//   - Each code exchange issues a unique, tracked refresh token.
//   - Each refresh invalidates the old refresh token and issues a new one.
//   - Using an already-invalidated refresh token returns an OAuth error.
func (m *MockGitHubServer) EnableTokenRotation() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenRotation = true
}

// SetExpiresInForToken configures the expires_in value returned when the
// given access token is issued via code exchange. Use a small value (e.g. 1)
// so the token expires immediately for testing on-demand refresh.
func (m *MockGitHubServer) SetExpiresInForToken(token string, expiresIn int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenExpiresIn[token] = expiresIn
}

// DisableTokenRotation turns off strict refresh token rotation tracking
// and returns to the default deterministic behavior.
func (m *MockGitHubServer) DisableTokenRotation() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenRotation = false
	m.validRefreshTokens = make(map[string]bool)
	m.refreshToAccessToken = make(map[string]string)
	m.refreshCounter = 0
}

// GetValidRefreshTokens returns a copy of the currently valid refresh tokens.
// Useful for test assertions.
func (m *MockGitHubServer) GetValidRefreshTokens() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := make(map[string]bool, len(m.validRefreshTokens))
	for k, v := range m.validRefreshTokens {
		copy[k] = v
	}
	return copy
}

// SetInstallationsForToken configures installations returned when the given
// access token calls /user/installations. The returned slice pointer can be
// appended to later to simulate new installations appearing.
func (m *MockGitHubServer) SetInstallationsForToken(token string, installs []MockInstallation) *[]MockInstallation {
	m.mu.Lock()
	defer m.mu.Unlock()
	if installs == nil {
		installs = []MockInstallation{}
	}
	m.tokenInstallations[token] = &installs
	return m.tokenInstallations[token]
}

// SetUserForToken configures the login returned by GET /user for the given token.
// An empty login causes GET /user to return 401 Bad credentials (simulating
// the wrong-account scenario). Call with a non-empty login to "fix" the token
// (simulating the user retrying with the correct account).
func (m *MockGitHubServer) SetUserForToken(token, login string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenUsers[token] = login
}

// ClearUserForToken removes per-token user override, falling back to default.
func (m *MockGitHubServer) ClearUserForToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokenUsers, token)
}

// AddInstallationForToken adds an installation to the per-token list.
// This is safe to call while the server is polling.
func (m *MockGitHubServer) AddInstallationForToken(token string, id int64, login string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ptr, ok := m.tokenInstallations[token]; ok {
		*ptr = append(*ptr, MockInstallation{ID: id, Login: login})
	} else {
		installs := []MockInstallation{{ID: id, Login: login}}
		m.tokenInstallations[token] = &installs
	}
}

// Close shuts down the mock server.
func (m *MockGitHubServer) Close() {
	m.Server.Close()
}

// URL returns the base URL of the mock server.
func (m *MockGitHubServer) URL() string {
	return m.Server.URL
}
