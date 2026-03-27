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
		// retry until both the VM appears AND it has non-zero memory
		// (the first poll may register the VM before cgroup stats arrive).
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
				// Match by name; require non-zero memory to confirm
				// the cgroup stats have actually been collected.
				if u.Name == boxName && u.MemoryBytes > 0 {
					found = true
					t.Logf("VM %s: cpu=%.1f%% mem=%d swap=%d disk=%d/%d", boxName, u.CpuPercent, u.MemoryBytes, u.SwapBytes, u.DiskBytes, u.DiskCapacityBytes)
					break
				}
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
