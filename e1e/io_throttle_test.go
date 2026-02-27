package e1e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestIOThrottle tests the desired-state cgroup syncer and IO throttle functionality
// using a single VM. It verifies:
//   - The syncer writes cpu.max for running VMs (desired state sync).
//   - Symmetric IO throttle (--io flag).
//   - Asymmetric IO throttle (--io-read/--io-write flags).
//   - Clearing an IO throttle (--io=clear).
func TestIOThrottle(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)
	e1eTestsOnlyRunOnce(t)

	// Register a support user (throttle-vm requires exe-sudo).
	pty, _, keyFile, email := registerForExeDev(t)
	defer pty.Disconnect()
	enableRootSupport(t, email)

	// Create a box and wait for SSH to be ready.
	bn := newBox(t, pty)
	defer pty.deleteBox(bn)
	waitForSSH(t, bn, keyFile)

	// Resolve the VM's container ID (needed for the cgroup scope path).
	ctx := Env.context(t)
	exeletClient := Env.servers.Exelets[0].Client()
	containerID := instanceIDByName(t, ctx, exeletClient, bn)
	exelet := Env.servers.Exelets[0]

	// Verify that the desired-state syncer writes cpu.max for running VMs.
	// Default CPUs=2 in test env, which sets AllocatedCpus=2
	// in the DB and produces cpu.max = "200000 100000".
	t.Run("desired_state_cpu_max", func(t *testing.T) {
		expected := "200000 100000" // 2 CPUs × 100000 period
		deadline := time.Now().Add(30 * time.Second)
		var cpuMax string
		for time.Now().Before(deadline) {
			out, err := exelet.Exec(ctx, fmt.Sprintf(
				"cat /sys/fs/cgroup/exelet.slice/*/vm-%s.scope/cpu.max 2>/dev/null",
				containerID))
			if err == nil && len(out) > 0 {
				cpuMax = strings.TrimSpace(string(out))
				if cpuMax == expected {
					return // success
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if cpuMax == "" {
			t.Fatal("cpu.max cgroup file not found within timeout")
		}
		t.Errorf("cpu.max = %q, want %q", cpuMax, expected)
	})

	// Verify symmetric IO throttle: 10 MB/s read and write.
	// 10M = 10 * 1024 * 1024 = 10485760 bytes/s
	t.Run("symmetric", func(t *testing.T) {
		pty.SendLine(fmt.Sprintf("throttle-vm %s --io=10M", bn))
		pty.Want("Updated cgroup overrides")
		pty.Want("io.max")
		pty.WantPrompt()

		pty.SendLine(fmt.Sprintf("throttle-vm %s --show", bn))
		pty.Want("io.max")
		pty.Want("rbps=10485760")
		pty.Want("wbps=10485760")
		pty.WantPrompt()

		// Wait for the desired-state syncer to reconcile the io.max cgroup file.
		// The syncer replaces the ~ placeholder with the actual MAJ:MIN device.
		deadline := time.Now().Add(30 * time.Second)
		var ioMax string
		for time.Now().Before(deadline) {
			out, err := exelet.Exec(ctx, fmt.Sprintf(
				"cat /sys/fs/cgroup/exelet.slice/*/vm-%s.scope/io.max 2>/dev/null",
				containerID))
			if err == nil && len(out) > 0 {
				ioMax = strings.TrimSpace(string(out))
				if strings.Contains(ioMax, "rbps=10485760") &&
					strings.Contains(ioMax, "wbps=10485760") &&
					!strings.Contains(ioMax, "~") {
					fields := strings.Fields(ioMax)
					if len(fields) >= 1 && strings.Contains(fields[0], ":") {
						t.Logf("io.max correctly set: %s", ioMax)
						return // success
					}
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if ioMax == "" {
			t.Fatal("io.max cgroup file not found within timeout")
		}
		t.Fatalf("io.max = %q, want line with MAJ:MIN rbps=10485760 wbps=10485760 (no ~ placeholder)", ioMax)
	})

	// Verify asymmetric IO throttle: 50M read, 20M write.
	// 50M = 52428800, 20M = 20971520
	t.Run("asymmetric", func(t *testing.T) {
		pty.SendLine(fmt.Sprintf("throttle-vm %s --io-read=50M --io-write=20M", bn))
		pty.Want("Updated cgroup overrides")
		pty.Want("io.max")
		pty.WantPrompt()

		pty.SendLine(fmt.Sprintf("throttle-vm %s --show", bn))
		pty.Want("io.max")
		pty.Want("rbps=52428800")
		pty.Want("wbps=20971520")
		pty.WantPrompt()

		deadline := time.Now().Add(30 * time.Second)
		var ioMax string
		for time.Now().Before(deadline) {
			out, err := exelet.Exec(ctx, fmt.Sprintf(
				"cat /sys/fs/cgroup/exelet.slice/*/vm-%s.scope/io.max 2>/dev/null",
				containerID))
			if err == nil && len(out) > 0 {
				ioMax = strings.TrimSpace(string(out))
				if strings.Contains(ioMax, "rbps=52428800") &&
					strings.Contains(ioMax, "wbps=20971520") &&
					!strings.Contains(ioMax, "~") {
					fields := strings.Fields(ioMax)
					if len(fields) >= 1 && strings.Contains(fields[0], ":") {
						t.Logf("io.max correctly set: %s", ioMax)
						return
					}
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		if ioMax == "" {
			t.Fatal("io.max cgroup file not found within timeout")
		}
		t.Fatalf("io.max = %q, want line with MAJ:MIN rbps=52428800 wbps=20971520", ioMax)
	})

	// Verify that --io=clear removes the IO override.
	t.Run("clear", func(t *testing.T) {
		// Set a throttle first (the VM already has one from the previous subtests,
		// but set it explicitly in case subtests are filtered).
		pty.SendLine(fmt.Sprintf("throttle-vm %s --io=10M", bn))
		pty.Want("Updated cgroup overrides")
		pty.WantPrompt()

		// Clear the IO throttle.
		pty.SendLine(fmt.Sprintf("throttle-vm %s --io=clear", bn))
		pty.Want("Updated cgroup overrides")
		pty.Want("rbps=max")
		pty.Want("wbps=max")
		pty.WantPrompt()

		// Verify --show shows the max values.
		pty.SendLine(fmt.Sprintf("throttle-vm %s --show", bn))
		pty.Want("io.max")
		pty.Want("rbps=max")
		pty.Want("wbps=max")
		pty.WantPrompt()
	})
}
