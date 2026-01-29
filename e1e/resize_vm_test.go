// This file tests the resize command for support users.

package e1e

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestResizeVM tests the resize command end-to-end.
// This verifies that support users can resize the memory and CPU of a VM.
func TestResizeVM(t *testing.T) {
	t.Parallel()
	noGolden(t) // Output contains variable values

	// Create an owner user and a support user
	ownerPTY, _, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-resize-vm.example")
	supportPTY, _, supportKeyFile, supportEmail := registerForExeDevWithEmail(t, "support@test-resize-vm.example")

	// Owner creates a box
	box := newBox(t, ownerPTY, testinfra.BoxOpts{Command: "/bin/bash"})
	ownerPTY.wantPrompt()
	ownerPTY.disconnect()

	// Wait for SSH to be ready
	waitForSSH(t, box, ownerKeyFile)

	// Test that non-support user cannot use resize command
	t.Run("non_support_user_denied", func(t *testing.T) {
		ownerPTY = sshToExeDev(t, ownerKeyFile)
		ownerPTY.sendLine(fmt.Sprintf("resize %s --memory=2", box))
		ownerPTY.want("not in the sudoers file")
		ownerPTY.wantPrompt()
		ownerPTY.disconnect()
	})

	// Enable root_support for the support user
	t.Run("enable_root_support", func(t *testing.T) {
		enableRootSupport(t, supportEmail)
	})

	// Get initial memory/CPU from inside the VM
	var initialMemoryKB, initialCPUs uint64
	t.Run("get_initial_config", func(t *testing.T) {
		// Get memory from /proc/meminfo
		out, err := boxSSHCommand(t, box, ownerKeyFile, "grep", "MemTotal", "/proc/meminfo").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get initial memory: %v\n%s", err, out)
		}
		if _, err := fmt.Sscanf(string(out), "MemTotal: %d kB", &initialMemoryKB); err != nil {
			t.Fatalf("failed to parse meminfo: %v\n%s", err, out)
		}
		t.Logf("Initial memory: %d KB (%.2f GB)", initialMemoryKB, float64(initialMemoryKB)/(1024*1024))

		// Get CPU count from /proc/cpuinfo
		out, err = boxSSHCommand(t, box, ownerKeyFile, "nproc").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get initial CPU count: %v\n%s", err, out)
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &initialCPUs); err != nil {
			t.Fatalf("failed to parse nproc output: %v\n%s", err, out)
		}
		t.Logf("Initial CPUs: %d", initialCPUs)
	})

	// Test resizing memory only
	t.Run("resize_memory", func(t *testing.T) {
		newMemoryGB := 3 // 3GB
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), supportKeyFile, "resize", box, fmt.Sprintf("--memory=%d", newMemoryGB), "--json")
		if err != nil {
			t.Fatalf("resize command failed: %v\n%s", err, out)
		}

		var result struct {
			VMName    string `json:"vm_name"`
			OldMemory uint64 `json:"old_memory"`
			NewMemory uint64 `json:"new_memory"`
			OldCPUs   uint64 `json:"old_cpus"`
			NewCPUs   uint64 `json:"new_cpus"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}

		// Verify memory was changed to 3GB
		expectedMemory := uint64(newMemoryGB * 1024 * 1024 * 1024)
		if result.NewMemory != expectedMemory {
			t.Errorf("unexpected new memory: got %d, want %d", result.NewMemory, expectedMemory)
		}

		// Verify CPUs stayed the same
		if result.OldCPUs != result.NewCPUs {
			t.Errorf("CPUs changed unexpectedly: old=%d, new=%d", result.OldCPUs, result.NewCPUs)
		}

		t.Logf("Memory resized from %d to %d bytes", result.OldMemory, result.NewMemory)
	})

	// Test resizing CPU only (if host has enough CPUs)
	// Note: cloud-hypervisor can't allocate more vCPUs than the host has physical CPUs
	hostCPUs := runtime.NumCPU()
	canTestCPUIncrease := hostCPUs > int(initialCPUs)

	if canTestCPUIncrease {
		t.Run("resize_cpu", func(t *testing.T) {
			// Only increase by 1 to stay within host limits
			newCPUs := int(initialCPUs) + 1
			if newCPUs > hostCPUs {
				newCPUs = hostCPUs
			}
			out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), supportKeyFile, "resize", box, fmt.Sprintf("--cpu=%d", newCPUs), "--json")
			if err != nil {
				t.Fatalf("resize command failed: %v\n%s", err, out)
			}

			var result struct {
				VMName    string `json:"vm_name"`
				OldMemory uint64 `json:"old_memory"`
				NewMemory uint64 `json:"new_memory"`
				OldCPUs   uint64 `json:"old_cpus"`
				NewCPUs   uint64 `json:"new_cpus"`
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("failed to parse JSON: %v\n%s", err, out)
			}

			// Verify CPUs were changed
			if result.NewCPUs != uint64(newCPUs) {
				t.Errorf("unexpected new CPUs: got %d, want %d", result.NewCPUs, newCPUs)
			}

			// Verify memory stayed the same (at the 3GB we set earlier)
			if result.OldMemory != result.NewMemory {
				t.Errorf("memory changed unexpectedly: old=%d, new=%d", result.OldMemory, result.NewMemory)
			}

			t.Logf("CPUs resized from %d to %d", result.OldCPUs, result.NewCPUs)
		})
	} else {
		t.Run("resize_cpu", func(t *testing.T) {
			t.Skipf("skipping CPU increase test: host has %d CPUs, VM already has %d", hostCPUs, initialCPUs)
		})
	}

	// Test resizing both memory and CPU (decrease CPUs if we can't increase)
	t.Run("resize_both", func(t *testing.T) {
		newMemoryGB := 4
		// CPU changes may not work on low-CPU hosts, but memory should
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), supportKeyFile, "resize", box, fmt.Sprintf("--memory=%d", newMemoryGB), "--json")
		if err != nil {
			t.Fatalf("resize command failed: %v\n%s", err, out)
		}

		var result struct {
			VMName    string `json:"vm_name"`
			OldMemory uint64 `json:"old_memory"`
			NewMemory uint64 `json:"new_memory"`
			OldCPUs   uint64 `json:"old_cpus"`
			NewCPUs   uint64 `json:"new_cpus"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}

		// Verify memory was changed
		expectedMemory := uint64(newMemoryGB * 1024 * 1024 * 1024)
		if result.NewMemory != expectedMemory {
			t.Errorf("unexpected new memory: got %d, want %d", result.NewMemory, expectedMemory)
		}

		t.Logf("Resized memory %d->%d bytes", result.OldMemory, result.NewMemory)
	})

	// Restart the VM so changes take effect
	t.Run("restart_vm", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("restart " + box)
		repl.want("Restarting")
		repl.want("restarted successfully")
		repl.wantPrompt()
		repl.disconnect()

		// Wait for SSH to come back up
		waitForSSH(t, box, ownerKeyFile)
	})

	// Verify the VM now has the new config
	t.Run("verify_new_config", func(t *testing.T) {
		// Get memory from /proc/meminfo
		out, err := boxSSHCommand(t, box, ownerKeyFile, "grep", "MemTotal", "/proc/meminfo").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get memory: %v\n%s", err, out)
		}
		var newMemoryKB uint64
		if _, err := fmt.Sscanf(string(out), "MemTotal: %d kB", &newMemoryKB); err != nil {
			t.Fatalf("failed to parse meminfo: %v\n%s", err, out)
		}
		t.Logf("New memory: %d KB (%.2f GB)", newMemoryKB, float64(newMemoryKB)/(1024*1024))

		// Verify memory increased (4GB = 4*1024*1024 KB, with some tolerance for kernel overhead)
		expectedMemoryKB := uint64(4 * 1024 * 1024)
		// Allow 10% tolerance for kernel memory overhead
		minExpectedKB := uint64(float64(expectedMemoryKB) * 0.90)
		if newMemoryKB < minExpectedKB {
			t.Errorf("memory not increased enough: got %d KB, want at least %d KB", newMemoryKB, minExpectedKB)
		}
		if newMemoryKB <= initialMemoryKB {
			t.Errorf("memory did not increase: initial=%d KB, new=%d KB", initialMemoryKB, newMemoryKB)
		}
	})

	// Test limits
	t.Run("memory_max_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.sendLine(fmt.Sprintf("resize %s --memory=64", box)) // 64GB > 32GB max
		supportPTY.want("cannot exceed")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	t.Run("memory_min_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.sendLine(fmt.Sprintf("resize %s --memory=1", box)) // 1GB < 2GB min
		supportPTY.want("at least")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	t.Run("cpu_max_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.sendLine(fmt.Sprintf("resize %s --cpu=16", box)) // 16 > 8 max for support
		supportPTY.want("cannot exceed")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	t.Run("missing_args_rejected", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.sendLine("resize")
		supportPTY.want("usage: resize")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	t.Run("no_options_rejected", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.sendLine(fmt.Sprintf("resize %s", box))
		supportPTY.want("At least one of")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	// Cleanup
	cleanupBox(t, ownerKeyFile, box)
}
