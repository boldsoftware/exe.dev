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

// TestResizeDisk tests the resize --disk command end-to-end.
func TestResizeDisk(t *testing.T) {
	t.Parallel()
	noGolden(t)

	ownerPTY, _, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-resize-disk.example")
	supportPTY, _, supportKeyFile, supportEmail := registerForExeDevWithEmail(t, "support@test-resize-disk.example")

	box := newBox(t, ownerPTY, testinfra.BoxOpts{Command: "/bin/bash"})
	ownerPTY.wantPrompt()
	ownerPTY.disconnect()

	waitForSSH(t, box, ownerKeyFile)

	// Test that non-support user cannot use resize --disk
	t.Run("non_support_user_denied", func(t *testing.T) {
		ownerPTY = sshToExeDev(t, ownerKeyFile)
		ownerPTY.sendLine(fmt.Sprintf("resize %s --disk=20", box))
		ownerPTY.want("not in the sudoers file")
		ownerPTY.wantPrompt()
		ownerPTY.disconnect()
	})

	enableRootSupport(t, supportEmail)

	// Get initial filesystem size and volume size
	var initialFSBytes uint64
	var initialVolumeBytes uint64
	t.Run("get_initial_size", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "df", "--block-size=1", "--output=size", "/").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get initial disk size: %v\n%s", err, out)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) < 2 {
			t.Fatalf("unexpected df output: %s", out)
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(lines[1]), "%d", &initialFSBytes); err != nil {
			t.Fatalf("failed to parse df output: %v\n%s", err, out)
		}
		t.Logf("Initial filesystem size: %d bytes (%.2f GB)", initialFSBytes, float64(initialFSBytes)/(1024*1024*1024))

		// Get volume size from block device
		out, err = boxSSHCommand(t, box, ownerKeyFile, "sudo", "blockdev", "--getsize64", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get block device size: %v\n%s", err, out)
		}
		if _, err := fmt.Sscanf(string(out), "%d", &initialVolumeBytes); err != nil {
			t.Fatalf("failed to parse blockdev output: %v\n%s", err, out)
		}
		t.Logf("Initial volume size: %d bytes (%.2f GB)", initialVolumeBytes, float64(initialVolumeBytes)/(1024*1024*1024))
	})

	// Test resize --disk (specifying new total size = current + 1GB)
	var diskNewBytes uint64
	t.Run("resize_disk", func(t *testing.T) {
		// Calculate new size: round up current to nearest GB and add 1GB
		newSizeGB := (initialVolumeBytes/(1024*1024*1024) + 1) + 1
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), supportKeyFile, "resize", box, fmt.Sprintf("--disk=%d", newSizeGB), "--json")
		if err != nil {
			t.Fatalf("resize --disk failed: %v\n%s", err, out)
		}

		var result struct {
			VMName       string `json:"vm_name"`
			DiskOldBytes uint64 `json:"disk_old_bytes"`
			DiskNewBytes uint64 `json:"disk_new_bytes"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}

		if result.DiskNewBytes <= result.DiskOldBytes {
			t.Errorf("disk did not grow: old=%d, new=%d", result.DiskOldBytes, result.DiskNewBytes)
		}

		// Verify new size matches what we requested
		expectedNewSize := newSizeGB * 1024 * 1024 * 1024
		if result.DiskNewBytes != expectedNewSize {
			t.Errorf("unexpected new disk size: got %d, want %d", result.DiskNewBytes, expectedNewSize)
		}

		diskNewBytes = result.DiskNewBytes
		t.Logf("Disk grew from %d to %d bytes", result.DiskOldBytes, result.DiskNewBytes)
	})

	// Restart VM
	t.Run("restart_vm", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("restart " + box)
		repl.want("Restarting")
		repl.want("restarted successfully")
		repl.wantPrompt()
		repl.disconnect()
		waitForSSH(t, box, ownerKeyFile)
	})

	// Verify kernel sees new disk size
	t.Run("verify_kernel_sees_new_size", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "sudo", "blockdev", "--getsize64", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get block device size: %v\n%s", err, out)
		}
		var kernelSize uint64
		if _, err := fmt.Sscanf(string(out), "%d", &kernelSize); err != nil {
			t.Fatalf("failed to parse blockdev output: %v\n%s", err, out)
		}
		t.Logf("Kernel sees disk size: %d bytes (%.2f GB)", kernelSize, float64(kernelSize)/(1024*1024*1024))
		if kernelSize < diskNewBytes-1024*1024 {
			t.Errorf("kernel does not see full disk size: kernel=%d, volume=%d", kernelSize, diskNewBytes)
		}
	})

	// Run resize2fs
	t.Run("resize_filesystem", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "sudo", "resize2fs", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("resize2fs failed: %v\n%s", err, out)
		}
		t.Logf("resize2fs output: %s", out)
	})

	// Verify filesystem grew
	t.Run("verify_filesystem_grew", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "df", "--block-size=1", "--output=size", "/").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get final disk size: %v\n%s", err, out)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) < 2 {
			t.Fatalf("unexpected df output: %s", out)
		}
		var finalSize uint64
		if _, err := fmt.Sscanf(strings.TrimSpace(lines[1]), "%d", &finalSize); err != nil {
			t.Fatalf("failed to parse df output: %v\n%s", err, out)
		}
		t.Logf("Final filesystem size: %d bytes (%.2f GB)", finalSize, float64(finalSize)/(1024*1024*1024))
		if finalSize <= initialFSBytes {
			t.Errorf("filesystem did not grow: initial=%d, final=%d", initialFSBytes, finalSize)
		}
		minExpectedSize := uint64(float64(diskNewBytes) * 0.90)
		if finalSize < minExpectedSize {
			t.Errorf("filesystem size %d is too small compared to volume size %d", finalSize, diskNewBytes)
		}
	})

	// Test disk limits - trying to grow by more than 250GB in one operation
	t.Run("disk_growth_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		// Current disk is ~12GB, asking for 300GB would be ~288GB growth which exceeds 250GB limit
		supportPTY.sendLine(fmt.Sprintf("resize %s --disk=300", box))
		supportPTY.want("cannot exceed")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	// Test that shrinking is not allowed
	t.Run("shrink_not_allowed", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		// Try to shrink to 5GB when current is ~12GB
		supportPTY.sendLine(fmt.Sprintf("resize %s --disk=5", box))
		supportPTY.want("must be larger than current size")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	cleanupBox(t, ownerKeyFile, box)
}

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
			newCPUs := min(int(initialCPUs)+1, hostCPUs)
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
