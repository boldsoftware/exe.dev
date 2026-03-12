package sshproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestStartAdoptsExistingProcess verifies that Start() adopts an existing socat
// process listening on the same port instead of spawning a duplicate.
func TestStartAdoptsExistingProcess(t *testing.T) {
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not found in PATH, skipping test")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	instanceDir := t.TempDir()
	// Allocate a dynamic port to avoid hardcoded port conflicts in CI.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	targetIP := "127.0.0.1"

	// Start socat manually (simulating a socat that survived exelet restart)
	cmd := exec.Command("socat",
		fmt.Sprintf("TCP-LISTEN:%d,fork", port),
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
	proxy := newSocatSSHProxy("test-instance", port, targetIP, instanceDir, "", log)
	if err := proxy.start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Verify that proxy adopted the existing PID
	if proxy.pid != manualPID {
		t.Errorf("expected proxy to adopt PID %d, but got PID %d", manualPID, proxy.pid)
	}

	// Count socat processes on this port to verify no duplicate
	count := countSocatProcesses(t, port)
	if count != 1 {
		t.Errorf("expected exactly 1 socat process on port %d, found %d", port, count)
	}
}

// TestStartNoDuplicateOnDoubleStart verifies that calling start() twice on the
// same port does not create a duplicate socat process. The second call should
// adopt the existing process.
func TestStartNoDuplicateOnDoubleStart(t *testing.T) {
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not found in PATH, skipping test")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	instanceDir := t.TempDir()
	port := allocatePort(t)
	targetIP := "127.0.0.1"

	// First start — should spawn socat.
	proxy1 := newSocatSSHProxy("test-instance", port, targetIP, instanceDir, "", log)
	if err := proxy1.start(); err != nil {
		t.Fatalf("first start() failed: %v", err)
	}
	defer killPID(proxy1.pid)
	firstPID := proxy1.pid

	// Second start — should adopt the existing process, not spawn a new one.
	proxy2 := newSocatSSHProxy("test-instance", port, targetIP, instanceDir, "", log)
	if err := proxy2.start(); err != nil {
		t.Fatalf("second start() failed: %v", err)
	}

	if proxy2.pid != firstPID {
		defer killPID(proxy2.pid)
		t.Errorf("second start() spawned new PID %d instead of adopting existing PID %d", proxy2.pid, firstPID)
	}

	count := countSocatProcesses(t, port)
	if count != 1 {
		t.Errorf("expected exactly 1 socat process on port %d, found %d", port, count)
	}
}

// TestRecoverProxiesCleansDuplicates verifies that RecoverProxies kills duplicate
// socat processes left over from previous exelet versions that used reuseaddr.
func TestRecoverProxiesCleansDuplicates(t *testing.T) {
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not found in PATH, skipping test")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dataDir := t.TempDir()
	port := allocatePort(t)
	targetIP := "127.0.0.1"
	instanceID := "test-instance-dup"
	instanceDir := filepath.Join(dataDir, "instances", instanceID)
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		t.Fatalf("failed to create instance dir: %v", err)
	}

	// Simulate the old bug: spawn 3 socat processes with reuseaddr on the
	// same port, just like what happens in prod. Start the first one and
	// wait for it to bind before spawning the rest, to avoid racing
	// against the kernel releasing the port from allocatePort.
	var cmds []*exec.Cmd
	var pids []int
	for i := 0; i < 3; i++ {
		cmd := exec.Command("socat",
			fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", port),
			fmt.Sprintf("TCP:%s:22,connect-timeout=3", targetIP))
		if err := cmd.Start(); err != nil {
			t.Fatalf("failed to start socat #%d: %v", i, err)
		}
		cmds = append(cmds, cmd)
		pids = append(pids, cmd.Process.Pid)
		if i == 0 {
			// Wait for the first socat to bind before spawning duplicates.
			waitForListener(t, port)
		}
	}
	defer func() {
		for _, cmd := range cmds {
			cmd.Process.Kill()
		}
	}()

	// Give the remaining socats time to bind.
	time.Sleep(100 * time.Millisecond)

	// Write metadata pointing to the first PID so recovery adopts it.
	metadata := proxyMetadata{
		PID:       pids[0],
		Port:      port,
		TargetIP:  targetIP,
		StartedAt: time.Now(),
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(instanceDir, "process-sshproxy.json"), data, 0o644); err != nil {
		t.Fatalf("failed to write metadata: %v", err)
	}

	// Verify we actually have duplicates before cleanup.
	allPIDs, err := findAllListeningPIDs(port)
	if err != nil {
		t.Fatalf("findAllListeningPIDs failed: %v", err)
	}
	if len(allPIDs) < 2 {
		t.Skipf("OS did not allow duplicate listeners (got %d), skipping", len(allPIDs))
	}
	t.Logf("before recovery: %d listeners on port %d (pids: %v)", len(allPIDs), port, allPIDs)

	// Run RecoverProxies — it should adopt PID[0] and kill the duplicates.
	mgr := NewManager(dataDir, "", log).(*socatManager)
	instances := []*api.Instance{
		{
			ID:      instanceID,
			State:   api.VMState_RUNNING,
			SSHPort: int32(port),
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: targetIP + "/32"},
				},
			},
		},
	}
	if err := mgr.RecoverProxies(context.Background(), instances); err != nil {
		t.Fatalf("RecoverProxies failed: %v", err)
	}

	// Give SIGTERM time to take effect.
	time.Sleep(200 * time.Millisecond)

	afterPIDs, err := findAllListeningPIDs(port)
	if err != nil {
		t.Fatalf("findAllListeningPIDs after recovery failed: %v", err)
	}
	t.Logf("after recovery: %d listeners on port %d (pids: %v)", len(afterPIDs), port, afterPIDs)
	if len(afterPIDs) != 1 {
		t.Errorf("expected 1 listener after recovery, got %d (pids: %v)", len(afterPIDs), afterPIDs)
	}
	if len(afterPIDs) == 1 && afterPIDs[0] != pids[0] {
		t.Errorf("expected adopted PID %d to survive, but surviving PID is %d", pids[0], afterPIDs[0])
	}
}

