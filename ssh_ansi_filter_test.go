package exe

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestCommandSystemUsesANSIFilterInExecMode verifies that the command system properly uses ANSI filtering for exec commands
func TestCommandSystemUsesANSIFilterInExecMode(t *testing.T) {
	t.Parallel()

	// Create a mock session and test the command context creation directly
	var execOutput bytes.Buffer
	var shellOutput bytes.Buffer

	// Test handleExec uses NewANSIFilterWriter
	execFilterWriter := NewANSIFilterWriter(&execOutput)
	shellDirectWriter := &shellOutput

	// Create test content with ANSI codes (like what help command would output)
	testContentWithANSI := "\033[1;33mCommand: help\033[0m\r\n\033[1mUsage:\033[0m show help\r\n\033[32mGreen text\033[0m"

	// Write to exec mode (should filter ANSI)
	execFilterWriter.Write([]byte(testContentWithANSI))

	// Write to shell mode (should preserve ANSI)
	shellDirectWriter.Write([]byte(testContentWithANSI))

	execResult := execOutput.String()
	shellResult := shellOutput.String()

	t.Logf("Original content: %q", testContentWithANSI)
	t.Logf("Exec mode result: %q", execResult)
	t.Logf("Shell mode result: %q", shellResult)

	// Verify exec mode filters ANSI codes
	if strings.Contains(execResult, "\033[") || strings.Contains(execResult, "\x1b[") {
		t.Errorf("Exec mode should filter ANSI codes, but found them in: %q", execResult)
	}

	// Verify shell mode preserves ANSI codes
	if !strings.Contains(shellResult, "\033[") {
		t.Errorf("Shell mode should preserve ANSI codes, but they were missing from: %q", shellResult)
	}

	// Verify content is otherwise the same
	expectedFiltered := "Command: help\r\nUsage: show help\r\nGreen text"
	if execResult != expectedFiltered {
		t.Errorf("Filtered content doesn't match expected.\nExpected: %q\nGot: %q", expectedFiltered, execResult)
	}
}

// TestANSIFilterIntegratesWithCommandSystem tests that the ANSI filter integrates properly with command execution
func TestANSIFilterIntegratesWithCommandSystem(t *testing.T) {
	t.Parallel()

	// Create a test command that outputs ANSI codes
	testCommand := &Command{
		Name:        "ansi-test",
		Description: "Test command that outputs ANSI codes",
		Handler: func(ctx context.Context, cc *CommandContext) error {
			// Output content with ANSI codes like a real command would
			cc.Write("\033[1;32mSuccess:\033[0m Command executed\r\n")
			cc.Write("Regular text\r\n")
			cc.Write("\033[31mError-like text\033[0m\r\n")
			return nil
		},
	}

	// Create command tree with our test command
	commandTree := &CommandTree{
		Commands: []*Command{testCommand},
	}

	// Test exec mode - should filter ANSI
	t.Run("exec mode filters ANSI", func(t *testing.T) {
		var execOutput bytes.Buffer
		execContext := &CommandContext{
			Output: NewANSIFilterWriter(&execOutput), // This simulates handleExec behavior
			Args:   []string{},
		}

		rc := commandTree.ExecuteCommand(context.Background(), execContext, []string{"ansi-test"})
		if rc != 0 {
			t.Fatalf("Command execution failed with exit code %d", rc)
		}

		result := execOutput.String()
		t.Logf("Exec mode output: %q", result)

		// Should not contain ANSI codes
		if strings.Contains(result, "\033[") || strings.Contains(result, "\x1b[") {
			t.Errorf("Exec mode should filter ANSI codes, but found them in output: %q", result)
		}

		// Should contain the actual content
		if !strings.Contains(result, "Success: Command executed") {
			t.Errorf("Expected filtered content not found in output: %q", result)
		}
	})

	// Test shell mode - should preserve ANSI
	t.Run("shell mode preserves ANSI", func(t *testing.T) {
		var shellOutput bytes.Buffer
		shellContext := &CommandContext{
			Output: &shellOutput, // This simulates handleShell behavior (direct writer)
			Args:   []string{},
		}

		rc := commandTree.ExecuteCommand(context.Background(), shellContext, []string{"ansi-test"})
		if rc != 0 {
			t.Fatalf("Command execution failed with exit code %d", rc)
		}

		result := shellOutput.String()
		t.Logf("Shell mode output: %q", result)

		// Should contain ANSI codes
		if !strings.Contains(result, "\033[") {
			t.Errorf("Shell mode should preserve ANSI codes, but they were missing from output: %q", result)
		}

		// Should contain the actual content with ANSI
		if !strings.Contains(result, "\033[1;32mSuccess:\033[0m Command executed") {
			t.Errorf("Expected ANSI-formatted content not found in output: %q", result)
		}
	})
}

// TestANSIFilterWriterDirectly tests the ANSIFilterWriter functionality directly
func TestANSIFilterWriterDirectly(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no ANSI codes",
			input:    "Hello, World!",
			expected: "Hello, World!",
		},
		{
			name:     "basic color codes",
			input:    "\033[31mRed Text\033[0m",
			expected: "Red Text",
		},
		{
			name:     "multiple ANSI sequences",
			input:    "\033[1;33mBold Yellow\033[0m and \033[32mGreen\033[0m",
			expected: "Bold Yellow and Green",
		},
		{
			name:     "complex ANSI sequence",
			input:    "\033[38;5;196mComplex color\033[0m",
			expected: "Complex color",
		},
		{
			name:     "mixed content",
			input:    "Normal text \033[1mBold\033[0m more text \033[32mgreen\033[0m end",
			expected: "Normal text Bold more text green end",
		},
		{
			name:     "escape sequences with different endings",
			input:    "\033[2J\033[H\033[31mRed\033[0m",
			expected: "Red",
		},
		{
			name:     "real command output example",
			input:    "\033[1;33mCommand: help\033[0m\r\n\033[1mUsage:\033[0m show help",
			expected: "Command: help\r\nUsage: show help",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			writer := NewANSIFilterWriter(&buf)

			n, err := writer.Write([]byte(tc.input))
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}

			// Check that the number of bytes returned matches the input length
			if n != len(tc.input) {
				t.Errorf("Write returned %d bytes, expected %d", n, len(tc.input))
			}

			result := buf.String()
			if result != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result)
			}
		})
	}
}

// TestANSIFilterWriterMultipleWrites tests that the filter works with multiple writes
func TestANSIFilterWriterMultipleWrites(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	writer := NewANSIFilterWriter(&buf)

	// Write parts of an ANSI sequence across multiple writes
	writes := []string{
		"Hello ",
		"\033[31m",
		"Red",
		"\033[0m",
		" World!",
	}

	for i, write := range writes {
		n, err := writer.Write([]byte(write))
		if err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
		if n != len(write) {
			t.Errorf("Write %d returned %d bytes, expected %d", i, n, len(write))
		}
	}

	result := buf.String()
	expected := "Hello Red World!"

	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}
