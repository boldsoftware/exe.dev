package exe

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"exe.dev/sshbuf"
	"golang.org/x/crypto/ssh"
)

// TestFirstTwoCharactersLostBug reproduces the bug where the first two characters
// are lost when typing an email address during SSH signup
func TestFirstTwoCharactersLostBug(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	
	// Create a server instance for testing
	server := &Server{}

	// Test multiple scenarios that could cause character loss
	testCases := []struct {
		name  string
		email string
	}{
		{"short email", "ab@c.co"},
		{"normal email", "user@example.com"},
		{"long email", "verylongusername@subdomain.example.org"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a mock channel that sends all characters at once (as can happen in real SSH)
			mockChannel := &BulkInputMockChannel{
				writeBuf: &bytes.Buffer{},
			}

			// Set the input that will be sent all at once
			mockChannel.SetBulkInput(tc.email + "\n")

			// Wrap with buffered channel
			bufferedChannel := sshbuf.New(mockChannel)

			// Give sshbuf time to read and buffer the bulk input
			time.Sleep(50 * time.Millisecond)

			// Read the line using the same function used during signup
			result, err := server.readLineFromChannel(bufferedChannel)
			if err != nil {
				t.Fatalf("Failed to read line: %v", err)
			}

			// Check if we lost characters
			if result != tc.email {
				t.Errorf("REPRODUCING BUG: Expected %q, got %q (lost %d characters)", 
					tc.email, result, len(tc.email)-len(result))
				
				// Log the exact characters lost
				if len(result) < len(tc.email) {
					lost := tc.email[:len(tc.email)-len(result)]
					t.Logf("Lost characters: %q", lost)
				}
			}
		})
	}
}

// ProblematicInputMockChannel simulates the specific input pattern that causes character loss
type ProblematicInputMockChannel struct {
	readBuf       *bytes.Buffer
	writeBuf      *bytes.Buffer
	mu            sync.Mutex
	problematicInput []byte
	readIndex     int
	firstRead     bool
}

func (m *ProblematicInputMockChannel) SetProblematicInput(input string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.problematicInput = []byte(input)
	m.readIndex = 0
	m.firstRead = true
}

func (m *ProblematicInputMockChannel) Read(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if there's any problematic input left
	if m.readIndex < len(m.problematicInput) {
		if m.firstRead {
			// On the first read, return multiple characters at once
			// This simulates what happens when a user types quickly and the SSH client
			// sends multiple characters in a single packet
			m.firstRead = false
			remainingBytes := len(m.problematicInput) - m.readIndex
			if remainingBytes > len(data) {
				remainingBytes = len(data)
			}
			// Return all available data at once - this triggers the bug
			copy(data, m.problematicInput[m.readIndex:m.readIndex+remainingBytes])
			m.readIndex += remainingBytes
			return remainingBytes, nil
		} else {
			// Subsequent reads return one character at a time
			data[0] = m.problematicInput[m.readIndex]
			m.readIndex++
			return 1, nil
		}
	}

	// Then check the regular buffer
	if m.readBuf.Len() > 0 {
		return m.readBuf.Read(data)
	}

	// No more data - return immediately with no data
	return 0, nil
}

func (m *ProblematicInputMockChannel) Write(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeBuf.Write(data)
}

func (m *ProblematicInputMockChannel) Close() error {
	return nil
}

func (m *ProblematicInputMockChannel) CloseWrite() error {
	return nil
}

func (m *ProblematicInputMockChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *ProblematicInputMockChannel) Stderr() io.ReadWriter {
	return m
}

var _ ssh.Channel = (*ProblematicInputMockChannel)(nil)

// BulkInputMockChannel sends all input at once, simulating rapid typing or paste
type BulkInputMockChannel struct {
	writeBuf    *bytes.Buffer
	mu          sync.Mutex
	bulkInput   []byte
	delivered   bool
}

func (m *BulkInputMockChannel) SetBulkInput(input string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bulkInput = []byte(input)
	m.delivered = false
}

