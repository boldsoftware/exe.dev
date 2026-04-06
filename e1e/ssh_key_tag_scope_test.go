package e1e

import (
	"encoding/json"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestSSHKeyTagScope tests the security boundaries of tag-scoped SSH keys.
// A tag-scoped key can only see, create, delete, and SSH into VMs carrying
// the designated tag. This test exercises every enforcement point end-to-end.
func TestSSHKeyTagScope(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 4)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// ── Setup ──────────────────────────────────────────────────────
	// Register user, create pre-existing VMs, enable root support.
	pty, _, rootKey, email := registerForExeDev(t)

	// pre-existing VMs: one tagged "ci", one untagged.
	preTagged := newBox(t, pty)
	preUntagged := newBox(t, pty)
	pty.Disconnect()

	enableRootSupport(t, email)

	waitForSSH(t, preTagged, rootKey)
	waitForSSH(t, preUntagged, rootKey)

	// Tag the first VM.
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "tag", preTagged, "ci")
	if err != nil {
		t.Fatalf("tag %s ci: %v\n%s", preTagged, err, out)
	}

	// Helper: parse ls --json output into a list of VM names.
	type lsVM struct {
		VMName string   `json:"vm_name"`
		Tags   []string `json:"tags"`
	}
	parseLs := func(t *testing.T, data []byte) []lsVM {
		t.Helper()
		var result struct {
			VMs []lsVM `json:"vms"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("parse ls JSON: %v\n%s", err, data)
		}
		return result.VMs
	}

	// Helper: check if a VM name appears in ls output.
	containsVM := func(vms []lsVM, name string) bool {
		for _, v := range vms {
			if v.VMName == name {
				return true
			}
		}
		return false
	}

	// Helper: add a scoped SSH key.
	addScopedKey := func(t *testing.T, flags []string) string {
		t.Helper()
		keyPath, pubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatalf("GenSSHKey: %v", err)
		}
		args := []string{"ssh-key", "add", "--json"}
		args = append(args, flags...)
		args = append(args, pubKey)
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, args...)
		if err != nil {
			t.Fatalf("ssh-key add %v: %v\n%s", flags, err, out)
		}
		var result struct{ Status string }
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse: %v\n%s", err, out)
		}
		if result.Status != "added" {
			t.Fatalf("expected status added, got %q", result.Status)
		}
		return keyPath
	}

	// Create the tag-scoped key.
	tagKey := addScopedKey(t, []string{"--tag=ci"})

	// ── 1. ls: only tagged VMs visible ─────────────────────────────
	t.Run("ls_shows_only_tagged_vms", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls --json: %v\n%s", err, out)
		}
		vms := parseLs(t, out)
		if !containsVM(vms, preTagged) {
			t.Errorf("tagged VM %s should be visible", preTagged)
		}
		if containsVM(vms, preUntagged) {
			t.Errorf("untagged VM %s should NOT be visible", preUntagged)
		}
	})

	// ── 2. rm: denied for untagged VMs ─────────────────────────────
	t.Run("rm_denied_for_untagged_vm", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "rm", preUntagged)
		// rm itself may exit 0 (it collects per-VM errors), so check output.
		_ = err
		if !strings.Contains(string(out), "not found") {
			t.Errorf("expected tag restriction error, got: %s", out)
		}
		// Verify the VM still exists (root key sees it).
		rootOut, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "ls", "--json")
		if err != nil {
			t.Fatalf("root ls: %v\n%s", err, rootOut)
		}
		if !containsVM(parseLs(t, rootOut), preUntagged) {
			t.Errorf("%s should still exist after denied rm", preUntagged)
		}
	})

	// ── 3. tag command: completely blocked ──────────────────────────
	t.Run("tag_add_blocked", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "tag", preTagged, "extra")
		if err == nil {
			t.Fatalf("tag add should fail for tag-scoped key, got: %s", out)
		}
		if !strings.Contains(string(out), "cannot modify tags") {
			t.Errorf("expected 'cannot modify tags' error, got: %s", out)
		}
	})

	t.Run("tag_remove_blocked", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "tag", "-d", preTagged, "ci")
		if err == nil {
			t.Fatalf("tag -d should fail for tag-scoped key, got: %s", out)
		}
		if !strings.Contains(string(out), "cannot modify tags") {
			t.Errorf("expected 'cannot modify tags' error, got: %s", out)
		}
	})

	// ── 4. SSH into VMs: allowed for tagged, denied for untagged ───
	t.Run("ssh_into_tagged_vm", func(t *testing.T) {
		cmd := Env.servers.BoxSSHCommand(Env.context(t), preTagged, tagKey, "echo", "hello-tag")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("SSH to tagged VM %s should succeed: %v\n%s", preTagged, err, out)
		}
		if !strings.Contains(string(out), "hello-tag") {
			t.Errorf("expected 'hello-tag', got: %s", out)
		}
	})

	t.Run("ssh_into_untagged_vm_denied", func(t *testing.T) {
		cmd := Env.servers.BoxSSHCommand(Env.context(t), preUntagged, tagKey, "echo", "hello")
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("SSH to untagged VM %s should fail, got: %s", preUntagged, out)
		}
	})

	// ── 5. new: auto-tags the created VM ────────────────────────────
	var newVMName string
	t.Run("new_auto_tags", func(t *testing.T) {
		reserveVMs(t, 1)
		pty := sshToExeDev(t, tagKey)
		newVMName = newBox(t, pty)
		pty.Disconnect()

		// Verify the VM is visible via the tag key's ls.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls: %v\n%s", err, out)
		}
		vms := parseLs(t, out)
		if !containsVM(vms, newVMName) {
			t.Fatalf("new VM %s should be visible via tag key", newVMName)
		}

		// Verify the VM has the "ci" tag (root key sees full details).
		rootOut, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "ls", "--json")
		if err != nil {
			t.Fatalf("root ls: %v\n%s", err, rootOut)
		}
		for _, vm := range parseLs(t, rootOut) {
			if vm.VMName == newVMName {
				found := false
				for _, tag := range vm.Tags {
					if tag == "ci" {
						found = true
					}
				}
				if !found {
					t.Errorf("new VM %s should have tag 'ci', has %v", newVMName, vm.Tags)
				}
				if len(vm.Tags) != 1 {
					t.Errorf("new VM %s should have exactly 1 tag, has %v", newVMName, vm.Tags)
				}
				break
			}
		}
	})

	// ── 6. rm: allowed for tagged VMs ───────────────────────────────
	t.Run("rm_allowed_for_tagged_vm", func(t *testing.T) {
		if newVMName == "" {
			t.Skip("new_auto_tags did not run")
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "rm", newVMName)
		if err != nil {
			t.Fatalf("rm tagged VM should succeed: %v\n%s", err, out)
		}
		// Verify it's gone.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls: %v\n%s", err, out)
		}
		if containsVM(parseLs(t, out), newVMName) {
			t.Errorf("%s should be gone after rm", newVMName)
		}
	})

	// ── 7. Tag removed by root → VM becomes invisible ──────────────
	t.Run("tag_removed_becomes_invisible", func(t *testing.T) {
		// Remove the "ci" tag from preTagged using the root key.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "tag", "-d", preTagged, "ci")
		if err != nil {
			t.Fatalf("root tag -d: %v\n%s", err, out)
		}

		// Now the tag key should no longer see preTagged.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls: %v\n%s", err, out)
		}
		if containsVM(parseLs(t, out), preTagged) {
			t.Errorf("%s should be invisible after tag removal", preTagged)
		}

		// SSH should also be denied now.
		cmd := Env.servers.BoxSSHCommand(Env.context(t), preTagged, tagKey, "echo", "hello")
		sshOut, sshErr := cmd.CombinedOutput()
		if sshErr == nil {
			t.Errorf("SSH to de-tagged VM should fail, got: %s", sshOut)
		}

		// Restore the tag for remaining tests.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "tag", preTagged, "ci")
		if err != nil {
			t.Fatalf("root re-tag: %v\n%s", err, out)
		}
	})

	// ── 8. VM created by root without the tag: invisible to tag key ─
	var rootOnlyVM string
	t.Run("root_vm_without_tag_invisible", func(t *testing.T) {
		reserveVMs(t, 1)
		pty := sshToExeDev(t, rootKey)
		rootOnlyVM = newBox(t, pty)
		pty.Disconnect()

		// Root's new VM has no tag; tag key can't see it.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls: %v\n%s", err, out)
		}
		if containsVM(parseLs(t, out), rootOnlyVM) {
			t.Errorf("%s (no tag) should be invisible to tag key", rootOnlyVM)
		}

		// rm should be denied.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "rm", rootOnlyVM)
		_ = err
		if !strings.Contains(string(out), "not found") {
			t.Errorf("expected tag restriction for rm, got: %s", out)
		}

		// SSH should be denied.
		waitForSSH(t, rootOnlyVM, rootKey)
		cmd := Env.servers.BoxSSHCommand(Env.context(t), rootOnlyVM, tagKey, "echo", "hello")
		sshOut, sshErr := cmd.CombinedOutput()
		if sshErr == nil {
			t.Errorf("SSH to untagged root VM should fail, got: %s", sshOut)
		}
	})

	// ── 9. Root tags the rootOnlyVM → it becomes visible to tag key ─
	t.Run("root_adds_tag_makes_visible", func(t *testing.T) {
		if rootOnlyVM == "" {
			t.Skip("root_vm_without_tag_invisible did not run")
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "tag", rootOnlyVM, "ci")
		if err != nil {
			t.Fatalf("root tag: %v\n%s", err, out)
		}

		// Now visible.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls: %v\n%s", err, out)
		}
		if !containsVM(parseLs(t, out), rootOnlyVM) {
			t.Errorf("%s should now be visible after tagging", rootOnlyVM)
		}

		// SSH should work now.
		cmd := Env.servers.BoxSSHCommand(Env.context(t), rootOnlyVM, tagKey, "echo", "hello-tag")
		sshOut, sshErr := cmd.CombinedOutput()
		if sshErr != nil {
			t.Errorf("SSH to now-tagged VM should succeed: %v\n%s", sshErr, sshOut)
		}
	})

	// ── 10. restart: allowed for tagged, denied for untagged ────────
	t.Run("restart_tagged_allowed", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "restart", preTagged)
		if err != nil {
			t.Errorf("restart tagged VM should succeed: %v\n%s", err, out)
		}
		// Wait for SSH to come back.
		waitForSSH(t, preTagged, rootKey)
	})

	t.Run("restart_untagged_denied", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "restart", preUntagged)
		if err == nil {
			t.Fatalf("restart untagged VM should fail, got: %s", out)
		}
		if !strings.Contains(string(out), "not found") {
			t.Errorf("expected tag restriction error, got: %s", out)
		}
	})

	// ── 11. lock/unlock: denied for untagged ────────────────────────
	t.Run("lock_untagged_denied", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "lock", preUntagged, "testing")
		if err == nil {
			t.Fatalf("lock untagged VM should fail, got: %s", out)
		}
		if !strings.Contains(string(out), "not found") {
			t.Errorf("expected tag restriction, got: %s", out)
		}
	})

	// ── 12. rename: denied for untagged ─────────────────────────────
	t.Run("rename_untagged_denied", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "rename", preUntagged, "sneaky-name")
		if err == nil {
			t.Fatalf("rename untagged VM should fail, got: %s", out)
		}
		if !strings.Contains(string(out), "not found") {
			t.Errorf("expected tag restriction, got: %s", out)
		}
	})

	// ── 13. Unrestricted commands still work with tag key ───────────
	t.Run("whoami_works", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "whoami")
		if err != nil {
			t.Errorf("whoami should work for tag-scoped key: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), email) {
			t.Errorf("whoami should show email %s, got: %s", email, out)
		}
	})

	t.Run("ssh_key_list_works", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "ssh-key", "list", "--json")
		if err != nil {
			t.Errorf("ssh-key list should work for tag-scoped key: %v\n%s", err, out)
		}
	})

	// ── 14. --vm and --tag mutually exclusive ────────────────────────
	t.Run("vm_and_tag_mutually_exclusive", func(t *testing.T) {
		_, pubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey,
			"ssh-key", "add", "--json", "--vm="+preTagged, "--tag=ci", pubKey)
		if err == nil {
			t.Fatalf("--vm + --tag should be rejected, got: %s", out)
		}
		if !strings.Contains(string(out), "mutually exclusive") {
			t.Errorf("expected 'mutually exclusive', got: %s", out)
		}
	})

	// ── 15. Non-sudoer cannot use --tag ─────────────────────────────
	t.Run("non_sudoer_denied_tag", func(t *testing.T) {
		pty3, _, keyFile3, _ := registerForExeDevWithEmail(t, "tag-nonsudo@ssh-key-tag.example")
		pty3.Disconnect()

		_, pubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile3,
			"ssh-key", "add", "--tag=ci", pubKey)
		if err == nil {
			t.Fatalf("non-sudoer should be denied --tag, got: %s", out)
		}
		if !strings.Contains(string(out), "root support privileges") {
			t.Errorf("expected root support error, got: %s", out)
		}
	})

	// ── 16. Tag key with combined --cmds restriction ────────────────
	t.Run("tag_plus_cmds", func(t *testing.T) {
		// Create a key scoped to tag "ci" and only allowing ls,whoami.
		comboKey := addScopedKey(t, []string{"--tag=ci", "--cmds=ls,whoami"})

		// ls works (allowed by cmds, scoped to ci).
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), comboKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls should work: %v\n%s", err, out)
		}
		vms := parseLs(t, out)
		if !containsVM(vms, preTagged) {
			t.Errorf("tagged VM should be visible")
		}
		if containsVM(vms, preUntagged) {
			t.Errorf("untagged VM should NOT be visible")
		}

		// rm is denied by cmds restriction.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), comboKey, "rm", preTagged)
		if err == nil {
			t.Fatalf("rm should be denied by cmds, got: %s", out)
		}
		if !strings.Contains(string(out), "command not allowed by SSH key permissions") {
			t.Errorf("expected command not allowed error, got: %s", out)
		}

		// new is denied by cmds restriction.
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), comboKey, "new")
		if err == nil {
			t.Fatalf("new should be denied by cmds, got: %s", out)
		}
	})

	// ── 17. Restriction inheritance: new key inherits caller's tag ──
	t.Run("restriction_inheritance_tag", func(t *testing.T) {
		// Create a tag key that also allows ssh-key add.
		escKey := addScopedKey(t, []string{"--tag=ci", "--cmds=ssh-key,ls"})

		// Add a key without explicit flags — it should inherit the ci tag.
		newKeyPath, newPubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), escKey,
			"ssh-key", "add", "--json", newPubKey)
		if err != nil {
			t.Fatalf("ssh-key add should succeed: %v\n%s", err, out)
		}

		// Verify the new key's permissions include the inherited tag.
		var addResult struct {
			Permissions struct {
				Tag  string   `json:"tag"`
				Cmds []string `json:"cmds"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(out, &addResult); err != nil {
			t.Fatalf("parse: %v\n%s", err, out)
		}
		if addResult.Permissions.Tag != "ci" {
			t.Errorf("expected inherited tag=ci, got %q", addResult.Permissions.Tag)
		}
		// cmds should also be inherited from the caller.
		if len(addResult.Permissions.Cmds) == 0 {
			t.Error("expected inherited cmds")
		}

		// The new key sees only ci-tagged VMs (not all VMs).
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), newKeyPath, "ls", "--json")
		if err != nil {
			t.Fatalf("ls with new key: %v\n%s", err, out)
		}
		vms := parseLs(t, out)
		if !containsVM(vms, preTagged) {
			t.Errorf("inherited key should see tagged VM")
		}
		if containsVM(vms, preUntagged) {
			t.Errorf("inherited key should NOT see untagged VM")
		}
	})

	// ── 17b. Cannot escalate by specifying a different tag ────────
	t.Run("restriction_inheritance_different_tag_rejected", func(t *testing.T) {
		escKey := addScopedKey(t, []string{"--tag=ci", "--cmds=ssh-key,ls"})

		_, pubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// Try to add a key scoped to a different tag.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), escKey,
			"ssh-key", "add", "--tag=deploy", pubKey)
		if err == nil {
			t.Fatalf("should reject different tag, got: %s", out)
		}
		if !strings.Contains(string(out), "scoped to tag") {
			t.Errorf("expected 'scoped to tag' error, got: %s", out)
		}
	})

	// ── 17c. Cmds-restricted key cannot grant commands it doesn't have ─
	t.Run("restriction_inheritance_cmds_superset_rejected", func(t *testing.T) {
		escKey := addScopedKey(t, []string{"--tag=ci", "--cmds=ssh-key,ls"})

		_, pubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// Try to grant rm (which the caller doesn't have).
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), escKey,
			"ssh-key", "add", "--cmds=ls,rm", pubKey)
		if err == nil {
			t.Fatalf("should reject superset cmds, got: %s", out)
		}
		if !strings.Contains(string(out), "does not allow command") {
			t.Errorf("expected 'does not allow command' error, got: %s", out)
		}
	})

	// ── 18. Permissions stored correctly in JSON output ─────────────
	t.Run("permissions_in_json", func(t *testing.T) {
		_, pubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey,
			"ssh-key", "add", "--json", "--tag=staging", pubKey)
		if err != nil {
			t.Fatalf("ssh-key add: %v\n%s", err, out)
		}
		var result struct {
			Permissions struct {
				Tag string `json:"tag"`
			} `json:"permissions"`
		}
		if err := json.Unmarshal(out, &result); err != nil {
			t.Fatalf("parse: %v\n%s", err, out)
		}
		if result.Permissions.Tag != "staging" {
			t.Errorf("expected tag=staging, got %q", result.Permissions.Tag)
		}
	})

	// ── 19. Invalid tag name rejected ───────────────────────────────
	t.Run("invalid_tag_rejected", func(t *testing.T) {
		_, pubKey, err := testinfra.GenSSHKey(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey,
			"ssh-key", "add", "--json", "--tag=INVALID", pubKey)
		if err == nil {
			t.Fatalf("invalid tag should be rejected, got: %s", out)
		}
		if !strings.Contains(string(out), "invalid tag name") {
			t.Errorf("expected invalid tag error, got: %s", out)
		}
	})

	// ── 20. Different tag scope sees different VMs ──────────────────
	t.Run("different_tag_isolation", func(t *testing.T) {
		// Add tag "deploy" to preUntagged using root.
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "tag", preUntagged, "deploy")
		if err != nil {
			t.Fatalf("root tag deploy: %v\n%s", err, out)
		}

		// Create a key scoped to "deploy".
		deployKey := addScopedKey(t, []string{"--tag=deploy"})

		// deploy key sees preUntagged (which now has "deploy"), not preTagged ("ci").
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), deployKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls: %v\n%s", err, out)
		}
		vms := parseLs(t, out)
		if !containsVM(vms, preUntagged) {
			t.Errorf("deploy key should see %s (has deploy tag)", preUntagged)
		}
		if containsVM(vms, preTagged) {
			t.Errorf("deploy key should NOT see %s (has ci tag only)", preTagged)
		}

		// ci key should NOT see the deploy-tagged VM (preUntagged has "deploy", not "ci").
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), tagKey, "ls", "--json")
		if err != nil {
			t.Fatalf("ls: %v\n%s", err, out)
		}
		vms = parseLs(t, out)
		if containsVM(vms, preUntagged) {
			t.Errorf("ci key should NOT see %s (has deploy, not ci)", preUntagged)
		}
		if !containsVM(vms, preTagged) {
			t.Errorf("ci key should see %s (has ci tag)", preTagged)
		}

		// Clean up: remove deploy tag.
		_, _ = Env.servers.RunExeDevSSHCommand(Env.context(t), rootKey, "tag", "-d", preUntagged, "deploy")
	})
}
