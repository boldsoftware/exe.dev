//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"shelley.exe.dev/seccomp"
)

// TestSeccompIntegration tests that the seccomp filter is installed
// automatically and prevents child processes from killing the parent.
func TestSeccompIntegration(t *testing.T) {
	if os.Getenv("TEST_SECCOMP_HELPER") == "1" {
		runSeccompHelper(t)
		return
	}

	// Re-exec this test in a subprocess
	cmd := exec.Command(os.Args[0], "-test.run=TestSeccompIntegration$", "-test.v")
	cmd.Env = append(os.Environ(), "TEST_SECCOMP_HELPER=1")
	output, err := cmd.CombinedOutput()
	t.Logf("Helper output:\n%s", output)
	if err != nil {
		t.Fatalf("Helper failed: %v", err)
	}
}

func runSeccompHelper(t *testing.T) {
	pid := os.Getpid()
	t.Logf("Helper PID: %d", pid)

	// Install seccomp filter (same as -seccomp flag does in main)
	if err := seccomp.BlockKillSelf(); err != nil {
		t.Fatalf("BlockKillSelf failed: %v", err)
	}
	t.Log("Seccomp filter installed")

	// Spawn a child that tries to kill us
	script := fmt.Sprintf("kill -TERM %d 2>&1; echo exit=$?", pid)
	cmd := exec.Command("sh", "-c", script)
	output, _ := cmd.CombinedOutput()
	t.Logf("Kill attempt output: %s", output)

	// Verify the kill was blocked (output should contain "Operation not permitted" or exit=1)
	outStr := string(output)
	if !strings.Contains(outStr, "Operation not permitted") && !strings.Contains(outStr, "exit=1") {
		t.Fatalf("Expected kill to fail with Operation not permitted, got: %s", outStr)
	}

	t.Log("SUCCESS: Child's kill attempt was blocked")
}

// TestSeccompPreservesKillOthers verifies that with seccomp enabled,
// we can still kill other processes (not ourselves).
func TestSeccompPreservesKillOthers(t *testing.T) {
	if os.Getenv("TEST_SECCOMP_KILL_OTHERS") == "1" {
		runSeccompKillOthersHelper(t)
		return
	}

	// Re-exec this test in a subprocess
	cmd := exec.Command(os.Args[0], "-test.run=TestSeccompPreservesKillOthers$", "-test.v")
	cmd.Env = append(os.Environ(), "TEST_SECCOMP_KILL_OTHERS=1")
	output, err := cmd.CombinedOutput()
	t.Logf("Helper output:\n%s", output)
	if err != nil {
		t.Fatalf("Helper failed: %v", err)
	}
}

func runSeccompKillOthersHelper(t *testing.T) {
	// Install seccomp filter
	if err := seccomp.BlockKillSelf(); err != nil {
		t.Fatalf("BlockKillSelf failed: %v", err)
	}
	t.Log("Seccomp filter installed")

	// Start a sleep process
	sleepCmd := exec.Command("sleep", "60")
	if err := sleepCmd.Start(); err != nil {
		t.Fatalf("Failed to start sleep: %v", err)
	}
	sleepPid := sleepCmd.Process.Pid
	t.Logf("Started sleep process with PID %d", sleepPid)

	// Kill the sleep process via a child shell - this should work
	script := fmt.Sprintf("kill -TERM %d 2>&1; echo exit=$?", sleepPid)
	cmd := exec.Command("sh", "-c", script)
	output, _ := cmd.CombinedOutput()
	t.Logf("Kill output: %s", output)

	// Verify the sleep process was killed (exit=0)
	if !strings.Contains(string(output), "exit=0") {
		t.Fatalf("Expected kill to succeed, got: %s", output)
	}

	sleepCmd.Wait()
	t.Log("SUCCESS: Killing other processes still works")
}

// Silence unused import warning
var _ = strconv.Itoa
