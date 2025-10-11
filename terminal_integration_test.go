package exe

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTerminalRouting(t *testing.T) {
	// Test that terminal subdomains are detected correctly
	tests := []struct {
		name     string
		host     string
		expected bool
	}{
		{"localhost terminal", "machine.xterm.localhost", true},
		{"localhost terminal with port", "machine.xterm.localhost:8080", true},
		{"production terminal", "machine.xterm.exe.dev", false}, // testing in dev mode
		{"regular proxy", "machine.localhost", false},
		{"main domain", "localhost", false},
		{"invalid", "xterm.localhost", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTerminalRequestWithBase(tt.host, ".xterm.localhost")
			if result != tt.expected {
				t.Errorf("isTerminalRequest(%q) = %v, want %v", tt.host, result, tt.expected)
			}
		})
	}
}

func TestTerminalStaticFiles(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Create a test user and auth them
	userID := "test-user-id"
	authCookie, err := server.createAuthCookie(t.Context(), userID, "testmachine.xterm.localhost")
	if err != nil {
		t.Fatalf("Failed to create auth cookie: %v", err)
	}

	// Test static file serving
	req := httptest.NewRequest("GET", "/static/xterm.js", nil)
	req.Host = "testmachine.xterm.localhost"
	req.AddCookie(&http.Cookie{Name: "exe-auth", Value: authCookie})
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200 for xterm.js, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Terminal") {
		t.Errorf("Expected xterm.js content, got response that doesn't contain 'Terminal'")
	}
}

func TestTerminalCleanupTimer(t *testing.T) {
	t.Parallel()
	// Test that inactive terminals are cleaned up
	// This is a quick unit test of the cleanup logic
	oldCleanupTicker := cleanupTicker
	oldTerminalSessions := terminalSessions

	// Reset state
	terminalSessions = make(map[string]*TerminalSession)

	// Create a mock terminal session that's old
	sessionKey := "test-user:test-machine:1"
	sess := &TerminalSession{
		EventsClients: make(map[chan []byte]bool),
		BoxName:       "test-box",
		UserID:        "test-user",
	}
	old := time.Now().Add(-15 * time.Minute)
	sess.LastActivity.Store(&old)
	terminalSessions[sessionKey] = sess

	// Run cleanup
	cleanupInactiveTerminals()

	// Should be cleaned up
	if len(terminalSessions) != 0 {
		t.Errorf("Expected terminal session to be cleaned up, but %d sessions remain", len(terminalSessions))
	}

	// Restore state
	cleanupTicker = oldCleanupTicker
	terminalSessions = oldTerminalSessions
}

func TestTerminalHostnameParsing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		hostname    string
		expected    string
		expectError bool
	}{
		{"valid localhost", "testmachine.xterm.localhost", "testmachine", false},
		{"valid localhost with port", "testmachine.xterm.localhost:8080", "testmachine", false},
		{"invalid - no machine name", ".xterm.localhost", "", true},
		{"invalid - multiple dots", "test.sub.xterm.localhost", "", true},
		{"invalid - not terminal domain", "testmachine.localhost", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseTerminalHostnameWithBase(tt.hostname, ".xterm.localhost")
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error for %q, but got none", tt.hostname)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error for %q: %v", tt.hostname, err)
				}
				if result != tt.expected {
					t.Errorf("parseTerminalHostname(%q) = %q, want %q", tt.hostname, result, tt.expected)
				}
			}
		})
	}
}
