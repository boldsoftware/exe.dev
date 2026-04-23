package e1e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestVCPUPoolEnforcement tests that the account-level cgroup slice enforces
// the plan's MaxCPUs as the cpu.max limit. Individual Small has MaxCPUs=2,
// so the account slice should get cpu.max = "200000 100000" (2 CPUs × 100ms period).
//
// This verifies that cgroup enforcement is plan-based (account pool), not
// derived from per-VM allocated CPUs.
func TestVCPUPoolEnforcement(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)
	e1eTestsOnlyRunOnce(t)

	// Register user with Individual billing (Small tier: MaxCPUs=2).
	pty, _, keyFile, email := registerForExeDevWithEmail(t, "vcpu-pool@test-vcpu-pool.example")
	defer pty.Disconnect()

	// Create a box and wait for SSH.
	bn := newBox(t, pty)
	defer pty.deleteBox(bn)
	waitForSSH(t, bn, keyFile)

	// Resolve the container ID and user ID.
	ctx := Env.context(t)
	exeletClient := Env.servers.Exelets[0].Client()
	containerID := instanceIDByName(t, ctx, exeletClient, bn)
	userID := getUserIDByEmail(t, email)
	exelet := Env.servers.Exelets[0]

	// Verify account-level group slice cpu.max is set to plan MaxCPUs (2).
	// Individual Small: MaxCPUs=2, so cpu.max = "200000 100000".
	// The desired-state syncer writes this to the account slice.
	t.Run("account_slice_cpu_max", func(t *testing.T) {
		expected := "200000 100000" // 2 CPUs × 100000 period
		deadline := time.Now().Add(90 * time.Second)
		var cpuMax string
		for time.Now().Before(deadline) {
			out, err := exelet.Exec(ctx, fmt.Sprintf(
				"cat /sys/fs/cgroup/exelet.slice/%s.slice/cpu.max 2>/dev/null",
				userID))
			if err == nil && len(out) > 0 {
				cpuMax = strings.TrimSpace(string(out))
				if cpuMax == expected {
					t.Logf("account slice cpu.max correctly set to %q", cpuMax)
					return // success
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if cpuMax == "" {
			t.Fatal("account slice cpu.max cgroup file not found within timeout")
		}
		t.Errorf("account slice cpu.max = %q, want %q (plan MaxCPUs=2)", cpuMax, expected)
	})

	// Verify per-VM scope cpu.max is also set correctly.
	// Each VM gets cpu.max = allocated_cpus * period (2 CPUs = "200000 100000").
	t.Run("vm_scope_cpu_max", func(t *testing.T) {
		expected := "200000 100000" // 2 CPUs × 100000 period
		deadline := time.Now().Add(30 * time.Second)
		var cpuMax string
		for time.Now().Before(deadline) {
			out, err := exelet.Exec(ctx, fmt.Sprintf(
				"cat /sys/fs/cgroup/exelet.slice/%s.slice/vm-%s.scope/cpu.max 2>/dev/null",
				userID, containerID))
			if err == nil && len(out) > 0 {
				cpuMax = strings.TrimSpace(string(out))
				if cpuMax == expected {
					t.Logf("VM scope cpu.max correctly set to %q", cpuMax)
					return // success
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if cpuMax == "" {
			t.Fatal("VM scope cpu.max cgroup file not found within timeout")
		}
		t.Errorf("VM scope cpu.max = %q, want %q", cpuMax, expected)
	})
}
