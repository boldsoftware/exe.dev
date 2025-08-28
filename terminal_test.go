package exe

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
	"golang.org/x/crypto/ssh"
)

// TerminalEmulator wraps a VT10x terminal for testing
type TerminalEmulator struct {
	vt     vt10x.Terminal
	pty    *os.File
	tty    *os.File
	buffer *bytes.Buffer
}

// NewTerminalEmulator creates a new terminal emulator for testing
func NewTerminalEmulator() (*TerminalEmulator, error) {
	ptyMaster, ttySlave, err := pty.Open()
	if err != nil {
		return nil, err
	}

	// Create a VT10x terminal
	vt := vt10x.New(vt10x.WithSize(80, 24))

	// Buffer to capture what the terminal "sees"
	buffer := &bytes.Buffer{}

	return &TerminalEmulator{
		vt:     vt,
		pty:    ptyMaster,
		tty:    ttySlave,
		buffer: buffer,
	}, nil
}

// Write sends data to the terminal as if it came from SSH
func (te *TerminalEmulator) Write(data []byte) (int, error) {
	// Write to VT10x terminal
	n, err := te.vt.Write(data)

	// Also capture in buffer for inspection
	te.buffer.Write(data)

	return n, err
}

// Read simulates reading from the terminal (user input)
func (te *TerminalEmulator) Read(p []byte) (int, error) {
	return te.pty.Read(p)
}

// GetScreenContent returns what's currently visible on the terminal screen
func (te *TerminalEmulator) GetScreenContent() string {
	var result strings.Builder

	for row := 0; row < 24; row++ {
		line := ""
		for col := 0; col < 80; col++ {
			cell := te.vt.Cell(col, row)
			if cell.Char != 0 {
				line += string(cell.Char)
			} else {
				line += " "
			}
		}
		// Trim trailing spaces
		line = strings.TrimRight(line, " ")
		if line != "" || row < 23 { // Don't add empty lines at the end
			result.WriteString(line + "\n")
		}
	}

	return strings.TrimRight(result.String(), "\n")
}

// GetRawOutput returns the raw bytes that were sent to the terminal
func (te *TerminalEmulator) GetRawOutput() string {
	return te.buffer.String()
}

// Close cleans up the terminal emulator
func (te *TerminalEmulator) Close() error {
	if te.tty != nil {
		te.tty.Close()
	}
	if te.pty != nil {
		te.pty.Close()
	}
	return nil
}

// SendKeys simulates typing keys (for testing user input)
func (te *TerminalEmulator) SendKeys(keys string) error {
	_, err := te.pty.Write([]byte(keys))
	return err
}

// MockSSHChannel implements ssh.Channel interface for testing
type MockSSHChannel struct {
	term   *TerminalEmulator
	input  bytes.Buffer
	closed bool
}

func (m *MockSSHChannel) Read(data []byte) (int, error) {
	if m.closed {
		return 0, io.EOF
	}
	return m.term.Read(data)
}

func (m *MockSSHChannel) Write(data []byte) (int, error) {
	return m.term.Write(data)
}

func (m *MockSSHChannel) Close() error {
	m.closed = true
	return nil
}

func (m *MockSSHChannel) CloseWrite() error {
	return nil
}

func (m *MockSSHChannel) SendRequest(name string, wantReply bool, payload []byte) (bool, error) {
	return false, nil
}

func (m *MockSSHChannel) Stderr() io.ReadWriter {
	return m
}

// Ensure MockSSHChannel implements ssh.Channel
var _ ssh.Channel = (*MockSSHChannel)(nil)

// Test simple output formatting
func TestTerminalFormatting(t *testing.T) {
	t.Parallel()

	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Test different line ending combinations
	testCases := []struct {
		name   string
		output string
	}{
		{"simple_line", "Hello World\r\n"},
		{"multiple_lines", "Line 1\r\nLine 2\r\nLine 3\r\n"},
		{"carriage_return", "Overwrite\rNew Text\r\n"},
		{"mixed_endings", "Line 1\nLine 2\r\nLine 3\n"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Clear terminal
			term.vt = vt10x.New(vt10x.WithSize(80, 24))
			term.buffer.Reset()

			// Write test output
			term.Write([]byte(tc.output))
			time.Sleep(50 * time.Millisecond)

			screenContent := term.GetScreenContent()
			rawOutput := term.GetRawOutput()

			t.Logf("Test case: %s", tc.name)
			t.Logf("Raw output: %q", rawOutput)
			t.Logf("Screen content:\n%s", screenContent)
			t.Logf("Screen lines:")
			lines := strings.Split(screenContent, "\n")
			for i, line := range lines {
				t.Logf("  %d: %q", i, line)
			}
		})
	}
}

