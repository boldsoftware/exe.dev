// This file tests self-serve VM resize end-to-end.
// Regular users can resize their own VM's memory, CPU, and disk within plan limits.

package e1e

import (
	"encoding/json"
	"fmt"
	"testing"

	"exe.dev/billing/plan"
	"exe.dev/e1e/testinfra"
	"exe.dev/stage"
)

// TestSelfServeResize tests that a regular user can resize their VM's disk
// via the resize command (no sudo required), and that plan limits are enforced.
func TestSelfServeResize(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDevWithEmail(t, "user@test-selfserveresize.example")

	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.WantPrompt()
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Verify test environment setup.
	env := stage.Test()
	if env.DefaultDisk != 11*1024*1024*1024 {
		t.Fatalf("test env DefaultDisk changed: got %d, expected 11 GiB", env.DefaultDisk)
	}

	// Get the initial disk size.
	var initialVolumeBytes uint64
	t.Run("get_initial_size", func(t *testing.T) {
		out, err := boxSSHCommand(t, box, keyFile, "sudo", "blockdev", "--getsize64", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get block device size: %v\n%s", err, out)
		}
		if _, err := fmt.Sscanf(string(out), "%d", &initialVolumeBytes); err != nil {
			t.Fatalf("failed to parse blockdev output: %v\n%s", err, out)
		}
		t.Logf("Initial volume size: %d bytes (%.2f GiB)", initialVolumeBytes, float64(initialVolumeBytes)/(1024*1024*1024))
	})

	// Self-serve disk resize within the env-capped ceiling.
	// In test env, EffectiveMaxDisk = 11 GiB (env caps the 75 GiB tier max).
	// The VM starts at 11 GiB, so we can't grow within the default cap.
	// Use a support override to give headroom, then test self-serve resize.
	t.Run("resize_with_override", func(t *testing.T) {
		// Give user a 30 GiB override so they have room to grow.
		setUserLimits(t, email, `{"max_disk": 32212254720}`) // 30 GiB

		newSizeGB := (initialVolumeBytes/(1024*1024*1024) + 1) + 1
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "resize", box, fmt.Sprintf("--disk=%d", newSizeGB), "--json")
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

		t.Logf("Self-serve disk resize: %d -> %d bytes", result.DiskOldBytes, result.DiskNewBytes)
	})

	// Verify kernel sees new size after restart.
	t.Run("verify_after_restart", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("restart " + box)
		repl.Want("Restarting")
		repl.Want("restarted successfully")
		repl.WantPrompt()
		repl.Disconnect()
		waitForSSH(t, box, keyFile)

		out, err := boxSSHCommand(t, box, keyFile, "sudo", "blockdev", "--getsize64", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get block device size: %v\n%s", err, out)
		}
		var newVolumeBytes uint64
		if _, err := fmt.Sscanf(string(out), "%d", &newVolumeBytes); err != nil {
			t.Fatalf("failed to parse blockdev output: %v\n%s", err, out)
		}
		if newVolumeBytes <= initialVolumeBytes {
			t.Errorf("kernel does not see grown disk after restart: got %d, had %d", newVolumeBytes, initialVolumeBytes)
		}
		t.Logf("Post-restart volume: %d bytes (%.2f GiB)", newVolumeBytes, float64(newVolumeBytes)/(1024*1024*1024))
	})

	// Exceeding plan ceiling is rejected.
	t.Run("exceeds_ceiling", func(t *testing.T) {
		// Override is 30 GiB — requesting 40 GiB should be rejected.
		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("resize %s --disk=40", box))
		repl.Want("exceeds the")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Shrinking disk is rejected.
	t.Run("shrink_rejected", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("resize %s --disk=5", box))
		repl.Want("must be larger than current size")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Memory resize within plan limits should work.
	t.Run("memory_resize", func(t *testing.T) {
		noGolden(t)
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "resize", box, "--memory=4", "--json")
		if err != nil {
			t.Fatalf("resize --memory=4 failed: %v\n%s", err, out)
		}
		var result struct {
			NewMemory uint64 `json:"new_memory"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if result.NewMemory != 4*1024*1024*1024 {
			t.Errorf("expected 4 GiB memory, got %d", result.NewMemory)
		}
	})

	// Memory exceeding plan limit should be rejected.
	t.Run("memory_exceeds_plan", func(t *testing.T) {
		noGolden(t)
		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("resize %s --memory=64", box))
		repl.Want("cannot exceed")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// CPU resize within plan limits should work.
	t.Run("cpu_resize", func(t *testing.T) {
		noGolden(t)
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "resize", box, "--cpu=2", "--json")
		if err != nil {
			t.Fatalf("resize --cpu=2 failed: %v\n%s", err, out)
		}
		var result struct {
			NewCPUs uint64 `json:"new_cpus"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if result.NewCPUs != 2 {
			t.Errorf("expected 2 CPUs, got %d", result.NewCPUs)
		}
	})

	// CPU exceeding plan limit should be rejected.
	t.Run("cpu_exceeds_plan", func(t *testing.T) {
		noGolden(t)
		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("resize %s --cpu=32", box))
		repl.Want("cannot exceed")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Clean up override.
	setUserLimits(t, email, "")

	// Without override, env caps at 11 GiB — VM is already larger, so any
	// resize would exceed the ceiling.
	t.Run("no_override_capped", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("resize %s --disk=20", box))
		repl.Want("exceeds the")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Cannot resize another user's VM.
	t.Run("other_user_denied", func(t *testing.T) {
		_, _, otherKeyFile, _ := registerForExeDevWithEmail(t, "other@test-selfserveresize.example")
		repl := sshToExeDev(t, otherKeyFile)
		repl.SendLine(fmt.Sprintf("resize %s --disk=20", box))
		repl.Want("not found")
		repl.WantPrompt()
		repl.Disconnect()
	})

	cleanupBox(t, keyFile, box)
}

// TestSelfServeResizeEntitlement tests that the VMResize entitlement is
// correctly granted/denied by plan category.
func TestSelfServeResizeEntitlement(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	noGolden(t)

	// Verify basic plan does NOT have VMResize.
	if plan.Grants(plan.ID(plan.CategoryBasic), plan.VMResize) {
		t.Fatal("basic plan should not grant VMResize")
	}

	// Verify individual plan DOES have VMResize.
	if !plan.Grants(plan.ID(plan.CategoryIndividual), plan.VMResize) {
		t.Fatal("individual plan should grant VMResize")
	}
}

// TestResizeVisibleInHelp verifies that the resize command shows up in the
// command listing for regular users (no longer hidden/support-only).
func TestResizeVisibleInHelp(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDevWithEmail(t, "help@test-resizehelp.example")
	pty.Disconnect()

	repl := sshToExeDev(t, keyFile)
	repl.SendLine("help")
	repl.WantRE(`resize`)
	repl.WantPrompt()
	repl.Disconnect()
}
