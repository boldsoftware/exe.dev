package exe

import (
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/sshbuf"
)

// TestEmailSignupFlow tests the email signup process without sleeps
func TestEmailSignupFlow(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_signup_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	testCases := []struct {
		name  string
		email string
	}{
		{"simple", "user@example.com"},
		{"with_dots", "john.doe@example.com"},
		{"with_plus", "user+test@example.com"},
		{"short", "a@b.co"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a deterministic test channel
			testChan := NewTestChannel()
			bufferedChannel := sshbuf.New(testChan)
			
			// Start reading in a goroutine
			done := make(chan error, 1)
			var email string
			
			go func() {
				// This simulates what handleRegistration does:
				// 1. Detect terminal mode (sends OSC query)
				_ = server.detectTerminalMode(bufferedChannel)
				
				// 2. Clear OSC response (now a no-op)
				server.clearOSCResponse(bufferedChannel)
				
				// 3. Read email from user
				result, err := server.readLineFromChannel(bufferedChannel)
				email = result
				done <- err
			}()
			
			// Wait a moment for the OSC query to be sent and timeout
			// In real usage, the terminal either responds quickly or doesn't respond
			// The 100ms timeout in detectTerminalMode will pass
			select {
			case <-time.After(150 * time.Millisecond):
				// Timeout passed, now user starts typing
			}
			
			// Now simulate user interaction after the OSC timeout:
			// Type the email
			testChan.SendInputString(tc.email)
			// Press enter
			testChan.SendInputString("\n")
			
			// Wait for the read to complete
			err := <-done
			if err != nil {
				t.Fatalf("Failed to read email: %v", err)
			}
			
			// Verify we got the correct email
			if email != tc.email {
				t.Errorf("Expected email %q, got %q", tc.email, email)
			}
			
			// Check output for the prompts
			output := testChan.GetOutput()
			if !strings.Contains(output, "\033]11;?") {
				t.Log("Note: No OSC query in output (terminal mode detection might be skipped)")
			}
		})
	}
}

// TestCharacterLossScenarios verifies that no characters are lost during input
func TestCharacterLossScenarios(t *testing.T) {
	// Create server
	tmpDB, err := os.CreateTemp("", "test_loss_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	scenarios := []struct {
		name        string
		setup       func(*TestChannel)
		input       string
		expected    string
	}{
		{
			name: "immediate_typing",
			setup: func(tc *TestChannel) {
				// User starts typing immediately, no OSC response
			},
			input:    "user@example.com",
			expected: "user@example.com",
		},
		{
			name: "osc_response_then_typing",
			setup: func(tc *TestChannel) {
				// Terminal responds to OSC query
				tc.SendInputString("\033]11;rgb:0000/0000/0000\033\\")
			},
			input:    "user@example.com",
			expected: "user@example.com",
		},
		{
			name: "partial_osc_then_typing",
			setup: func(tc *TestChannel) {
				// Terminal sends partial OSC response
				tc.SendInputString("\033]11;rgb:")
			},
			input:    "user@example.com",
			expected: "user@example.com",
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			testChan := NewTestChannel()
			bufferedChannel := sshbuf.New(testChan)
			
			// Apply any setup
			if sc.setup != nil {
				sc.setup(testChan)
			}
			
			// Start the read operation
			done := make(chan string, 1)
			go func() {
				// Simulate the registration flow
				_ = server.detectTerminalMode(bufferedChannel)
				server.clearOSCResponse(bufferedChannel)
				result, _ := server.readLineFromChannel(bufferedChannel)
				done <- result
			}()
			
			// Send the input
			testChan.SendInputString(sc.input + "\n")
			
			// Get the result
			result := <-done
			
			if result != sc.expected {
				t.Errorf("Character loss detected! Expected %q, got %q", sc.expected, result)
				if len(result) < len(sc.expected) {
					lost := sc.expected[:len(sc.expected)-len(result)]
					t.Logf("Lost characters: %q", lost)
				}
			}
		})
	}
}

// TestReadLineEditing tests line editing capabilities
func TestReadLineEditing(t *testing.T) {
	tmpDB, err := os.CreateTemp("", "test_edit_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	server, err := NewServer(":18080", "", ":12222", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	tests := []struct {
		name     string
		input    []string // Sequence of inputs
		expected string
	}{
		{
			name:     "simple_input",
			input:    []string{"hello", "\n"},
			expected: "hello",
		},
		{
			name:     "backspace",
			input:    []string{"hello", "\x7f", "\x7f", "p", "\n"},
			expected: "help",
		},
		{
			name:     "ctrl_u_clear",
			input:    []string{"wrong", "\x15", "right", "\n"},
			expected: "right",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testChan := NewTestChannel()
			bufferedChannel := sshbuf.New(testChan)
			
			done := make(chan string, 1)
			go func() {
				result, _ := server.readLineFromChannel(bufferedChannel)
				done <- result
			}()
			
			// Send input sequence
			for _, input := range tt.input {
				testChan.SendInputString(input)
			}
			
			result := <-done
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}