// TestFindAllListeningPIDs verifies that findAllListeningPIDs discovers multiple
// socat processes bound to the same port via reuseaddr.
func TestFindAllListeningPIDs(t *testing.T) {
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not found in PATH, skipping test")
	}

	port := allocatePort(t)

	// Spawn two socats with reuseaddr on the same port.
	cmd1 := exec.Command("socat",
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", port),
		fmt.Sprintf("TCP:127.0.0.1:22,connect-timeout=3"))
	cmd2 := exec.Command("socat",
		fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", port),
		fmt.Sprintf("TCP:127.0.0.1:22,connect-timeout=3"))

	if err := cmd1.Start(); err != nil {
		t.Fatalf("failed to start socat 1: %v", err)
	}
	defer cmd1.Process.Kill()

	waitForListener(t, port)

	if err := cmd2.Start(); err != nil {
		t.Fatalf("failed to start socat 2: %v", err)
	}
	defer cmd2.Process.Kill()

	// Give the second process time to bind.
	time.Sleep(200 * time.Millisecond)

	pids, err := findAllListeningPIDs(port)
	if err != nil {
		t.Fatalf("findAllListeningPIDs failed: %v", err)
	}
	if len(pids) < 2 {
		t.Skipf("OS did not allow duplicate listeners (got %d PIDs), skipping", len(pids))
	}

	// Verify both PIDs are present.
	pidSet := make(map[int]bool)
	for _, p := range pids {
		pidSet[p] = true
	}
	if !pidSet[cmd1.Process.Pid] {
		t.Errorf("missing PID %d from results %v", cmd1.Process.Pid, pids)
	}
	if !pidSet[cmd2.Process.Pid] {
		t.Errorf("missing PID %d from results %v", cmd2.Process.Pid, pids)
	}
}

// TestStartRejectsReuseaddr verifies that without reuseaddr, a second socat
// cannot bind to the same port (the kernel returns EADDRINUSE). This is the
// mechanism that prevents future duplicates.
func TestStartRejectsReuseaddr(t *testing.T) {
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not found in PATH, skipping test")
	}

	port := allocatePort(t)

	// First socat without reuseaddr.
	cmd1 := exec.Command("socat",
		fmt.Sprintf("TCP-LISTEN:%d,fork", port),
		"TCP:127.0.0.1:22,connect-timeout=3")
	if err := cmd1.Start(); err != nil {
		t.Fatalf("failed to start first socat: %v", err)
	}
	defer cmd1.Process.Kill()

	waitForListener(t, port)

	// Second socat without reuseaddr should fail to bind.
	cmd2 := exec.Command("socat",
		fmt.Sprintf("TCP-LISTEN:%d,fork", port),
		"TCP:127.0.0.1:22,connect-timeout=3")
	if err := cmd2.Start(); err != nil {
		t.Fatalf("failed to start second socat: %v", err)
	}
	defer cmd2.Process.Kill()

	// Wait for it to exit (it should fail quickly with EADDRINUSE).
	done := make(chan error, 1)
	go func() { done <- cmd2.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("second socat should have failed with EADDRINUSE but exited successfully")
		}
		// Expected: socat exits with error because port is taken.
		t.Logf("second socat correctly failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("second socat did not exit within 3s — it may have bound successfully (duplicate)")
	}

	// Confirm only one listener.
	pids, err := findAllListeningPIDs(port)
	if err != nil {
		t.Fatalf("findAllListeningPIDs failed: %v", err)
	}
	if len(pids) != 1 {
		t.Errorf("expected 1 listener, got %d (pids: %v)", len(pids), pids)
	}
}

// allocatePort returns a free port by briefly listening and closing.
func allocatePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// waitForListener polls until something is listening on port or times out.
func waitForListener(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pid, err := findListeningPID(port); err == nil && pid > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for listener on port %d", port)
}

// killPID sends SIGKILL to a process, ignoring errors.
func killPID(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		p.Kill()
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
