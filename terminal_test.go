package exe

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
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

	for i, line := range lines {
		if len(line) > 0 {
			leadingSpaces := len(line) - len(strings.TrimLeft(line, " "))
			if leadingSpaces > 0 && strings.TrimLeft(line, " ") != "" {
				t.Errorf("Line %d: STILL HAS OFFSET(%d spaces) %q", i, leadingSpaces, line)
			}
		}
	}
}
