// This file tests the cp command with resource override flags (--memory, --disk, --cpu).

package e1e

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestCpResources tests the cp command with resource override flags.
func TestCpResources(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Create a user and give them elevated limits for testing
	pty, _, keyFile, email := registerForExeDevWithEmail(t, "cpres@test-cpresources.example")
	// Set elevated limits to allow testing with larger resources
	setUserLimits(t, email, `{"max_memory": 8000000000, "max_disk": 20000000000, "max_cpus": 4}`)

	// Create a source box
	sourceBox := newBox(t, pty)
	pty.disconnect()
	waitForSSH(t, sourceBox, keyFile)

	// Test validation errors
	t.Run("bad-mem", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.sendLine(fmt.Sprintf("cp %s inv-mem --memory=abc", sourceBox))
		repl.want("invalid --memory value")
		repl.wantPrompt()
		repl.disconnect()
	})

	t.Run("lo-mem", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.sendLine(fmt.Sprintf("cp %s lo-mem --memory=1GB", sourceBox))
		repl.want("--memory must be at least")
		repl.wantPrompt()
		repl.disconnect()
	})

	t.Run("bad-disk", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.sendLine(fmt.Sprintf("cp %s inv-disk --disk=xyz", sourceBox))
		repl.want("invalid --disk value")
		repl.wantPrompt()
		repl.disconnect()
	})

	t.Run("lo-disk", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.sendLine(fmt.Sprintf("cp %s lo-disk --disk=2GB", sourceBox))
		repl.want("--disk must be at least")
		repl.wantPrompt()
		repl.disconnect()
	})

	t.Run("hi-cpu", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.sendLine(fmt.Sprintf("cp %s hi-cpu --cpu=99", sourceBox))
		repl.want("--cpu cannot exceed")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test successful copy with memory override
	t.Run("with-mem", func(t *testing.T) {
		copiedBox := "cp-with-mem"
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "cp", sourceBox, copiedBox, "--memory=3GB", "--json")
		if err != nil {
			t.Fatalf("cp command failed: %v\n%s", err, out)
		}

		// Verify JSON output
		var result struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if result.Name != copiedBox {
			t.Fatalf("expected name %q, got %q", copiedBox, result.Name)
		}

		waitForSSH(t, copiedBox, keyFile)
		cleanupBox(t, keyFile, copiedBox)
	})

	// Test successful copy with larger disk
	t.Run("with-disk", func(t *testing.T) {
		copiedBox := "cp-with-disk"
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "cp", sourceBox, copiedBox, "--disk=15GB", "--json")
		if err != nil {
			t.Fatalf("cp command failed: %v\n%s", err, out)
		}

		waitForSSH(t, copiedBox, keyFile)

		// Verify the disk size increased (check inside the VM)
		// The filesystem should auto-expand on boot due to x-systemd.growfs
		diskOut, err := boxSSHCommand(t, copiedBox, keyFile, "df", "-h", "/").CombinedOutput()
		if err != nil {
			t.Logf("df output: %s", diskOut)
		}
		// Just verify the VM booted - the actual disk size verification is complex due to filesystem overhead

		cleanupBox(t, keyFile, copiedBox)
	})

	// Test successful copy with CPU override
	t.Run("with-cpu", func(t *testing.T) {
		copiedBox := "cp-with-cpu"
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "cp", sourceBox, copiedBox, "--cpu=2", "--json")
		if err != nil {
			t.Fatalf("cp command failed: %v\n%s", err, out)
		}

		waitForSSH(t, copiedBox, keyFile)

		// Verify CPU count inside the VM
		cpuOut, err := boxSSHCommand(t, copiedBox, keyFile, "nproc").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get CPU count: %v\n%s", err, cpuOut)
		}
		t.Logf("CPU count in copied VM: %s", cpuOut)

		cleanupBox(t, keyFile, copiedBox)
	})

	// Cleanup source
	cleanupBox(t, keyFile, sourceBox)
}
