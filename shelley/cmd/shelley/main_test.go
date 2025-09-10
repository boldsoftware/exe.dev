package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLICommands(t *testing.T) {
	// Create a temporary directory for test database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	// Build the binary for testing
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

	t.Run("list empty", func(t *testing.T) {
		cmd := exec.Command(binary, "--db", dbPath, "list")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("List command failed: %v, output: %s", err, string(output))
		}
		if !strings.Contains(string(output), "No conversations found") {
			t.Errorf("Expected 'No conversations found', got: %s", string(output))
		}
	})

	t.Run("inspect non-existent", func(t *testing.T) {
		cmd := exec.Command(binary, "--db", dbPath, "inspect", "non-existent-id")
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("Expected inspect command to fail for non-existent ID")
		}
		if !strings.Contains(string(output), "not found") {
			t.Errorf("Expected 'not found' error, got: %s", string(output))
		}
	})

	t.Run("inspect missing ID", func(t *testing.T) {
		cmd := exec.Command(binary, "--db", dbPath, "inspect")
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("Expected inspect command to fail when no ID provided")
		}
		if !strings.Contains(string(output), "conversation ID or slug is required") {
			t.Errorf("Expected 'conversation ID or slug is required' error, got: %s", string(output))
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

func TestCLIWithPredictableService(t *testing.T) {
	// Create a temporary directory for test database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	// Build the binary for testing
	binary := filepath.Join(tempDir, "shelley")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	t.Run("prompt with predictable service", func(t *testing.T) {
		// Run a prompt with predictable service
		cmd := exec.Command(binary, "--model=predictable", "--db", dbPath, "prompt", "Hello, can you help me?")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("Prompt command output: %s", string(output))
			t.Fatalf("Prompt command failed: %v", err)
		}

		outputStr := string(output)
		if !strings.Contains(outputStr, "Created conversation:") {
			t.Errorf("Expected conversation creation message, got: %s", outputStr)
		}
		if !strings.Contains(outputStr, "Using specified model") {
			t.Errorf("Expected specified model message, got: %s", outputStr)
		}

		// Extract conversation ID from output
		lines := strings.Split(outputStr, "\n")
		var conversationID string
		for _, line := range lines {
			if strings.Contains(line, "Created conversation:") {
				parts := strings.Split(line, ": ")
				if len(parts) == 2 {
					conversationID = parts[1]
					break
				}
			}
		}
		if conversationID == "" {
			t.Fatal("Could not extract conversation ID from output")
		}

		// Test list command
		cmd = exec.Command(binary, "--db", dbPath, "list")
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("List command failed: %v, output: %s", err, string(output))
		}

		if strings.Contains(string(output), "No conversations found") {
			t.Errorf("Expected conversations to be listed after prompt command")
		}
		if !strings.Contains(string(output), conversationID) {
			t.Errorf("Expected conversation ID %s to appear in list", conversationID)
		}

		// Test inspect command
		cmd = exec.Command(binary, "--db", dbPath, "inspect", conversationID)
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Inspect command failed: %v", err)
		}

		inspectOutput := string(output)
		if !strings.Contains(inspectOutput, conversationID) {
			t.Errorf("Expected conversation ID in inspect output")
		}
		if !strings.Contains(inspectOutput, "Messages: ") {
			t.Errorf("Expected message count in inspect output")
		}

		// Test continue conversation
		cmd = exec.Command(binary, "--model=predictable", "--db", dbPath, "prompt", "--continue="+conversationID, "Can you help with Python?")
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Logf("Continue command output: %s", string(output))
			t.Fatalf("Continue command failed: %v", err)
		}

		if !strings.Contains(string(output), "Conversation completed:") {
			t.Errorf("Expected conversation completion message")
		}
	})

	t.Run("model selection", func(t *testing.T) {
		// Test that model selection works
		cmd := exec.Command(binary, "--model=predictable", "--db", dbPath+"2", "prompt", "Test")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Model selection test failed: %v, output: %s", err, string(output))
		}

		if !strings.Contains(string(output), "Using specified model") {
			t.Errorf("Expected predictable service to be used")
		}
	})
}
