package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProfileFlagIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Build the binary
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "exed-test")

	buildCmd := exec.Command("go", "build", "-o", binaryPath, ".")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build exed: %v\nOutput: %s", err, output)
	}

	// Test 1: Profile flag creates a file at the specified path
	t.Run("profile with custom path", func(t *testing.T) {
		profilePath := filepath.Join(tmpDir, "custom-profile.prof")

		// Start exed with profile flag (it will fail to start due to missing deps, but profile should be created)
		cmd := exec.Command(binaryPath, "-dev", "test", "-db", "TMP", "-profile", profilePath)

		// Start the command
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start exed: %v", err)
		}

		// Wait a bit for profiling to start
		time.Sleep(1 * time.Second)

		// Kill the process
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("failed to kill process: %v", err)
		}
		cmd.Wait()

		// Check if profile file was created
		if _, err := os.Stat(profilePath); os.IsNotExist(err) {
			t.Errorf("profile file was not created at %s", profilePath)
		}
	})

	// Test 2: Open flag requires dev mode
	t.Run("open without dev mode fails", func(t *testing.T) {
		cmd := exec.Command(binaryPath, "-open")
		output, err := cmd.CombinedOutput()

		if err == nil {
			t.Errorf("expected error when using -open without dev mode, got none")
		}

		if !contains(string(output), "dev mode") {
			t.Errorf("expected error message about dev mode, got: %s", output)
		}
	})

	// Test 3: Open flag works with dev mode
	t.Run("open with dev mode", func(t *testing.T) {
		// This test just verifies the flag is accepted, we can't test browser opening in CI
		cmd := exec.Command(binaryPath, "-dev", "test", "-db", "TMP", "-open")

		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start exed: %v", err)
		}

		// Give it a moment to start
		time.Sleep(500 * time.Millisecond)

		// Kill the process
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("failed to kill process: %v", err)
		}
		cmd.Wait()
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
