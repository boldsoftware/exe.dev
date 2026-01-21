// This file tests the grow command for support users.

package e1e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestGrowDisk tests the grow command end-to-end.
// This verifies that support users can grow the disk of a VM.
func TestGrowDisk(t *testing.T) {
	t.Parallel()
	noGolden(t) // Output contains variable disk sizes

	// Create an owner user and a support user
	ownerPTY, _, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-grow-disk.example")
	supportPTY, _, supportKeyFile, supportEmail := registerForExeDevWithEmail(t, "support@test-grow-disk.example")

	// Owner creates a box
	box := newBox(t, ownerPTY, testinfra.BoxOpts{Command: "/bin/bash"})
	ownerPTY.wantPrompt()
	ownerPTY.disconnect()

	// Wait for SSH to be ready
	waitForSSH(t, box, ownerKeyFile)

	// Test that non-support user cannot use grow command
	t.Run("non_support_user_denied", func(t *testing.T) {
		ownerPTY = sshToExeDev(t, ownerKeyFile)
		ownerPTY.sendLine(fmt.Sprintf("grow %s 1", box))
		ownerPTY.want("not in the sudoers file")
		ownerPTY.wantPrompt()
		ownerPTY.disconnect()
	})

	// Enable root_support for the support user
	t.Run("enable_root_support", func(t *testing.T) {
		enableRootSupport(t, supportEmail)
	})

	// Get initial filesystem size
	var initialFSBytes uint64
	t.Run("get_initial_size", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "df", "--block-size=1", "--output=size", "/").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get initial disk size: %v\n%s", err, out)
		}
		// Parse the size from df output (second line, after header)
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) < 2 {
			t.Fatalf("unexpected df output: %s", out)
		}
		var size uint64
		if _, err := fmt.Sscanf(strings.TrimSpace(lines[1]), "%d", &size); err != nil {
			t.Fatalf("failed to parse df output: %v\n%s", err, out)
		}
		initialFSBytes = size
		t.Logf("Initial filesystem size: %d bytes (%.2f GB)", initialFSBytes, float64(initialFSBytes)/(1024*1024*1024))
	})

	// Test that support user can grow the disk
	var volumeNewBytes uint64
	t.Run("grow_disk", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), supportKeyFile, "grow", box, "1", "--json")
		if err != nil {
			t.Fatalf("grow command failed: %v\n%s", err, out)
		}

		var result struct {
			VMName         string `json:"vm_name"`
			VolumeOldBytes uint64 `json:"volume_old_bytes"`
			VolumeNewBytes uint64 `json:"volume_new_bytes"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}

		// Verify the ZFS volume was grown
		if result.VolumeNewBytes <= result.VolumeOldBytes {
			t.Errorf("volume did not grow: old=%d, new=%d", result.VolumeOldBytes, result.VolumeNewBytes)
		}

		// Verify volume growth amount is ~1GB
		expectedGrowth := uint64(1 * 1024 * 1024 * 1024)
		actualVolumeGrowth := result.VolumeNewBytes - result.VolumeOldBytes
		if actualVolumeGrowth != expectedGrowth {
			t.Errorf("unexpected volume growth: got %d, want %d", actualVolumeGrowth, expectedGrowth)
		}

		volumeNewBytes = result.VolumeNewBytes
		t.Logf("Volume grew from %d to %d bytes", result.VolumeOldBytes, result.VolumeNewBytes)
	})

	// Restart the VM so the guest kernel sees the new disk size
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

	// Verify the kernel now sees the new disk size
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

		// The kernel should now see the full volume size
		if kernelSize < volumeNewBytes-1024*1024 { // Allow 1MB tolerance for metadata
			t.Errorf("kernel does not see full disk size: kernel=%d, volume=%d", kernelSize, volumeNewBytes)
		}
	})

	// Run resize2fs to expand the filesystem
	t.Run("resize_filesystem", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, ownerKeyFile, "sudo", "resize2fs", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("resize2fs failed: %v\n%s", err, out)
		}
		t.Logf("resize2fs output: %s", out)
	})

	// Verify the filesystem grew by checking resize2fs output and df
	t.Run("verify_filesystem_grew", func(t *testing.T) {
		// Get filesystem size from df
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
		t.Logf("Final filesystem size (df): %d bytes (%.2f GB)", finalSize, float64(finalSize)/(1024*1024*1024))

		// The key verification is that the filesystem grew after resize2fs
		// Note: df reports filesystem size which may differ slightly from block device size
		// due to reserved blocks and metadata. The important thing is that it grew.
		if finalSize <= initialFSBytes {
			t.Errorf("filesystem did not grow: initial=%d, final=%d", initialFSBytes, finalSize)
		}

		growth := finalSize - initialFSBytes
		t.Logf("Filesystem grew by %d bytes (%.2f GB)", growth, float64(growth)/(1024*1024*1024))

		// Verify final size is close to the new volume size (within 10% for metadata overhead)
		minExpectedSize := uint64(float64(volumeNewBytes) * 0.90)
		if finalSize < minExpectedSize {
			t.Errorf("filesystem size %d is too small compared to volume size %d", finalSize, volumeNewBytes)
		}
	})

	// Test size limits
	t.Run("size_limit_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.sendLine(fmt.Sprintf("grow %s 300", box))
		supportPTY.want("cannot exceed")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	// Test minimum size
	t.Run("min_size_enforced", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.sendLine(fmt.Sprintf("grow %s 0", box))
		supportPTY.want("at least 1GB")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	// Test missing arguments
	t.Run("missing_args_rejected", func(t *testing.T) {
		supportPTY = sshToExeDev(t, supportKeyFile)
		supportPTY.sendLine("grow")
		supportPTY.want("usage: grow")
		supportPTY.wantPrompt()
		supportPTY.disconnect()
	})

	// Cleanup
	cleanupBox(t, ownerKeyFile, box)
}
