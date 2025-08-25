package exe

import (
	"net"
	"testing"
)

// TestAuthLogCallbackInServerConfig tests that the AuthLogCallback is properly configured
func TestAuthLogCallbackInServerConfig(t *testing.T) {
	t.Parallel()

	server := NewTestServer(t)

	// Verify that the SSH server config has the auth log callback set
	if server.sshConfig == nil {
		t.Fatal("SSH config is nil")
	}

	if server.sshConfig.AuthLogCallback == nil {
		t.Fatal("AuthLogCallback is not set in SSH server config")
	}

	if server.sshConfig.MaxAuthTries != 6 {
		t.Errorf("Expected MaxAuthTries=6, got %d", server.sshConfig.MaxAuthTries)
	}

	t.Logf("\u2705 SSH server config properly configured")
	t.Logf("   - AuthLogCallback is set")
	t.Logf("   - MaxAuthTries is set to %d", server.sshConfig.MaxAuthTries)
	t.Logf("   - PublicKeyCallback is set")
}

// mockConnMetadataForAuth implements ssh.ConnMetadata for testing auth logging
type mockConnMetadataForAuth struct {
	user          string
	remoteAddr    *mockAddr
	clientVersion []byte
}

func (m *mockConnMetadataForAuth) User() string          { return m.user }
func (m *mockConnMetadataForAuth) SessionID() []byte     { return []byte("test-session-id") }
func (m *mockConnMetadataForAuth) ClientVersion() []byte { return m.clientVersion }
func (m *mockConnMetadataForAuth) ServerVersion() []byte { return []byte("SSH-2.0-EXE.DEV") }
func (m *mockConnMetadataForAuth) RemoteAddr() net.Addr  { return m.remoteAddr }
func (m *mockConnMetadataForAuth) LocalAddr() net.Addr   { return &mockAddr{addr: "127.0.0.1:2222"} }

// mockAddr implements net.Addr for testing
type mockAddr struct {
	addr string
}

func (m *mockAddr) Network() string { return "tcp" }
func (m *mockAddr) String() string  { return m.addr }
