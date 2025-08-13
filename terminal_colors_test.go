package exe

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"exe.dev/sshbuf"
)

// MockChannelWithResponse is a mock SSH channel that returns a pre-configured response
type MockChannelWithResponse struct {
	response   []byte
	buffer     *bytes.Buffer
	readCalled bool
	writeCalled bool
}

func (m *MockChannelWithResponse) Read(data []byte) (int, error) {
	m.readCalled = true
	if len(m.response) == 0 {
		return 0, nil
	}
	n := copy(data, m.response)
	m.response = m.response[n:]
	return n, nil
}

func (m *MockChannelWithResponse) Write(data []byte) (int, error) {
	m.writeCalled = true
	if m.buffer != nil {
		return m.buffer.Write(data)
	}
	return len(data), nil
}

func (m *MockChannelWithResponse) Close() error {
	return nil
}

func (m *MockChannelWithResponse) CloseWrite() error {
	return nil
}

func (m *MockChannelWithResponse) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return true, nil
}

func (m *MockChannelWithResponse) Stderr() io.ReadWriter {
	return m.buffer
}

func TestParseBackgroundColor(t *testing.T) {
	tests := []struct {
		name     string
		response string
		expected TerminalMode
	}{
		{
			name:     "Dark terminal - black background",
			response: "\033]11;rgb:0000/0000/0000\033\\",
			expected: TerminalModeDark,
		},
		{
			name:     "Light terminal - white background",
			response: "\033]11;rgb:ffff/ffff/ffff\033\\",
			expected: TerminalModeLight,
		},
		{
			name:     "Dark terminal - dark gray",
			response: "\033]11;rgb:2020/2020/2020\033\\",
			expected: TerminalModeDark,
		},
		{
			name:     "Light terminal - light gray",
			response: "\033]11;rgb:e0e0/e0e0/e0e0\033\\",
			expected: TerminalModeLight,
		},
		{
			name:     "Dark terminal - typical dark theme",
			response: "\033]11;rgb:1e1e/1e1e/1e1e\033\\",
			expected: TerminalModeDark,
		},
		{
			name:     "Light terminal - typical light theme",
			response: "\033]11;rgb:f5f5/f5f5/f5f5\033\\",
			expected: TerminalModeLight,
		},
		{
			name:     "BEL terminator",
			response: "\033]11;rgb:0000/0000/0000\007",
			expected: TerminalModeDark,
		},
		{
			name:     "2-digit hex values - dark",
			response: "\033]11;rgb:10/10/10\033\\",
			expected: TerminalModeDark,
		},
		{
			name:     "2-digit hex values - light",
			response: "\033]11;rgb:f0/f0/f0\033\\",
			expected: TerminalModeLight,
		},
		{
			name:     "Invalid response - defaults to dark",
			response: "invalid",
			expected: TerminalModeDark,
		},
		{
			name:     "Empty response - defaults to dark",
			response: "",
			expected: TerminalModeDark,
		},
		{
			name:     "Malformed RGB - defaults to dark",
			response: "\033]11;rgb:invalid\033\\",
			expected: TerminalModeDark,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseBackgroundColor(tt.response)
			if result != tt.expected {
				t.Errorf("parseBackgroundColor(%q) = %v, want %v", tt.response, result, tt.expected)
			}
		})
	}
}

