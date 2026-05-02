package e1e

import (
	"testing"
	"time"

	api "exe.dev/pkg/api/exe/resource/v1"
)

// TestTopCommand tests the hidden "top" command end-to-end.
// It creates a VM, verifies the exelet reports usage via ListVMUsage,
// and exercises the interactive TUI over the SSH PTY.
func TestTopCommand(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	boxName := newBox(t, pty)
	waitForSSH(t, boxName, keyFile)

	// Subtests share the same box to avoid extra VM creation overhead.

	// 1. Verify the exelet reports VM usage via ListVMUsage RPC.
	//    This confirms the data pipeline that the top command relies on.
	t.Run("exelet_reports_vm_usage", func(t *testing.T) {
		exeletClient := Env.servers.Exelets[0].Client()
		ctx := Env.context(t)

		// The resource manager polls on an interval, so we may need to
		// retry until the VM appears with all expected fields populated.
		// Cgroup stats (MemoryBytes, DiskLogicalBytes) and config-derived
		// fields (CPUs, MemCapacityBytes) can arrive on different polls:
		// the first poll may register the VM with cgroup stats before
		// VMConfig is available, leaving CPUs/MemCapacityBytes at 0.
		deadline := time.Now().Add(90 * time.Second)
		var found bool
		for time.Now().Before(deadline) {
			stream, err := exeletClient.ListVMUsage(ctx, &api.ListVMUsageRequest{})
			if err != nil {
				t.Fatalf("ListVMUsage RPC failed: %v", err)
			}
			for {
				resp, err := stream.Recv()
				if err != nil {
					break
				}
				u := resp.GetUsage()
				if u == nil {
					continue
				}
				if u.Name != boxName {
					continue
				}
				// Wait until both cgroup stats and config-derived fields
				// have been collected.
				if u.MemoryBytes == 0 || u.DiskLogicalBytes == 0 || u.CPUs == 0 || u.MemCapacityBytes == 0 {
					continue
				}
				found = true
				t.Logf("VM %s: cpu=%.1f%% mem=%d swap=%d disk=%d disk_logical=%d cap=%d mem_cap=%d cpus=%d",
					boxName, u.CpuPercent, u.MemoryBytes, u.SwapBytes,
					u.DiskBytes, u.DiskLogicalBytes, u.DiskCapacityBytes,
					u.MemCapacityBytes, u.CPUs)
				if u.DiskLogicalBytes < u.DiskBytes {
					t.Errorf("DiskLogicalBytes (%d) should be >= DiskBytes (%d)", u.DiskLogicalBytes, u.DiskBytes)
				}
				break
			}
			if found {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !found {
			t.Fatalf("ListVMUsage never reported non-zero usage for %s within deadline", boxName)
		}
	})

	// 2. Run "top" interactively and verify it shows the VM.
	t.Run("interactive_top", func(t *testing.T) {
		pty.SendLine("top")

		// The TUI should display the header and the VM row.
		// The View truncates names to 19 chars, so match a prefix.
		pty.Want("exe top")
		namePrefix := boxName
		if len(namePrefix) > 15 {
			namePrefix = namePrefix[:15]
		}
		pty.Want(namePrefix)

		// Quit the TUI by sending 'q'.
		pty.Send("q")
		pty.WantPrompt()
	})

	// Clean up with a fresh SSH connection. The original PTY may have
	// residual escape sequences from the alt-screen TUI.
	cleanupBox(t, keyFile, boxName)
}
