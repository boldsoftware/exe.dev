package e1e

import (
	"strings"
	"testing"
)

// TestTeamTransfer tests the "team transfer" command end-to-end, including
// adversarial cases for permission checks and edge cases.
func TestTeamTransfer(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Setup: owner, member1 (sudoer), member2 (regular user)
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-team-transfer.example")
	member1PTY, _, member1KeyFile, member1Email := registerForExeDevWithEmail(t, "member1@test-team-transfer.example")
	member2PTY, _, member2KeyFile, member2Email := registerForExeDevWithEmail(t, "member2@test-team-transfer.example")
	ownerPTY.Disconnect()
	member1PTY.Disconnect()
	member2PTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_xfer_e2e", "XferTeam", ownerEmail)
	addTeamMember(t, "team_xfer_e2e", member1Email)
	addTeamMember(t, "team_xfer_e2e", member2Email)

	// Promote member1 to sudoer so they can also use transfer
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), ownerKeyFile, "team", "promote", "team_xfer_e2e", member1Email, "sudoer")
	if err != nil {
		t.Fatalf("promote member1 failed: %v\n%s", err, out)
	}

	// Member1 creates a VM
	member1PTY = sshToExeDev(t, member1KeyFile)
	member1Box := newBox(t, member1PTY)
	member1PTY.Disconnect()
	waitForSSH(t, member1Box, member1KeyFile)

	// --- Adversarial: regular member cannot use transfer ---
	t.Run("RegularMemberCannotTransfer", func(t *testing.T) {
		repl := sshToExeDev(t, member2KeyFile)
		repl.SendLine("team transfer " + member1Box + " " + ownerEmail)
		repl.Want("command not available")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Adversarial: transfer to non-team member fails ---
	t.Run("TransferToNonTeamMember", func(t *testing.T) {
		outsiderPTY, _, _, outsiderEmail := registerForExeDevWithEmail(t, "outsider@test-team-transfer.example")
		outsiderPTY.Disconnect()

		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team transfer " + member1Box + " " + outsiderEmail)
		repl.Want("not in this team")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Adversarial: transfer to self (current owner) is a no-op ---
	t.Run("TransferToSelfRejected", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team transfer " + member1Box + " " + member1Email)
		repl.Want("already owned")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Adversarial: transfer nonexistent VM ---
	t.Run("TransferNonexistentVM", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team transfer nonexistent-vm-xyz " + member2Email)
		repl.Want("not found")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Adversarial: removal blocked before transfer ---
	t.Run("RemovalBlockedWithVMs", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team remove " + member1Email)
		repl.Want("still have")
		repl.Want("VM")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Happy path: transfer VM from member1 to member2 ---
	t.Run("TransferToMember2", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team transfer " + member1Box + " " + member2Email)
		repl.Want("Transferred")
		repl.Want(member2Email)
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Verify new owner sees the VM ---
	t.Run("NewOwnerSeesVM", func(t *testing.T) {
		repl := sshToExeDev(t, member2KeyFile)
		repl.SendLine("ls")
		repl.Want(member1Box)
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Verify old owner no longer sees the VM ---
	t.Run("OldOwnerLostVM", func(t *testing.T) {
		repl := sshToExeDev(t, member1KeyFile)
		repl.SendLine("ls")
		repl.Reject(member1Box)
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- New owner can SSH into the transferred VM ---
	t.Run("NewOwnerCanSSH", func(t *testing.T) {
		pty := sshToBox(t, member1Box, member2KeyFile)
		pty.SendLine("echo transfer-ok")
		pty.Want("transfer-ok")
		pty.Disconnect()
	})

	// --- Removal succeeds after transfer ---
	t.Run("RemovalSucceedsAfterTransfer", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team remove " + member1Email)
		repl.Want("Removed")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Double transfer: member2 → owner ---
	t.Run("DoubleTransfer", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team transfer " + member1Box + " " + ownerEmail)
		repl.Want("Transferred")
		repl.Want(ownerEmail)
		repl.WantPrompt()
		repl.Disconnect()
	})

	// --- Owner can SSH after double transfer ---
	t.Run("OwnerCanSSHAfterDoubleTransfer", func(t *testing.T) {
		pty := sshToBox(t, member1Box, ownerKeyFile)
		pty.SendLine("echo double-transfer-ok")
		pty.Want("double-transfer-ok")
		pty.Disconnect()
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("rm " + member1Box)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamUnenrollForce tests that root's "team unenroll --force" bypasses
// the VM check, while plain "team unenroll" is blocked.
func TestTeamUnenrollForce(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-unenroll-force.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-unenroll-force.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_unenroll_f", "UnenrollTeam", ownerEmail)
	addTeamMember(t, "team_unenroll_f", memberEmail)

	// Member creates a VM
	repl := sshToExeDev(t, memberKeyFile)
	box := newBox(t, repl)
	repl.Disconnect()
	waitForSSH(t, box, memberKeyFile)

	// Plain unenroll is blocked
	t.Run("UnenrollBlockedWithVMs", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), ownerKeyFile, "team", "unenroll", "team_unenroll_f", memberEmail)
		if err == nil {
			t.Fatalf("expected unenroll to fail, got: %s", out)
		}
		if !strings.Contains(string(out), "still have") {
			t.Fatalf("expected 'still have' error, got: %s", out)
		}
	})

	// Force unenroll succeeds
	t.Run("ForceUnenrollSucceeds", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), ownerKeyFile, "team", "unenroll", "team_unenroll_f", memberEmail, "--force")
		if err != nil {
			t.Fatalf("force unenroll failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Removed") {
			t.Fatalf("expected 'Removed' in output, got: %s", out)
		}
	})

	// Cleanup: delete the now-orphaned VM
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("rm " + box)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamTransferCleansShares tests that transferring a box cleans up
// individual shares and team shares, so stale permissions don't persist.
func TestTeamTransferCleansShares(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-xfer-shares.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-xfer-shares.example")
	recipientPTY, _, recipientKeyFile, recipientEmail := registerForExeDevWithEmail(t, "recipient@test-xfer-shares.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()
	recipientPTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_xfer_sh", "XferShareTeam", ownerEmail)
	addTeamMember(t, "team_xfer_sh", memberEmail)
	addTeamMember(t, "team_xfer_sh", recipientEmail)

	// Member creates a VM
	memberPTY = sshToExeDev(t, memberKeyFile)
	box := newBox(t, memberPTY)
	memberPTY.Disconnect()
	waitForSSH(t, box, memberKeyFile)

	// Member shares the box with team
	t.Run("SetupShares", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("share add " + box + " team")
		repl.Want("Shared")
		repl.WantPrompt()

		// Also share with owner individually
		repl.SendLine("share add " + box + " " + ownerEmail)
		repl.Want("Invitation sent")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Transfer the box to recipient
	t.Run("TransferBox", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team transfer " + box + " " + recipientEmail)
		repl.Want("Transferred")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// After transfer, shares should be cleaned up — share show should be empty
	t.Run("SharesCleanedUp", func(t *testing.T) {
		repl := sshToExeDev(t, recipientKeyFile)
		repl.SendLine("share show " + box)
		// Should NOT show the old individual or team shares
		repl.Reject(ownerEmail)
		repl.Reject("XferShareTeam")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, recipientKeyFile)
		repl.SendLine("rm " + box)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})
}
