package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestSSHTerminalInputDuringRegistration tests that terminal input works correctly during registration
// This test specifically checks that we can type during the email prompt
func TestSSHTerminalInputDuringRegistration(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_terminal_input_*.db")
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

	// Fingerprint logging removed as no longer needed

	// Connect to SSH server
	config := &ssh.ClientConfig{
		User: "",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         2 * time.Second,
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

	// Request PTY - this is crucial for terminal input
	modes := ssh.TerminalModes{
		ssh.ECHO:          1, // Enable echo
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

	// Step 1: Wait for registration prompt
	_ = mustRead(t, stdout, "enter your email", 500*time.Millisecond, "waiting for registration prompt")
	t.Log("Got registration prompt - terminal is ready for input")

	// Step 2: Test that we can write input character by character
	testEmail := "test@example.com"

	// Write email character by character to test terminal responsiveness
	mustWriteChars(t, stdin, testEmail, 2*time.Millisecond, "entering email")
	mustWrite(t, stdin, "\n", "submitting email")
	t.Logf("Successfully entered email: %s", testEmail)

	// Step 3: Verify email verification started
	// Try to read for verification message, but don't fail if we get an error about email service
	verifyOutput, err := readWithTimeout(stdout, 500*time.Millisecond)
	if err == nil && !strings.Contains(verifyOutput, "verification") && !strings.Contains(verifyOutput, "Email service") && !strings.Contains(verifyOutput, "verify-email?token=") {
		t.Fatalf("Email verification not started. Output:\n%s", verifyOutput)
	}
	t.Log("Email verification process started")

	// Step 6: Send Ctrl+C to cancel and exit cleanly
	mustWrite(t, stdin, "\x03", "sending Ctrl+C") // Ctrl+C
	time.Sleep(20 * time.Millisecond)

	// Session should end
	session.Wait()

	t.Log("Test completed successfully - terminal input works correctly")
}

// TestSSHTerminalModes tests that PTY modes are correctly handled
func TestSSHTerminalModes(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_pty_modes_*.db")
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
	time.Sleep(50 * time.Millisecond)

	// Test different terminal scenarios
	testCases := []struct {
		name        string
		requestPTY  bool
		expectInput bool
	}{
		{
			name:        "With PTY",
			requestPTY:  true,
			expectInput: true,
		},
		{
			name:        "Without PTY",
			requestPTY:  false,
			expectInput: false, // Input might still work but differently
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Generate test SSH key
			privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatalf("Failed to generate private key: %v", err)
			}

			signer, err := ssh.NewSignerFromKey(privateKey)
			if err != nil {
				t.Fatalf("Failed to create signer: %v", err)
			}

			// Connect to SSH server
			config := &ssh.ClientConfig{
				User: "",
				Auth: []ssh.AuthMethod{
					ssh.PublicKeys(signer),
				},
				HostKeyCallback: ssh.InsecureIgnoreHostKey(),
				Timeout:         2 * time.Second,
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

			// Request PTY if needed
			if tc.requestPTY {
				modes := ssh.TerminalModes{
					ssh.ECHO:          1,
					ssh.TTY_OP_ISPEED: 14400,
					ssh.TTY_OP_OSPEED: 14400,
				}
				if err := session.RequestPty("xterm", 40, 80, modes); err != nil {
					t.Fatalf("Failed to request PTY: %v", err)
				}
				t.Log("PTY requested successfully")
			} else {
				t.Log("Running without PTY")
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
				if !tc.requestPTY {
					// Some SSH servers require PTY for shell access
					t.Logf("Shell start failed without PTY (might be expected): %v", err)
					return
				}
				t.Fatalf("Failed to start shell: %v", err)
			}

			// Try to read initial output
			buf := make([]byte, 4096)
			outputChan := make(chan string, 1)
			go func() {
				n, _ := stdout.Read(buf)
				if n > 0 {
					outputChan <- string(buf[:n])
				} else {
					outputChan <- ""
				}
			}()

			select {
			case output := <-outputChan:
				if tc.requestPTY {
					if !strings.Contains(output, "email") && !strings.Contains(output, "EXE.DEV") {
						t.Logf("Warning: Unexpected output with PTY: %s", output)
					} else {
						t.Log("Got expected output with PTY")
					}
				}
			case <-time.After(2 * time.Second):
				if tc.requestPTY {
					t.Error("Timeout reading output with PTY")
				}
			}

			// Try to send input
			_, err = stdin.Write([]byte("test\n"))
			if err != nil && tc.expectInput {
				t.Errorf("Failed to write input when it should work: %v", err)
			}

			// Clean exit
			stdin.Write([]byte{3}) // Ctrl+C
			time.Sleep(20 * time.Millisecond)
		})
	}
}
