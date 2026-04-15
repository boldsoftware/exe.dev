// This file tests tier-based disk quota enforcement end-to-end.
// The tier catalog is the source of truth for disk sizes; stage.Env.DefaultDisk
// acts as a cap for both default disk and max disk in local/test environments.

package e1e

import (
	"fmt"
	"testing"

	"exe.dev/billing/plan"
	"exe.dev/stage"
)

// TestDiskQuotaNew tests disk quota enforcement for the new command.
// In the test env, DefaultDisk=11GB caps both the default and the ceiling.
// Support overrides bypass the env cap.
func TestDiskQuotaNew(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	regularPTY, _, keyFile, regularEmail := registerForExeDevWithEmail(t, "regular@test-diskquota-new.example")

	// Verify the expected test environment config.
	env := stage.Test()
	if env.DefaultDisk != 11*1024*1024*1024 {
		t.Fatalf("test env DefaultDisk changed: got %d, expected 11 GiB", env.DefaultDisk)
	}
	tierMaxDisk := plan.MaxDiskForPlan(plan.ID(plan.CategoryIndividual))
	if tierMaxDisk != 75*1024*1024*1024 {
		t.Fatalf("individual plan MaxDisk changed: got %d, expected 75 GiB", tierMaxDisk)
	}
	// In test env, EffectiveMaxDisk is capped to 11GB.
	effMax := plan.EffectiveMaxDisk(plan.ID(plan.CategoryIndividual), 0, env.DefaultDisk)
	if effMax != env.DefaultDisk {
		t.Fatalf("EffectiveMaxDisk in test env: got %d, expected %d", effMax, env.DefaultDisk)
	}

	// Create a VM with defaults — should get env-capped disk (11 GiB)
	box := newBox(t, regularPTY)
	regularPTY.Disconnect()
	waitForSSH(t, box, keyFile)

	t.Run("default_disk", func(t *testing.T) {
		expectedDiskBytes := uint64(env.DefaultDisk)

		out, err := boxSSHCommand(t, box, keyFile, "sudo", "blockdev", "--getsize64", "/dev/vda").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get block device size: %v\n%s", err, out)
		}
		var blockDevSize uint64
		if _, err := fmt.Sscanf(string(out), "%d", &blockDevSize); err != nil {
			t.Fatalf("failed to parse blockdev output: %v\n%s", err, out)
		}
		if blockDevSize < expectedDiskBytes {
			t.Errorf("block device too small: got %d, want at least %d", blockDevSize, expectedDiskBytes)
		}
		if blockDevSize > expectedDiskBytes+1024*1024*1024 {
			t.Errorf("block device too large: got %d, expected ~%d", blockDevSize, expectedDiskBytes)
		}
		t.Logf("VM disk size: %d bytes (%.1f GiB)", blockDevSize, float64(blockDevSize)/(1024*1024*1024))
	})

	cleanupBox(t, keyFile, box)

	// --disk above env-capped ceiling (11 GiB) is rejected
	t.Run("above_ceiling", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		bn := boxName(t)
		repl.SendLine(fmt.Sprintf("new --name=%s --disk=20GB", bn))
		repl.Want("--disk cannot exceed")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Support override (30 GiB) lifts the env-capped ceiling
	t.Run("support_override_lifts_ceiling", func(t *testing.T) {
		setUserLimits(t, regularEmail, `{"max_disk": 32212254720}`) // 30 GiB

		// 20 GiB was rejected above, now it should pass validation.
		repl := sshToExeDev(t, keyFile)
		bn := boxName(t)
		repl.SendLine(fmt.Sprintf("new --name=%s --disk=20GB", bn))
		repl.WantRE("Creating")
		repl.Want("Ready")
		repl.WantPrompt()
		repl.Disconnect()

		waitForSSH(t, bn, keyFile)
		cleanupBox(t, keyFile, bn)
		setUserLimits(t, regularEmail, "")
	})

	// Override still has its own ceiling
	t.Run("support_override_ceiling", func(t *testing.T) {
		setUserLimits(t, regularEmail, `{"max_disk": 32212254720}`) // 30 GiB

		repl := sshToExeDev(t, keyFile)
		bn := boxName(t)
		repl.SendLine(fmt.Sprintf("new --name=%s --disk=40GB", bn))
		repl.Want("--disk cannot exceed")
		repl.WantPrompt()
		repl.Disconnect()

		setUserLimits(t, regularEmail, "")
	})
}

// TestDiskQuotaCp tests disk quota enforcement for the cp command.
func TestDiskQuotaCp(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	pty, _, keyFile, email := registerForExeDevWithEmail(t, "cpquota@test-diskquota-cp.example")

	sourceBox := newBox(t, pty)
	pty.Disconnect()
	waitForSSH(t, sourceBox, keyFile)

	// cp --disk above env-capped ceiling rejected
	t.Run("above_ceiling", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("cp %s cp-over-ceil --disk=20GB", sourceBox))
		repl.Want("--disk cannot exceed")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Support override lifts cp ceiling
	t.Run("support_override_lifts_ceiling", func(t *testing.T) {
		setUserLimits(t, email, `{"max_disk": 32212254720}`) // 30 GiB

		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("cp %s cp-override --disk=20GB", sourceBox))
		repl.Want("Copying")
		repl.WantPrompt()
		repl.Disconnect()

		waitForSSH(t, "cp-override", keyFile)
		cleanupBox(t, keyFile, "cp-override")
		setUserLimits(t, email, "")
	})

	// Override still has a ceiling
	t.Run("support_override_ceiling", func(t *testing.T) {
		setUserLimits(t, email, `{"max_disk": 32212254720}`) // 30 GiB

		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("cp %s cp-over-override --disk=40GB", sourceBox))
		repl.Want("--disk cannot exceed")
		repl.WantPrompt()
		repl.Disconnect()

		setUserLimits(t, email, "")
	})

	cleanupBox(t, keyFile, sourceBox)
}
