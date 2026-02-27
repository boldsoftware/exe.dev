package e1e

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestDesiredStateSync verifies that the desired-state syncer writes
// cgroup files (e.g., cpu.max) for running VMs.
func TestDesiredStateSync(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)
	e1eTestsOnlyRunOnce(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.Disconnect()

	// Create a box. Default CPUs=2 in test env, which sets AllocatedCpus=2
	// in the DB and produces cpu.max = "200000 100000".
	bn := newBox(t, pty)
	defer pty.deleteBox(bn)
	waitForSSH(t, bn, keyFile)

	// Find the container ID via exelet client
	ctx := Env.context(t)
	exeletClient := Env.servers.Exelets[0].Client()
	containerID := instanceIDByName(t, ctx, exeletClient, bn)

	// Poll for the cpu.max cgroup file to be updated by the syncer.
	// The resource manager creates the cgroup scope (with kernel default
	// "max 100000"), then the syncer writes the desired cpu.max value
	// on its next poll (interval=1s in tests).
	//
	// We don't know the exact user_id (group), so glob for it:
	// /sys/fs/cgroup/exelet.slice/*/vm-{containerID}.scope/cpu.max
	exelet := Env.servers.Exelets[0]
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
}
