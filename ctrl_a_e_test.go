package exe

import (
	"bytes"
	"testing"

	"exe.dev/sshbuf"
)

// TestReadLineCtrlAE tests that Ctrl+A and Ctrl+E move cursor correctly
func TestReadLineCtrlAE(t *testing.T) {
	// Create a server for testing
	server, err := NewServer(":0", ":0", ":0", ":memory:", true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	t.Run("Ctrl+A moves to beginning", func(t *testing.T) {
		// Create a mock channel with pre-filled input
		readBuf := &bytes.Buffer{}
		// Type "hello"
		readBuf.WriteString("hello")
		// Send Ctrl+A (ASCII 1)
		readBuf.WriteByte(1)
		// Type "X" at the beginning
		readBuf.WriteByte('X')
		// Press Enter
		readBuf.WriteByte('\n')

		mockChannel := &SimpleMockChannel{
			readBuf:  readBuf,
			writeBuf: &bytes.Buffer{},
		}
		// Wrap the mock channel with SSHBufferedChannel
		bufferedChannel := sshbuf.New(mockChannel)

		// Read the line
		input, err := server.readLineFromChannel(bufferedChannel)
		if err != nil {
			t.Fatalf("readLineFromChannel failed: %v", err)
		}

		// Should get "Xhello" since we inserted X at the beginning
		if input != "Xhello" {
			t.Errorf("Expected 'Xhello', got '%s'", input)
		}

		// Check the output contains backspace sequences for cursor movement
		output := mockChannel.writeBuf.String()
		// Should see "hello" being echoed, then backspaces for Ctrl+A
		if !bytes.Contains([]byte(output), []byte("hello")) {
			t.Error("Should echo 'hello'")
		}
		// Should have backspaces to move cursor to beginning (5 backspaces for "hello")
		backspaceCount := bytes.Count([]byte(output), []byte("\b"))
		if backspaceCount < 5 {
			t.Errorf("Expected at least 5 backspaces for Ctrl+A, got %d", backspaceCount)
		}
	})

	t.Run("Ctrl+E moves to end", func(t *testing.T) {
		// Create a mock channel with pre-filled input
		readBuf := &bytes.Buffer{}
		// Type "hello"
		readBuf.WriteString("hello")
		// Send Ctrl+A to go to beginning
		readBuf.WriteByte(1)
		// Send Ctrl+E to go to end
		readBuf.WriteByte(5)
		// Type "X" at the end
		readBuf.WriteByte('X')
		// Press Enter
		readBuf.WriteByte('\n')

		mockChannel := &SimpleMockChannel{
			readBuf:  readBuf,
			writeBuf: &bytes.Buffer{},
		}
		// Wrap the mock channel with SSHBufferedChannel
		bufferedChannel := sshbuf.New(mockChannel)

		// Read the line
		input, err := server.readLineFromChannel(bufferedChannel)
		if err != nil {
			t.Fatalf("readLineFromChannel failed: %v", err)
		}

		// Should get "helloX" since we went back to the end
		if input != "helloX" {
			t.Errorf("Expected 'helloX', got '%s'", input)
		}
	})

	t.Run("Insert in middle with Ctrl+A", func(t *testing.T) {
		// Create a mock channel with pre-filled input
		readBuf := &bytes.Buffer{}
		// Type "word"
		readBuf.WriteString("word")
		// Send Ctrl+A to go to beginning
		readBuf.WriteByte(1)
		// Type "my " at the beginning
		readBuf.WriteString("my ")
		// Press Enter
		readBuf.WriteByte('\n')

		mockChannel := &SimpleMockChannel{
			readBuf:  readBuf,
			writeBuf: &bytes.Buffer{},
		}
		// Wrap the mock channel with SSHBufferedChannel
		bufferedChannel := sshbuf.New(mockChannel)

		// Read the line
		input, err := server.readLineFromChannel(bufferedChannel)
		if err != nil {
			t.Fatalf("readLineFromChannel failed: %v", err)
		}

		// Should get "my word"
		if input != "my word" {
			t.Errorf("Expected 'my word', got '%s'", input)
		}
	})

	t.Run("Backspace after Ctrl+A", func(t *testing.T) {
		// Create a mock channel with pre-filled input
		readBuf := &bytes.Buffer{}
		// Type "hello"
		readBuf.WriteString("hello")
		// Send Ctrl+A to go to beginning
		readBuf.WriteByte(1)
		// Type 'x' at the beginning
		readBuf.WriteByte('x')
		// Now backspace should delete the 'x' we just typed
		readBuf.WriteByte(127)
		// Press Enter
		readBuf.WriteByte('\n')

		mockChannel := &SimpleMockChannel{
			readBuf:  readBuf,
			writeBuf: &bytes.Buffer{},
		}
		// Wrap the mock channel with SSHBufferedChannel
		bufferedChannel := sshbuf.New(mockChannel)

		// Read the line
		input, err := server.readLineFromChannel(bufferedChannel)
		if err != nil {
			t.Fatalf("readLineFromChannel failed: %v", err)
		}

		// Should get "hello" since we inserted 'x' at beginning then deleted it
		if input != "hello" {
			t.Errorf("Expected 'hello', got '%s'", input)
		}
	})

	t.Run("Ctrl+D after Ctrl+A", func(t *testing.T) {
		// Create a mock channel with pre-filled input
		readBuf := &bytes.Buffer{}
		// Type "hello"
		readBuf.WriteString("hello")
		// Send Ctrl+A to go to beginning
		readBuf.WriteByte(1)
		// Send Ctrl+D to delete character at cursor (should delete 'h')
		readBuf.WriteByte(4)
		// Press Enter
		readBuf.WriteByte('\n')

		mockChannel := &SimpleMockChannel{
			readBuf:  readBuf,
			writeBuf: &bytes.Buffer{},
		}
		// Wrap the mock channel with SSHBufferedChannel
		bufferedChannel := sshbuf.New(mockChannel)

		// Read the line
		input, err := server.readLineFromChannel(bufferedChannel)
		if err != nil {
			t.Fatalf("readLineFromChannel failed: %v", err)
		}

		// Should get "ello" since we deleted 'h'
		if input != "ello" {
			t.Errorf("Expected 'ello', got '%s'", input)
		}
	})
}