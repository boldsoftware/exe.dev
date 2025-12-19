package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
					if !strings.Contains(outputStr, "-systemd-activation") {
						t.Errorf("Expected serve help to show -systemd-activation flag, got: %s", outputStr)
					}
					return
				}
			}
		}
		// If no error or different error, that's also fine for this basic test
		t.Logf("Serve command output: %s", string(output))
	})
}

func TestSystemdListenerErrors(t *testing.T) {
	// Save original environment
	origPID := os.Getenv("LISTEN_PID")
	origFDs := os.Getenv("LISTEN_FDS")
	defer func() {
		os.Setenv("LISTEN_PID", origPID)
		os.Setenv("LISTEN_FDS", origFDs)
	}()

	t.Run("no LISTEN_FDS", func(t *testing.T) {
		os.Unsetenv("LISTEN_FDS")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListener()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS not set")
		}
		if !strings.Contains(err.Error(), "LISTEN_FDS not set") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("wrong LISTEN_PID", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "1")
		os.Setenv("LISTEN_PID", "99999999") // Unlikely to match our PID
		_, err := systemdListener()
		if err == nil {
			t.Fatal("Expected error when LISTEN_PID doesn't match")
		}
		if !strings.Contains(err.Error(), "does not match current PID") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("invalid LISTEN_FDS", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "notanumber")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListener()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS is invalid")
		}
		if !strings.Contains(err.Error(), "invalid LISTEN_FDS") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})

	t.Run("zero LISTEN_FDS", func(t *testing.T) {
		os.Setenv("LISTEN_FDS", "0")
		os.Unsetenv("LISTEN_PID")
		_, err := systemdListener()
		if err == nil {
			t.Fatal("Expected error when LISTEN_FDS is 0")
		}
		if !strings.Contains(err.Error(), "expected at least 1") {
			t.Errorf("Unexpected error message: %v", err)
		}
	})
}

func TestSystemdListenerIntegration(t *testing.T) {
	// This test simulates what systemd does: create a listener, get the fd,
	// and pass it to a child process via environment and fd inheritance.
	// Since we can't easily test in-process (fd 3 is likely already in use),
	// we test by spawning a subprocess.

	tempDir := t.TempDir()
	binary := filepath.Join(tempDir, "shelley")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to build binary: %v", err)
	}

	// Create a listener on a random port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to create listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// Get the file descriptor from the listener
	tcpListener := listener.(*net.TCPListener)
	file, err := tcpListener.File()
	if err != nil {
		listener.Close()
		t.Fatalf("Failed to get file from listener: %v", err)
	}
	listener.Close() // Close original listener, file still holds the socket

	// Create a temp database for the test
	dbPath := filepath.Join(tempDir, "test.db")

	// Spawn shelley with the file descriptor as fd 3
	// Note: We don't set LISTEN_PID here because we don't know the child PID yet.
	// The systemdListener function handles missing LISTEN_PID gracefully.
	cmd = exec.Command(binary, "-db", dbPath, "serve", "-systemd-activation")
	// Build environment without LISTEN_PID (will be inherited from parent otherwise)
	// and add LISTEN_FDS=1
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "LISTEN_PID=") {
			env = append(env, e)
		}
	}
	env = append(env, "LISTEN_FDS=1")
	cmd.Env = env
	cmd.ExtraFiles = []*os.File{file} // This makes the file fd 3 in the child
	var stderrBuf, stdoutBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	// Start the process
	if err := cmd.Start(); err != nil {
		file.Close()
		t.Fatalf("Failed to start shelley: %v", err)
	}
	file.Close() // Close our copy after child inherits it

	// Wait a bit for the server to start
	time.Sleep(500 * time.Millisecond)

	// Try to connect to the server
	var resp *http.Response
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 10; i++ {
		resp, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/version", port))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Kill the server
	cmd.Process.Kill()
	cmd.Wait()

	if err != nil {
		t.Fatalf("Failed to connect to server: %v\nstdout: %s\nstderr: %s", err, stdoutBuf.String(), stderrBuf.String())
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("Unexpected status code %d, body: %s", resp.StatusCode, body)
	}
}