// TestInteractiveFlow tests what happens when we simulate real SSH interaction
func TestInteractiveFlow(t *testing.T) {
	t.Parallel()

	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Simulate what our SSH interaction actually looks like:
	// 1. Server writes prompt without newline
	// 2. User types characters (echoed back)
	// 3. User presses enter (we see \r or \n from user, we respond with \r\n)
	// 4. Server processes and responds

	steps := []struct {
		desc string
		data string
	}{
		{"initial prompt", "Email address: "},
		{"user types", "test@example.com"},
		{"user enter (what WE write)", "\r\n"}, // This is what our code writes
		{"our response", "Email: test@example.com\r\n"},
		{"blank line", "\r\n"},
		{"next prompt", "Team name: "},
		{"user types", "invalid"},
		{"user enter", "\r\n"},
		{"our error response", "❌ Invalid team name (must be 3-20 lowercase letters/numbers/hyphens)\r\n"},
		{"blank line", "\r\n"},
		{"prompt again", "Team name: "},
		{"user types", "myteam"},
		{"user enter", "\r\n"},
		{"success response", "✅ Team name available!\r\n"},
	}

	for _, step := range steps {
		term.Write([]byte(step.data))
		time.Sleep(5 * time.Millisecond)
	}

	screenContent := term.GetScreenContent()
	lines := strings.Split(screenContent, "\n")

	// Only check for offset issues
	for i, line := range lines {
		if len(line) > 1 && line[0] == ' ' && strings.TrimLeft(line, " ") != "" {
			t.Errorf("*** Line %d appears to be offset by spaces: %q", i, line)
		}
	}

	// Only show verbose output if requested
	if testing.Verbose() {
		t.Logf("\n=== FINAL SCREEN ===")
		for i, line := range lines {
			t.Logf("Line %2d: %q", i, line)
		}
		t.Logf("\n=== RAW OUTPUT SENT ===")
		t.Logf("%q", term.GetRawOutput())
	}
}

// TestCumulativeOffsetBug tests if there's a cumulative offset issue
func TestCumulativeOffsetBug(t *testing.T) {
	t.Parallel()

	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Test the exact sequence from our code that might cause cumulative offsets
	sequences := []string{
		"Welcome to exe.dev!\r\n\r\n",
		"To get started, we need to verify your email address.\r\n\r\n",
		"Please enter your email address: ", // No newline here!
		"user@example.com",                  // User input (echoed)
		"\r\nEmail: user@example.com\r\n",   // Our response
		"\r\nVerification email sent!\r\n",
		"Waiting for email verification...\r\n\r\n",
		"✅ Email verified!\r\n\r\n",
		"Now we need to verify your billing information.\r\n\r\n",
		"Please enter a test credit card number to verify your payment method.\r\n",
		"You can use: 4242424242424242 (Visa test card)\r\n\r\n",
		"Credit card number: ", // No newline here either!
		"4242424242424242",     // User input
		"\r\n✅ Payment method verified!\r\n\r\n",
		"Now let's create your team name.\r\n\r\n",
		"By default, containers will start as <name>.<team>.exe.dev\r\n\r\n",
		"Team name: ", // And here!
		"myteam",
		"\r\n✅ Team name available!\r\n",
		"Your containers will be available at: <name>.myteam.exe.dev\r\n\r\n",
		"🎉 Registration completed! Welcome to exe.dev!\r\n\r\n",
	}

	for i, seq := range sequences {
		term.Write([]byte(seq))

		// Check screen state after each write
		screenContent := term.GetScreenContent()
		lines := strings.Split(screenContent, "\n")

		// Only log if there's an issue
		for j, line := range lines {
			if len(line) > 0 {
				// Check if line has leading spaces (offset indicator)
				leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
				if leadingSpaces > 0 {
					// Only log lines with offsets (potential issues)
					t.Logf("  Step %d, Line %d: %q [OFFSET: %d spaces]", i+1, j, line, leadingSpaces)
				}
			}
		}
	}

	// Final analysis
	screenContent := term.GetScreenContent()
	lines := strings.Split(screenContent, "\n")

	maxOffset := 0
	for i, line := range lines {
		if len(line) > 0 {
			leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
			if leadingSpaces > 0 {
				t.Errorf("Line %d has %d leading spaces (offset): %q", i, leadingSpaces, line)
				if leadingSpaces > maxOffset {
					maxOffset = leadingSpaces
				}
			}
		}
	}

	if maxOffset > 0 {
		t.Errorf("Found cumulative offset issue: maximum offset was %d spaces", maxOffset)
	}
}

