package execore

import (
	"fmt"
	"strings"
	"testing"
)

// mockKeyboardChallenge simulates a client responding to keyboard interactive challenges
type mockKeyboardChallenge struct {
	responses []string
	index     int
}

func (m *mockKeyboardChallenge) challenge(user, instruction, question string, echo bool) (string, error) {
	if m.index >= len(m.responses) {
		return "", fmt.Errorf("no more responses available")
	}
	response := m.responses[m.index]
	m.index++
	return response, nil
}

// mockConnection implements libplugin.ConnMetadata for testing
type mockConnection struct {
	user string
	addr string
	meta map[string]string
}

func (m *mockConnection) User() string {
	return m.user
}

func (m *mockConnection) RemoteAddr() string {
	return m.addr
}

func (m *mockConnection) GetMeta(key string) string {
	if m.meta == nil {
		return ""
	}
	return m.meta[key]
}

func (m *mockConnection) UniqueID() string {
	return "test-unique-id"
}

func (m *mockConnection) LocalAddress() string {
	return "127.0.0.1:2222"
}

func TestKeyboardInteractiveAuthentication(t *testing.T) {
	t.Parallel()
	piper := NewPiperPlugin(nil, 0)

	// Create mock connection metadata
	mockConn := &mockConnection{
		user: "testuser",
		addr: "127.0.0.1:12345",
	}

	// Create mock keyboard interactive challenge client
	mockClient := &mockKeyboardChallenge{
		responses: []string{""}, // User presses Enter
	}

	// Test keyboard interactive authentication
	upstream, err := piper.handleKeyboardInteractive(mockConn, mockClient.challenge)

	// Should return nil upstream (deny access)
	if upstream != nil {
		t.Errorf("Expected nil upstream, got %v", upstream)
	}

	// Should return an error explaining public key requirement
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	if !strings.Contains(err.Error(), "SSH public key authentication is required") {
		t.Errorf("Expected error to mention SSH public key requirement, got: %v", err)
	}

	t.Logf("✅ Keyboard interactive authentication correctly denies access with helpful message: %v", err)
}

func TestKeyboardInteractiveNoRetries(t *testing.T) {
	t.Parallel()
	piper := NewPiperPlugin(nil, 0)

	// Create mock connection metadata with same unique ID
	mockConn := &mockConnection{
		user: "testuser",
		addr: "127.0.0.1:12345",
	}

	challengeCallCount := 0
	mockClient := func(user, instruction, question string, echo bool) (string, error) {
		challengeCallCount++
		return "", nil // User presses Enter
	}

	// First call - should show message
	upstream1, err1 := piper.handleKeyboardInteractive(mockConn, mockClient)
	if upstream1 != nil {
		t.Errorf("Expected nil upstream on first call, got %v", upstream1)
	}
	if err1 == nil {
		t.Error("Expected error on first call, got nil")
	}
	if challengeCallCount != 1 {
		t.Errorf("Expected challenge to be called once on first attempt, got %d", challengeCallCount)
	}

	// Second call with same connection - should NOT show message again
	upstream2, err2 := piper.handleKeyboardInteractive(mockConn, mockClient)
	if upstream2 != nil {
		t.Errorf("Expected nil upstream on retry, got %v", upstream2)
	}
	if err2 == nil {
		t.Error("Expected error on retry, got nil")
	}
	if challengeCallCount != 1 {
		t.Errorf("Expected challenge to still be called only once after retry, got %d", challengeCallCount)
	}

	t.Log("✅ Keyboard interactive authentication shows message only once per connection")
}
