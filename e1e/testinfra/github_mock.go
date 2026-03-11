package testinfra

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// MockGitHubServer is a fake GitHub server for e2e tests.
// It serves the token exchange and user API endpoints.
type MockGitHubServer struct {
	Server *httptest.Server
}

// NewMockGitHubServer creates and starts a mock GitHub server.
func NewMockGitHubServer() *MockGitHubServer {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "ghu_mock_token",
			"refresh_token": "ghr_mock_refresh",
			"token_type":    "bearer",
		})
	})

	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"login": "testghuser",
		})
	})

	// User installations endpoint — returns installations accessible to the user.
	mux.HandleFunc("GET /user/installations", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"total_count": 1,
			"installations": []map[string]any{
				{
					"id": 12345,
					"account": map[string]any{
						"login": "testghuser",
					},
				},
			},
		})
	})

	// Installation lookup endpoint — returns the account the app is installed on.
	mux.HandleFunc("GET /app/installations/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": 12345,
			"account": map[string]any{
				"login": "testghuser",
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

	srv := httptest.NewServer(mux)
	return &MockGitHubServer{Server: srv}
}

// Close shuts down the mock server.
func (m *MockGitHubServer) Close() {
	m.Server.Close()
}

// URL returns the base URL of the mock server.
func (m *MockGitHubServer) URL() string {
	return m.Server.URL
}
