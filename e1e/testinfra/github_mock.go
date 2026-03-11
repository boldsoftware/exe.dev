package testinfra

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

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
