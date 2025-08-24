package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestNewSSHServerBasicConnection tests basic connection to the new SSH server
func TestNewSSHServerBasicConnection(t *testing.T) {
	// Create a test server
	dbPath := fmt.Sprintf("/tmp/test_new_ssh_%d.db", time.Now().UnixNano())
	defer os.Remove(dbPath)

	server, err := NewServer(":8080", "", "", ":0", dbPath, "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = true

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	// Start the SSH server in a goroutine
	go func() {
		sshServer := NewSSHServer(server)
		if err := sshServer.Start(addr); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Generate a test SSH key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	// Create SSH client config
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// Try to connect
	client, err := ssh.Dial("tcp", addr, config)
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

	// Test exec command
	output, err := session.CombinedOutput("help")
	if err == nil {
		// Command succeeded - check output
		if !strings.Contains(string(output), "exe.dev") {
			t.Errorf("Unexpected help output: %s", output)
		}
	} else {
		// For unregistered users, we expect a specific message
		if !strings.Contains(string(output), "Please complete registration") {
			t.Errorf("Unexpected error output: %s", output)
		}
	}
}

// TestNewSSHServerInteractiveShell tests interactive shell with the new SSH server
func TestNewSSHServerInteractiveShell(t *testing.T) {
	// Create a test server
	dbPath := fmt.Sprintf("/tmp/test_new_ssh_shell_%d.db", time.Now().UnixNano())
	defer os.Remove(dbPath)

	server, err := NewServer(":8080", "", "", ":0", dbPath, "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = true
	server.testMode = true // Skip animations

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	// Start the SSH server in a goroutine
	sshDone := make(chan error, 1)
	go func() {
		sshServer := NewSSHServer(server)
		sshDone <- sshServer.Start(addr)
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Generate a test SSH key pair
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	// Create SSH client config
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// Connect to the server
	client, err := ssh.Dial("tcp", addr, config)
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

	// Set up pipes for stdin/stdout
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin pipe: %v", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}

	// Request a PTY
	if err := session.RequestPty("xterm", 80, 24, ssh.TerminalModes{}); err != nil {
		t.Fatalf("Failed to request PTY: %v", err)
	}

	// Start the shell
	if err := session.Shell(); err != nil {
		t.Fatalf("Failed to start shell: %v", err)
	}

	// Read initial output (should show registration prompt)
	buf := make([]byte, 4096)
	n, err := stdout.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("Failed to read output: %v", err)
	}

	output := string(buf[:n])
	// In test mode the animated welcome is shown quickly
	if !strings.Contains(output, "███████╗") && !strings.Contains(output, "type ssh to get a server") {
		t.Errorf("Unexpected initial output: %s", output)
	}

	// Send Ctrl+C to exit
	stdin.Write([]byte{3})

	// Wait a bit for the session to close
	time.Sleep(100 * time.Millisecond)
}

// TestNewSSHServerWithRegisteredUser tests the new SSH server with a registered user
func TestNewSSHServerWithRegisteredUser(t *testing.T) {
	// Create a test server
	dbPath := fmt.Sprintf("/tmp/test_new_ssh_registered_%d.db", time.Now().UnixNano())
	defer os.Remove(dbPath)

	server, err := NewServer(":8080", "", "", ":0", dbPath, "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.quietMode = true
	server.testMode = true

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

	// Create user
	userID, err := generateUserID()
	if err != nil {
		t.Fatalf("Failed to generate user ID: %v", err)
	}
	_, err = server.db.Exec(`
		INSERT INTO users (user_id, email)
		VALUES (?, ?)`,
		userID, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// SSH key will be added later with proper details

	// Create alloc for user
	allocID := "test-alloc-" + userID[:8]
	_, err = server.db.Exec(`
		INSERT INTO allocs (alloc_id, user_id, alloc_type, region, docker_host, created_at, stripe_customer_id, billing_email)
		VALUES (?, ?, 'medium', 'aws-us-west-2', '', datetime('now'), '', ?)`,
		allocID, userID, email)
	if err != nil {
		t.Fatalf("Failed to create alloc: %v", err)
	}

	// Add SSH key
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (user_id, public_key, verified, device_name)
		VALUES (?, ?, ?, ?)`,
		userID, string(ssh.MarshalAuthorizedKey(publicKey)), true, "test-device")
	if err != nil {
		t.Fatalf("Failed to add SSH key: %v", err)
	}

	// Find a free port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	// Start the SSH server
	go func() {
		sshServer := NewSSHServer(server)
		sshServer.Start(addr)
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

	// Create SSH client config
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// Connect to the server
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatalf("Failed to connect to SSH server: %v", err)
	}
	defer client.Close()

	// Test exec command as registered user
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	output, err := session.CombinedOutput("help")
	if err != nil {
		t.Fatalf("Failed to execute help command: %v", err)
	}

	// Check that we get the help output for registered users
	if !strings.Contains(string(output), "list") || !strings.Contains(string(output), "new") {
		t.Errorf("Expected help output for registered user, got: %s", output)
	}
}
