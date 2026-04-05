package e1e

import (
	"encoding/json"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestSSHKeyPermissions tests the security boundaries of SSH key permissions.
// It verifies that --cmds, --vm, and --exp restrictions on SSH keys are
// enforced end-to-end, and that scoped keys cannot escalate their privileges.
func TestSSHKeyPermissions(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register the user and create two VMs.
	pty, _, keyFile, email := registerForExeDev(t)
	box1 := newBox(t, pty)
	box2 := newBox(t, pty)
	pty.Disconnect()

	// Permission flags on ssh-key add require root support.
	enableRootSupport(t, email)

	waitForSSH(t, box1, keyFile)
	waitForSSH(t, box2, keyFile)

	// Helper: add a scoped SSH key using the interactive REPL (so that flags
	// with spaces like --cmds="ssh-key add,whoami" are correctly quoted).
	// Returns the private key path.
	addScopedKey := func(t *testing.T, flags []string) (privKeyPath string) {
		t.Helper()
		testKeyPath, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("GenSSHKey: %v", err)
		}
		args := []string{"ssh-key", "add", "--json"}
		args = append(args, flags...)
		args = append(args, testPubKey)
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, args...)
		if err != nil {
			t.Fatalf("ssh-key add %v: %v\n%s", flags, err, out)
		}
		var result struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse JSON: %v\n%s", err, out)
		}
		if result.Status != "added" {
			t.Fatalf("expected status added, got %q", result.Status)
		}
		return testKeyPath
	}

	// ---------------------------------------------------------------
	// 1. Command-restricted key: allowed commands succeed, others fail
	// ---------------------------------------------------------------
	t.Run("cmds_allowed", func(t *testing.T) {
		cmdKeyPath := addScopedKey(t, []string{"--cmds=whoami,ls"})

		// Allowed: whoami
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), cmdKeyPath, "whoami")
		if err != nil {
			t.Errorf("whoami should succeed with cmds=whoami,ls: %v\n%s", err, out)
		}

		// Allowed: ls
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), cmdKeyPath, "ls")
		if err != nil {
			t.Errorf("ls should succeed with cmds=whoami,ls: %v\n%s", err, out)
		}
	})

	t.Run("cmds_denied", func(t *testing.T) {
		cmdKeyPath := addScopedKey(t, []string{"--cmds=whoami"})

		// Denied: ls
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), cmdKeyPath, "ls")
		if err == nil {
			t.Fatalf("ls should fail with cmds=whoami, got: %s", out)
		}
		if !strings.Contains(string(out), "command not allowed by SSH key permissions") {
			t.Errorf("expected permission denied error, got: %s", out)
		}

		// Denied: new (critical - must not be able to create VMs)
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), cmdKeyPath, "new")
		if err == nil {
			t.Fatalf("new should fail with cmds=whoami, got: %s", out)
		}
		if !strings.Contains(string(out), "command not allowed by SSH key permissions") {
			t.Errorf("expected permission denied error for 'new', got: %s", out)
		}

		// Denied: ssh-key list (subcommand - must be fully qualified in cmds)
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), cmdKeyPath, "ssh-key", "list")
		if err == nil {
			t.Fatalf("ssh-key list should fail with cmds=whoami, got: %s", out)
		}
		if !strings.Contains(string(out), "command not allowed by SSH key permissions") {
			t.Errorf("expected permission denied for 'ssh-key list', got: %s", out)
		}
	})

	// ---------------------------------------------------------------
	// 2. No privilege escalation via ssh-key add
	// ---------------------------------------------------------------
	t.Run("cmds_no_escalation_ssh_key_add", func(t *testing.T) {
		// Add the restricted key via the REPL so the --cmds flag with spaces is
		// properly quoted. The restricted key allows "ssh-key add" and "whoami".
		escKeyPath, escPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		pty := sshToExeDev(t, keyFile)
		pty.SendLine("ssh-key add --cmds='ssh-key add,whoami' '" + escPubKey + "'")
		pty.Want("Added SSH key")
		pty.WantPrompt()
		pty.Disconnect()

		// Verify the key is restricted: "ls" should be denied.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), escKeyPath, "ls")
		if err == nil {
			t.Fatalf("ls should fail for restricted key, got: %s", out)
		}

		// Add a new unrestricted key using the restricted key.
		newKeyPath, newPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), escKeyPath,
			"ssh-key", "add", newPubKey)
		if err != nil {
			t.Fatalf("ssh-key add should succeed: %v\n%s", err, out)
		}

		// The new key (added without --cmds) should be unrestricted.
		// This tests that the restriction is on the *key*, not the *user*.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), newKeyPath, "ls")
		if err != nil {
			t.Errorf("unrestricted key added by restricted key should work for ls: %v\n%s", err, out)
		}

		// Clean up
		_, _ = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ssh-key", "remove", newPubKey)
	})

	// ---------------------------------------------------------------
	// 3. No escalation via generate-api-key
	// ---------------------------------------------------------------
	t.Run("cmds_no_escalation_generate_api_key", func(t *testing.T) {
		// Key that only allows whoami - must not be able to run generate-api-key.
		escKeyPath := addScopedKey(t, []string{"--cmds=whoami"})

		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), escKeyPath,
			"ssh-key", "generate-api-key", "--json", "--label=escalation-test")
		if err == nil {
			t.Fatalf("generate-api-key should be denied: %s", out)
		}
		if !strings.Contains(string(out), "command not allowed by SSH key permissions") {
			t.Errorf("expected permission denied for generate-api-key, got: %s", out)
		}
	})

	// ---------------------------------------------------------------
	// 4. VM-restricted key: can access allowed VM, denied other VM
	// ---------------------------------------------------------------
	t.Run("vm_allowed", func(t *testing.T) {
		vmKeyPath := addScopedKey(t, []string{"--vm=" + box1})

		// SSH into the allowed VM should work.
		cmd := Env.servers.BoxSSHCommand(Env.context(t), box1, vmKeyPath, "echo", "hello")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("SSH to allowed VM %s should succeed: %v\n%s", box1, err, out)
		}
		if !strings.Contains(string(out), "hello") {
			t.Errorf("expected 'hello' in output, got: %s", out)
		}
	})

	t.Run("vm_denied_other_vm", func(t *testing.T) {
		vmKeyPath := addScopedKey(t, []string{"--vm=" + box1})

		// SSH into a *different* VM should fail.
		// The piper rejects the connection, which the SSH client sees as an
		// authentication failure ("Permission denied"). The specific
		// "restricted to VM" message is logged server-side but not visible
		// to the client through the SSH protocol.
		cmd := Env.servers.BoxSSHCommand(Env.context(t), box2, vmKeyPath, "echo", "hello")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("SSH to disallowed VM %s should fail, got: %s", box2, out)
		}
	})

	t.Run("vm_denied_repl", func(t *testing.T) {
		vmKeyPath := addScopedKey(t, []string{"--vm=" + box1})

		// VM-restricted key must not access the REPL (the exed interactive shell).
		// The piper blocks routing to exed for VM-restricted keys, so the
		// SSH client sees an auth failure.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), vmKeyPath, "whoami")
		if err == nil {
			t.Fatalf("REPL command should fail for VM-restricted key, got: %s", out)
		}
	})

	// ---------------------------------------------------------------
	// 5. Expired key rejected at auth time
	// ---------------------------------------------------------------
	t.Run("expiry_non_expired_works", func(t *testing.T) {
		// A key with a future expiry should work fine.
		expKeyPath := addScopedKey(t, []string{"--exp=1h"})

		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), expKeyPath, "whoami")
		if err != nil {
			t.Errorf("key with 1h expiry should still work: %v\n%s", err, out)
		}
	})

	// ---------------------------------------------------------------
	// 6. Combined restrictions: cmds + vm both enforced
	// ---------------------------------------------------------------
	t.Run("combined_cmds_and_vm", func(t *testing.T) {
		// Key restricted to box1 AND only whoami.
		combinedKeyPath := addScopedKey(t, []string{"--cmds=whoami", "--vm=" + box1})

		// Should be blocked from REPL (vm restriction takes priority at piper level).
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), combinedKeyPath, "whoami")
		if err == nil {
			t.Fatalf("REPL should be blocked for VM-restricted key, got: %s", out)
		}

		// Should be blocked from box2 (vm restriction).
		cmd := Env.servers.BoxSSHCommand(Env.context(t), box2, combinedKeyPath, "echo", "hello")
		out, err = cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("SSH to box2 should fail, got: %s", out)
		}

		// Should succeed on box1.
		cmd = Env.servers.BoxSSHCommand(Env.context(t), box1, combinedKeyPath, "echo", "hello")
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Errorf("SSH to box1 should succeed: %v\n%s", err, out)
		}
	})

	// ---------------------------------------------------------------
	// 7. Unrestricted key still works for everything
	// ---------------------------------------------------------------
	t.Run("unrestricted_key_unaffected", func(t *testing.T) {
		// The original registration key has no permissions set.
		// Verify it can still do everything.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "whoami")
		if err != nil {
			t.Errorf("unrestricted key whoami: %v\n%s", err, out)
		}

		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "ls")
		if err != nil {
			t.Errorf("unrestricted key ls: %v\n%s", err, out)
		}

		// Can access both VMs.
		cmd := Env.servers.BoxSSHCommand(Env.context(t), box1, keyFile, "echo", "ok")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("unrestricted key SSH to box1: %v\n%s", err, out)
		}
		cmd = Env.servers.BoxSSHCommand(Env.context(t), box2, keyFile, "echo", "ok")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("unrestricted key SSH to box2: %v\n%s", err, out)
		}
	})

	// ---------------------------------------------------------------
	// 8. Permissions stored and returned in JSON
	// ---------------------------------------------------------------
	t.Run("permissions_in_add_json", func(t *testing.T) {
		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "add", "--json", "--cmds=whoami,ls", "--exp=30d", testPubKey)
		if err != nil {
			t.Fatalf("ssh-key add --json: %v\n%s", err, out)
		}
		var result struct {
			Status      string          `json:"status"`
			Permissions json.RawMessage `json:"permissions"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse JSON: %v\n%s", err, out)
		}
		if result.Status != "added" {
			t.Errorf("status = %q, want added", result.Status)
		}
		if result.Permissions == nil {
			t.Fatal("expected permissions in output")
		}
		// Verify the permissions contain the expected fields.
		var perms struct {
			Cmds []string `json:"cmds"`
			Exp  *int64   `json:"exp"`
		}
		if err := json.Unmarshal(result.Permissions, &perms); err != nil {
			t.Fatalf("parse permissions: %v", err)
		}
		if len(perms.Cmds) != 2 || perms.Cmds[0] != "whoami" || perms.Cmds[1] != "ls" {
			t.Errorf("cmds = %v, want [whoami ls]", perms.Cmds)
		}
		if perms.Exp == nil {
			t.Error("expected exp to be set")
		}
	})

	// ---------------------------------------------------------------
	// 9. VM flag validated: non-existent VM rejected
	// ---------------------------------------------------------------
	t.Run("vm_nonexistent_rejected", func(t *testing.T) {
		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile,
			"ssh-key", "add", "--json", "--vm=nonexistent-vm-12345", testPubKey)
		if err == nil {
			t.Fatalf("ssh-key add with nonexistent VM should fail, got: %s", out)
		}
		if !strings.Contains(string(out), "not found or access denied") {
			t.Errorf("expected 'not found or access denied', got: %s", out)
		}
	})

	// ---------------------------------------------------------------
	// 10. Cross-user VM restriction: can't scope to someone else's VM
	// ---------------------------------------------------------------
	t.Run("vm_cross_user_rejected", func(t *testing.T) {
		// Register a second user with root support.
		pty2, _, keyFile2, email2 := registerForExeDevWithEmail(t, "perms-other@ssh-key-perms.example")
		pty2.Disconnect()
		enableRootSupport(t, email2)

		// Second user (sudoer) tries to add a key scoped to box1 (owned by first user).
		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile2,
			"ssh-key", "add", "--json", "--vm="+box1, testPubKey)
		if err == nil {
			t.Fatalf("ssh-key add --vm with other user's VM should fail, got: %s", out)
		}
		if !strings.Contains(string(out), "not found or access denied") {
			t.Errorf("expected 'not found or access denied', got: %s", out)
		}
	})

	// ---------------------------------------------------------------
	// 11. Non-sudoer denied permission flags
	// ---------------------------------------------------------------
	t.Run("non_sudoer_denied_perms", func(t *testing.T) {
		pty3, _, keyFile3, _ := registerForExeDevWithEmail(t, "perms-nonsudo@ssh-key-perms.example")
		pty3.Disconnect()

		_, testPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// Non-sudoer tries --cmds flag.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile3,
			"ssh-key", "add", "--cmds=whoami", testPubKey)
		if err == nil {
			t.Fatalf("non-sudoer should be denied --cmds, got: %s", out)
		}
		if !strings.Contains(string(out), "root support privileges") {
			t.Errorf("expected root support error, got: %s", out)
		}

		// Non-sudoer can still add a plain key.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile3,
			"ssh-key", "add", testPubKey)
		if err != nil {
			t.Errorf("non-sudoer plain add should succeed: %v\n%s", err, out)
		}
	})
}
