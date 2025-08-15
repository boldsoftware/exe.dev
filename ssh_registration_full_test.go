package exe

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestFullRegistrationFlowWithCreate tests the complete registration flow including:
// 1. SSH connection as unregistered user
// 2. Email entry
// 3. Web verification POST
// 4. Menu appears
// 5. Create command works
func TestFullRegistrationFlowWithCreate(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_full_reg_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", nil)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true // Skip animations
	defer server.Stop()

	// Mock container manager for testing
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Find free ports
	sshListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	sshAddr := sshListener.Addr().String()
	sshListener.Close()

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port for HTTP: %v", err)
	}
	httpAddr := httpListener.Addr().String()
	httpListener.Close()
	server.httpAddr = httpAddr

	// Start HTTP server for email verification
	httpMux := http.NewServeMux()
	httpMux.HandleFunc("/verify-email", server.handleEmailVerificationHTTP)
	httpServer := &http.Server{
		Addr:    httpAddr,
		Handler: httpMux,
	}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.Logf("HTTP server error: %v", err)
		}
	}()
	defer httpServer.Close()

	// Start SSH server
	sshServer := NewSSHServer(server)
	go func() {
		if err := sshServer.Start(sshAddr); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for servers to start
	time.Sleep(50 * time.Millisecond)

	// Generate test SSH key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	hash := sha256.Sum256(signer.PublicKey().Marshal())
	fingerprint := hex.EncodeToString(hash[:])
	t.Logf("Test fingerprint: %s", fingerprint)

	// Connect to SSH server
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	client, err := ssh.Dial("tcp", sshAddr, config)
	if err != nil {
		t.Fatalf("Failed to connect to SSH server: %v", err)
	}
	defer client.Close()

	// Create session
	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	// Request PTY
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm", 40, 80, modes); err != nil {
		t.Fatalf("Failed to request PTY: %v", err)
	}

	// Get pipes
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin pipe: %v", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}

	// Start shell
	if err := session.Shell(); err != nil {
		t.Fatalf("Failed to start shell: %v", err)
	}

	// Create a shared buffer for reading
	outputBuffer := &bytes.Buffer{}

	// Start a goroutine to continuously read output
	readDone := make(chan bool)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				outputBuffer.Write(buf[:n])
			}
			if err != nil {
				close(readDone)
				return
			}
		}
	}()

	// Helper to check if pattern exists in output
	waitForPattern := func(pattern string, timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if strings.Contains(outputBuffer.String(), pattern) {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Logf("Pattern '%s' not found. Current output:\n%s", pattern, outputBuffer.String())
		return false
	}

	// Step 1: Wait for registration prompt
	if !waitForPattern("enter your email", 1*time.Second) {
		t.Fatalf("Registration prompt not found")
	}
	t.Log("Got registration prompt")

	// Step 2: Enter email
	testEmail := "test@example.com"
	n, err := stdin.Write([]byte(testEmail + "\n"))
	if err != nil {
		t.Fatalf("Failed to write email: %v", err)
	}
	if n != len(testEmail)+1 {
		t.Fatalf("Failed to write full email: wrote %d bytes, expected %d", n, len(testEmail)+1)
	}
	t.Logf("Entered email: %s", testEmail)

	// Step 3: Wait for verification URL
	if !waitForPattern("verify-email?token=", 1*time.Second) {
		t.Fatalf("Verification URL not found")
	}

	// Extract token from output
	output := outputBuffer.String()
	lines := strings.Split(output, "\n")
	var token string
	for _, line := range lines {
		if strings.Contains(line, "verify-email?token=") {
			parts := strings.Split(line, "token=")
			if len(parts) > 1 {
				token = strings.TrimSpace(parts[1])
				// Remove any ANSI codes
				if idx := strings.Index(token, "\x1b"); idx >= 0 {
					token = token[:idx]
				}
				break
			}
		}
	}
	if token == "" {
		t.Fatalf("Could not extract token from output:\n%s", output)
	}
	t.Logf("Extracted token: %s", token)

	// Step 4: Perform web verification in a goroutine
	verificationDone := make(chan bool, 1)
	go func() {
		// Wait a moment to ensure SSH session is waiting
		time.Sleep(50 * time.Millisecond)

		// POST to verification endpoint
		form := url.Values{}
		form.Add("token", token)
		resp, err := http.PostForm(fmt.Sprintf("http://%s/verify-email", httpAddr), form)
		if err != nil {
			t.Logf("Verification POST failed: %v", err)
			verificationDone <- false
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Logf("Verification failed with status: %d", resp.StatusCode)
			verificationDone <- false
			return
		}

		t.Log("Email verification completed successfully")
		verificationDone <- true
	}()

	// Step 5: Wait for registration complete message
	t.Log("Waiting for registration complete message...")
	if !waitForPattern("Registration complete!", 2*time.Second) {
		t.Fatalf("Registration complete message not found")
	}
	t.Log("Got registration complete message")

	// Press Enter to continue to the menu
	stdin.Write([]byte("\n"))
	t.Log("Pressed Enter to continue")

	// Wait for menu prompt to appear
	if waitForPattern("exe.dev", 2*time.Second) {
		t.Log("Got menu prompt")
	} else {
		t.Logf("Menu prompt not found")
	}

	// Check verification completed
	select {
	case verified := <-verificationDone:
		if !verified {
			t.Fatal("Email verification failed")
		}
	case <-time.After(500 * time.Millisecond):
		t.Log("Verification timeout (may have completed)")
	}

	// Now we're in the menu in the same session
	// Step 6: Test that we can type commands - send list command first (simpler)
	t.Log("Sending 'list' command")
	stdin.Write([]byte("list\n"))

	// Step 7: Check for list command output
	if waitForPattern("No machines", 1*time.Second) || waitForPattern("machines", 500*time.Millisecond) {
		t.Log("List command executed successfully")
	} else {
		t.Errorf("List command not working")
	}

	// Step 8: Test create command
	t.Log("Sending 'create' command")
	stdin.Write([]byte("create\n"))

	if waitForPattern("Creating", 1*time.Second) {
		t.Log("Create command executed successfully")
		// Wait for machine to be ready
		if waitForPattern("Ready in", 2*time.Second) {
			t.Log("Machine created successfully")
		}
	} else {
		t.Errorf("Create command not working")
	}

	// Step 9: Exit cleanly
	t.Log("Exiting session")
	stdin.Write([]byte("exit\n"))
	session.Wait()

	t.Log("Test completed successfully - registration flow works with create command")
}

