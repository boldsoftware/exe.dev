package e1e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestIOThrottle verifies that the throttle-vm --io flag stores an IO
// bandwidth override and that the desired-state syncer resolves the ~
// device placeholder to a real MAJ:MIN and writes the io.max cgroup file.
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

	// Apply symmetric IO throttle: 10 MB/s read and write.
	// 10M = 10 * 1024 * 1024 = 10485760 bytes/s
	pty.SendLine(fmt.Sprintf("throttle-vm %s --io=10M", bn))
	pty.Want("Updated cgroup overrides")
	pty.Want("io.max")
	pty.WantPrompt()

	// Verify --show reports the override with the ~ placeholder.
	pty.SendLine(fmt.Sprintf("throttle-vm %s --show", bn))
	pty.Want("io.max")
	pty.Want("rbps=10485760")
	pty.Want("wbps=10485760")
	pty.WantPrompt()

	// Wait for the desired-state syncer to reconcile the io.max cgroup file.
	// The syncer replaces the ~ placeholder with the actual MAJ:MIN device.
	exelet := Env.servers.Exelets[0]
	deadline := time.Now().Add(30 * time.Second)
	var ioMax string
	for time.Now().Before(deadline) {
		out, err := exelet.Exec(ctx, fmt.Sprintf(
			"cat /sys/fs/cgroup/exelet.slice/*/vm-%s.scope/io.max 2>/dev/null",
			containerID))
		if err == nil && len(out) > 0 {
			ioMax = strings.TrimSpace(string(out))
			// The file should contain a real MAJ:MIN (not ~) with rbps and wbps values.
			if strings.Contains(ioMax, "rbps=10485760") &&
				strings.Contains(ioMax, "wbps=10485760") &&
				!strings.Contains(ioMax, "~") {
				// Verify the device field looks like MAJ:MIN (e.g. "8:0").
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
}

// TestIOThrottleAsymmetric verifies that --io-read and --io-write set
// different read/write bandwidth limits.
func TestIOThrottleAsymmetric(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, email := registerForExeDev(t)
	defer pty.Disconnect()
	enableRootSupport(t, email)

	bn := newBox(t, pty)
	defer pty.deleteBox(bn)
	waitForSSH(t, bn, keyFile)

	ctx := Env.context(t)
	exeletClient := Env.servers.Exelets[0].Client()
	containerID := instanceIDByName(t, ctx, exeletClient, bn)

	// Apply asymmetric IO throttle: 50M read, 20M write.
	// 50M = 52428800, 20M = 20971520
	pty.SendLine(fmt.Sprintf("throttle-vm %s --io-read=50M --io-write=20M", bn))
	pty.Want("Updated cgroup overrides")
	pty.Want("io.max")
	pty.WantPrompt()

	// Verify --show reports correct asymmetric values.
	pty.SendLine(fmt.Sprintf("throttle-vm %s --show", bn))
	pty.Want("io.max")
	pty.Want("rbps=52428800")
	pty.Want("wbps=20971520")
	pty.WantPrompt()

	// Wait for the syncer to write io.max with resolved device.
	exelet := Env.servers.Exelets[0]
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
}

// TestIOThrottleClear verifies that --io=clear removes the IO override
// and the syncer writes "max" values (effectively unlimited).
func TestIOThrottleClear(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, email := registerForExeDev(t)
	defer pty.Disconnect()
	enableRootSupport(t, email)

	bn := newBox(t, pty)
	defer pty.deleteBox(bn)
	waitForSSH(t, bn, keyFile)

	// First set a throttle.
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
}
