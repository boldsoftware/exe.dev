package exe

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/sshbuf"
)

func TestShowAnimatedWelcomeWithDarkMode(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create mock channel that responds with dark terminal colors
	var outputBuf bytes.Buffer
	mockChannel := &MockChannelWithResponse{
		response: []byte("\033]11;rgb:1e1e/1e1e/1e1e\033\\"), // Dark background
		buffer:   &outputBuf,
	}
	
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)
	
	// Give the background reader time to start
	time.Sleep(10 * time.Millisecond)

	// Call the animated welcome function
	server.showAnimatedWelcome(bufferedChannel)

	// Check output
	rawOutput := outputBuf.String()

	// Should contain the fade to black sequence for dark mode
	if !strings.Contains(rawOutput, "\033[30m") {
		t.Error("Expected black color code (\\033[30m) in dark mode animation")
	}
	
	// Should contain the OSC 11 query
	if !strings.Contains(rawOutput, "\033]11;?") {
		t.Error("Expected OSC 11 query in output")
	}

	t.Log("Dark mode animation completed successfully")
}

func TestShowAnimatedWelcomeWithLightMode(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create mock channel that responds with light terminal colors
	var outputBuf bytes.Buffer
	mockChannel := &MockChannelWithResponse{
		response: []byte("\033]11;rgb:f5f5/f5f5/f5f5\033\\"), // Light background
		buffer:   &outputBuf,
	}
	
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)
	
	// Give the background reader time to start
	time.Sleep(10 * time.Millisecond)

	// Call the animated welcome function
	server.showAnimatedWelcome(bufferedChannel)

	// Check output
	rawOutput := outputBuf.String()

	// Should contain the fade to white sequence for light mode
	if !strings.Contains(rawOutput, "\033[37m") {
		t.Error("Expected white color code (\\033[37m) in light mode animation")
	}
	
	// Should contain light green colors in the fade sequence
	if !strings.Contains(rawOutput, "\033[38;5;194m") {
		t.Error("Expected light green color code in light mode fade sequence")
	}
	
	// Should contain the OSC 11 query
	if !strings.Contains(rawOutput, "\033]11;?") {
		t.Error("Expected OSC 11 query in output")
	}

	t.Log("Light mode animation completed successfully")
}

func TestRegistrationFlowWithLightMode(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create mock channel that responds with light terminal colors
	var outputBuf bytes.Buffer
	mockChannel := &MockChannelWithResponse{
		// Provide light mode response twice (once for animation, once for gray text)
		response: []byte("\033]11;rgb:ffff/ffff/ffff\033\\\033]11;rgb:ffff/ffff/ffff\033\\"),
		buffer:   &outputBuf,
	}
	
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)
	
	// Give the background reader time to start
	time.Sleep(10 * time.Millisecond)

	// Start registration in a goroutine since it will block waiting for input
	done := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				// Registration will panic/exit when it tries to read input
				// That's expected for this test
			}
			done <- true
		}()
		server.handleRegistration(bufferedChannel, "test-fingerprint")
	}()

	// Give the animation and initial output some time to run
	time.Sleep(2 * time.Second)

	// Wait for completion or timeout
	select {
	case <-done:
		// Registration completed (or exited)
	case <-time.After(3 * time.Second):
		t.Log("Registration flow timed out (expected since we can't simulate full input)")
	}

	// Check that the output uses black text instead of gray for light mode
	rawOutput := outputBuf.String()
	
	// In light mode, we should use black (\033[0;30m) instead of gray (\033[2;37m)
	// for the numbered list items
	if strings.Contains(rawOutput, "\033[2;37m1. Email Verification") {
		t.Error("Found gray text in light mode - should use black text")
	}
	
	// Should have used black text for the list
	if strings.Contains(rawOutput, "1. Email Verification") {
		// Check that black color code appears near the list
		if !strings.Contains(rawOutput, "\033[0;30m") {
			t.Error("Expected black color code (\\033[0;30m) for text in light mode")
		}
	}

	t.Log("Registration flow with light mode text colors verified")
}