package sshpool

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"exe.dev/ctrhosttest"
)

// TestPool tests the SSH connection pool functionality
func TestPool(t *testing.T) {
	// Skip if we don't have a test SSH host available
	testHost := os.Getenv("TEST_SSH_HOST")
	if testHost == "" {
		// Fall back to CTR_HOST when available to make CI easier to configure
		if ctr := os.Getenv("CTR_HOST"); ctr != "" {
			testHost = ctr
		} else {
			// As a last resort, auto-detect the local dev host
			if detected := ctrhosttest.Detect(nil); detected != "" {
				testHost = detected
			}
		}
	}
	if testHost == "" {
		t.Skip("TEST_SSH_HOST or CTR_HOST not set, skipping SSH pool tests")
	}

	// Create a new SSH connection pool
	pool := New()
	defer pool.Close()

	ctx := t.Context()

	// Test 1: Execute a simple command
	t.Run("SimpleCommand", func(t *testing.T) {
		cmd := pool.ExecCommand(ctx, testHost, "echo", "test")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to execute command: %v, output: %s", err, output)
		}

		result := strings.TrimSpace(string(output))
		if result != "test" {
			t.Errorf("Expected 'test', got '%s'", result)
		}
	})

	// Test 2: Verify connection reuse
	t.Run("ConnectionReuse", func(t *testing.T) {
		// First command should establish connection
		start1 := time.Now()
		cmd1 := pool.ExecCommand(ctx, testHost, "echo", "first")
		output1, err1 := cmd1.CombinedOutput()
		duration1 := time.Since(start1)

		if err1 != nil {
			t.Fatalf("First command failed: %v", err1)
		}

		// Second command should reuse connection and be faster
		start2 := time.Now()
		cmd2 := pool.ExecCommand(ctx, testHost, "echo", "second")
		output2, err2 := cmd2.CombinedOutput()
		duration2 := time.Since(start2)

		if err2 != nil {
			t.Fatalf("Second command failed: %v", err2)
		}

		t.Logf("First command took: %v", duration1)
		t.Logf("Second command took: %v", duration2)

		// The second command should typically be faster due to connection reuse
		// But we won't assert this strictly as timing can vary
		if strings.TrimSpace(string(output1)) != "first" {
			t.Errorf("First command output mismatch: %s", output1)
		}
		if strings.TrimSpace(string(output2)) != "second" {
			t.Errorf("Second command output mismatch: %s", output2)
		}
	})

	// Test 3: Multiple concurrent commands
	t.Run("ConcurrentCommands", func(t *testing.T) {
		const numCommands = 10
		results := make(chan string, numCommands)
		errors := make(chan error, numCommands)

		for i := 0; i < numCommands; i++ {
			go func(n int) {
				cmd := pool.ExecCommand(ctx, testHost, "echo", fmt.Sprintf("cmd%d", n))
				output, err := cmd.CombinedOutput()
				if err != nil {
					errors <- err
					return
				}
				results <- strings.TrimSpace(string(output))
			}(i)
		}

		// Collect results
		for i := 0; i < numCommands; i++ {
			select {
			case err := <-errors:
				t.Errorf("Concurrent command failed: %v", err)
			case result := <-results:
				if !strings.HasPrefix(result, "cmd") {
					t.Errorf("Unexpected result: %s", result)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Timeout waiting for concurrent commands")
			}
		}
	})

	// Test 4: Connection recovery after failure
	t.Run("ConnectionRecovery", func(t *testing.T) {
		// This test is harder to implement without mocking
		// For now, just verify the pool handles invalid hosts gracefully
		invalidHost := "invalid-host-that-does-not-exist.local"
		cmd := pool.ExecCommand(ctx, invalidHost, "echo", "test")
		_, err := cmd.CombinedOutput()

		if err == nil {
			t.Error("Expected error for invalid host, got none")
		}

		// Should still work with valid host after failure
		cmd = pool.ExecCommand(ctx, testHost, "echo", "recovered")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Failed to recover after invalid host: %v", err)
		}

		outStr := strings.TrimSpace(string(output))
		if !strings.HasSuffix(outStr, "recovered") {
			t.Errorf("Recovery command output mismatch: %s", output)
		}
	})

	// Test 5: Stream command with stdin/stdout
	t.Run("StreamCommand", func(t *testing.T) {
		stdin := strings.NewReader("hello world")
		var stdout strings.Builder

		err := pool.StreamCommand(ctx, testHost, stdin, &stdout, nil, "cat")
		if err != nil {
			t.Fatalf("Stream command failed: %v", err)
		}

		if stdout.String() != "hello world" {
			t.Errorf("Stream output mismatch: got '%s', want 'hello world'", stdout.String())
		}
	})

	// Test 6: Check host connection
	t.Run("CheckHostConnection", func(t *testing.T) {
		err := pool.CheckHostConnection(ctx, testHost)
		if err != nil {
			t.Errorf("Failed to check host connection: %v", err)
		}

		// Check invalid host
		err = pool.CheckHostConnection(ctx, "invalid-host.local")
		if err == nil {
			t.Error("Expected error for invalid host check")
		}
	})
}
