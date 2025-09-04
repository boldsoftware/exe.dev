package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestBrowserCommandIntegration(t *testing.T) {
	t.Parallel()
	// Create server
	server := NewTestServer(t)

	// Generate test SSH key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	signer, err := ssh.NewSignerFromKey(privateKey)
	require.NoError(t, err)

	// Connect to SSH server
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
	}

	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", server.sshLn.tcp.Port), config)
	require.NoError(t, err)
	defer client.Close()

	// Try running browser command before registration
	session, err := client.NewSession()
	require.NoError(t, err)
	defer session.Close()

	var output strings.Builder
	session.Stderr = &output
	session.Stdout = &output

	// Run browser command - should fail for unregistered user
	err = session.Run("browser")
	// Don't require NoError since unregistered users should see registration prompt

	result := output.String()
	// Should contain registration prompt
	require.Contains(t, result, "complete registration", "should prompt for registration")
}
