package container

import (
	"strings"
	"testing"
)

// TestSpecificPortParsingIssue tests the exact format that was failing
func TestSpecificPortParsingIssue(t *testing.T) {
	// Test the exact format from the error message
	testInput := "0.0.0.0:32930"
	expectedPort := 32930

	result, err := parseDockerPortMapping(testInput)
	if err != nil {
		t.Errorf("Parsing failed for '%s': %v", testInput, err)
		return
	}

	if result != expectedPort {
		t.Errorf("Expected port %d, got %d", expectedPort, result)
	}

	t.Logf("Successfully parsed '%s' -> %d", testInput, result)
}

// TestDebugPortParsing tests various edge cases that might cause issues
func TestDebugPortParsing(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected int
		hasError bool
	}{
		{"exact_error_case", "0.0.0.0:32930", 32930, false},
		{"with_whitespace", " 0.0.0.0:32930 ", 32930, false},
		{"with_newline", "0.0.0.0:32930\n", 32930, false},
		{"with_carriage_return", "0.0.0.0:32930\r\n", 32930, false},
		{"ipv6_equivalent", "[::]:32930", 32930, false},
		{"zero_ip", "0.0.0.0:0", 0, false},
		{"high_port", "0.0.0.0:65535", 65535, false},
		{"empty_after_trim", "   \n\r  ", 0, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate the actual processing that happens in getContainerSSHPort
			portStr := strings.TrimSpace(tc.input)
			if portStr == "" && tc.hasError {
				t.Logf("Expected empty string case")
				return
			}

			result, err := parseDockerPortMapping(portStr)
			if tc.hasError {
				if err == nil {
					t.Errorf("Expected error for '%s', but got result: %d", tc.input, result)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error for '%s': %v", tc.input, err)
				return
			}

			if result != tc.expected {
				t.Errorf("Input '%s': expected %d, got %d", tc.input, tc.expected, result)
			}

			t.Logf("✅ Successfully parsed '%s' -> %d", tc.input, result)
		})
	}
}

// TestMultilineDockerPortOutput tests handling of Docker port output that might have multiple lines
func TestMultilineDockerPortOutput(t *testing.T) {
	// Docker sometimes returns multiple lines for port mappings
	testCases := []struct {
		name     string
		input    string
		expected int
		hasError bool
	}{
		{"single_line_ipv4", "0.0.0.0:32930", 32930, false},
		{"single_line_ipv6", "[::]:32930", 32930, false},
		{"multiline_both", "0.0.0.0:32930\n[::]:32930", 32930, false}, // Docker might return both IPv4 and IPv6
		{"multiline_with_empty", "0.0.0.0:32930\n\n[::]:32930", 32930, false},
		{"ipv6_first", "[::]:32930\n0.0.0.0:32930", 32930, false},
		{"extra_whitespace", "  0.0.0.0:32930  \n  [::]:32930  ", 32930, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Simulate how getContainerSSHPort processes the output
			lines := strings.Split(strings.TrimSpace(tc.input), "\n")

			// Take the first non-empty line (which is what the current code does)
			var firstLine string
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" {
					firstLine = line
					break
				}
			}

			if firstLine == "" {
				if tc.hasError {
					t.Logf("Expected empty case")
					return
				}
				t.Errorf("Got empty line when processing: %q", tc.input)
				return
			}

			result, err := parseDockerPortMapping(firstLine)
			if tc.hasError {
				if err == nil {
					t.Errorf("Expected error for '%s', but got result: %d", tc.input, result)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error for '%s' (first line: '%s'): %v", tc.input, firstLine, err)
				return
			}

			if result != tc.expected {
				t.Errorf("Input '%s' (first line: '%s'): expected %d, got %d", tc.input, firstLine, tc.expected, result)
			}

			t.Logf("✅ Successfully processed multiline input -> %d", result)
		})
	}
}
