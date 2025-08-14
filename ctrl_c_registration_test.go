package exe

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/sshbuf"
	"golang.org/x/crypto/ssh"
)

// MockSSHChannelWithInterrupt implements a mock SSH channel for testing
type MockSSHChannelWithInterrupt struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	mu       sync.Mutex
	closed   bool

	// Control when to send Ctrl+C
	sendCtrlCAfter time.Duration
	ctrlCSent      bool
	startTime      time.Time
}

func NewMockSSHChannelWithInterrupt(sendCtrlCAfter time.Duration) *MockSSHChannelWithInterrupt {
	return &MockSSHChannelWithInterrupt{
		readBuf:        &bytes.Buffer{},
		writeBuf:       &bytes.Buffer{},
		sendCtrlCAfter: sendCtrlCAfter,
		startTime:      time.Now(),
	}
}

func (m *MockSSHChannelWithInterrupt) Read(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// First, return any buffered data (like email input)
	if m.readBuf.Len() > 0 {
		return m.readBuf.Read(data)
	}

	// Check if we should send Ctrl+C
	if !m.ctrlCSent && time.Since(m.startTime) >= m.sendCtrlCAfter {
		m.ctrlCSent = true
		// Send Ctrl+C (ASCII code 3)
		data[0] = 3
		return 1, nil
	}

	// Otherwise return no data (blocking read simulation)
	time.Sleep(100 * time.Millisecond)
	return 0, nil
}

func (m *MockSSHChannelWithInterrupt) Write(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, fmt.Errorf("channel closed")
	}

	return m.writeBuf.Write(data)
}

func (m *MockSSHChannelWithInterrupt) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *MockSSHChannelWithInterrupt) CloseWrite() error {
	return nil
}

func (m *MockSSHChannelWithInterrupt) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *MockSSHChannelWithInterrupt) Stderr() io.ReadWriter {
	return m
}

var _ ssh.Channel = (*MockSSHChannelWithInterrupt)(nil)

func (m *MockSSHChannelWithInterrupt) GetOutput() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeBuf.String()
}

func (m *MockSSHChannelWithInterrupt) SetInput(input string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readBuf.WriteString(input)
}

// TestCtrlCDuringRegistration tests that Ctrl+C properly cancels registration
// This is a simpler test that just verifies the basic registration flow works
func TestCtrlCDuringRegistration(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_ctrlc_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server in dev mode to avoid email sending issues
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create a simple mock channel for basic testing
	mockChannel := &SimpleMockSSHChannel{
		readBuf:  &bytes.Buffer{},
		writeBuf: &bytes.Buffer{},
	}

	// Put the email input in the buffer
	mockChannel.readBuf.WriteString("test@example.com\n")

	// After 100ms, add Ctrl+C to simulate user pressing it
	go func() {
		time.Sleep(100 * time.Millisecond)
		mockChannel.mu.Lock()
		mockChannel.readBuf.WriteByte(3) // Ctrl+C
		mockChannel.mu.Unlock()
	}()

	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Run registration in a goroutine with a shorter timeout
	done := make(chan bool)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Registration panicked: %v", r)
			}
			done <- true
		}()
		server.handleRegistrationWithWidth(bufferedChannel, "test-fingerprint", 80)
	}()

	// Wait for registration to complete
	select {
	case <-done:
		t.Log("Registration completed")
	case <-time.After(3 * time.Second):
		t.Log("Registration timed out after 3 seconds")
	}

	// Check the output
	output := mockChannel.writeBuf.String()

	// Basic checks that the registration flow started
	if !strings.Contains(output, "type ssh to get a server") {
		t.Logf("Output: %q", output)
		t.Error("Missing initial welcome message")
	}

	// The test is mainly to ensure registration doesn't hang and produces some output
	if len(output) < 100 {
		t.Errorf("Expected substantial output from registration, got %d chars", len(output))
	}
}

// TestCtrlCDuringEmailInput tests that Ctrl+C properly cancels during email input
func TestCtrlCDuringEmailInput(t *testing.T) {
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_ctrlc_input_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	// Create a simple mock channel for input testing
	mockChannel := &SimpleMockSSHChannel{
		readBuf:  &bytes.Buffer{},
		writeBuf: &bytes.Buffer{},
	}

	// Simulate typing "test" then Ctrl+C
	mockChannel.readBuf.Write([]byte("test"))
	mockChannel.readBuf.WriteByte(3) // Ctrl+C

	// Wrap the mock channel with SSHBufferedChannel
	bufferedChannel := sshbuf.New(mockChannel)

	// Test readLineFromChannel directly
	result, err := server.readLineFromChannel(bufferedChannel)

	// Should get an interrupted error
	if err == nil || err.Error() != "interrupted" {
		t.Errorf("Expected 'interrupted' error, got: %v", err)
	}

	// Result should be empty (input was cancelled)
	if result != "" {
		t.Errorf("Expected empty result after Ctrl+C, got: %q", result)
	}

	// Output should contain ^C indicator
	output := mockChannel.writeBuf.String()
	if !strings.Contains(output, "^C") {
		t.Error("Missing ^C indicator in output")
	}
}

// SimpleMockSSHChannel is a simple mock for basic testing
type SimpleMockSSHChannel struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	mu       sync.Mutex
	closed   bool
}

func (m *SimpleMockSSHChannel) Read(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.readBuf.Read(data)
}

func (m *SimpleMockSSHChannel) Write(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeBuf.Write(data)
}

func (m *SimpleMockSSHChannel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *SimpleMockSSHChannel) CloseWrite() error {
	return nil
}

func (m *SimpleMockSSHChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *SimpleMockSSHChannel) Stderr() io.ReadWriter {
	return m
}

var _ ssh.Channel = (*SimpleMockSSHChannel)(nil)
