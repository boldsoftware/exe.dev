package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"strings"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func TestBrowserCommandGeneration(t *testing.T) {
	t.Parallel()
	// Create server
	server := NewTestServer(t)

	// Create a test user in the database
	userID := "test-user-123"
	email := "test@example.com"
	err := server.db.Tx(context.Background(), func(ctx context.Context, tx *sqlite.Tx) error {
		queries := exedb.New(tx.Conn())
		return queries.InsertUser(ctx, exedb.InsertUserParams{
			UserID: userID,
			Email:  email,
		})
	})
	require.NoError(t, err)

	// Create user and alloc objects
	user := &User{
		UserID: userID,
		Email:  email,
	}

	alloc := &Alloc{
		AllocID:   "test-alloc",
		AllocType: "test",
		Region:    "test",
	}

	// Create SSH server and command context
	sshServer := NewSSHServer(server, nil)
	cc := &CommandContext{
		User:      user,
		Alloc:     alloc,
		PublicKey: "test-key",
		Args:      []string{},
		SSHServer: sshServer,
		Output:    &strings.Builder{},
	}

	// Execute the browser command
	err = sshServer.handleBrowserCommand(context.Background(), cc)
	require.NoError(t, err, "browser command should execute successfully")

	// Check output
	output := cc.Output.(*strings.Builder).String()
	require.Contains(t, output, "Generating web authentication link", "should show generation message")
	require.Contains(t, output, "Magic link generated!", "should show success message")
	require.Contains(t, output, "Click this link to access", "should show instruction")
	require.Contains(t, output, "This link will expire in 15 minutes", "should show expiry warning")

	// Extract the URL from the output
	lines := strings.Split(output, "\n")
	var magicURL string
	for _, line := range lines {
		// Look for the line containing the URL
		if strings.Contains(line, "http://localhost:") && strings.Contains(line, "/auth/verify?token=") {
			// Strip ANSI codes to get clean URL
			magicURL = stripANSI(line)
			magicURL = strings.TrimSpace(magicURL)
			break
		}
	}
	require.NotEmpty(t, magicURL, "should contain a magic URL")

	// Verify that the URL contains the expected components
	require.Contains(t, magicURL, "http://localhost:", "should be localhost URL")
	require.Contains(t, magicURL, "/auth/verify?token=", "should contain auth verify endpoint")

	// Extract token from URL
	parts := strings.Split(magicURL, "token=")
	require.Len(t, parts, 2, "URL should contain token parameter")
	token := parts[1]
	require.NotEmpty(t, token, "token should not be empty")

	// Verify token exists in database
	err = server.db.Rx(context.Background(), func(ctx context.Context, rx *sqlite.Rx) error {
		queries := exedb.New(rx.Conn())
		verification, err := queries.GetEmailVerificationByToken(ctx, token)
		if err != nil {
			return err
		}

		// Verify the token is for the correct user
		require.Equal(t, user.UserID, verification.UserID, "token should be for the correct user")
		require.Equal(t, user.Email, verification.Email, "token should be for the correct email")

		// Verify token expires in approximately 15 minutes
		expectedExpiry := time.Now().Add(15 * time.Minute)
		timeDiff := verification.ExpiresAt.Sub(expectedExpiry).Abs()
		require.Less(t, timeDiff, 2*time.Minute, "token should expire in approximately 15 minutes")

		return nil
	})
	require.NoError(t, err, "should be able to verify token in database")
}

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

// stripANSI removes ANSI escape sequences from a string
func stripANSI(s string) string {
	// Simple ANSI stripper for test purposes - removes sequences like \033[1;36m and \033[0m
	result := s
	for {
		start := strings.Index(result, "\033[")
		if start == -1 {
			break
		}
		end := strings.Index(result[start:], "m")
		if end == -1 {
			break
		}
		result = result[:start] + result[start+end+1:]
	}
	return result
}
