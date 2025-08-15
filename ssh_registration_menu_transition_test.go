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

// TestRegistrationToMenuTransition tests that characters aren't lost when transitioning from registration to menu
func TestRegistrationToMenuTransition(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_menu_transition_*.db")
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

	// Connect via SSH (unregistered user)
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
		ssh.ECHO:          1,
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

	// Read initial registration prompt
	buf := make([]byte, 4096)
	n, err := stdout.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read initial output: %v", err)
	}
	output := string(buf[:n])
	t.Logf("Initial output: %s", output)

	// Look for email prompt
	if !strings.Contains(output, "enter your email") {
		// Keep reading if we need more output
		for i := 0; i < 5; i++ {
			n, err = stdout.Read(buf)
			if err != nil {
				break
			}
			output += string(buf[:n])
			if strings.Contains(output, "enter your email") {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	if !strings.Contains(output, "enter your email") {
		t.Fatalf("Expected email prompt, got: %s", output)
	}

	// Send email
	_, err = stdin.Write([]byte(email + "\n"))
	if err != nil {
		t.Fatalf("Failed to send email: %v", err)
	}

	// Wait for verification email message
	output = ""
	for i := 0; i < 10; i++ {
		n, err = stdout.Read(buf)
		if err != nil && err != io.EOF {
			break
		}
		if n > 0 {
			output += string(buf[:n])
			if strings.Contains(output, "Verification email sent") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !strings.Contains(output, "Verification email sent") {
		t.Fatalf("Expected verification email message, got: %s", output)
	}

	// Get the verification token from the server
	var token string
	server.emailVerificationsMu.Lock()
	for t, v := range server.emailVerifications {
		if v.PublicKeyFingerprint == fingerprint {
			token = t
			break
		}
	}
	server.emailVerificationsMu.Unlock()

	if token == "" {
		t.Fatalf("No verification token found")
	}

	// Simulate email verification in a goroutine
	go func() {
		time.Sleep(500 * time.Millisecond)

		server.emailVerificationsMu.Lock()
		if v, exists := server.emailVerifications[token]; exists {
			// Create user
			err := server.createUser(fingerprint, email)
			if err != nil {
				t.Logf("Failed to create user: %v", err)
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
		}
		server.emailVerificationsMu.Unlock()
	}()

	// Wait for verification to complete and "Press [Enter]" prompt
	output = ""
	pressEnterAppeared := false
	for i := 0; i < 30; i++ {
		n, err = stdout.Read(buf)
		if err != nil && err != io.EOF {
			break
		}
		if n > 0 {
			output += string(buf[:n])
			// Look for the press enter prompt
			if strings.Contains(output, "Press [Enter]") {
				pressEnterAppeared = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !pressEnterAppeared {
		t.Fatalf("Press [Enter] prompt didn't appear. Output: %s", output)
	}

	// Press Enter to continue to menu
	_, err = stdin.Write([]byte{'\n'})
	if err != nil {
		t.Fatalf("Failed to send Enter: %v", err)
	}

	// Now wait for the menu prompt to appear
	output = ""
	menuAppeared := false
	for i := 0; i < 10; i++ {
		n, err = stdout.Read(buf)
		if err != nil && err != io.EOF {
			break
		}
		if n > 0 {
			output += string(buf[:n])
			// Look for the menu prompt
			if strings.Contains(output, "exe.dev") && strings.Contains(output, "▶") {
				menuAppeared = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !menuAppeared {
		t.Fatalf("Menu prompt didn't appear after pressing Enter. Output: %s", output)
	}

	// Now test that we can type commands without losing characters
	// Send "help" command
	testCommand := "help"
	t.Logf("Sending command: %s", testCommand)

	// Clear any pending output first
	for {
		session.Stdout = &bytes.Buffer{}
		time.Sleep(50 * time.Millisecond)
		break
	}

	// Send each character with small delay to simulate typing
	for _, char := range testCommand {
		_, err = stdin.Write([]byte{byte(char)})
		if err != nil {
			t.Fatalf("Failed to send character '%c': %v", char, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Send enter
	_, err = stdin.Write([]byte{'\n'})
	if err != nil {
		t.Fatalf("Failed to send enter: %v", err)
	}

	// Read the response
	output = ""
	helpAppeared := false
	for i := 0; i < 20; i++ {
		n, err = stdout.Read(buf)
		if err != nil && err != io.EOF {
			break
		}
		if n > 0 {
			output += string(buf[:n])
			// Check if help output appeared
			if strings.Contains(output, "Machine Management") || strings.Contains(output, "Show this help") {
				helpAppeared = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !helpAppeared {
		// Check if the command was echoed correctly
		if !strings.Contains(output, "help") {
			t.Errorf("Command 'help' was not echoed correctly, suggesting character loss. Output: %s", output)
		}
		t.Fatalf("Help output didn't appear. Output: %s", output)
	}

	// Test another command to ensure consistent behavior
	testCommand2 := "list"
	t.Logf("Sending second command: %s", testCommand2)

	for _, char := range testCommand2 {
		_, err = stdin.Write([]byte{byte(char)})
		if err != nil {
			t.Fatalf("Failed to send character '%c': %v", char, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Send enter
	_, err = stdin.Write([]byte{'\n'})
	if err != nil {
		t.Fatalf("Failed to send enter: %v", err)
	}

	// Read response
	output = ""
	for i := 0; i < 10; i++ {
		n, err = stdout.Read(buf)
		if err != nil && err != io.EOF {
			break
		}
		if n > 0 {
			output += string(buf[:n])
			// Look for expected output (either "No machines" or machine list)
			if strings.Contains(output, "machines") || strings.Contains(output, "Machine") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify the command was processed
	if !strings.Contains(output, "list") && !strings.Contains(output, "machines") {
		t.Errorf("Second command 'list' may have had character loss. Output: %s", output)
	}

	t.Logf("Test completed successfully - no character loss detected in menu after registration")
}
