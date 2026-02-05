// This file tests the --memory, --disk, and --cpu flags for the new command.
// Normal users are limited to the environment defaults, while users with
// per-user limits can specify higher values.

package e1e

import (
	"fmt"
	"testing"
)

// TestResVal tests validation of resource flags without creating VMs.
// These tests only check error messages and don't require exelet to work.
func TestResVal(t *testing.T) {
	t.Parallel()
	noGolden(t)

	// Create two users: a regular user and one that will get elevated limits
	regularPTY, _, _, _ := registerForExeDevWithEmail(t, "regular@test-resval.example")
	elevatedPTY, _, _, elevatedEmail := registerForExeDevWithEmail(t, "elevated@test-resval.example")

	// Test minimum validation for regular user
	t.Run("mem-low", func(t *testing.T) {
		bn := boxName(t)
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --memory=1GB", bn))
		regularPTY.want("--memory must be at least")
		regularPTY.wantPrompt()
	})

	t.Run("disk-low", func(t *testing.T) {
		bn := boxName(t)
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --disk=2GB", bn))
		regularPTY.want("--disk must be at least")
		regularPTY.wantPrompt()
	})

	// Test that regular user cannot exceed defaults
	t.Run("mem-hi", func(t *testing.T) {
		bn := boxName(t)
		// For test env: max is max(1GB, 2GB) = 2GB, so asking for 3GB should fail
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --memory=3GB", bn))
		regularPTY.want("--memory cannot exceed")
		regularPTY.wantPrompt()
	})

	t.Run("disk-hi", func(t *testing.T) {
		bn := boxName(t)
		// Test env default is 11GB, asking for 20GB should fail
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --disk=20GB", bn))
		regularPTY.want("--disk cannot exceed")
		regularPTY.wantPrompt()
	})

	t.Run("cpu-hi", func(t *testing.T) {
		bn := boxName(t)
		// Default CPUs is 2, so asking for 4 should fail
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --cpu=4", bn))
		regularPTY.want("--cpu cannot exceed")
		regularPTY.wantPrompt()
	})

	// Test invalid size formats
	t.Run("mem-bad", func(t *testing.T) {
		bn := boxName(t)
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --memory=abc", bn))
		regularPTY.want("invalid --memory value")
		regularPTY.wantPrompt()
	})

	t.Run("disk-bad", func(t *testing.T) {
		bn := boxName(t)
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --disk=xyz", bn))
		regularPTY.want("invalid --disk value")
		regularPTY.wantPrompt()
	})

	// Set elevated per-user limits
	t.Run("set-lim", func(t *testing.T) {
		setUserLimits(t, elevatedEmail, `{"max_memory": 32000000000, "max_disk": 128000000000, "max_cpus": 8}`)
	})

	// Test that elevated user still cannot exceed their limits
	t.Run("s-mem-hi", func(t *testing.T) {
		bn := boxName(t)
		// 64GB exceeds the 32GB per-user limit
		elevatedPTY.sendLine(fmt.Sprintf("new --name=%s --memory=64GB", bn))
		elevatedPTY.want("--memory cannot exceed")
		elevatedPTY.wantPrompt()
	})

	t.Run("s-cpu-hi", func(t *testing.T) {
		bn := boxName(t)
		// 16 CPUs exceeds the 8 CPU per-user limit
		elevatedPTY.sendLine(fmt.Sprintf("new --name=%s --cpu=16", bn))
		elevatedPTY.want("--cpu cannot exceed")
		elevatedPTY.wantPrompt()
	})

	t.Run("s-disk-hi", func(t *testing.T) {
		bn := boxName(t)
		// 256GB exceeds the 128GB per-user limit
		elevatedPTY.sendLine(fmt.Sprintf("new --name=%s --disk=256GB", bn))
		elevatedPTY.want("--disk cannot exceed")
		elevatedPTY.wantPrompt()
	})

	regularPTY.disconnect()
	elevatedPTY.disconnect()
}

// TestResCreate tests creating VMs with custom resource flags.
// These tests require a working exelet to actually create VMs.
func TestResCreate(t *testing.T) {
	t.Parallel()
	noGolden(t)

	regularPTY, _, _, _ := registerForExeDevWithEmail(t, "regular@test-rescreate.example")
	elevatedPTY, _, _, elevatedEmail := registerForExeDevWithEmail(t, "elevated@test-rescreate.example")

	// Test that regular user can create with defaults (cpu=0 means use default)
	t.Run("def", func(t *testing.T) {
		bn := boxName(t)
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --cpu=0", bn))
		regularPTY.wantRe("Creating")
		regularPTY.want("Ready")
		regularPTY.wantPrompt()
		regularPTY.deleteBox(bn)
	})

	// Test that regular user can use values at minimums
	// Note: --disk is not tested at minimum (4GB) because the exeuntu base image is 10GB,
	// and you can't shrink a disk below its base image size.
	t.Run("min", func(t *testing.T) {
		bn := boxName(t)
		// 2GB memory (min), 1 CPU - disk uses default since base image is larger than min
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --memory=2GB --cpu=1", bn))
		regularPTY.wantRe("Creating")
		regularPTY.want("Ready")
		regularPTY.wantPrompt()
		regularPTY.deleteBox(bn)
	})

	// Set elevated per-user limits
	t.Run("set-lim", func(t *testing.T) {
		setUserLimits(t, elevatedEmail, `{"max_memory": 8000000000, "max_cpus": 4}`)
	})

	// Test that elevated user can exceed regular limits
	t.Run("s-mem", func(t *testing.T) {
		bn := boxName(t)
		// 4GB exceeds test default but is within per-user limit of 8GB
		elevatedPTY.sendLine(fmt.Sprintf("new --name=%s --memory=4GB", bn))
		elevatedPTY.wantRe("Creating")
		elevatedPTY.want("Ready")
		elevatedPTY.wantPrompt()
		elevatedPTY.deleteBox(bn)
	})

	t.Run("s-cpu", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping CPU test in short mode")
		}
		bn := boxName(t)
		elevatedPTY.sendLine(fmt.Sprintf("new --name=%s --cpu=2", bn))
		elevatedPTY.wantRe("Creating")
		elevatedPTY.want("Ready")
		elevatedPTY.wantPrompt()
		elevatedPTY.deleteBox(bn)
	})

	regularPTY.disconnect()
	elevatedPTY.disconnect()
}
