package exe

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"exe.dev/sshbuf"
	"golang.org/x/crypto/ssh"
)

// TestEmailInputAfterFix verifies that the fix resolves the character loss issue
func TestEmailInputAfterFix(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_fix_*.db")
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
	defer server.Stop()

	testCases := []struct {
		name  string
		email string
	}{
		{"starts with us", "user@example.com"},
		{"short", "ab@c.co"},
		{"typical", "test@domain.org"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock channel that simulates the registration flow
			mockChannel := &RegistrationFlowChannel{
				writeBuf: &bytes.Buffer{},
			}

			// Simulate user typing email after seeing prompt
			mockChannel.SetInput(tc.email + "\n")

			bufferedChannel := sshbuf.New(mockChannel)

			// Simulate the flow that happens in handleRegistrationWithWidth
			// 1. detectTerminalMode (should no longer cause issues)
			terminalMode := server.detectTerminalMode(bufferedChannel)
			_ = terminalMode

			// 2. User input should now be preserved
			result, err := server.readLineFromChannel(bufferedChannel)
			if err != nil {
				t.Fatalf("Failed to read email: %v", err)
			}

			if result != tc.email {
				t.Errorf("Expected %q, got %q", tc.email, result)
				if len(result) < len(tc.email) {
					t.Logf("Lost %d characters", len(tc.email)-len(result))
				}
			}
		})
	}
}

// RegistrationFlowChannel simulates the registration flow
type RegistrationFlowChannel struct {
	writeBuf    *bytes.Buffer
	input       []byte
	inputPos    int
	mu          sync.Mutex
	inputReady  bool
}

func (c *RegistrationFlowChannel) SetInput(input string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.input = []byte(input)
	c.inputPos = 0
	// Simulate user starting to type shortly after the prompt
	time.AfterFunc(50*time.Millisecond, func() {
		c.mu.Lock()
		c.inputReady = true
		c.mu.Unlock()
	})
}

func (c *RegistrationFlowChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Simulate OSC response first if this looks like a terminal query
	// This is a simple simulation that returns no OSC response (timeout case)
	if !c.inputReady {
		time.Sleep(10 * time.Millisecond)
		return 0, nil // No OSC response, which should be fine
	}

	if c.inputPos >= len(c.input) {
		time.Sleep(10 * time.Millisecond)
		return 0, nil
	}

	// Return user input character by character
	if len(p) > 0 {
		p[0] = c.input[c.inputPos]
		c.inputPos++
		return 1, nil
	}

	return 0, nil
}

func (c *RegistrationFlowChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *RegistrationFlowChannel) Close() error       { return nil }
func (c *RegistrationFlowChannel) CloseWrite() error  { return nil }
func (c *RegistrationFlowChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (c *RegistrationFlowChannel) Stderr() io.ReadWriter { return c }

var _ ssh.Channel = (*RegistrationFlowChannel)(nil)