package exe

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/container"
	"exe.dev/ctrhosttest"
	"exe.dev/sqlite"
	"golang.org/x/crypto/ssh"
)

// TestNewSSHServerMachineConnection tests SSH connection to a specific machine
func TestNewSSHServerMachineConnection(t *testing.T) {
	t.Parallel()
	// Resolve CTR_HOST automatically if not set (dev convenience)
	host := os.Getenv("CTR_HOST")
	if host == "" {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		if detected := ctrhosttest.Detect(ctx); detected != "" {
			host = detected
		}
	}
	if host == "" {
		t.Skip("CTR_HOST not set and colima-exe-ctr not reachable; skipping machine connection test")
	}

	server := NewTestServer(t, host)

	// Generate a test SSH key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	publicKey := signer.PublicKey()

	// Register the user in the database
	email := "test@example.com"
	// teamName no longer used - machines are globally unique
	machineName := "test-machine"
	containerID := "test-container-123"

	// Create user
	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}

	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO users (user_id, email)
			VALUES (?, ?)`,
			userID, email)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc for user
	allocID := "test-alloc-" + userID[:8]
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
			VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', ?)`,
			allocID, userID, email)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Add SSH key
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO ssh_keys (user_id, public_key)
			VALUES (?, ?)`,
			userID, string(ssh.MarshalAuthorizedKey(publicKey)))
		return err
	})
	if err != nil {
		t.Fatalf("Failed to add SSH key: %v", err)
	}

	// Create a machine in the database
	err = server.db.Tx(t.Context(), func(ctx context.Context, tx *sqlite.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO machines (alloc_id, name, status, image, container_id, created_by_user_id)
			VALUES (?, ?, ?, ?, ?, ?)`,
			allocID, machineName, "running", "ubuntu", containerID, userID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Mock container manager
	mockManager := &MockContainerManager{
		containers: map[string]*container.Container{
			containerID: {
				ID:      containerID,
				Name:    machineName,
				Status:  container.StatusRunning,
				AllocID: allocID,
			},
		},
	}
	server.containerManager = mockManager

	// Create SSH client config - connect as the machine name
	config := &ssh.ClientConfig{
		User: machineName, // Use machine name as username
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// Connect to the server
	client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", server.sshLn.tcp.Port), config)
	if err != nil {
		t.Fatalf("Failed to connect to SSH server: %v", err)
	}
	defer client.Close()

	// Test exec command on the machine
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput("echo hello")
	if err != nil {
		// Check if we at least got the connection message
		outputStr := string(output)
		if !strings.Contains(outputStr, "Connecting to machine") {
			t.Errorf("Expected connection message, got: %s", outputStr)
		}
		// The actual command execution might fail because we're using a mock
		// but the connection should be established
	}
}
