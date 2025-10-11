package exe

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// TestSSHTerminalModes tests that PTY modes are correctly handled
func TestSSHTerminalModes(t *testing.T) {
	t.Parallel()
	// Create server
	server := NewTestServer(t)

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

			client, err := ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", server.sshLn.tcp.Port), config)
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
