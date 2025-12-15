//go:build linux

package seccomp

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestBlockKillSelf(t *testing.T) {
	// This test must run in a subprocess because seccomp filters are inherited
	// by child processes and cannot be removed once installed.
	if os.Getenv("TEST_SECCOMP_SUBPROCESS") == "1" {
		runSeccompTestSubprocess(t)
		return
	}

	// Re-exec this test in a subprocess
	cmd := exec.Command(os.Args[0], "-test.run=TestBlockKillSelf$", "-test.v")
	cmd.Env = append(os.Environ(), "TEST_SECCOMP_SUBPROCESS=1")
	output, err := cmd.CombinedOutput()
	t.Logf("Subprocess output:\n%s", output)
	if err != nil {
		t.Fatalf("Subprocess failed: %v", err)
	}
}

func runSeccompTestSubprocess(t *testing.T) {
	pid := os.Getpid()
	t.Logf("Running seccomp test in subprocess with PID %d", pid)

	// Install the seccomp filter
	if err := BlockKillSelf(); err != nil {
		t.Fatalf("BlockKillSelf failed: %v", err)
	}
	t.Log("Seccomp filter installed")

	// Now spawn a child process that tries to kill us
	// We use a shell command because we need a separate process
	cmd := exec.Command("sh", "-c", "kill -TERM "+strconv.Itoa(pid)+" 2>&1; echo exit_code=$?")
	output, _ := cmd.CombinedOutput()
	t.Logf("Kill attempt output: %s", output)

	// The kill should have failed with EPERM
	// If we're still alive, the seccomp filter worked!
	t.Log("We survived the kill attempt!")

	// Also verify we can still kill other things (like a sleep process)
	sleepCmd := exec.Command("sleep", "60")
	if err := sleepCmd.Start(); err != nil {
		t.Fatalf("Failed to start sleep: %v", err)
	}
	sleepPid := sleepCmd.Process.Pid

	// Kill the sleep process - this should work
	if err := syscall.Kill(sleepPid, syscall.SIGTERM); err != nil {
		t.Errorf("Failed to kill sleep process: %v", err)
	}
	sleepCmd.Wait()
	t.Logf("Successfully killed sleep process %d", sleepPid)

	// Try to kill ourselves directly - this should fail
	err := syscall.Kill(pid, syscall.SIGTERM)
	if err == nil {
		t.Fatal("Expected kill of self to fail, but it succeeded")
	}
	if err != syscall.EPERM {
		t.Fatalf("Expected EPERM, got %v", err)
	}
	t.Logf("Kill of self correctly returned EPERM")

	// Try to kill using negative PID (process group kill) - this should also fail
	err = syscall.Kill(-pid, syscall.SIGTERM)
	if err == nil {
		t.Fatal("Expected kill of -self to fail, but it succeeded")
	}
	if err != syscall.EPERM {
		t.Fatalf("Expected EPERM for negative PID, got %v", err)
	}
	t.Logf("Kill of -self correctly returned EPERM")
}

func TestBlockKillSelf_ChildCannotKillParent(t *testing.T) {
	// This is the main test: verify that after installing seccomp,
	// a child process cannot kill the parent (shelley) process.
	if os.Getenv("TEST_SECCOMP_CHILD_SUBPROCESS") == "1" {
		runChildCannotKillParentSubprocess(t)
		return
	}

	// Re-exec this test in a subprocess
	cmd := exec.Command(os.Args[0], "-test.run=TestBlockKillSelf_ChildCannotKillParent$", "-test.v")
	cmd.Env = append(os.Environ(), "TEST_SECCOMP_CHILD_SUBPROCESS=1")
	output, err := cmd.CombinedOutput()
	t.Logf("Subprocess output:\n%s", output)
	if err != nil {
		t.Fatalf("Subprocess failed: %v", err)
	}
}

func runChildCannotKillParentSubprocess(t *testing.T) {
	pid := os.Getpid()
	t.Logf("Parent process PID: %d", pid)

	// Install the seccomp filter BEFORE spawning children
	if err := BlockKillSelf(); err != nil {
		t.Fatalf("BlockKillSelf failed: %v", err)
	}
	t.Log("Seccomp filter installed in parent")

	// Spawn a child process that tries to kill the parent using positive PID
	// The child inherits the seccomp filter, which blocks kill(parent_pid, ...)
	script := fmt.Sprintf(`
echo "Child attempting to kill parent PID %d"
kill -TERM %d 2>&1
result=$?
echo "kill exit code: $result"
if [ $result -ne 0 ]; then
    echo "SUCCESS: kill was blocked"
    exit 0
else
    echo "FAILURE: kill succeeded (parent should be dead)"
    exit 1
fi
`, pid, pid)

	cmd := exec.Command("sh", "-c", script)
	output, err := cmd.CombinedOutput()
	t.Logf("Child output (positive PID):\n%s", output)

	// Check that the child reported success (kill was blocked)
	if err != nil {
		t.Fatalf("Child process reported failure (positive PID): %v", err)
	}

	// Verify the output contains our success message
	if !strings.Contains(string(output), "SUCCESS: kill was blocked") {
		t.Fatalf("Expected success message in output (positive PID)")
	}

	// We're still alive!
	t.Logf("Parent (PID %d) survived child's positive PID kill attempt", pid)

	// Now test with negative PID (process group kill)
	negScript := fmt.Sprintf(`
echo "Child attempting to kill parent process group with PID -%d"
kill -TERM -%d 2>&1
result=$?
echo "kill exit code: $result"
if [ $result -ne 0 ]; then
    echo "SUCCESS: kill -pid was blocked"
    exit 0
else
    echo "FAILURE: kill -pid succeeded (parent should be dead)"
    exit 1
fi
`, pid, pid)

	negCmd := exec.Command("sh", "-c", negScript)
	negOutput, negErr := negCmd.CombinedOutput()
	t.Logf("Child output (negative PID):\n%s", negOutput)

	// Check that the child reported success (kill was blocked)
	if negErr != nil {
		t.Fatalf("Child process reported failure (negative PID): %v", negErr)
	}

	// Verify the output contains our success message
	if !strings.Contains(string(negOutput), "SUCCESS: kill -pid was blocked") {
		t.Fatalf("Expected success message in output (negative PID)")
	}

	// We're still alive!
	t.Logf("Parent (PID %d) survived child's negative PID kill attempt", pid)
}
