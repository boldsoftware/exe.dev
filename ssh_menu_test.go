package exe

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestSSHMenuAfterRegistration tests that the menu works after registration
func TestSSHMenuAfterRegistration(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_menu_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true // Skip animations
	defer server.Stop()

	// Find a free port for SSH
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	sshAddr := listener.Addr().String()
	listener.Close()

	// Start SSH server
	sshServer := NewSSHServer(server)
	go func() {
		if err := sshServer.Start(sshAddr); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

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

	// Set up a registered user with a team
	email := "test@example.com"

	// Create user and team directly
	err = server.createUser(fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Get the personal team that was created
	var personalTeamName string
	err = server.db.QueryRow(`
		SELECT name FROM teams WHERE owner_fingerprint = ? AND is_personal = TRUE`,
		fingerprint).Scan(&personalTeamName)
	if err != nil {
		t.Fatalf("Failed to get personal team: %v", err)
	}
	t.Logf("Personal team created: %s", personalTeamName)

	// Store SSH key as verified with the correct team
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name, default_team)
		VALUES (?, ?, ?, 1, 'Primary Device', ?)`,
		fingerprint, email, publicKeyStr, personalTeamName)
	if err != nil {
		t.Fatalf("Failed to store SSH key: %v", err)
	}

	// Verify team membership was created
	var memberCount int
	err = server.db.QueryRow(`
		SELECT COUNT(*) FROM team_members WHERE user_fingerprint = ?`,
		fingerprint).Scan(&memberCount)
	if err != nil {
		t.Fatalf("Failed to count team members: %v", err)
	}
	t.Logf("User is member of %d teams", memberCount)

	// Test authentication directly to see what permissions are returned
	perms, err := server.AuthenticatePublicKey(nil, signer.PublicKey())
	if err != nil {
		t.Fatalf("Failed to authenticate public key: %v", err)
	}
	t.Logf("Authentication result - registered: %s, email: %s",
		perms.Extensions["registered"], perms.Extensions["email"])

	// Now test SSH connection with menu interaction
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}

	// Connect to SSH server
	client, err := ssh.Dial("tcp", sshAddr, config)
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

	// Set up pseudo terminal
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm", 40, 80, modes); err != nil {
		t.Fatalf("Failed to request PTY: %v", err)
	}

	// Set up stdin/stdout/stderr
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatalf("Failed to get stdin pipe: %v", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}

	// Start the shell
	t.Log("Starting shell session...")
	if err := session.Shell(); err != nil {
		t.Fatalf("Failed to start shell: %v", err)
	}
	t.Log("Shell started successfully")

	// Read initial output (should see welcome menu)
	buf := make([]byte, 4096)
	outputCollected := &bytes.Buffer{}

	// Use goroutine to read output
	outputChan := make(chan string, 1)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			n, err := stdout.Read(buf)
			if err != nil {
				if err != io.EOF {
					t.Logf("Read error: %v", err)
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			if n > 0 {
				outputCollected.Write(buf[:n])
				// Look for the prompt (registered users don't get welcome message)
				if strings.Contains(outputCollected.String(), "exe.dev") &&
					strings.Contains(outputCollected.String(), "▶") {
					outputChan <- outputCollected.String()
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		outputChan <- outputCollected.String()
	}()

	// Wait for prompt or timeout
	select {
	case output := <-outputChan:
		t.Logf("Initial output:\n%s", output)
		// Registered users go straight to prompt, no welcome message
	case <-time.After(3 * time.Second):
		t.Logf("Output collected so far:\n%s", outputCollected.String())
		t.Fatal("Timeout waiting for menu prompt")
	}

	// Test sending a command
	t.Log("Sending 'help' command")
	if _, err := stdin.Write([]byte("help\n")); err != nil {
		t.Fatalf("Failed to write help command: %v", err)
	}

	// Read response - spin waiting for complete output
	outputCollected.Reset()

	// Read whatever is available with a simple timeout loop
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := stdout.Read(buf)
		if n > 0 {
			outputCollected.Write(buf[:n])
		}
		if err != nil && err != io.EOF {
			break
		}
		// If we got the complete help text and prompt back, we're done
		output := outputCollected.String()
		if strings.Contains(output, "EXE.DEV") &&
			strings.Contains(output, "commands") &&
			strings.Contains(output, "exit") &&
			strings.Contains(output, "▶") {
			break
		}
		// Small sleep just to avoid tight spinning
		time.Sleep(10 * time.Millisecond)
	}

	output := outputCollected.String()
	t.Logf("Help output:\n%s", output)

	// Check for the new help format without sections
	if !strings.Contains(output, "EXE.DEV") || !strings.Contains(output, "commands") {
		t.Errorf("Expected help text with 'EXE.DEV commands', got: %s", output)
	}
	if !strings.Contains(output, "list") || !strings.Contains(output, "new") {
		t.Error("Expected help text with machine commands")
	}

	// Test exit command
	t.Log("Sending 'exit' command")
	if _, err := stdin.Write([]byte("exit\n")); err != nil {
		t.Fatalf("Failed to write exit command: %v", err)
	}

	// Wait for session to close
	err = session.Wait()
	t.Logf("Session ended with: %v", err)
}

// TestSSHMenuInteractiveCommands tests various menu commands
func TestSSHMenuInteractiveCommands(t *testing.T) {
	t.Skip("Skipping interactive test - complex readline interaction")

	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_menu_cmds_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true
	defer server.Stop()

	// Mock container manager for testing
	mockManager := NewMockContainerManager()
	server.containerManager = mockManager

	// Find a free port for SSH
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	sshAddr := listener.Addr().String()
	listener.Close()

	// Start SSH server
	sshServer := NewSSHServer(server)
	go func() {
		if err := sshServer.Start(sshAddr); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

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

	// Set up a registered user
	email := "test@example.com"
	teamName := "testteam"

	err = server.createUser(fingerprint, email)
	if err != nil {
		t.Fatalf("Failed to create user: %v", err)
	}

	// Store SSH key
	_, err = server.db.Exec(`
		INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name, default_team)
		VALUES (?, ?, ?, 1, 'Primary Device', ?)`,
		fingerprint, email, publicKeyStr, teamName)
	if err != nil {
		t.Fatalf("Failed to store SSH key: %v", err)
	}

	// Connect to SSH
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
		t.Fatalf("Failed to get stdin pipe: %v", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to get stdout pipe: %v", err)
	}

	if err := session.Shell(); err != nil {
		t.Fatalf("Failed to start shell: %v", err)
	}

	// Helper to read until prompt
	readUntilPrompt := func() string {
		buf := make([]byte, 4096)
		output := &bytes.Buffer{}
		deadline := time.Now().Add(2 * time.Second)

		for time.Now().Before(deadline) {
			n, err := stdout.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if strings.Contains(output.String(), "▶") {
					return output.String()
				}
			}
			if err != nil && err != io.EOF {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return output.String()
	}

	// Wait for initial prompt
	initialOutput := readUntilPrompt()
	t.Logf("Initial output:\n%s", initialOutput)

	// Test commands
	commands := []struct {
		cmd      string
		expected string
	}{
		{"list\n", "No machines found"},
		{"help\n", "EXE.DEV commands"},
		{"?\n", "EXE.DEV commands"},
	}

	for _, tc := range commands {
		t.Logf("Testing command: %s", strings.TrimSpace(tc.cmd))

		if _, err := stdin.Write([]byte(tc.cmd)); err != nil {
			t.Errorf("Failed to write command %s: %v", tc.cmd, err)
			continue
		}

		output := readUntilPrompt()
		if !strings.Contains(output, tc.expected) {
			t.Errorf("Command %s: expected output to contain %q, got:\n%s",
				strings.TrimSpace(tc.cmd), tc.expected, output)
		}
	}

	// Send exit
	stdin.Write([]byte("exit\n"))
	session.Wait()
}

// TestRegistrationToMenuFlow tests the complete flow from registration to menu
func TestRegistrationToMenuFlow(t *testing.T) {
	t.Skip("Skipping registration flow test - complex readline interaction")
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_reg_flow_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	server.testMode = true
	defer server.Stop()

	// Find a free port for SSH
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to find free port: %v", err)
	}
	sshAddr := listener.Addr().String()
	listener.Close()

	// Start SSH server
	sshServer := NewSSHServer(server)
	go func() {
		if err := sshServer.Start(sshAddr); err != nil {
			t.Logf("SSH server error: %v", err)
		}
	}()

	// Wait for server to start
	time.Sleep(100 * time.Millisecond)

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
	email := "test@example.com"

	// Simulate the registration and verification flow
	// This simulates what happens when user completes email verification

	// 1. User would normally go through registration, we'll simulate verification completion
	token := server.generateToken()

	// Create the verification entry as if registration started
	verification := &EmailVerification{
		PublicKeyFingerprint: fingerprint,
		PublicKey:            publicKeyStr,
		Email:                email,
		Token:                token,
		CompleteChan:         make(chan struct{}),
		CreatedAt:            time.Now(),
	}

	server.emailVerificationsMu.Lock()
	server.emailVerifications[token] = verification
	server.emailVerificationsMu.Unlock()

	// 2. Simulate email verification completion in a goroutine
	verificationComplete := make(chan bool, 1)
	go func() {
		// Wait a moment to let SSH connection establish
		time.Sleep(500 * time.Millisecond)

		// Simulate the HTTP verification handler
		server.emailVerificationsMu.Lock()
		if v, exists := server.emailVerifications[token]; exists {
			// Create user
			err := server.createUser(fingerprint, email)
			if err != nil {
				t.Logf("Failed to create user: %v", err)
				verificationComplete <- false
				server.emailVerificationsMu.Unlock()
				return
			}

			// Store SSH key
			_, err = server.db.Exec(`
				INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
				VALUES (?, ?, ?, 1, 'Primary Device')`,
				fingerprint, email, publicKeyStr)
			if err != nil {
				t.Logf("Failed to store SSH key: %v", err)
			}

			// Signal completion
			close(v.CompleteChan)
			delete(server.emailVerifications, token)
			verificationComplete <- true
		}
		server.emailVerificationsMu.Unlock()
	}()

	// 3. Connect via SSH (this would be the user's existing connection)
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

	// The session should show registration prompt initially
	buf := make([]byte, 8192)
	output := &bytes.Buffer{}

	// Collect output with timeout
	outputDone := make(chan struct{})
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			n, err := stdout.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				// Check if we've reached the menu
				if strings.Contains(output.String(), "Setting up your workspace") &&
					strings.Contains(output.String(), "EXE.DEV commands") {
					outputDone <- struct{}{}
					return
				}
			}
			if err != nil && err != io.EOF {
				t.Logf("Read error: %v", err)
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		outputDone <- struct{}{}
	}()

	// Wait for verification to complete
	verified := <-verificationComplete
	if !verified {
		t.Fatal("Verification failed")
	}

	// Wait for output or timeout
	<-outputDone

	finalOutput := output.String()
	t.Logf("Session output:\n%s", finalOutput)

	// Check that we got to the menu
	if !strings.Contains(finalOutput, "Setting up your workspace") {
		t.Error("Expected 'Setting up your workspace' message")
	}

	if !strings.Contains(finalOutput, "EXE.DEV commands") {
		t.Error("Expected to see menu commands")
	}

	// The session should still be active - try sending a command
	if strings.Contains(finalOutput, "▶") {
		// We have a prompt, try a command
		stdin.Write([]byte("help\n"))

		// Read response
		output.Reset()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			n, _ := stdout.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if strings.Contains(output.String(), "EXE.DEV commands") {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
		}

		if !strings.Contains(output.String(), "EXE.DEV commands") {
			t.Error("Menu doesn't respond to commands after registration")
		}
	}

	// Clean exit
	stdin.Write([]byte("exit\n"))
	session.Wait()
}
