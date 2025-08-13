package exe

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/sshbuf"
)

func TestShowAnimatedWelcome(t *testing.T) {
	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create mock channel and terminal
	var outputBuf bytes.Buffer
	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Override the buffer for output capture
	term.buffer = &outputBuf

	mockChannel := &MockSSHChannel{
		term: term,
	}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Call the animated welcome function
	start := time.Now()
	server.showAnimatedWelcome(bufferedChannel)
	elapsed := time.Since(start)

	// Check that it took some time (the animation should take at least 1 second)
	if elapsed < 800*time.Millisecond {
		t.Errorf("Animation completed too quickly: %v (expected at least 800ms)", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Animation took too long: %v (expected less than 3s)", elapsed)
	}

	// Check output
	rawOutput := outputBuf.String()

	// The output should contain ANSI escape codes for:
	// - Screen clearing
	// - Cursor movement
	// - Color codes
	// - The ASCII art characters
	if !strings.Contains(rawOutput, "\033[2J") {
		t.Error("Expected screen clear command in output")
	}
	if !strings.Contains(rawOutput, "\033[H") {
		t.Error("Expected cursor home command in output")
	}
	if !strings.Contains(rawOutput, "███") {
		t.Error("Expected ASCII art characters in output")
	}
	if !strings.Contains(rawOutput, "\033[1;32m") {
		t.Error("Expected color codes in output")
	}

	t.Logf("Animation completed in %v", elapsed)
	t.Logf("Output length: %d characters", len(rawOutput))
}

func TestAnimatedWelcomeIntegration(t *testing.T) {
	// Test that the registration flow with animated welcome works
	// This is a minimal test since full registration testing would require
	// interactive input simulation which is complex

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create mock channel and terminal
	var outputBuf bytes.Buffer
	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Override the buffer for output capture
	term.buffer = &outputBuf

	mockChannel := &MockSSHChannel{
		term: term,
	}
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

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

	// Give the animation some time to run
	time.Sleep(2 * time.Second)

	// Send some input to unstick the registration flow
	term.Write([]byte("\r\n"))

	// Wait for completion or timeout
	select {
	case <-done:
		// Registration completed (or exited)
	case <-time.After(5 * time.Second):
		t.Log("Registration flow timed out (expected since we can't simulate full input)")
	}

	// Check that the animation and signup content appeared
	rawOutput := outputBuf.String()

	if !strings.Contains(rawOutput, "███") {
		t.Error("Expected ASCII art in registration flow")
	}
	if !strings.Contains(rawOutput, "type ssh to get a server") {
		t.Error("Expected signup tagline in registration flow")
	}
	if !strings.Contains(rawOutput, "Email Verification") {
		t.Error("Expected setup steps in registration flow")
	}

	t.Log("Registration flow with animated welcome started successfully")
}

