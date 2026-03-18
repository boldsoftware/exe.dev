// Adversarial tests for the team self-service commands (team enable / team disable).

package e1e

import (
	"strings"
	"testing"
)

// TestTeamEnableDisableLifecycle tests the full self-service lifecycle:
// enable a team, verify it works, disable it, verify cleanup, re-enable.
func TestTeamEnableDisableLifecycle(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-self-service.example")
	ownerPTY.Disconnect()

	// Enable: happy path
	t.Run("Enable", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team enable")
		repl.Want("Teams lets you:")
		repl.Want("billing owner")
		repl.SendLine("yes")
		repl.Want("Team name:")
		repl.SendLine("Self Service Squad")
		repl.Want("created")
		repl.Want("tm_")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Verify team info shows correctly
	t.Run("VerifyEnabled", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team")
		repl.Want("Self Service Squad")
		repl.Want("billing_owner")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Enable again should fail — already in a team
	t.Run("EnableWhileInTeam", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team enable")
		repl.Want("command not available")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Disable: happy path (team is empty — only the owner)
	t.Run("Disable", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team disable")
		repl.Want("Disabling team")
		repl.Want("Self Service Squad")
		repl.SendLine("yes")
		repl.Want("has been disabled")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Verify team is gone
	t.Run("VerifyDisabled", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team")
		repl.Want("not part of a team")
		repl.Want("team enable")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Re-enable after disable
	t.Run("ReEnable", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team enable")
		repl.Want("Teams lets you:")
		repl.SendLine("yes")
		repl.Want("Team name:")
		repl.SendLine("Self Service Squad")
		repl.Want("created")
		repl.WantPrompt()

		// Verify
		repl.SendLine("team")
		repl.Want("Self Service Squad")
		repl.WantPrompt()

		// Clean up — disable again
		repl.SendLine("team disable")
		repl.Want("Disabling team")
		repl.SendLine("yes")
		repl.Want("has been disabled")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamEnableCancel tests that declining the confirmation prompt cancels cleanly.
func TestTeamEnableCancel(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	userPTY, _, userKeyFile, _ := registerForExeDevWithEmail(t, "cancel@test-enable-cancel.example")
	userPTY.Disconnect()

	t.Run("DeclineConfirmation", func(t *testing.T) {
		repl := sshToExeDev(t, userKeyFile)
		repl.SendLine("team enable")
		repl.Want("Enable teams?")
		repl.SendLine("no")
		repl.Want("Cancelled")
		repl.WantPrompt()

		// Verify no team was created
		repl.SendLine("team")
		repl.Want("not part of a team")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamDisableRefusedWithMembers tests that disabling a team with other members
// is refused and provides guidance to remove them first.
func TestTeamDisableRefusedWithMembers(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-disable-members.example")
	memberPTY, _, _, memberEmail := registerForExeDevWithEmail(t, "member@test-disable-members.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()

	// Create team and add member
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_disable_members", "DisableMembersTeam", ownerEmail)
	addTeamMember(t, "team_disable_members", memberEmail)

	// Try to disable — should be refused
	t.Run("RefusedWithMembers", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team disable")
		repl.Want("1 other member")
		repl.Want("team remove")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Remove member, then disable should work
	t.Run("SucceedsAfterRemoval", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team remove " + memberEmail)
		repl.Want("Removed")
		repl.WantPrompt()

		repl.SendLine("team disable")
		repl.Want("Disabling team")
		repl.SendLine("yes")
		repl.Want("has been disabled")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamDisableNonOwnerDenied tests that regular members and admins cannot
// run team disable — only billing owners can.
func TestTeamDisableNonOwnerDenied(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-disable-nonowner.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-disable-nonowner.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_disable_nonowner", "NonOwnerTeam", ownerEmail)
	addTeamMember(t, "team_disable_nonowner", memberEmail)

	// Member tries to disable — command not available
	t.Run("MemberCannotDisable", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("team disable")
		repl.Want("command not available")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Cleanup: remove member so owner can disable
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team remove " + memberEmail)
		repl.Want("Removed")
		repl.WantPrompt()
		repl.SendLine("team disable")
		repl.Want("Disabling team")
		repl.SendLine("yes")
		repl.Want("has been disabled")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamDisableCancel tests that declining disable confirmation leaves the team intact.
func TestTeamDisableCancel(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-disable-cancel.example")
	ownerPTY.Disconnect()

	// Enable a team
	repl := sshToExeDev(t, ownerKeyFile)
	repl.SendLine("team enable")
	repl.Want("Enable teams?")
	repl.SendLine("yes")
	repl.Want("Team name:")
	repl.SendLine("Cancel Test Team")
	repl.Want("created")
	repl.WantPrompt()
	repl.Disconnect()

	// Decline disable
	t.Run("DeclineDisable", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team disable")
		repl.Want("Disable team?")
		repl.SendLine("no")
		repl.Want("Cancelled")
		repl.WantPrompt()

		// Verify team still exists
		repl.SendLine("team")
		repl.Want("Cancel Test Team")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Clean up
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team disable")
		repl.SendLine("yes")
		repl.Want("has been disabled")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamDisableCleansUpShares tests that disabling a team properly cleans
// up team shares and pending invites so they don't leak or become orphaned.
func TestTeamDisableCleansUpShares(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	ownerPTY, _, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-disable-cleanup.example")
	ownerPTY.Disconnect()

	// Enable team
	repl := sshToExeDev(t, ownerKeyFile)
	repl.SendLine("team enable")
	repl.Want("Enable teams?")
	repl.SendLine("yes")
	repl.Want("Team name:")
	repl.SendLine("Cleanup Test Team")
	repl.Want("created")
	repl.WantPrompt()

	// Create a box and share it with team
	box := newBox(t, repl)
	repl.Disconnect()

	waitForSSH(t, box, ownerKeyFile)

	repl = sshToExeDev(t, ownerKeyFile)
	repl.SendLine("share add " + box + " team")
	repl.Want("Shared")
	repl.WantPrompt()

	// Create a pending invite (to a non-existent user so it stays pending)
	repl.SendLine("team add phantom@test-disable-cleanup.example")
	repl.Want("Invited")
	repl.WantPrompt()
	repl.Disconnect()

	// Disable the team
	repl = sshToExeDev(t, ownerKeyFile)
	repl.SendLine("team disable")
	repl.Want("Disabling team")
	repl.SendLine("yes")
	repl.Want("has been disabled")
	repl.WantPrompt()

	// Verify the box still exists but share is gone
	repl.SendLine("share show " + box)
	repl.Reject("Cleanup Test Team")
	repl.WantPrompt()

	// Verify the box is still there
	repl.SendLine("ls")
	repl.Want(box)
	repl.WantPrompt()

	// Clean up: rm the box
	repl.SendLine("rm " + box)
	repl.Want("deleted")
	repl.WantPrompt()
	repl.Disconnect()
}

// TestTeamEnableNonInteractive tests that team enable fails gracefully
// when run from a non-interactive SSH exec session.
func TestTeamEnableNonInteractive(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	userPTY, _, userKeyFile, _ := registerForExeDevWithEmail(t, "noninteractive@test-enable.example")
	userPTY.Disconnect()

	// team enable via exec (non-interactive)
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), userKeyFile, "team", "enable")
	if err == nil {
		t.Fatalf("expected team enable to fail in non-interactive mode, got: %s", out)
	}
	if !strings.Contains(string(out), "interactive") {
		t.Fatalf("expected 'interactive' in error message, got: %s", out)
	}
}

// TestTeamEnableEmptyName tests that an empty team name is rejected.
func TestTeamEnableEmptyName(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	userPTY, _, userKeyFile, _ := registerForExeDevWithEmail(t, "emptyname@test-enable.example")
	userPTY.Disconnect()

	repl := sshToExeDev(t, userKeyFile)
	repl.SendLine("team enable")
	repl.Want("Enable teams?")
	repl.SendLine("yes")

	// Try empty name
	repl.Want("Team name:")
	repl.SendLine("")
	repl.Want("cannot be empty")

	// Valid name to finish
	repl.SendLine("Valid Name")
	repl.Want("created")
	repl.WantPrompt()

	// Clean up
	repl.SendLine("team disable")
	repl.Want("Disabling team")
	repl.SendLine("yes")
	repl.Want("has been disabled")
	repl.WantPrompt()
	repl.Disconnect()
}
