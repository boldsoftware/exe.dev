package exe

import (
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

// TestRegistrationCtrlCHandling tests that Ctrl+C works during email verification
func TestRegistrationCtrlCHandling(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_ctrlc_*.db")
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
	t.Logf("Initial output received")

	// Look for email prompt
	for !strings.Contains(output, "enter your email") {
		n, err = stdout.Read(buf)
		if err != nil && err != io.EOF {
			break
		}
		if n > 0 {
			output += string(buf[:n])
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !strings.Contains(output, "enter your email") {
		t.Fatalf("Expected email prompt, got: %s", output)
	}

	// Send email
	email := "test@example.com"
	_, err = stdin.Write([]byte(email + "\n"))
	if err != nil {
		t.Fatalf("Failed to send email: %v", err)
	}

	// Wait for "Waiting for email verification" message
	output = ""
	for i := 0; i < 10; i++ {
		n, err = stdout.Read(buf)
		if err != nil && err != io.EOF {
			break
		}
		if n > 0 {
			output += string(buf[:n])
			if strings.Contains(output, "Waiting for email verification") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !strings.Contains(output, "Waiting for email verification") {
		t.Fatalf("Expected 'Waiting for email verification' message, got: %s", output)
	}

	// Test 1: Send Ctrl+C during verification wait
	t.Logf("Sending Ctrl+C to cancel registration")
	_, err = stdin.Write([]byte{3}) // Ctrl+C
	if err != nil {
		t.Fatalf("Failed to send Ctrl+C: %v", err)
	}

	// Should see "Registration cancelled" message
	output = ""
	cancelled := false
	for i := 0; i < 10; i++ {
		n, err = stdout.Read(buf)
		if err != nil && err != io.EOF {
			// Session might close after cancellation
			break
		}
		if n > 0 {
			output += string(buf[:n])
			if strings.Contains(output, "Registration cancelled") {
				cancelled = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !cancelled {
		t.Errorf("Expected 'Registration cancelled' message after Ctrl+C, got: %s", output)
	}

	t.Logf("Ctrl+C handling test completed successfully")
}

// TestRegistrationInputDiscarding tests that input is discarded during email verification
func TestRegistrationInputDiscarding(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_discard_*.db")
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

	// Go through registration flow
	buf := make([]byte, 4096)
	n, _ := stdout.Read(buf)
	output := string(buf[:n])

	// Wait for email prompt
	for !strings.Contains(output, "enter your email") {
		n, _ = stdout.Read(buf)
		if n > 0 {
			output += string(buf[:n])
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Send email
	stdin.Write([]byte(email + "\n"))

	// Wait for verification message
	output = ""
	for i := 0; i < 10; i++ {
		n, _ = stdout.Read(buf)
		if n > 0 {
			output += string(buf[:n])
			if strings.Contains(output, "Waiting for email verification") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Send some random input that should be discarded
	t.Logf("Sending random input that should be discarded")
	stdin.Write([]byte("random text that should be ignored"))
	
	// Get verification token
	var token string
	server.emailVerificationsMu.Lock()
	for t, v := range server.emailVerifications {
		if v.PublicKeyFingerprint == fingerprint {
			token = t
			break
		}
	}
	server.emailVerificationsMu.Unlock()

	// Complete verification
	go func() {
		time.Sleep(200 * time.Millisecond)
		server.emailVerificationsMu.Lock()
		if v, exists := server.emailVerifications[token]; exists {
			server.createUser(fingerprint, email)
			server.db.Exec(`
				INSERT INTO ssh_keys (fingerprint, user_email, public_key, verified, device_name)
				VALUES (?, ?, ?, 1, 'Primary Device')`,
				fingerprint, email, publicKeyStr)
			close(v.CompleteChan)
			delete(server.emailVerifications, token)
		}
		server.emailVerificationsMu.Unlock()
	}()

	// Wait for "Press [Enter]" prompt
	output = ""
	for i := 0; i < 20; i++ {
		n, _ = stdout.Read(buf)
		if n > 0 {
			output += string(buf[:n])
			if strings.Contains(output, "Press [Enter]") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !strings.Contains(output, "Press [Enter]") {
		t.Fatalf("Expected 'Press [Enter]' prompt, got: %s", output)
	}

	// The random input we sent earlier should have been discarded
	// Press Enter to continue
	stdin.Write([]byte("\n"))

	// Wait for menu
	output = ""
	for i := 0; i < 10; i++ {
		n, _ = stdout.Read(buf)
		if n > 0 {
			output += string(buf[:n])
			if strings.Contains(output, "exe.dev") && strings.Contains(output, "▶") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Send a command to verify the menu works and previous input was discarded
	stdin.Write([]byte("help\n"))

	output = ""
	for i := 0; i < 10; i++ {
		n, _ = stdout.Read(buf)
		if n > 0 {
			output += string(buf[:n])
			if strings.Contains(output, "Machine Management") {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !strings.Contains(output, "Machine Management") {
		t.Errorf("Help command didn't work properly, suggesting input wasn't properly discarded")
	}

	t.Logf("Input discarding test completed successfully")
}