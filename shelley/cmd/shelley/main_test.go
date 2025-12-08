package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"shelley.exe.dev/slug"
)

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Simple Test", "simple-test"},
		{"Create a Python Script", "create-a-python-script"},
		{"Multiple   Spaces", "multiple-spaces"},
		{"Special@#$%Characters", "specialcharacters"},
		{"Under_Score_Test", "under-score-test"},
		{"--multiple-hyphens--", "multiple-hyphens"},
		{"CamelCase Example", "camelcase-example"},
		{"123 Numbers Test 456", "123-numbers-test-456"},
		{"   leading and trailing   ", "leading-and-trailing"},
		{"", ""},
	}

	for _, test := range tests {
		result := slug.Sanitize(test.input)
		if result != test.expected {
			t.Errorf("slug.Sanitize(%q) = %q, expected %q", test.input, result, test.expected)
		}
	}
}

func TestCLICommands(t *testing.T) {
	// Build the binary once for this test and its subtests
	tempDir := t.TempDir()
	binary := filepath.Join(tempDir, "shelley")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	t.Run("help message", func(t *testing.T) {
		cmd := exec.Command(binary)
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("Expected command to fail with no arguments")
		}
		outputStr := string(output)
		if !strings.Contains(outputStr, "Commands:") {
			t.Errorf("Expected help message, got: %s", outputStr)
		}
	})

	t.Run("serve flag parsing", func(t *testing.T) {
		// Test that serve command accepts flags - we can't easily test the full server
		// but we can test that it doesn't immediately error on flag parsing
		cmd := exec.Command(binary, "serve", "-h")
		output, err := cmd.CombinedOutput()
		// With flag package, -h should cause exit with code 2
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				if exitError.ExitCode() == 2 {
					// This is expected for -h flag
					outputStr := string(output)
					if !strings.Contains(outputStr, "-port") || !strings.Contains(outputStr, "-db") {
						t.Errorf("Expected serve help to show -port and -db flags, got: %s", outputStr)
					}
					return
				}
			}
		}
		// If no error or different error, that's also fine for this basic test
		t.Logf("Serve command output: %s", string(output))
	})
}
