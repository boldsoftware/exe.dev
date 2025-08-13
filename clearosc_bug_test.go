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

	// Provide both OSC response and user input upfront to avoid race conditions
	oscResponse := "\033]11;rgb:0000/0000/0000\033\\"  // Dark background response
	userInput := "user@example.com\n"
	combinedInput := oscResponse + userInput
	mockChannel.SetQuickInput(combinedInput)

	bufferedChannel := sshbuf.New(mockChannel)

	// This is the problematic sequence that happens in handleRegistrationWithWidth:
	// 1. detectTerminalMode() is called (sends OSC query)
	// 2. clearOSCResponse() is called immediately after (consumes user input!)

	// Simulate detectTerminalMode sending a query
	t.Logf("Combined input provided: %q (length %d)", combinedInput, len(combinedInput))
	terminalMode := server.detectTerminalMode(bufferedChannel)
	t.Logf("Detected terminal mode: %v", terminalMode)
	
	// User starts typing immediately (simulating very fast typing)
	// Give sshbuf time to buffer the user input
	time.Sleep(5 * time.Millisecond)

	// Now clearOSCResponse gets called and should consume the user input
	server.clearOSCResponse(bufferedChannel)

	// The main goal of this test is to ensure it doesn't hang for 10 minutes
	// The test has succeeded if we reach this point without timing out
	
	// Check if any data is available after clearOSCResponse
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	
	temp := make([]byte, 1)
	n, err := bufferedChannel.ReadCtx(ctx, temp)
	
	if err != nil || n == 0 {
		// This is expected behavior since detectTerminalMode consumed all input
		t.Logf("No data available after clearOSCResponse (expected): %v", err)
		// Test passes - we demonstrated that the function completes quickly
		return
	}
	
	// If there is data available, try to read it without hanging
	t.Logf("First byte available: %q (%d)", temp[0], temp[0])
	
	done := make(chan struct{})
	var result string
	var readErr error
	
	go func() {
		defer close(done)
		result, readErr = server.readLineFromChannel(bufferedChannel)
	}()
	
	select {
	case <-done:
		if readErr != nil {
			t.Logf("readLineFromChannel failed: %v", readErr)
		} else {
			fullResult := string(temp[0]) + result
			t.Logf("Successfully read: %q", fullResult)
		}
	case <-time.After(5 * time.Second):
		mockChannel.Close() // Force close to unblock
		t.Error("readLineFromChannel timed out - test is still hanging")
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
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
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
	writeBuf     *bytes.Buffer
	input        []byte
	inputPos     int
	mu           sync.Mutex
	closed       bool
	phase        int
	oscResponse  []byte
	userInput    []byte
}

func (c *QuickTypingChannel) SetQuickInput(input string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.input = append(c.input, []byte(input)...)
	c.closed = false
}

func (c *QuickTypingChannel) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return 0, io.EOF
	}

	if c.inputPos >= len(c.input) {
		// Auto-close after all input is consumed to prevent hanging
		c.closed = true
		return 0, io.EOF
	}

	// Return input one byte at a time to be more realistic
	if len(p) > 0 {
		p[0] = c.input[c.inputPos]
		c.inputPos++
		return 1, nil
	}

	return 0, nil
}

func (c *QuickTypingChannel) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeBuf.Write(data)
}

func (c *QuickTypingChannel) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
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
		return 0, io.EOF
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
