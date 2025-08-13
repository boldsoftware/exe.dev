package exe

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

// SimpleMockChannel is a basic mock SSH channel for testing
type SimpleMockChannel struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	mu       sync.Mutex
	closed   bool
}

func (m *SimpleMockChannel) Read(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.closed {
		return 0, io.EOF
	}
	
	return m.readBuf.Read(data)
}

func (m *SimpleMockChannel) Write(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if m.closed {
		return 0, io.EOF
	}
	
	return m.writeBuf.Write(data)
}

func (m *SimpleMockChannel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *SimpleMockChannel) CloseWrite() error {
	return m.Close()
}

func (m *SimpleMockChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *SimpleMockChannel) Stderr() io.ReadWriter {
	return m
}

// TestReadLineCtrlU tests that Ctrl+U clears the line
func TestReadLineCtrlU(t *testing.T) {
	// Create a server for testing
	server, err := NewServer(":0", ":0", ":0", ":memory:", true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create a mock channel with pre-filled input
	readBuf := &bytes.Buffer{}
	// Type "hello"
	readBuf.WriteString("hello")
	// Send Ctrl+U (ASCII 21)
	readBuf.WriteByte(21)
	// Type "world"
	readBuf.WriteString("world")
	// Press Enter
	readBuf.WriteByte('\n')
	
	mockChannel := &SimpleMockChannel{
		readBuf:  readBuf,
		writeBuf: &bytes.Buffer{},
	}

	// Read the line
	input, err := server.readLineFromChannel(mockChannel)
	if err != nil {
		t.Fatalf("readLineFromChannel failed: %v", err)
	}

	// Should only get "world" since "hello" was cleared
	if input != "world" {
		t.Errorf("Expected 'world', got '%s'", input)
	}

	// Check the output to verify the line was cleared visually
	output := mockChannel.writeBuf.String()
	
	// Should contain:
	// - "hello" being echoed
	// - 5 backspace sequences ("\b \b") to clear "hello"
	// - "world" being echoed
	// - "\r\n" at the end
	
	// Count backspace sequences
	backspaceCount := 0
	for i := 0; i < len(output)-2; i++ {
		if output[i:i+3] == "\b \b" {
			backspaceCount++
		}
	}
	
	if backspaceCount != 5 { // "hello" is 5 characters
		t.Errorf("Expected 5 backspace sequences for clearing 'hello', found %d", backspaceCount)
	}
	
	t.Logf("Output buffer: %q", output)
	t.Logf("Final input: %q", input)
}

// TestReadLineCtrlUEmpty tests that Ctrl+U on empty line does nothing
func TestReadLineCtrlUEmpty(t *testing.T) {
	// Create a server for testing
	server, err := NewServer(":0", ":0", ":0", ":memory:", true, "")
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create a mock channel with pre-filled input
	readBuf := &bytes.Buffer{}
	// Send Ctrl+U (ASCII 21) on empty line
	readBuf.WriteByte(21)
	// Type "test"
	readBuf.WriteString("test")
	// Press Enter
	readBuf.WriteByte('\n')
	
	mockChannel := &SimpleMockChannel{
		readBuf:  readBuf,
		writeBuf: &bytes.Buffer{},
	}

	// Read the line
	input, err := server.readLineFromChannel(mockChannel)
	if err != nil {
		t.Fatalf("readLineFromChannel failed: %v", err)
	}

	// Should get "test"
	if input != "test" {
		t.Errorf("Expected 'test', got '%s'", input)
	}

	// Check that no backspaces were sent for the empty Ctrl+U
	output := mockChannel.writeBuf.String()
	
	// Should just have "test" echoed and "\r\n"
	// No backspace sequences should appear before "test"
	if output[:4] != "test" {
		t.Errorf("Expected output to start with 'test', got: %q", output[:4])
	}
	
	t.Logf("Output buffer: %q", output)
	t.Logf("Final input: %q", input)
}