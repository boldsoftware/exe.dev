package exe

import (
	"os"
	"testing"
	"time"

	"exe.dev/sshbuf"
)

// TestTestChannel verifies TestChannel works correctly
func TestTestChannel(t *testing.T) {
	tc := NewTestChannel()
	defer tc.Close()

	// Send some data
	tc.SendInputString("hello")

	// Read it back
	buf := make([]byte, 5)
	n, err := tc.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != 5 {
		t.Fatalf("Expected to read 5 bytes, got %d", n)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("Expected 'hello', got %q", string(buf[:n]))
	}

	t.Log("TestChannel basic test passed")
}

// TestSshBufWithTestChannel verifies sshbuf.Channel works with TestChannel
func TestSshBufWithTestChannel(t *testing.T) {
	tc := NewTestChannel()
	defer tc.Close()

	bc := sshbuf.New(tc)
	defer bc.Close()

	// Send some data
	tc.SendInputString("hello")

	// Give readLoop time to process
	time.Sleep(10 * time.Millisecond)

	// Read it back
	buf := make([]byte, 5)
	n, err := bc.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != 5 {
		t.Fatalf("Expected to read 5 bytes, got %d", n)
	}
	if string(buf[:n]) != "hello" {
		t.Fatalf("Expected 'hello', got %q", string(buf[:n]))
	}

	t.Log("sshbuf.Channel with TestChannel test passed")
}

// TestReadLineFromChannel tests readLineFromChannel directly
func TestReadLineFromChannel(t *testing.T) {
	tmpDB, err := os.CreateTemp("", "test_readline_*.db")
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

	tc := NewTestChannel()
	defer tc.Close()

	bc := sshbuf.New(tc)
	defer bc.Close()

	// Start reading in goroutine
	done := make(chan string, 1)
	errChan := make(chan error, 1)

	go func() {
		result, err := server.readLineFromChannel(bc)
		if err != nil {
			errChan <- err
			return
		}
		done <- result
	}()

	// Wait a moment for readLineFromChannel to start
	time.Sleep(10 * time.Millisecond)

	// Send input
	tc.SendInputString("test@example.com")
	tc.SendInputString("\n")

	// Wait for result
	select {
	case result := <-done:
		if result != "test@example.com" {
			t.Errorf("Expected 'test@example.com', got %q", result)
		}
		t.Log("readLineFromChannel test passed")
	case err := <-errChan:
		t.Fatalf("readLineFromChannel failed: %v", err)
	case <-time.After(1 * time.Second):
		t.Fatal("readLineFromChannel timed out")
	}
}

// TestEmailSignupFlow tests the email signup process
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
			defer testChan.Close()

			bufferedChannel := sshbuf.New(testChan)
			defer bufferedChannel.Close()

			// Start reading in a goroutine
			done := make(chan struct{})
			var email string
			var readErr error

			go func() {
				defer close(done)

				// Just test readLineFromChannel without detectTerminalMode
				// since detectTerminalMode adds timing complexity
				email, readErr = server.readLineFromChannel(bufferedChannel)
			}()

			// Give readLineFromChannel time to start waiting for input
			time.Sleep(20 * time.Millisecond)

			// Now simulate user interaction:
			// Type the email and press enter
			testChan.SendInputString(tc.email + "\n")

			// Wait for the read to complete with timeout
			select {
			case <-done:
				if readErr != nil {
					t.Fatalf("Failed to read email: %v", readErr)
				}
			case <-time.After(1 * time.Second):
				t.Fatal("readLineFromChannel timed out after sending input")
			}

			// Verify we got the correct email
			if email != tc.email {
				t.Errorf("Expected email %q, got %q", tc.email, email)
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