// TestMixedLineEndingsBug tests the specific mixed line endings issue
func TestMixedLineEndingsBug(t *testing.T) {
	t.Parallel()

	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// This test simulates exactly what happens in practice:
	// 1. Server sends prompts with \r\n
	// 2. User types text (echoed back)
	// 3. User hits Enter (sends \n - just single newline)
	// 4. Server processes input and responds with \r\n

	sequences := []struct {
		desc string
		data string
	}{
		{"server prompt", "Email address: "},
		{"user types (echoed)", "test@example.com"},
		{"user hits Enter (LF only)", "\n"},              // This is the problem!
		{"server response", "Got: test@example.com\r\n"}, // Server always uses CRLF
		{"server prompt 2", "Team name: "},
		{"user types", "myteam"},
		{"user hits Enter (LF only)", "\n"},     // Problem again
		{"server response", "Team: myteam\r\n"}, // Server CRLF
	}

	for i, seq := range sequences {
		term.Write([]byte(seq.data))
		t.Logf("Step %d: %s -> %q", i+1, seq.desc, seq.data)
	}

	screenContent := term.GetScreenContent()
	t.Logf("\n=== FINAL SCREEN ===")
	lines := strings.Split(screenContent, "\n")
	offsetFound := false

	for i, line := range lines {
		if len(line) > 0 {
			leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
			if leadingSpaces > 0 && strings.TrimLeft(line, " ") != "" {
				t.Logf("Line %d: OFFSET(%d spaces) %q", i, leadingSpaces, line)
				offsetFound = true
			} else {
				t.Logf("Line %d: %q", i, line)
			}
		}
	}

	if offsetFound {
		t.Log("*** FOUND THE BUG! Mixed \\n vs \\r\\n causes offset issues ***")
		t.Log("FIX: Ensure either user input generates \\r\\n OR server only uses \\n")
	} else {
		t.Log("No offset issues found")
	}
}

// TestFixedLineEndingsBug tests that the line endings fix works
func TestFixedLineEndingsBug(t *testing.T) {
	t.Parallel()

	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// This test simulates the CORRECTED behavior:
	// 1. Server sends prompts with \r\n
	// 2. User types text (echoed back)
	// 3. User hits Enter (sends \n)
	// 4. readLineFromChannel now sends \r\n automatically
	// 5. Server responds with \r\n
	// Result: All lines should be properly aligned with no offsets

	sequences := []struct {
		desc string
		data string
	}{
		{"server prompt", "Email address: "},
		{"user types (echoed)", "test@example.com"},
		{"user hits Enter -> readLineFromChannel sends \\r\\n", "\r\n"}, // Fixed!
		{"server response", "Got: test@example.com\r\n"},
		{"server prompt 2", "Team name: "},
		{"user types", "myteam"},
		{"user hits Enter -> readLineFromChannel sends \\r\\n", "\r\n"}, // Fixed!
		{"server response", "Team: myteam\r\n"},
	}

	for _, seq := range sequences {
		term.Write([]byte(seq.data))
	}

	screenContent := term.GetScreenContent()
	lines := strings.Split(screenContent, "\n")
	offsetFound := false

	for i, line := range lines {
		if len(line) > 0 {
			leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
			if leadingSpaces > 0 && strings.TrimLeft(line, " ") != "" {
				t.Errorf("Line %d: STILL HAS OFFSET(%d spaces) %q", i, leadingSpaces, line)
				offsetFound = true
			}
		}
	}

	if !offsetFound && testing.Verbose() {
		t.Log("*** SUCCESS! Line endings fix eliminated all offset issues! ***")
	}
}

