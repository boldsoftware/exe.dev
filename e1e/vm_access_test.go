// This file tests vm+vmname SSH access — the name-based equivalent of
// ssh vmname.exe.xyz (IP-shard-based routing).

package e1e

import (
	"fmt"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestVMAccess verifies that `ssh vm+vmname@exe.dev` has the same auth
// semantics as `ssh vmname.exe.xyz`:
//
//  1. Owner can reach their own VM.
//  2. Team admin can reach a member's VM.
//  3. Regular team member CANNOT reach another member's VM (no team_ssh).
//  4. Regular team member CAN reach a VM when the owner enables team_ssh.
//  5. Non-team-member is always denied.
func TestVMAccess(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	noGolden(t)

	// Register users: team owner (admin), team member, outsider
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-vm-access.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-vm-access.example")
	outsiderPTY, _, outsiderKeyFile, _ := registerForExeDevWithEmail(t, "outsider@test-vm-access.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()
	outsiderPTY.Disconnect()

	// Create a team; owner is billing_owner (admin).
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "vm_access_e2e", "VMAccessTest", ownerEmail)
	addTeamMember(t, "vm_access_e2e", memberEmail)

	// Member creates a VM.
	memberPTY = sshToExeDev(t, memberKeyFile)
	box := newBox(t, memberPTY, testinfra.BoxOpts{Command: "/bin/bash"})
	memberPTY.WantPrompt()
	memberPTY.Disconnect()

	waitForSSH(t, box, memberKeyFile)

	// Create a test file for verification.
	createTestFile := boxSSHCommand(t, box, memberKeyFile, "echo", "vm-access-marker", ">", "/home/exedev/vm-test.txt")
	if out, err := createTestFile.CombinedOutput(); err != nil {
		t.Fatalf("failed to create test file: %v\n%s", err, out)
	}

	vmBox := "vm+" + box

	// 1. Owner (team admin) can reach member's VM via vm+.
	t.Run("admin_granted", func(t *testing.T) {
		cmd := boxSSHCommand(t, vmBox, ownerKeyFile, "cat", "/home/exedev/vm-test.txt")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("expected SSH to succeed for team admin: %v\n%s", err, out)
		}
		if got := string(out); got != "vm-access-marker\n" {
			t.Errorf("unexpected output: got %q, want %q", got, "vm-access-marker\n")
		}
	})

	// 2. Member can reach their own VM via vm+.
	t.Run("owner_self_access", func(t *testing.T) {
		cmd := boxSSHCommand(t, vmBox, memberKeyFile, "cat", "/home/exedev/vm-test.txt")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("expected SSH to succeed for VM owner: %v\n%s", err, out)
		}
		if got := string(out); got != "vm-access-marker\n" {
			t.Errorf("unexpected output: got %q, want %q", got, "vm-access-marker\n")
		}
	})

	// 3. Outsider (not in team) is denied.
	t.Run("outsider_denied", func(t *testing.T) {
		cmd := boxSSHCommand(t, vmBox, outsiderKeyFile, "true")
		if err := cmd.Run(); err == nil {
			t.Errorf("expected SSH to fail for non-team-member")
		}
	})

	// 4. Owner creates a VM; member can't reach it without team_ssh,
	//    then can after team_ssh is enabled.
	ownerPTY = sshToExeDev(t, ownerKeyFile)
	ownerBox := newBox(t, ownerPTY, testinfra.BoxOpts{Command: "/bin/bash"})
	ownerPTY.WantPrompt()
	ownerPTY.Disconnect()

	waitForSSH(t, ownerBox, ownerKeyFile)

	createOwnerFile := boxSSHCommand(t, ownerBox, ownerKeyFile, "echo", "owner-vm-marker", ">", "/home/exedev/owner-test.txt")
	if out, err := createOwnerFile.CombinedOutput(); err != nil {
		t.Fatalf("failed to create test file: %v\n%s", err, out)
	}

	vmOwnerBox := "vm+" + ownerBox

	// 4a. Member cannot reach owner's VM without team_ssh.
	t.Run("member_denied_no_team_ssh", func(t *testing.T) {
		cmd := boxSSHCommand(t, vmOwnerBox, memberKeyFile, "true")
		if err := cmd.Run(); err == nil {
			t.Errorf("expected SSH to fail for team member without team_ssh")
		}
	})

	// Enable team_ssh on owner's box.
	ownerPTY = sshToExeDev(t, ownerKeyFile)
	ownerPTY.SendLine(fmt.Sprintf("share access allow %s", ownerBox))
	ownerPTY.Want("Route updated successfully")
	ownerPTY.WantPrompt()
	ownerPTY.Disconnect()

	// 4b. Member CAN reach owner's VM after team_ssh enabled.
	t.Run("member_granted_team_ssh", func(t *testing.T) {
		cmd := boxSSHCommand(t, vmOwnerBox, memberKeyFile, "cat", "/home/exedev/owner-test.txt")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("expected SSH to succeed for team member with team_ssh: %v\n%s", err, out)
		}
		if got := string(out); got != "owner-vm-marker\n" {
			t.Errorf("unexpected output: got %q, want %q", got, "owner-vm-marker\n")
		}
	})

	// Cleanup
	cleanupBox(t, memberKeyFile, box)
	cleanupBox(t, ownerKeyFile, ownerBox)
}