func TestDetectTerminalMode(t *testing.T) {
	// Create a test server
	server := &Server{
		quietMode: true,
	}

	tests := []struct {
		name     string
		response []byte
		expected TerminalMode
	}{
		{
			name:     "Dark terminal response",
			response: []byte("\033]11;rgb:0000/0000/0000\033\\"),
			expected: TerminalModeDark,
		},
		{
			name:     "Light terminal response",
			response: []byte("\033]11;rgb:ffff/ffff/ffff\033\\"),
			expected: TerminalModeLight,
		},
		{
			name:     "No response - defaults to dark",
			response: []byte{},
			expected: TerminalModeDark,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock channel with response
			mockChannel := &MockChannelWithResponse{
				response: tt.response,
				buffer:   &bytes.Buffer{},
			}
			
			// Wrap with buffered channel
			channel := sshbuf.New(mockChannel)
			
			// Give the background reader a moment to start
			time.Sleep(10 * time.Millisecond)
			
			result := server.detectTerminalMode(channel)
			
			// Verify OSC 11 query was sent
			if !mockChannel.writeCalled {
				t.Error("Expected Write to be called for OSC 11 query")
			}
			
			if result != tt.expected {
				t.Errorf("detectTerminalMode() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetTerminalColors(t *testing.T) {
	server := &Server{
		quietMode: true,
	}

	t.Run("Dark mode colors", func(t *testing.T) {
		colors := server.getTerminalColors(TerminalModeDark)
		
		// Check gray text is gray for dark mode
		if colors.grayText != "\033[2;37m" {
			t.Errorf("Dark mode gray text = %q, want %q", colors.grayText, "\033[2;37m")
		}
		
		// Check fade ends in black
		lastStep := colors.fadeSteps[len(colors.fadeSteps)-1]
		if lastStep.color != "\033[30m" {
			t.Errorf("Dark mode fade final color = %q, want %q", lastStep.color, "\033[30m")
		}
		
		// Check we have the right number of fade steps
		if len(colors.fadeSteps) != 7 {
			t.Errorf("Dark mode fade steps count = %d, want 7", len(colors.fadeSteps))
		}
	})

	t.Run("Light mode colors", func(t *testing.T) {
		colors := server.getTerminalColors(TerminalModeLight)
		
		// Check gray text is black for light mode
		if colors.grayText != "\033[0;30m" {
			t.Errorf("Light mode gray text = %q, want %q", colors.grayText, "\033[0;30m")
		}
		
		// Check fade ends in white
		lastStep := colors.fadeSteps[len(colors.fadeSteps)-1]
		if lastStep.color != "\033[37m" {
			t.Errorf("Light mode fade final color = %q, want %q", lastStep.color, "\033[37m")
		}
		
		// Check we have the right number of fade steps
		if len(colors.fadeSteps) != 7 {
			t.Errorf("Light mode fade steps count = %d, want 7", len(colors.fadeSteps))
		}
	})
}

func TestGetGrayText(t *testing.T) {
	server := &Server{
		quietMode: true,
	}

	tests := []struct {
		name     string
		response []byte
		expected string
	}{
		{
			name:     "Dark terminal - returns gray",
			response: []byte("\033]11;rgb:0000/0000/0000\033\\"),
			expected: "\033[2;37m",
		},
		{
			name:     "Light terminal - returns black",
			response: []byte("\033]11;rgb:ffff/ffff/ffff\033\\"),
			expected: "\033[0;30m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock channel with response
			mockChannel := &MockChannelWithResponse{
				response: tt.response,
				buffer:   &bytes.Buffer{},
			}
			
			// Wrap with buffered channel
			channel := sshbuf.New(mockChannel)
			
			// Give the background reader a moment to start
			time.Sleep(10 * time.Millisecond)
			
			result := server.getGrayText(channel)
			
			if result != tt.expected {
				t.Errorf("getGrayText() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestClearOSCResponse(t *testing.T) {
	server := &Server{
		quietMode: true,
	}

	// Create mock channel with leftover OSC response
	leftoverResponse := []byte("leftover\033\\more")
	mockChannel := &MockChannelWithResponse{
		response: leftoverResponse,
		buffer:   &bytes.Buffer{},
	}
	
	// Wrap with buffered channel
	channel := sshbuf.New(mockChannel)
	
	// Give the background reader a moment to read the data
	time.Sleep(20 * time.Millisecond)
	
	// Clear the OSC response
	server.clearOSCResponse(channel)
	
	// Try to read - should get remaining data after terminator
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	
	buf := make([]byte, 100)
	n, _ := channel.ReadCtx(ctx, buf)
	
	// We should have cleared up to and including the terminator
	result := string(buf[:n])
	if result != "more" && result != "" {
		// Could be empty if clearOSCResponse consumed everything
		t.Errorf("After clearOSCResponse, got %q, expected 'more' or empty", result)
	}
}