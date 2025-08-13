package exe

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/sshbuf"
	"golang.org/x/crypto/ssh"
)

// TestEmailSignupAfterCharacterLossFix verifies that the character loss bug is fixed
func TestEmailSignupAfterCharacterLossFix(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_email_signup_fix_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server in dev mode
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Test cases that were problematic before the fix
	testCases := []struct {
		name  string
		email string
	}{
		{"starts with user", "user@example.com"},    // First two chars: "us"
		{"starts with john", "john@domain.org"},     // First two chars: "jo"
		{"starts with test", "test@company.com"},    // First two chars: "te"
		{"short email", "a@b.co"},                   // Very short
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a channel that simulates the exact sequence from handleRegistrationWithWidth
			mockChannel := &SignupFlowChannel{
				writeBuf: &bytes.Buffer{},
			}

			// Simulate user typing quickly after prompt appears
			mockChannel.SetUserEmail(tc.email + "\n")

			bufferedChannel := sshbuf.New(mockChannel)

			// Simulate the exact sequence that happens in handleRegistrationWithWidth:
			// 1. detectTerminalMode() - sends OSC query
			terminalMode := server.detectTerminalMode(bufferedChannel)
			
			// 2. clearOSCResponse() - this used to consume user input but now should be safe
			server.clearOSCResponse(bufferedChannel)
			
			// 3. User input should still be intact
			email, err := server.readLineFromChannel(bufferedChannel)
			if err != nil {
				t.Fatalf("Failed to read email: %v", err)
			}

			// Verify we got the complete email without character loss
			if email != tc.email {
				t.Errorf("Character loss detected! Expected %q, got %q", tc.email, email)
				if len(email) < len(tc.email) {
					lost := tc.email[:len(tc.email)-len(email)]
					t.Logf("Lost characters: %q", lost)
					if len(email) == len(tc.email)-2 {
						t.Log("This looks like the original bug (first 2 characters lost)")
					}
				}
			} else {
				t.Logf("✓ Email input preserved correctly: %q", email)
			}

			_ = terminalMode
		})
	}
}

// SignupFlowChannel simulates the exact conditions during signup
type SignupFlowChannel struct {
	writeBuf  *bytes.Buffer
	userEmail []byte
	emailPos  int
	mu        sync.Mutex
	stage     int // 0: OSC query, 1: user typing
}

func (c *SignupFlowChannel) SetUserEmail(email string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.userEmail = []byte(email)
	c.emailPos = 0
	c.stage = 0
}

func (c *SignupFlowChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.stage {
	case 0:
		// First stage: respond to OSC query (or timeout)
		// Simulate no OSC response (many terminals don't support it)
		c.stage = 1
		time.Sleep(5 * time.Millisecond) // Brief delay before user starts typing
		return 0, nil

	case 1:
		// Second stage: user typing email
		if c.emailPos >= len(c.userEmail) {
			time.Sleep(100 * time.Millisecond)
			return 0, nil
		}

		// Simulate user typing - return characters one by one
		if len(p) > 0 {
			p[0] = c.userEmail[c.emailPos]
			c.emailPos++
			return 1, nil
		}
	}

	return 0, nil
}

func (c *SignupFlowChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *SignupFlowChannel) Close() error       { return nil }
func (c *SignupFlowChannel) CloseWrite() error  { return nil }
func (c *SignupFlowChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (c *SignupFlowChannel) Stderr() io.ReadWriter { return c }

var _ ssh.Channel = (*SignupFlowChannel)(nil)

// TestReadLineFromChannelWithRapidInput tests readLineFromChannel specifically
func TestReadLineFromChannelWithRapidInput(t *testing.T) {
	server := &Server{}

	// Test rapid input scenarios
	testInputs := []string{
		"user@example.com\n",
		"a@b.co\n",
		"test123@domain.org\n",
		"very.long.email.address@subdomain.example.com\n",
	}

	for _, input := range testInputs {
		expected := strings.TrimSuffix(input, "\n")
		t.Run("input_"+expected, func(t *testing.T) {
			// Create a simple channel that provides input immediately
			mockChannel := &SimpleInputChannel{
				input:    []byte(input),
				writeBuf: &bytes.Buffer{},
			}

			bufferedChannel := sshbuf.New(mockChannel)

			// Give sshbuf time to read and buffer the input
			time.Sleep(10 * time.Millisecond)

			result, err := server.readLineFromChannel(bufferedChannel)
			if err != nil {
				t.Fatalf("Failed to read line: %v", err)
			}

			if result != expected {
				t.Errorf("Expected %q, got %q", expected, result)
			}
		})
	}
}

// SimpleInputChannel provides input immediately
type SimpleInputChannel struct {
	input    []byte
	inputPos int
	writeBuf *bytes.Buffer
	mu       sync.Mutex
}

func (c *SimpleInputChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inputPos >= len(c.input) {
		time.Sleep(100 * time.Millisecond)
		return 0, nil
	}

	// Return one character at a time to simulate typing
	if len(p) > 0 {
		p[0] = c.input[c.inputPos]
		c.inputPos++
		return 1, nil
	}

	return 0, nil
}

func (c *SimpleInputChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *SimpleInputChannel) Close() error       { return nil }
func (c *SimpleInputChannel) CloseWrite() error  { return nil }
func (c *SimpleInputChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (c *SimpleInputChannel) Stderr() io.ReadWriter { return c }

var _ ssh.Channel = (*SimpleInputChannel)(nil)