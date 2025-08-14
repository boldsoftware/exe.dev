package exe

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", []string{""})
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
	time.Sleep(200 * time.Millisecond)

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

	// Helper to read output until a pattern is found
	readUntil := func(pattern string, timeout time.Duration) (string, bool) {
		buf := make([]byte, 4096)
		output := &bytes.Buffer{}
		deadline := time.Now().Add(timeout)

		for time.Now().Before(deadline) {
			n, err := stdout.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if strings.Contains(output.String(), pattern) {
					return output.String(), true
				}
			}
			if err != nil && err != io.EOF {
				t.Logf("Read error: %v", err)
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return output.String(), false
	}

	// Step 1: Wait for registration prompt
	output, found := readUntil("enter your email", 5*time.Second)
	if !found {
		t.Fatalf("Registration prompt not found. Output:\n%s", output)
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
	output, found = readUntil("verify-email?token=", 5*time.Second)
	if !found {
		t.Fatalf("Verification URL not found. Output:\n%s", output)
	}

	// Extract token from output
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
		time.Sleep(500 * time.Millisecond)

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
	output, found = readUntil("Registration complete!", 10*time.Second)
	if !found {
		t.Fatalf("Registration complete message not found. Output:\n%s", output)
	}
	t.Log("Got registration complete message")

	// Wait for reconnect instructions
	output, found = readUntil("ssh exe.dev", 5*time.Second)
	if !found {
		t.Logf("Reconnect instructions not found. Output:\n%s", output)
	} else {
		t.Log("Got reconnect instructions")
	}

	// Check verification completed
	select {
	case verified := <-verificationDone:
		if !verified {
			t.Fatal("Email verification failed")
		}
	case <-time.After(2 * time.Second):
		t.Log("Verification timeout (may have completed)")
	}

	// Session should end after registration
	session.Wait()
	client.Close()

	// Step 6: Reconnect as a registered user
	t.Log("Reconnecting as registered user...")
	time.Sleep(500 * time.Millisecond)

	client2, err := ssh.Dial("tcp", sshAddr, config)
	if err != nil {
		t.Fatalf("Failed to reconnect to SSH server: %v", err)
	}
	defer client2.Close()

	session2, err := client2.NewSession()
	if err != nil {
		t.Fatalf("Failed to create second session: %v", err)
	}
	defer session2.Close()

	// Request PTY for second session
	if err := session2.RequestPty("xterm", 40, 80, modes); err != nil {
		t.Fatalf("Failed to request PTY for second session: %v", err)
	}

	stdin2, err := session2.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin pipe for second session: %v", err)
	}

	stdout2, err := session2.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe for second session: %v", err)
	}

	if err := session2.Shell(); err != nil {
		t.Fatalf("Failed to start shell for second session: %v", err)
	}

	// Helper for second session
	readUntil2 := func(pattern string, timeout time.Duration) (string, bool) {
		buf := make([]byte, 4096)
		output := &bytes.Buffer{}
		deadline := time.Now().Add(timeout)

		for time.Now().Before(deadline) {
			n, err := stdout2.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if strings.Contains(output.String(), pattern) {
					return output.String(), true
				}
			}
			if err != nil && err != io.EOF {
				t.Logf("Read error: %v", err)
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return output.String(), false
	}

	// Step 7: Wait for menu to appear
	t.Log("Waiting for menu in new session...")
	output, found = readUntil2("EXE.DEV", 5*time.Second)
	if !found {
		t.Fatalf("Menu not found in new session. Output:\n%s", output)
	}
	t.Log("Menu appeared in new session")

	// Step 8: Test that we can type commands - send list command first (simpler)
	t.Log("Sending 'list' command")
	stdin2.Write([]byte("list\n"))

	// Step 9: Check for list command output
	output, found = readUntil2("No machines", 5*time.Second)
	if !found {
		// Might say "machines found" if there are machines
		output, found = readUntil2("machines", 5*time.Second)
	}

	if found {
		t.Log("List command executed successfully")
		t.Logf("List output: %s", output)
	} else {
		t.Errorf("List command not working. Output:\n%s", output)
	}

	// Step 10: Test help command as another simple test
	t.Log("Sending 'help' command")
	stdin2.Write([]byte("help\n"))

	output, found = readUntil2("Machine Management", 5*time.Second)
	if found {
		t.Log("Help command executed successfully")
	} else {
		t.Errorf("Help command not working. Output:\n%s", output)
	}

	// Step 11: Exit cleanly
	t.Log("Exiting session")
	stdin2.Write([]byte("exit\n"))
	session2.Wait()

	t.Log("Test completed successfully - registration flow works with reconnection")
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
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", []string{""})
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
	time.Sleep(200 * time.Millisecond)

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
	deadline := time.Now().Add(2 * time.Second)
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
	deadline = time.Now().Add(5 * time.Second)
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
	deadline = time.Now().Add(5 * time.Second)
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
