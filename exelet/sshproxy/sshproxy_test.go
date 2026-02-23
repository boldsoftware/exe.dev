package sshproxy

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestStartAdoptsExistingProcess verifies that Start() adopts an existing socat
// process listening on the same port instead of spawning a duplicate.
func TestStartAdoptsExistingProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("test requires Linux /proc filesystem")
	}
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not found in PATH, skipping test")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	instanceDir := t.TempDir()
	port := 29876 // Below Linux ephemeral range (32768-60999)
	targetIP := "127.0.0.1"

	// Start socat manually (simulating a socat that survived exelet restart)
	cmd := exec.Command("socat",
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", port),
		fmt.Sprintf("TCP:%s:22,connect-timeout=3", targetIP))
	cmd.SysProcAttr = nil // Run in same process group for easier cleanup
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start manual socat: %v", err)
	}
	manualPID := cmd.Process.Pid
	defer cmd.Process.Kill()

	// Wait for socat to start listening
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pid, err := findListeningPID(port); err == nil && pid > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Now create SSHProxy and call Start() - it should adopt, not create duplicate
	proxy := NewSSHProxy("test-instance", port, targetIP, instanceDir, "", log)
	if err := proxy.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Verify that proxy adopted the existing PID
	if proxy.PID != manualPID {
		t.Errorf("expected proxy to adopt PID %d, but got PID %d", manualPID, proxy.PID)
	}

	// Count socat processes on this port to verify no duplicate
	count := countSocatProcesses(t, port)
	if count != 1 {
		t.Errorf("expected exactly 1 socat process on port %d, found %d", port, count)
	}
}

// countSocatProcesses counts how many socat processes are listening on the given port
func countSocatProcesses(t *testing.T, port int) int {
	t.Helper()
	// Use pgrep to find socat processes, then filter by port
	cmd := exec.Command("sh", "-c",
		fmt.Sprintf("ps aux | grep 'socat.*TCP-LISTEN:%d' | grep -v grep | wc -l", port))
	out, err := cmd.Output()
	if err != nil {
		t.Logf("warning: failed to count socat processes: %v", err)
		return -1
	}
	count := 0
	for _, b := range out {
		if b >= '0' && b <= '9' {
			count = count*10 + int(b-'0')
		}
	}
	return count
}
