package exe

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

// TestSSHDirectExecCommands tests direct command execution via SSH
func TestSSHDirectExecCommands(t *testing.T) {
	t.Parallel()
	server := NewTestServer(t)

	// Use mock container manager
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Generate test SSH key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	// hash := sha256.Sum256(signer.PublicKey().Marshal()) // No longer needed

	// Set up registered user
	email := "test@example.com"
	// teamName no longer used - machines are globally unique
	publicKeyStr := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))

	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}
	// Create alloc for user
	allocID := "test-alloc-" + userID[:8]
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		if _, err := tx.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email); err != nil {
			return err
		}
		// Add the SSH key to ssh_keys table and mark it as verified
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`,
			userID, publicKeyStr); err != nil {
			return err
		}
		// Add a second SSH key to test multiple key display
		if _, err := tx.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`,
			userID, "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDummykey..."); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', ?)`,
			allocID, userID, email); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to create user, SSH keys and alloc: %v", err)
	}

	// Test cases for exec commands
	testCases := []struct {
		name     string
		command  string
		expected string
	}{
		{
			name:     "list command",
			command:  "list",
			expected: "No machines found",
		},
		{
			name:     "help command",
			command:  "help",
			expected: "EXE.DEV",
		},
		{
			name:     "whoami command",
			command:  "whoami",
			expected: "test@example.com",
		},
		{
			name:     "help whoami command",
			command:  "help whoami",
			expected: "Show your user information",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create SSH client config
			config := &ssh.ClientConfig{
				User: "",
				Auth: []ssh.AuthMethod{
					ssh.PublicKeys(signer),
				},
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				Timeout:         5 * time.Second,
			}

			// Connect to SSH server
			client, err := ssh.Dial("tcp", server.sshLn.addr, config)
			if err != nil {
				t.Fatalf("Failed to connect to SSH server: %v", err)
			}
			defer client.Close()

			// Create a session
			session, err := client.NewSession()
			if err != nil {
				t.Fatalf("Failed to create session: %v", err)
			}
			defer session.Close()

			// Execute command
			var stdout, stderr bytes.Buffer
			session.Stdout = &stdout
			session.Stderr = &stderr

			err = session.Run(tc.command)

			output := stdout.String() + stderr.String()
			t.Logf("Command '%s' output: %s", tc.command, output)

			if !strings.Contains(output, tc.expected) {
				t.Errorf("Expected output to contain '%s', got: %s", tc.expected, output)
			}
		})
	}
}