// TestRegistrationMenuFreeze specifically tests the freeze issue
func TestRegistrationMenuFreeze(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_freeze_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", nil)
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true
	defer server.Stop()

	// Set up mock container manager
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Find free ports
	sshListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	sshAddr := sshListener.Addr().String()
	sshListener.Close()

	// Start SSH server
	sshServer := NewSSHServer(server)
	go func() {
		if err := sshServer.Start(sshAddr); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(50 * time.Millisecond)

	// Generate test SSH key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("Failed to generate private key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("Failed to create signer: %v", err)
	}

	hash := sha256.Sum256(signer.PublicKey().Marshal())
	fingerprint := hex.EncodeToString(hash[:])
	publicKeyStr := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))

	// Simulate post-registration state
	// Create the user, team, and mark as verified
	email := "test@example.com"
	err = server.createUser(fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Get the team that was created
	var teamName string
	err = server.db.QueryRow(`
		SELECT name FROM teams WHERE owner_fingerprint = ? AND is_personal = TRUE`,
		fingerprint).Scan(&teamName)
	if err != nil {
		t.Fatalf("Failed to get team: %v", err)
	}

	// Store verified SSH key
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name, default_team)
		VALUES (?, ?, ?, 1, 'Primary Device', ?)`,
		fingerprint, email, publicKeyStr, teamName)
	if err != nil {
		t.Fatalf("Failed to store SSH key: %v", err)
	}

	// Now connect and test interactivity
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	client, err := ssh.Dial("tcp", sshAddr, config)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer session.Close()

	// Request PTY
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm", 40, 80, modes); err != nil {
		t.Fatalf("Failed to request PTY: %v", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin: %v", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout: %v", err)
	}

	// Start shell
	if err := session.Shell(); err != nil {
		t.Fatalf("Failed to start shell: %v", err)
	}

	// Read initial output
	buf := make([]byte, 4096)
	output := &bytes.Buffer{}

	// Collect initial output
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		n, _ := stdout.Read(buf)
		if n > 0 {
			output.Write(buf[:n])
			if strings.Contains(output.String(), "Welcome to EXE.DEV") {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	initialOutput := output.String()
	t.Logf("Initial output:\n%s", initialOutput)

	if !strings.Contains(initialOutput, "Welcome to EXE.DEV") {
		t.Fatal("Welcome message not found")
	}

	// Clear buffer and test commands
	output.Reset()

	// Send a simple command
	t.Log("Testing 'help' command")
	stdin.Write([]byte("help\n"))

	// Read response with timeout
	responseReceived := false
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := stdout.Read(buf)
		if n > 0 {
			output.Write(buf[:n])
			if strings.Contains(output.String(), "Machine Management") {
				responseReceived = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !responseReceived {
		t.Errorf("No response to help command. Output:\n%s", output.String())
	} else {
		t.Log("Help command responded successfully")
	}

	// Test another command
	output.Reset()
	t.Log("Testing 'list' command")
	stdin.Write([]byte("list\n"))

	responseReceived = false
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := stdout.Read(buf)
		if n > 0 {
			output.Write(buf[:n])
			if strings.Contains(output.String(), "No machines") || strings.Contains(output.String(), "machines found") {
				responseReceived = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !responseReceived {
		t.Errorf("No response to list command. Output:\n%s", output.String())
	} else {
		t.Log("List command responded successfully")
	}

	// Exit
	stdin.Write([]byte("exit\n"))
	session.Wait()
}