func (m *BulkInputMockChannel) Read(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.delivered && len(m.bulkInput) > 0 {
		// Deliver all input at once - this simulates what happens when
		// a user types very quickly or pastes text
		m.delivered = true
		n := copy(data, m.bulkInput)
		if n < len(m.bulkInput) {
			// If buffer is too small, save the rest for next read
			remaining := make([]byte, len(m.bulkInput)-n)
			copy(remaining, m.bulkInput[n:])
			m.bulkInput = remaining
			m.delivered = false
		} else {
			m.bulkInput = nil
		}
		return n, nil
	}

	// No more data - return immediately with no data
	return 0, nil
}

func (m *BulkInputMockChannel) Write(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeBuf.Write(data)
}

func (m *BulkInputMockChannel) Close() error {
	return nil
}

func (m *BulkInputMockChannel) CloseWrite() error {
	return nil
}

func (m *BulkInputMockChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *BulkInputMockChannel) Stderr() io.ReadWriter {
	return m
}

var _ ssh.Channel = (*BulkInputMockChannel)(nil)

// TestEmailInputDuringRegistration tests the actual registration flow to catch character loss
func TestEmailInputDuringRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	
	// Create temporary database
	tmpDB, err := os.CreateTemp("", "test_email_input_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create server in dev mode
	server, err := NewServer(":0", "", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	testEmail := "user@example.com"

	// Create a more realistic mock channel that simulates SSH input timing
	mockChannel := &RealisticSSHInputChannel{
		writeBuf: &bytes.Buffer{},
	}

	// Simulate the user typing the email quickly after the prompt appears
	mockChannel.SetDelayedInput(testEmail + "\n", 100*time.Millisecond)

	// Wrap with buffered channel
	bufferedChannel := sshbuf.New(mockChannel)

	// Run registration in a goroutine so we can control timing
	var registrationErr error
	done := make(chan bool)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Registration panicked: %v", r)
			}
			done <- true
		}()
		// This calls readLineFromChannel internally
		server.handleRegistration(bufferedChannel, "test-fingerprint")
	}()

	// Wait for registration to complete or timeout
	select {
	case <-done:
		t.Log("Registration process completed")
	case <-time.After(5 * time.Second):
		t.Log("Registration timed out")
	}

	// Check the output for any signs of character loss
	output := mockChannel.writeBuf.String()
	
	if registrationErr != nil {
		t.Logf("Registration error: %v", registrationErr)
	}

	// Look for signs that the email was processed incorrectly
	if strings.Contains(output, "ser@example.com") || strings.Contains(output, "er@example.com") {
		t.Error("FOUND CHARACTER LOSS BUG: Email appears to be missing first characters in output")
		t.Logf("Full output: %s", output)
	}

	// Also check if validation failed due to character loss
	if strings.Contains(output, "invalid email") || strings.Contains(output, "Please enter a valid email") {
		t.Log("Email validation failed - this could indicate character loss")
		t.Logf("Full output: %s", output)
	}
}

// RealisticSSHInputChannel simulates realistic SSH input timing
type RealisticSSHInputChannel struct {
	writeBuf     *bytes.Buffer
	mu           sync.Mutex
	delayedInput []byte
	inputTimer   *time.Timer
	inputReady   bool
	inputIndex   int
}

func (m *RealisticSSHInputChannel) SetDelayedInput(input string, delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delayedInput = []byte(input)
	m.inputReady = false
	m.inputIndex = 0
	
	// Start timer to make input available after delay
	m.inputTimer = time.AfterFunc(delay, func() {
		m.mu.Lock()
		m.inputReady = true
		m.mu.Unlock()
	})
}

func (m *RealisticSSHInputChannel) Read(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.inputReady && m.inputIndex < len(m.delayedInput) {
		// Return one character at a time to simulate normal typing
		data[0] = m.delayedInput[m.inputIndex]
		m.inputIndex++
		return 1, nil
	}

	// No data available yet
	time.Sleep(10 * time.Millisecond)
	return 0, nil
}

func (m *RealisticSSHInputChannel) Write(data []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writeBuf.Write(data)
}

func (m *RealisticSSHInputChannel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inputTimer != nil {
		m.inputTimer.Stop()
	}
	return nil
}

func (m *RealisticSSHInputChannel) CloseWrite() error {
	return nil
}

func (m *RealisticSSHInputChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *RealisticSSHInputChannel) Stderr() io.ReadWriter {
	return m
}

var _ ssh.Channel = (*RealisticSSHInputChannel)(nil)