package exe

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/sshbuf"
	"golang.org/x/crypto/ssh"
)

// TestDirectCharacterLoss tests readLineFromChannel directly with various input patterns
func TestDirectCharacterLoss(t *testing.T) {
	server := &Server{}

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple email", "test@example.com\n", "test@example.com"},
		{"starts with us", "user@domain.com\n", "user@domain.com"},
		{"short", "a@b.co\n", "a@b.co"},
		{"long", "verylongusername@subdomain.example.org\n", "verylongusername@subdomain.example.org"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test with immediate input (all at once)
			t.Run("immediate", func(t *testing.T) {
				mockChannel := &SimpleTestChannel{
					input:    []byte(tc.input),
					writeBuf: &bytes.Buffer{},
				}
				bufferedChannel := sshbuf.New(mockChannel)

				// Give sshbuf time to read the input
				time.Sleep(10 * time.Millisecond)

				result, err := server.readLineFromChannel(bufferedChannel)
				if err != nil {
					t.Fatalf("Failed to read line: %v", err)
				}

				if result != tc.expected {
					t.Errorf("Expected %q, got %q (lost %d characters)",
						tc.expected, result, len(tc.expected)-len(result))
					if len(result) < len(tc.expected) {
						lost := tc.expected[:len(tc.expected)-len(result)]
						t.Logf("Lost characters: %q", lost)
					}
				}
			})

			// Test with character-by-character input (simulating typing)
			t.Run("typing", func(t *testing.T) {
				mockChannel := &TypingSimulatorChannel{
					input:       []byte(tc.input),
					writeBuf:    &bytes.Buffer{},
					typingDelay: 50 * time.Millisecond,
				}
				bufferedChannel := sshbuf.New(mockChannel)

				result, err := server.readLineFromChannel(bufferedChannel)
				if err != nil {
					t.Fatalf("Failed to read line: %v", err)
				}

				if result != tc.expected {
					t.Errorf("Expected %q, got %q (lost %d characters)",
						tc.expected, result, len(tc.expected)-len(result))
					if len(result) < len(tc.expected) {
						lost := tc.expected[:len(tc.expected)-len(result)]
						t.Logf("Lost characters: %q", lost)
					}
				}
			})
		})
	}
}

// SimpleTestChannel provides input all at once
type SimpleTestChannel struct {
	input    []byte
	inputPos int
	writeBuf *bytes.Buffer
	mu       sync.Mutex
}

func (c *SimpleTestChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inputPos >= len(c.input) {
		time.Sleep(100 * time.Millisecond)
		return 0, nil
	}

	// Return all available data at once
	n := copy(p, c.input[c.inputPos:])
	c.inputPos += n
	return n, nil
}

func (c *SimpleTestChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *SimpleTestChannel) Close() error      { return nil }
func (c *SimpleTestChannel) CloseWrite() error { return nil }
func (c *SimpleTestChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (c *SimpleTestChannel) Stderr() io.ReadWriter { return c }

var _ ssh.Channel = (*SimpleTestChannel)(nil)

// TypingSimulatorChannel simulates realistic typing with delays
type TypingSimulatorChannel struct {
	input       []byte
	inputPos    int
	writeBuf    *bytes.Buffer
	mu          sync.Mutex
	typingDelay time.Duration
	lastRead    time.Time
}

func (c *TypingSimulatorChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inputPos >= len(c.input) {
		time.Sleep(100 * time.Millisecond)
		return 0, nil
	}

	// Simulate typing delay
	if !c.lastRead.IsZero() && time.Since(c.lastRead) < c.typingDelay {
		time.Sleep(c.typingDelay - time.Since(c.lastRead))
	}

	// Return one character at a time to simulate typing
	if len(p) > 0 {
		p[0] = c.input[c.inputPos]
		c.inputPos++
		c.lastRead = time.Now()
		return 1, nil
	}

	return 0, nil
}

func (c *TypingSimulatorChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *TypingSimulatorChannel) Close() error      { return nil }
func (c *TypingSimulatorChannel) CloseWrite() error { return nil }
func (c *TypingSimulatorChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (c *TypingSimulatorChannel) Stderr() io.ReadWriter { return c }

var _ ssh.Channel = (*TypingSimulatorChannel)(nil)

// TestCharacterLossOnFastInput specifically tests if fast input causes character loss
func TestCharacterLossOnFastInput(t *testing.T) {
	server := &Server{}

	// Test the specific pattern mentioned in the bug report
	email := "user@example.com"

	// Create a channel that sends the first few characters very quickly
	mockChannel := &FastInitialInputChannel{
		input:    []byte(email + "\n"),
		writeBuf: &bytes.Buffer{},
	}
	bufferedChannel := sshbuf.New(mockChannel)

	// Give sshbuf time to buffer the initial burst
	time.Sleep(20 * time.Millisecond)

	result, err := server.readLineFromChannel(bufferedChannel)
	if err != nil {
		t.Fatalf("Failed to read line: %v", err)
	}

	// Check if we specifically lost the first two characters
	if len(result) == len(email)-2 && result == email[2:] {
		t.Errorf("BUG REPRODUCED: Lost first two characters. Expected %q, got %q", email, result)
	} else if result != email {
		t.Errorf("Expected %q, got %q", email, result)
	}

	// Check what was echoed back to the user
	output := mockChannel.writeBuf.String()
	if !strings.Contains(output, email) && strings.Contains(output, email[2:]) {
		t.Errorf("Character loss visible in output: %q", output)
	}
}

// FastInitialInputChannel sends input in a burst initially then slows down
type FastInitialInputChannel struct {
	input     []byte
	inputPos  int
	writeBuf  *bytes.Buffer
	mu        sync.Mutex
	burstSent bool
}

func (c *FastInitialInputChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inputPos >= len(c.input) {
		time.Sleep(100 * time.Millisecond)
		return 0, nil
	}

	if !c.burstSent {
		// Send first 3 characters in a burst
		c.burstSent = true
		burstSize := 3
		if burstSize > len(c.input) {
			burstSize = len(c.input)
		}
		if burstSize > len(p) {
			burstSize = len(p)
		}

		copy(p, c.input[c.inputPos:c.inputPos+burstSize])
		c.inputPos += burstSize
		return burstSize, nil
	}

	// Then send one character at a time
	if len(p) > 0 {
		p[0] = c.input[c.inputPos]
		c.inputPos++
		return 1, nil
	}

	return 0, nil
}

func (c *FastInitialInputChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *FastInitialInputChannel) Close() error      { return nil }
func (c *FastInitialInputChannel) CloseWrite() error { return nil }
func (c *FastInitialInputChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (c *FastInitialInputChannel) Stderr() io.ReadWriter { return c }

var _ ssh.Channel = (*FastInitialInputChannel)(nil)
