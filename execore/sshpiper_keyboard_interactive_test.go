package execore

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tg123/sshpiper/libplugin"
)

// mockKeyboardChallenge simulates a client responding to keyboard interactive challenges.
// The new handleKeyboardInteractive never invokes the challenge callback (it returns
// an AuthDenialError banner instead), but we still pass one through the type signature.
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
}

func (m *mockConnection) User() string {
	return m.user
}

func (m *mockConnection) RemoteAddr() string {
	return m.addr
}

func (m *mockConnection) GetMeta(string) string {
	return ""
}

func (m *mockConnection) UniqueID() string {
	return "test-unique-id"
}

func (m *mockConnection) LocalAddress() string {
	return "127.0.0.1:2222"
}

// TestKeyboardInteractiveAuthentication verifies that when a client with no
// SSH key falls through to keyboard-interactive, the handler returns a
// terminal AuthDenialError carrying the "SSH keys are required" banner.
// sshpiperd surfaces that via SSH_MSG_USERAUTH_BANNER before the auth failure,
// and (because it's terminal) does not offer any further auth methods.
func TestKeyboardInteractiveAuthentication(t *testing.T) {
	t.Parallel()
	piper := NewPiperPlugin(nil, "127.0.0.1", 0)

	mockConn := &mockConnection{
		user: "testuser",
		addr: "127.0.0.1:12345",
	}

	mockClient := &mockKeyboardChallenge{}

	upstream, err := piper.handleKeyboardInteractive(mockConn, mockClient.challenge)

	if upstream != nil {
		t.Errorf("Expected nil upstream, got %v", upstream)
	}
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	var denial *libplugin.AuthDenialError
	if !errors.As(err, &denial) {
		t.Fatalf("Expected *libplugin.AuthDenialError, got %T: %v", err, err)
	}
	if !strings.Contains(denial.Banner, "SSH keys are required") {
		t.Errorf("Expected banner to mention SSH keys are required, got: %q", denial.Banner)
	}
	if !strings.Contains(denial.Banner, "ssh-keygen") {
		t.Errorf("Expected banner to mention ssh-keygen, got: %q", denial.Banner)
	}

	if mockClient.index != 0 {
		t.Errorf("Expected challenge callback to be untouched, was called %d times", mockClient.index)
	}
}
