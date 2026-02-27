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

// TestResize tests the resize command end-to-end using a single VM.
// It verifies that support users can resize disk, memory, and CPU,
// and that appropriate limits are enforced.
func TestResize(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-resize.example")
	supportPTY, _, supportKeyFile, supportEmail := registerForExeDevWithEmail(t, "support@test-resize.example")

	box := newBox(t, ownerPTY, testinfra.BoxOpts{Command: "/bin/bash"})
	ownerPTY.WantPrompt()
	ownerPTY.Disconnect()

	waitForSSH(t, box, ownerKeyFile)

	// Test that non-support user cannot use resize
	t.Run("non_support_user_denied", func(t *testing.T) {
		ownerPTY = sshToExeDev(t, ownerKeyFile)
		ownerPTY.SendLine(fmt.Sprintf("resize %s --disk=20", box))
		ownerPTY.Want("not in the sudoers file")
		ownerPTY.WantPrompt()
		ownerPTY.SendLine(fmt.Sprintf("resize %s --memory=2", box))
		ownerPTY.Want("not in the sudoers file")
		ownerPTY.WantPrompt()
		ownerPTY.Disconnect()
	})

	enableRootSupport(t, supportEmail)

	// --- Disk resize ---

	var initialFSBytes uint64
	var initialVolumeBytes uint64
	t.Run("disk_get_initial_size", func(t *testing.T) {
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

		out, err = boxSSHCommand(t, box, ownerKeyFile, "sudo", "blockdev", "--getsize64", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get block device size: %v\n%s", err, out)
		}
		if _, err := fmt.Sscanf(string(out), "%d", &initialVolumeBytes); err != nil {
			t.Fatalf("failed to parse blockdev output: %v\n%s", err, out)
		}
		t.Logf("Initial volume size: %d bytes (%.2f GB)", initialVolumeBytes, float64(initialVolumeBytes)/(1024*1024*1024))
	})

	var diskNewBytes uint64
	t.Run("disk_resize", func(t *testing.T) {
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

		expectedNewSize := newSizeGB * 1024 * 1024 * 1024
		if result.DiskNewBytes != expectedNewSize {
			t.Errorf("unexpected new disk size: got %d, want %d", result.DiskNewBytes, expectedNewSize)
		}

		diskNewBytes = result.DiskNewBytes
		t.Logf("Disk grew from %d to %d bytes", result.DiskOldBytes, result.DiskNewBytes)
	})

	t.Run("disk_restart_vm", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("restart " + box)
		repl.Want("Restarting")
		repl.Want("restarted successfully")
		repl.WantPrompt()
		repl.Disconnect()
		waitForSSH(t, box, ownerKeyFile)
	})

	t.Run("disk_verify_kernel_sees_new_size", func(t *testing.T) {
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

	t.Run("disk_resize_filesystem", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "sudo", "resize2fs", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("resize2fs failed: %v\n%s", err, out)
		}
		t.Logf("resize2fs output: %s", out)
	})

	t.Run("disk_verify_filesystem_grew", func(t *testing.T) {
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

	t.Run("disk_growth_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.SendLine(fmt.Sprintf("resize %s --disk=300", box))
		supportPTY.Want("cannot exceed")
		supportPTY.WantPrompt()
		supportPTY.Disconnect()
	})

	t.Run("disk_shrink_not_allowed", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.SendLine(fmt.Sprintf("resize %s --disk=5", box))
		supportPTY.Want("must be larger than current size")
		supportPTY.WantPrompt()
		supportPTY.Disconnect()
	})

	// --- VM (memory/CPU) resize ---

	var initialMemoryKB, initialCPUs uint64
	t.Run("vm_get_initial_config", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "grep", "MemTotal", "/proc/meminfo").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get initial memory: %v\n%s", err, out)
		}
		if _, err := fmt.Sscanf(string(out), "MemTotal: %d kB", &initialMemoryKB); err != nil {
			t.Fatalf("failed to parse meminfo: %v\n%s", err, out)
		}
		t.Logf("Initial memory: %d KB (%.2f GB)", initialMemoryKB, float64(initialMemoryKB)/(1024*1024))

		out, err = boxSSHCommand(t, box, ownerKeyFile, "nproc").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get initial CPU count: %v\n%s", err, out)
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &initialCPUs); err != nil {
			t.Fatalf("failed to parse nproc output: %v\n%s", err, out)
		}
		t.Logf("Initial CPUs: %d", initialCPUs)
	})

	t.Run("vm_resize_memory", func(t *testing.T) {
		newMemoryGB := 3
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

		expectedMemory := uint64(newMemoryGB * 1024 * 1024 * 1024)
		if result.NewMemory != expectedMemory {
			t.Errorf("unexpected new memory: got %d, want %d", result.NewMemory, expectedMemory)
		}

		if result.OldCPUs != result.NewCPUs {
			t.Errorf("CPUs changed unexpectedly: old=%d, new=%d", result.OldCPUs, result.NewCPUs)
		}

		t.Logf("Memory resized from %d to %d bytes", result.OldMemory, result.NewMemory)
	})

	hostCPUs := runtime.NumCPU()
	canTestCPUIncrease := hostCPUs > int(initialCPUs)

	if canTestCPUIncrease {
		t.Run("vm_resize_cpu", func(t *testing.T) {
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

			if result.NewCPUs != uint64(newCPUs) {
				t.Errorf("unexpected new CPUs: got %d, want %d", result.NewCPUs, newCPUs)
			}

			if result.OldMemory != result.NewMemory {
				t.Errorf("memory changed unexpectedly: old=%d, new=%d", result.OldMemory, result.NewMemory)
			}

			t.Logf("CPUs resized from %d to %d", result.OldCPUs, result.NewCPUs)
		})
	} else {
		t.Run("vm_resize_cpu", func(t *testing.T) {
			t.Skipf("skipping CPU increase test: host has %d CPUs, VM already has %d", hostCPUs, initialCPUs)
		})
	}

	t.Run("vm_resize_both", func(t *testing.T) {
		newMemoryGB := 4
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

		expectedMemory := uint64(newMemoryGB * 1024 * 1024 * 1024)
		if result.NewMemory != expectedMemory {
			t.Errorf("unexpected new memory: got %d, want %d", result.NewMemory, expectedMemory)
		}

		t.Logf("Resized memory %d->%d bytes", result.OldMemory, result.NewMemory)
	})

	t.Run("vm_restart", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("restart " + box)
		repl.Want("Restarting")
		repl.Want("restarted successfully")
		repl.WantPrompt()
		repl.Disconnect()

		waitForSSH(t, box, ownerKeyFile)
	})

	t.Run("vm_verify_new_config", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "grep", "MemTotal", "/proc/meminfo").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get memory: %v\n%s", err, out)
		}
		var newMemoryKB uint64
		if _, err := fmt.Sscanf(string(out), "MemTotal: %d kB", &newMemoryKB); err != nil {
			t.Fatalf("failed to parse meminfo: %v\n%s", err, out)
		}
		t.Logf("New memory: %d KB (%.2f GB)", newMemoryKB, float64(newMemoryKB)/(1024*1024))

		expectedMemoryKB := uint64(4 * 1024 * 1024)
		minExpectedKB := uint64(float64(expectedMemoryKB) * 0.90)
		if newMemoryKB < minExpectedKB {
			t.Errorf("memory not increased enough: got %d KB, want at least %d KB", newMemoryKB, minExpectedKB)
		}
		if newMemoryKB <= initialMemoryKB {
			t.Errorf("memory did not increase: initial=%d KB, new=%d KB", initialMemoryKB, newMemoryKB)
		}
	})

	t.Run("vm_memory_max_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.SendLine(fmt.Sprintf("resize %s --memory=64", box))
		supportPTY.Want("cannot exceed")
		supportPTY.WantPrompt()
		supportPTY.Disconnect()
	})

	t.Run("vm_memory_min_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.SendLine(fmt.Sprintf("resize %s --memory=1", box))
		supportPTY.Want("at least")
		supportPTY.WantPrompt()
		supportPTY.Disconnect()
	})

	t.Run("vm_cpu_max_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.SendLine(fmt.Sprintf("resize %s --cpu=16", box))
		supportPTY.Want("cannot exceed")
		supportPTY.WantPrompt()
		supportPTY.Disconnect()
	})

	t.Run("missing_args_rejected", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.SendLine("resize")
		supportPTY.Want("usage: resize")
		supportPTY.WantPrompt()
		supportPTY.Disconnect()
	})

	t.Run("no_options_rejected", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.SendLine(fmt.Sprintf("resize %s", box))
		supportPTY.Want("At least one of")
		supportPTY.WantPrompt()
		supportPTY.Disconnect()
	})

	cleanupBox(t, ownerKeyFile, box)
}
