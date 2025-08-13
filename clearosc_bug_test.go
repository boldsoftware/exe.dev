package exe

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"exe.dev/sshbuf"
	"golang.org/x/crypto/ssh"
)

// TestClearOSCResponseConsumesUserInput reproduces the bug where clearOSCResponse
// consumes user input that arrives quickly after the prompt
func TestClearOSCResponseConsumesUserInput(t *testing.T) {
	server := &Server{}

	// Create a mock channel that simulates user typing immediately after prompt
	mockChannel := &QuickTypingChannel{
		writeBuf: &bytes.Buffer{},
	}

	// Simulate user typing "user@example.com" very quickly
	userInput := "user@example.com\n"
	mockChannel.SetQuickInput(userInput)

	bufferedChannel := sshbuf.New(mockChannel)

	// This is the problematic sequence that happens in handleRegistrationWithWidth:
	// 1. detectTerminalMode() is called (sends OSC query)
	// 2. clearOSCResponse() is called immediately after (consumes user input!)

	// Simulate detectTerminalMode sending a query
	terminalMode := server.detectTerminalMode(bufferedChannel)
	_ = terminalMode

	// User starts typing immediately (simulating very fast typing)
	// Give sshbuf time to buffer the user input
	time.Sleep(5 * time.Millisecond)

	// Now clearOSCResponse gets called and should consume the user input
	server.clearOSCResponse(bufferedChannel)

	// Try to read what the user typed - this should fail because clearOSCResponse consumed it
	result, err := server.readLineFromChannel(bufferedChannel)
	
	if err != nil {
		// If we get an error, the input was likely consumed
		t.Logf("readLineFromChannel failed (input was consumed): %v", err)
	}

	// Check if we lost characters
	if result != "user@example.com" {
		if len(result) < len("user@example.com") {
			t.Errorf("BUG REPRODUCED: clearOSCResponse consumed user input. Expected %q, got %q", 
				"user@example.com", result)
			t.Logf("Lost %d characters", len("user@example.com")-len(result))
		}
	}
}

// TestClearOSCResponseTiming tests the timing sensitivity of the bug
func TestClearOSCResponseTiming(t *testing.T) {
	server := &Server{}

	testCases := []struct {
		name  string
		delay time.Duration
	}{
		{"immediate typing", 0},
		{"very fast typing", 5 * time.Millisecond},
		{"fast typing", 15 * time.Millisecond}, // This should work since it's > 10ms timeout
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockChannel := &DelayedTypingChannel{
				writeBuf:    &bytes.Buffer{},
				typingDelay: tc.delay,
			}

			userInput := "test@example.com\n"
			mockChannel.SetInput(userInput)

			bufferedChannel := sshbuf.New(mockChannel)

			// Trigger the problematic sequence
			server.detectTerminalMode(bufferedChannel)
			
			// User input starts arriving after the delay
			time.Sleep(tc.delay + 1*time.Millisecond)
			
			// clearOSCResponse consumes anything in the buffer
			server.clearOSCResponse(bufferedChannel)

			// Try to read user input
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			temp := make([]byte, 1)
			n, err := bufferedChannel.ReadCtx(ctx, temp)

			if tc.delay < 10*time.Millisecond {
				// Input should be consumed by clearOSCResponse
				if n > 0 {
					t.Errorf("Expected no input after clearOSCResponse (delay=%v), but got: %c", 
						tc.delay, temp[0])
				}
			} else {
				// Input should still be available
				if n == 0 || err != nil {
					t.Errorf("Expected input to be available (delay=%v), but got n=%d, err=%v", 
						tc.delay, n, err)
				}
			}
		})
	}
}

// QuickTypingChannel simulates a user typing immediately
type QuickTypingChannel struct {
	writeBuf *bytes.Buffer
	input    []byte
	inputPos int
	mu       sync.Mutex
}

func (c *QuickTypingChannel) SetQuickInput(input string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.input = []byte(input)
	c.inputPos = 0
}

func (c *QuickTypingChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.inputPos >= len(c.input) {
		time.Sleep(100 * time.Millisecond)
		return 0, nil
	}

	// Return all input immediately (simulates very fast typing)
	n := copy(p, c.input[c.inputPos:])
	c.inputPos += n
	return n, nil
}

func (c *QuickTypingChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *QuickTypingChannel) Close() error       { return nil }
func (c *QuickTypingChannel) CloseWrite() error  { return nil }
func (c *QuickTypingChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (c *QuickTypingChannel) Stderr() io.ReadWriter { return c }

var _ ssh.Channel = (*QuickTypingChannel)(nil)

// DelayedTypingChannel simulates typing after a specified delay
type DelayedTypingChannel struct {
	writeBuf    *bytes.Buffer
	input       []byte
	inputPos    int
	typingDelay time.Duration
	startTime   time.Time
	mu          sync.Mutex
}

func (c *DelayedTypingChannel) SetInput(input string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.input = []byte(input)
	c.inputPos = 0
	c.startTime = time.Now()
}

func (c *DelayedTypingChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Wait for the typing delay before making input available
	if time.Since(c.startTime) < c.typingDelay {
		time.Sleep(c.typingDelay - time.Since(c.startTime))
	}

	if c.inputPos >= len(c.input) {
		time.Sleep(100 * time.Millisecond)
		return 0, nil
	}

	// Return input one character at a time
	if len(p) > 0 {
		p[0] = c.input[c.inputPos]
		c.inputPos++
		return 1, nil
	}

	return 0, nil
}

func (c *DelayedTypingChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *DelayedTypingChannel) Close() error       { return nil }
func (c *DelayedTypingChannel) CloseWrite() error  { return nil }
func (c *DelayedTypingChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}
func (c *DelayedTypingChannel) Stderr() io.ReadWriter { return c }

var _ ssh.Channel = (*DelayedTypingChannel)(nil)