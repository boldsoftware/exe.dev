// This file contains e1e tests for the cp command.

package e1e

import (
	"encoding/json"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestCp tests the cp command end-to-end.
// It creates a source VM with marker files, then runs multiple subtests to verify
// cp behavior including error cases, JSON output, random names, and successful copying.
// All subtests share the same source VM to minimize VM creation overhead.
func TestCp(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Test usage error first (doesn't need a VM)
	t.Run("Usage", func(t *testing.T) {
		pty.SendLine("cp")
		pty.Want("usage")
		pty.WantPrompt()
	})

	// Create source VM
	sourceBox := newBox(t, pty)
	pty.Disconnect()
	waitForSSH(t, sourceBox, keyFile)

	// Write marker file on source box to verify data copying
	// Use tee to write since SSH command quoting with sh -c can be problematic
	teeCmd := boxSSHCommand(t, sourceBox, keyFile, "tee", "/home/exedev/cp-test.txt")
	teeCmd.Stdin = strings.NewReader("cp-source-marker\n")
	if err := teeCmd.Run(); err != nil {
		t.Fatalf("failed to write marker file: %v", err)
	}
	// Sync to ensure data is on disk
	if err := boxSSHCommand(t, sourceBox, keyFile, "sync").Run(); err != nil {
		t.Fatalf("failed to sync: %v", err)
	}

	// Get source box's SSH host key fingerprint for later comparison
	// SSH keys are stored at /exe.dev/etc/ssh/ (not /etc/ssh/)
	sourceHostKeyOut, err := boxSSHCommand(t, sourceBox, keyFile, "sudo", "ssh-keygen", "-lf", "/exe.dev/etc/ssh/ssh_host_ed25519_key.pub").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to get source SSH host key: %v\n%s", err, sourceHostKeyOut)
	}
	sourceHostKeyFingerprint := strings.TrimSpace(string(sourceHostKeyOut))

	// Get source box's machine-id for later comparison
	sourceMachineIDOut, err := boxSSHCommand(t, sourceBox, keyFile, "cat", "/etc/machine-id").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to get source machine-id: %v\n%s", err, sourceMachineIDOut)
	}
	sourceMachineID := strings.TrimSpace(string(sourceMachineIDOut))

	// Run error case subtests first (these don't modify the source VM)
	t.Run("InvalidCopyName", func(t *testing.T) {
		// Try to copy with an invalid name (too short)
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("cp " + sourceBox + " abc")
		repl.Want("invalid")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("SourceNotFound", func(t *testing.T) {
		// Try to copy a non-existent VM
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("cp nonexistent-vm-xyz copied-box")
		repl.Want("not found")
		repl.WantPrompt()
		repl.Disconnect()
	})

	t.Run("NameConflict", func(t *testing.T) {
		// Try to copy to a name that already exists (the source box itself)
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("cp " + sourceBox + " " + sourceBox)
		repl.Want("already exists")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test cp with --json flag (creates and cleans up a copy)
	t.Run("JSONOutput", func(t *testing.T) {
		copiedBox := "copied-json-test"
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "cp", sourceBox, copiedBox, "--json")
		if err != nil {
			t.Fatalf("cp command failed: %v\n%s", err, out)
		}

		// Verify JSON output contains expected fields
		var result struct {
			Name  string `json:"name"`
			State string `json:"state"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("failed to parse JSON output: %v\n%s", err, out)
		}
		if result.Name != copiedBox {
			t.Fatalf("expected name %q in JSON output, got %q", copiedBox, result.Name)
		}

		// Cleanup this copy
		cleanupBox(t, keyFile, copiedBox)
	})

	// Test cp without specifying name (random name generated)
	t.Run("RandomName", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine("cp " + sourceBox)
		repl.Want("Copying")
		repl.WantPrompt()
		repl.Disconnect()

		// Get box list via JSON to find the copied box
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ls", "--json")
		if err != nil {
			t.Fatalf("ls --json failed: %v\n%s", err, out)
		}

		var lsResult struct {
			VMs []struct {
				VMName string `json:"vm_name"`
			} `json:"vms"`
		}
		if err := json.Unmarshal(out, &lsResult); err != nil {
			t.Fatalf("failed to parse ls JSON: %v\n%s", err, out)
		}

		// Find the copy (the box that's not the source)
		var copiedBox string
		for _, vm := range lsResult.VMs {
			if vm.VMName != sourceBox {
				copiedBox = vm.VMName
				break
			}
		}
		if copiedBox == "" {
			t.Fatalf("could not find copied box in ls output: %s", out)
		}

		// Canonicalize the random name for golden file output
		testinfra.AddCanonicalization(copiedBox, "RANDOM_COPY_NAME")

		// Cleanup the random-named copy
		cleanupBox(t, keyFile, copiedBox)
	})

	// Test successful cp (this one we keep for subsequent verification tests)
	var copiedBox string
	t.Run("Success", func(t *testing.T) {
		copiedBox = "copied-from-source"

		repl := sshToExeDev(t, keyFile)
		repl.SendLine("cp " + sourceBox + " " + copiedBox)
		repl.Want("Copying")
		repl.WantPrompt()
		repl.Disconnect()

		// Wait for copied box SSH to be ready
		waitForSSH(t, copiedBox, keyFile)
	})

	// Verify data was copied (marker file exists)
	t.Run("DataCopied", func(t *testing.T) {
		if copiedBox == "" {
			t.Skip("cp failed, skipping data verification")
		}

		out, err := boxSSHCommand(t, copiedBox, keyFile, "cat", "/home/exedev/cp-test.txt").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read marker file in copied box: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "cp-source-marker") {
			t.Fatalf("expected marker file to contain 'cp-source-marker', got: %s", out)
		}
	})

	// Verify hostname is different in copied box
	t.Run("HostnameUpdated", func(t *testing.T) {
		if copiedBox == "" {
			t.Skip("cp failed, skipping hostname verification")
		}

		// Check hostname command output
		out, err := boxSSHCommand(t, copiedBox, keyFile, "hostname").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get hostname: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), copiedBox) {
			t.Fatalf("expected hostname to contain %q, got %q", copiedBox, string(out))
		}

		// Check /etc/hostname
		out, err = boxSSHCommand(t, copiedBox, keyFile, "cat", "/etc/hostname").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read /etc/hostname: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), copiedBox) {
			t.Fatalf("expected /etc/hostname to contain %q, got %q", copiedBox, string(out))
		}

		// Check /etc/hosts contains new hostname
		out, err = boxSSHCommand(t, copiedBox, keyFile, "cat", "/etc/hosts").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read /etc/hosts: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), copiedBox) {
			t.Fatalf("expected /etc/hosts to contain %q, got %q", copiedBox, string(out))
		}
		// Should NOT contain source box name
		if strings.Contains(string(out), sourceBox) {
			t.Fatalf("/etc/hosts should not contain source box name %q, got: %s", sourceBox, out)
		}
	})

	// Verify SSH host keys are regenerated (different from source)
	t.Run("SSHHostKeysRegenerated", func(t *testing.T) {
		if copiedBox == "" {
			t.Skip("cp failed, skipping SSH host key verification")
		}

		copiedHostKeyOut, err := boxSSHCommand(t, copiedBox, keyFile, "sudo", "ssh-keygen", "-lf", "/exe.dev/etc/ssh/ssh_host_ed25519_key.pub").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get copied SSH host key: %v\n%s", err, copiedHostKeyOut)
		}
		copiedHostKeyFingerprint := strings.TrimSpace(string(copiedHostKeyOut))

		if sourceHostKeyFingerprint == copiedHostKeyFingerprint {
			t.Fatalf("copied box has same SSH host key as source - keys should be regenerated\nsource: %s\ncopied: %s",
				sourceHostKeyFingerprint, copiedHostKeyFingerprint)
		}
	})

	// Verify machine-id is regenerated (different from source)
	t.Run("MachineIDRegenerated", func(t *testing.T) {
		if copiedBox == "" {
			t.Skip("cp failed, skipping machine-id verification")
		}

		copiedMachineIDOut, err := boxSSHCommand(t, copiedBox, keyFile, "cat", "/etc/machine-id").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get copied machine-id: %v\n%s", err, copiedMachineIDOut)
		}
		copiedMachineID := strings.TrimSpace(string(copiedMachineIDOut))

		if sourceMachineID == copiedMachineID {
			t.Fatalf("copied box has same machine-id as source - machine-id should be regenerated\nsource: %s\ncopied: %s",
				sourceMachineID, copiedMachineID)
		}
	})

	// Verify metadata service returns correct name for copied box
	t.Run("MetadataService", func(t *testing.T) {
		if copiedBox == "" {
			t.Skip("cp failed, skipping metadata verification")
		}

		out, err := boxSSHCommand(t, copiedBox, keyFile, "curl", "--max-time", "10", "-s", "http://169.254.169.254/").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to get metadata: %v\n%s", err, out)
		}
		var resp struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("failed to parse metadata response: %v\n%s", err, out)
		}
		if resp.Name != copiedBox {
			t.Fatalf("expected metadata name %q, got %q", copiedBox, resp.Name)
		}
	})

	// Verify both VMs are running independently
	t.Run("BothVMsRunning", func(t *testing.T) {
		if copiedBox == "" {
			t.Skip("cp failed, skipping concurrent run verification")
		}

		// Write different files to each box to verify independence
		// Use tee since SSH command quoting with sh -c is problematic
		sourceTee := boxSSHCommand(t, sourceBox, keyFile, "tee", "/tmp/independent.txt")
		sourceTee.Stdin = strings.NewReader("source-independent\n")
		if err := sourceTee.Run(); err != nil {
			t.Fatalf("failed to write to source box: %v", err)
		}
		copiedTee := boxSSHCommand(t, copiedBox, keyFile, "tee", "/tmp/independent.txt")
		copiedTee.Stdin = strings.NewReader("copied-independent\n")
		if err := copiedTee.Run(); err != nil {
			t.Fatalf("failed to write to copied box: %v", err)
		}

		// Verify each box has its own file
		sourceOut, err := boxSSHCommand(t, sourceBox, keyFile, "cat", "/tmp/independent.txt").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read from source box: %v\n%s", err, sourceOut)
		}
		if !strings.Contains(string(sourceOut), "source-independent") {
			t.Fatalf("source box should have 'source-independent', got: %s", sourceOut)
		}

		copiedOut, err := boxSSHCommand(t, copiedBox, keyFile, "cat", "/tmp/independent.txt").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read from copied box: %v\n%s", err, copiedOut)
		}
		if !strings.Contains(string(copiedOut), "copied-independent") {
			t.Fatalf("copied box should have 'copied-independent', got: %s", copiedOut)
		}
	})

	// Verify writes to copy don't affect source (COW isolation)
	t.Run("COWIsolation", func(t *testing.T) {
		if copiedBox == "" {
			t.Skip("cp failed, skipping COW isolation verification")
		}

		// Modify the marker file in copied box
		// Use tee since SSH command quoting with sh -c is problematic
		modifyTee := boxSSHCommand(t, copiedBox, keyFile, "tee", "/home/exedev/cp-test.txt")
		modifyTee.Stdin = strings.NewReader("modified-in-copy\n")
		if err := modifyTee.Run(); err != nil {
			t.Fatalf("failed to modify marker file in copy: %v", err)
		}
		// Sync to ensure data is on disk
		if err := boxSSHCommand(t, copiedBox, keyFile, "sync").Run(); err != nil {
			t.Fatalf("failed to sync: %v", err)
		}

		// Verify source box still has original content
		sourceOut, err := boxSSHCommand(t, sourceBox, keyFile, "cat", "/home/exedev/cp-test.txt").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read marker file from source: %v\n%s", err, sourceOut)
		}
		if !strings.Contains(string(sourceOut), "cp-source-marker") {
			t.Fatalf("source marker file should still contain 'cp-source-marker', got: %s", sourceOut)
		}

		// Verify copied box has modified content
		copiedOut, err := boxSSHCommand(t, copiedBox, keyFile, "cat", "/home/exedev/cp-test.txt").CombinedOutput()
		if err != nil {
			t.Fatalf("failed to read marker file from copy: %v\n%s", err, copiedOut)
		}
		if !strings.Contains(string(copiedOut), "modified-in-copy") {
			t.Fatalf("copied marker file should contain 'modified-in-copy', got: %s", copiedOut)
		}
	})

	// Cleanup
	if copiedBox != "" {
		cleanupBox(t, keyFile, copiedBox)
	}
	cleanupBox(t, keyFile, sourceBox)
}