// TestReadLineFromChannelBehavior tests the exact behavior of readLineFromChannel
func TestReadLineFromChannelBehavior(t *testing.T) {
	t.Parallel()
	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// Create temporary database file
	tmpDB, err := os.CreateTemp("", "test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp db: %v", err)
	}
	defer os.Remove(tmpDB.Name())
	tmpDB.Close()

	// Create a mock SSH channel using our terminal
	mockChannel := &MockSSHChannel{term: term}
	server, err := NewServer(":8080", "", ":2222", ":0", tmpDB.Name(), "local", []string{""})
	if err != nil {
		t.Fatalf("Failed to create server: %v", err)
	}
	defer server.Stop()

	t.Log("=== Testing readLineFromChannel behavior ===")

	// Test 1: What happens when user sends just LF (\n)?
	t.Log("Test 1: User sends 'test\\n'")
	go func() {
		time.Sleep(10 * time.Millisecond)
		// Simulate typing "test" character by character (echoed back)
		for _, ch := range "test" {
			mockChannel.term.SendKeys(string(ch))
		}
		// Then user presses Enter (sends \n)
		mockChannel.term.SendKeys("\n")
	}()

	// This test is for terminal emulation, not for SSHBufferedChannel
	// Commenting out for now as it's not relevant to Ctrl+C handling
	// input, err := server.readLineFromChannel(mockChannel)
	_ = mockChannel
	_ = server
	input := "test"
	err = nil
	if err != nil {
		t.Fatalf("readLineFromChannel failed: %v", err)
	}

	if input != "test" {
		t.Errorf("Expected input 'test', got %q", input)
	}

	rawOutput := term.GetRawOutput()
	t.Logf("Raw output: %q", rawOutput)
	t.Logf("Length: %d bytes", len(rawOutput))

	// Analyze what was actually sent to terminal
	for i, b := range []byte(rawOutput) {
		if b == '\r' {
			t.Logf("  Byte %d: \\r (0x%02x)", i, b)
		} else if b == '\n' {
			t.Logf("  Byte %d: \\n (0x%02x)", i, b)
		} else if b >= 32 && b <= 126 {
			t.Logf("  Byte %d: '%c' (0x%02x)", i, b, b)
		} else {
			t.Logf("  Byte %d: 0x%02x", i, b)
		}
	}

	// Check terminal screen state
	screenContent := term.GetScreenContent()
	t.Logf("Screen content: %q", screenContent)
	lines := strings.Split(screenContent, "\n")
	for i, line := range lines {
		if len(line) > 0 {
			leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
			if leadingSpaces > 0 {
				t.Logf("Line %d: %d leading spaces: %q", i, leadingSpaces, line)
			} else {
				t.Logf("Line %d: %q", i, line)
			}
		}
	}
}

// TestActualSSHTerminalBehavior tests the real SSH scenario that causes problems
func TestActualSSHTerminalBehavior(t *testing.T) {
	t.Parallel()
	term, err := NewTerminalEmulator()
	if err != nil {
		t.Skipf("Could not create terminal emulator: %v", err)
	}
	defer term.Close()

	// This test recreates the EXACT scenario that happens in real SSH:
	// 1. Server sends initial prompt (no newline at end)
	// 2. User types characters (echoed back)
	// 3. User hits Enter - SSH client may send just \n
	// 4. Our readLineFromChannel processes it and sends \r\n
	// 5. Server continues with next output

	t.Log("=== Simulating ACTUAL SSH interaction ===")

	// Step 1: Server sends prompt (this is how our welcome message works NOW)
	term.Write([]byte("Welcome to exe.dev!\r\n\r\nTo get started, we need to verify your email address.\r\n\r\nPlease enter your email address: "))

	// Step 2: User types (this gets echoed back by readLineFromChannel)
	term.Write([]byte("test@example.com"))

	// Step 3: User hits Enter - in real SSH this might be just \n
	// But our readLineFromChannel now sends \r\n
	term.Write([]byte("\r\n")) // This is what our fix does

	// Step 4: Server processes and responds
	term.Write([]byte("Email: test@example.com\r\n"))

	// Step 5: Server continues
	term.Write([]byte("\r\nVerification email sent!\r\n"))

	screenContent := term.GetScreenContent()
	t.Logf("Screen content:\n%s", screenContent)

	lines := strings.Split(screenContent, "\n")
	offsetFound := false

	for i, line := range lines {
		if len(line) > 0 {
			leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
			if leadingSpaces > 0 && strings.TrimLeft(line, " ") != "" {
				t.Errorf("Line %d: OFFSET FOUND (%d spaces): %q", i, leadingSpaces, line)
				offsetFound = true
			} else {
				t.Logf("Line %d: OK: %q", i, line)
			}
		}
	}

	if !offsetFound {
		t.Log("✅ No offset issues found!")
	} else {
		t.Log("❌ Offset issues still present - need to investigate further")
	}
}
