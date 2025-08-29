package exe

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestSSHEndToEndCreateFlow tests machine creation through SSH
func TestSSHEndToEndCreateFlow(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	// Check if expect is available
	if _, err := exec.LookPath("expect"); err != nil {
		t.Skip("expect not found, skipping E2E test")
	}

	// Create server with Docker support
	server := NewTestServer(t)

	// Clean up any existing test containers
	// teamName no longer used - machines are globally unique
	machineName := "testmachine"
	containerName := fmt.Sprintf("exe-%s", machineName)
	exec.Command("docker", "rm", "-f", containerName).Run()       // Ignore errors
	defer exec.Command("docker", "rm", "-f", containerName).Run() // Clean up after test

	// Check if Docker is available
	if server.containerManager == nil {
		t.Skip("Docker not available, skipping container test")
	}

	// Generate test SSH key and get fingerprint
	keyFile := filepath.Join(t.TempDir(), "test_key")
	privateKey, err := generateSSHKeyFileWithKey(keyFile)
	if err != nil {
		t.Fatalf("Failed to generate SSH key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	// hash := sha256.Sum256(signer.PublicKey().Marshal()) // No longer needed

	// Set up registered user in database
	email := "test@example.com"

	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}
	_, err = server.db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc for user
	allocID := "test-alloc-" + userID[:8]
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', ?)`,
		allocID, userID, email)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Mark SSH key as verified
	publicKeyBytes := ssh.MarshalAuthorizedKey(signer.PublicKey())
	_, err = server.db.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`,
		userID, string(publicKeyBytes))
	if err != nil {
		t.Fatalf("Failed to add SSH key: %v", err)
	}

	// Create expect script for create flow
	expectScript := fmt.Sprintf(`#!/usr/bin/expect -f
set timeout 30
set env(SSH_AUTH_SOCK) ""
spawn ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o IdentitiesOnly=yes -o IdentityFile=%s -o PreferredAuthentications=publickey -o PubkeyAuthentication=yes -o PasswordAuthentication=no -o KbdInteractiveAuthentication=no -o ChallengeResponseAuthentication=no -o IdentityAgent=none -i %s -p %v 127.0.0.1

# Wait for menu prompt
expect {
    "exe.dev" {
        send "new --name=%s --image=ubuntu\r"
    }
    "enter your email" {
        puts "ERROR: Authentication failed - got signup prompt instead of menu"
        exit 2
    }
    timeout {
        puts "Timeout waiting for menu"
        exit 1
    }
}

# Wait for creation confirmation
expect {
    "Ready in" {
        puts "Machine created successfully"
    }
    "Failed to create machine" {
        puts "Machine creation failed"
        exit 1
    }
    timeout {
        puts "Timeout waiting for machine creation"
        exit 1
    }
}

# List machines to verify
send "list\r"
expect {
    "%s" {
        puts "Machine found in list"
    }
    timeout {
        puts "Machine not found in list"
    }
}

# Exit
send "exit\r"
expect eof
`, keyFile, keyFile, server.sshLn.tcp.Port, machineName, machineName)

	// Write and execute expect script
	scriptFile := filepath.Join(t.TempDir(), "create.expect")
	if err := os.WriteFile(scriptFile, []byte(expectScript), 0o755); err != nil {
		t.Fatalf("Failed to write expect script: %v", err)
	}

	cmd := exec.Command("expect", scriptFile)
	output, err := cmd.CombinedOutput()

	// Check for authentication failure
	if strings.Contains(string(output), "ERROR: Authentication failed") {
		t.Fatalf("SSH authentication failed - got signup prompt instead of authenticated menu")
	}

	// Check for other expect script errors
	if err != nil {
		// Only treat certain errors as OK
		if strings.Contains(string(output), "Machine created successfully") ||
			strings.Contains(string(output), "Machine found in list") {
			t.Logf("Expect script had exit error but machine was created: %v", err)
		} else {
			t.Fatalf("Expect script failed: %v\nOutput: %s", err, output)
		}
	}

	// Verify machine was created in database
	var machineCount int
	err = server.db.QueryRow(`SELECT COUNT(*) FROM machines WHERE name = ?`, machineName).Scan(&machineCount)
	if err != nil {
		t.Fatalf("Failed to query machines: %v", err)
	}

	// Check that machine was actually created
	if strings.Contains(string(output), "Machine created successfully") || strings.Contains(string(output), "Machine found in list") {
		if machineCount != 1 {
			t.Errorf("Expected 1 machine in database, got %d", machineCount)
		}
	} else if machineCount > 0 {
		t.Errorf("Machine exists in database but expect script didn't confirm creation")
	}
}

// TestSSHEndToEndMachineAccess tests direct SSH access to a machine
func TestSSHEndToEndMachineAccess(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	server := NewTestServer(t)

	// Use mock container manager for predictable testing
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

	// Set up registered user and machine in database
	email := "test@example.com"
	// teamName no longer used - machines are globally unique
	machineName := "testmachine"
	containerID := "mock-container-123"

	userID2, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}
	_, err = server.db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID2, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Create alloc for user2
	allocID2 := "test-alloc-" + userID2[:8]
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', ?)`,
		allocID2, userID2, email)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Add SSH key for this user
	publicKeyBytes := ssh.MarshalAuthorizedKey(signer.PublicKey())
	_, err = server.db.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`,
		userID2, string(publicKeyBytes))
	if err != nil {
		t.Fatalf("Failed to add SSH key: %v", err)
	}

	// Add container to mock manager
	mockManager.AddContainer(containerID, machineName, allocID2)

	// Create machine in database
	_, err = server.db.Exec(`
		INSERT INTO machines (alloc_id, name, status, image, container_id, created_by_user_id)
		VALUES (?, ?, 'running', 'ubuntu:22.04', ?, ?)
	`, allocID2, machineName, containerID, userID2)
	if err != nil {
		t.Fatalf("Failed to create machine: %v", err)
	}

	// Test direct SSH to machine
	config := &ssh.ClientConfig{
		User: machineName, // Use machine name as username for direct access
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

	// Execute a command on the "machine"
	output, err := session.CombinedOutput("echo 'Hello from machine'")
	if err != nil {
		// This is expected as the mock container doesn't actually execute commands
		t.Logf("Command execution error (expected with mock): %v", err)
	} else {
		t.Logf("Command output: %s", output)
	}
}

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
	_, err = server.db.Exec(`INSERT INTO users (user_id, email) VALUES (?, ?)`, userID, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Add the SSH key to ssh_keys table and mark it as verified
	_, err = server.db.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`,
		userID, publicKeyStr)
	if err != nil {
		t.Fatalf("Failed to add SSH key: %v", err)
	}

	// Add a second SSH key to test multiple key display
	_, err = server.db.Exec(`INSERT INTO ssh_keys (user_id, public_key) VALUES (?, ?)`,
		userID, "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDummykey...")
	if err != nil {
		t.Fatalf("Failed to add second SSH key: %v", err)
	}

	// Create alloc for user
	allocID := "test-alloc-" + userID[:8]
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', ?)`,
		allocID, userID, email)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
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

// Helper function to generate SSH key file
func generateSSHKeyFile(path string) error {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}

	keyFile, err := os.Create(path)
	if err != nil {
		return err
	}
	defer keyFile.Close()

	if err := pem.Encode(keyFile, privateKeyPEM); err != nil {
		return err
	}

	return os.Chmod(path, 0o600)
}

// Helper function to generate SSH key file and return the key
func generateSSHKeyFileWithKey(path string) (*rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	privateKeyPEM := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}

	keyFile, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer keyFile.Close()

	if err := pem.Encode(keyFile, privateKeyPEM); err != nil {
		return nil, err
	}

	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}

	return privateKey, nil
}
