package exe

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"

	"exe.dev/sshbuf"
	"golang.org/x/crypto/ssh"
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

// Ensure SimpleMockChannel implements ssh.Channel
var _ ssh.Channel = (*SimpleMockChannel)(nil)

// TestReadLineCtrlU tests that Ctrl+U clears the line
func TestReadLineCtrlU(t *testing.T) {
	// Create a server for testing
	server, err := NewServer(":0", ":0", ":0", ":memory:", "local", []string{""})
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
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Read the line
	input, err := server.readLineFromChannel(bufferedChannel)
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
	// - backspaces and spaces to clear "hello"
	// - "world" being echoed
	// - "\r\n" at the end
	
	// The new implementation moves cursor to beginning with backspaces,
	// then writes spaces to clear, then moves back again
	// So we should see: 5 backspaces, 5 spaces, 5 backspaces for clearing
	
	// Check that "hello" was echoed
	if !strings.Contains(output, "hello") {
		t.Errorf("Expected 'hello' to be echoed in output")
	}
	
	// Check that we have enough backspaces (at least 5 for moving to start)
	backspaceCount := strings.Count(output, "\b")
	if backspaceCount < 5 {
		t.Errorf("Expected at least 5 backspaces for Ctrl+U, found %d", backspaceCount)
	}
	
	// Check that "world" was echoed
	if !strings.Contains(output, "world") {
		t.Errorf("Expected 'world' to be echoed in output")
	}
	
	t.Logf("Output buffer: %q", output)
	t.Logf("Final input: %q", input)
}

// TestReadLineCtrlUEmpty tests that Ctrl+U on empty line does nothing
func TestReadLineCtrlUEmpty(t *testing.T) {
	// Create a server for testing
	server, err := NewServer(":0", ":0", ":0", ":memory:", "local", []string{""})
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
	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Read the line
	input, err := server.readLineFromChannel(bufferedChannel)
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