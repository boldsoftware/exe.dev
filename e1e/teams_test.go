// This file contains e2e tests for the teams feature.

package e1e

import (
	"testing"

	"exe.dev/e1e/testinfra"
)

// createTeam creates a team via the SSH `team create` command.
// The owner must have root_support enabled before calling this.
func createTeam(t *testing.T, ownerKeyFile, teamID, displayName, ownerEmail string) {
	t.Helper()
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), ownerKeyFile, "team", "create", teamID, displayName, ownerEmail)
	if err != nil {
		t.Fatalf("team create failed: %v\noutput: %s", err, out)
	}
}

// addTeamMember adds a member to the team via the SSH `team add` command.
// Must be called as a team owner.
func addTeamMember(t *testing.T, ownerKeyFile, memberEmail string) {
	t.Helper()
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), ownerKeyFile, "team", "add", memberEmail)
	if err != nil {
		t.Fatalf("team add %s failed: %v\noutput: %s", memberEmail, err, out)
	}
}

// TestTeams tests the teams feature end-to-end.
// It creates a team with an owner and a member, then tests various operations.
func TestTeams(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register two users - one will be the owner, one will be a member
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-teams.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-teams.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()

	// Create a team via SSH (requires root_support for `team create`)
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_test_e2e", "TestTeam", ownerEmail)

	// Add the member via SSH `team add`
	addTeamMember(t, ownerKeyFile, memberEmail)

	// Test: Owner can see team command and get team info
	t.Run("OwnerTeamInfo", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team")
		repl.Want("TestTeam")
		repl.Want("owner")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Member can see team command and get team info
	t.Run("MemberTeamInfo", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("team")
		repl.Want("TestTeam")
		repl.Want("user")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Owner can list team members (PTY-based test)
	// Note: want() calls must match the OUTPUT ORDER since ExpectString consumes the buffer
	t.Run("OwnerTeamMembers", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("team members")
		repl.Want("Team members:")
		repl.Want(memberEmail) // member appears first in output
		repl.Want(ownerEmail)  // owner appears second
		repl.Want("(owner)")   // (owner) indicator after owner email
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Member can list team members (PTY-based test)
	// Note: want() calls must match the OUTPUT ORDER since ExpectString consumes the buffer
	t.Run("MemberTeamMembers", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("team members")
		repl.Want("Team members:")
		repl.Want(memberEmail) // member appears first in output
		repl.Want(ownerEmail)  // owner appears second
		repl.Want("(owner)")   // (owner) indicator after owner email
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Member creates a VM
	memberPTY = sshToExeDev(t, memberKeyFile)
	memberBox := newBox(t, memberPTY)
	memberPTY.Disconnect()

	waitForSSH(t, memberBox, memberKeyFile)

	// Test: Owner can see member's VM in "Team VMs" section
	t.Run("OwnerSeesTeamVMs", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("ls")
		repl.Want("Team VMs:")
		repl.Want(memberBox)
		repl.Want(memberEmail) // Should show creator email
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Member only sees their own VMs (no Team VMs section)
	t.Run("MemberSeesOnlyOwnVMs", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("ls")
		repl.Want("Your VMs:")
		repl.Want(memberBox)
		repl.Reject("Team VMs") // Member should NOT see Team VMs section
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Member cannot use team add (owner only)
	t.Run("MemberCannotTeamAdd", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("team add someone@example.com")
		repl.Want("command not available") // Command not available to members
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Member cannot use team remove (owner only)
	t.Run("MemberCannotTeamRemove", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("team remove someone@example.com")
		repl.Want("command not available") // Command not available to members
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Owner creates a VM
	ownerPTY = sshToExeDev(t, ownerKeyFile)
	ownerBox := newBox(t, ownerPTY)
	ownerPTY.Disconnect()

	waitForSSH(t, ownerBox, ownerKeyFile)

	// Test: Member cannot rm owner's VM
	t.Run("MemberCannotRmOwnerVM", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("rm " + ownerBox)
		repl.Want("not found")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Member cannot rename owner's VM
	t.Run("MemberCannotRenameOwnerVM", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("rename " + ownerBox + " stolen-box")
		repl.Want("not found")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Member cannot cp owner's VM
	t.Run("MemberCannotCpOwnerVM", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("cp " + ownerBox)
		repl.Want("not found")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Owner CAN rm member's VM
	t.Run("OwnerCanRmMemberVM", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("rm " + memberBox)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Cleanup: delete owner's box
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("rm " + ownerBox)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamOwnerCanManageMemberVMs tests that team owners can perform
// various operations on team member VMs (rename, cp, ssh).
func TestTeamOwnerCanManageMemberVMs(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register two users
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-team-manage.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-team-manage.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()

	// Create team and add member via SSH commands
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_manage_e2e", "ManageTeam", ownerEmail)
	addTeamMember(t, ownerKeyFile, memberEmail)

	// Member creates a VM
	memberPTY = sshToExeDev(t, memberKeyFile)
	memberBox := newBox(t, memberPTY)
	memberPTY.Disconnect()

	waitForSSH(t, memberBox, memberKeyFile)

	// Test: Owner can rename member's VM
	t.Run("OwnerCanRenameMemberVM", func(t *testing.T) {
		newName := "renamed-" + memberBox[:8] + "-vm"
		testinfra.AddCanonicalization(newName, "RENAMED_BOX")
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("rename " + memberBox + " " + newName)
		repl.Want("Renamed")
		repl.Want(newName)
		repl.WantPrompt()
		repl.Disconnect()

		// Update memberBox for subsequent tests
		memberBox = newName
	})

	// Test: Owner can cp member's VM
	t.Run("OwnerCanCpMemberVM", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("cp " + memberBox + " copied-vm")
		repl.Want("Created")
		repl.Want("copied-vm")
		repl.WantPrompt()

		// Clean up the copied VM
		repl.SendLine("rm copied-vm")
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("rm " + memberBox)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamSharing tests the "share add team" and "share remove team" commands.
func TestTeamSharing(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register three users - owner, member1, member2
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-team-share.example")
	member1PTY, _, _, member1Email := registerForExeDevWithEmail(t, "member1@test-team-share.example")
	member2PTY, _, _, member2Email := registerForExeDevWithEmail(t, "member2@test-team-share.example")
	ownerPTY.Disconnect()
	member1PTY.Disconnect()
	member2PTY.Disconnect()

	// Create team and add members via SSH commands
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_share_e2e", "ShareTeam", ownerEmail)
	addTeamMember(t, ownerKeyFile, member1Email)
	addTeamMember(t, ownerKeyFile, member2Email)

	// Owner creates a VM
	ownerPTY = sshToExeDev(t, ownerKeyFile)
	ownerBox := newBox(t, ownerPTY)
	ownerPTY.Disconnect()

	waitForSSH(t, ownerBox, ownerKeyFile)

	// Test: Owner shares VM with team
	t.Run("OwnerSharesWithTeam", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("share add " + ownerBox + " team")
		repl.Want("Shared")
		repl.Want("ShareTeam") // team display name
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: share show displays team name
	t.Run("ShareShowDisplaysTeam", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("share show " + ownerBox)
		repl.Want("Shared with teams:")
		repl.Want("ShareTeam")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Sharing again shows "Already shared"
	t.Run("ShareAgainShowsAlready", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("share add " + ownerBox + " team")
		repl.Want("Already shared")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Owner removes team sharing
	t.Run("OwnerRemovesTeamShare", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("share remove " + ownerBox + " team")
		repl.Want("Removed team access")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: share show no longer displays team
	t.Run("ShareShowNoTeam", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("share show " + ownerBox)
		repl.Reject("ShareTeam")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Non-team user gets error when trying team
	t.Run("NonTeamUser", func(t *testing.T) {
		// Register a user not in any team
		lonelyPTY, _, lonelyKeyFile, _ := registerForExeDevWithEmail(t, "lonely@test-team-share.example")
		lonelyPTY.Disconnect()

		// Create a VM for the lonely user
		lonelyPTY = sshToExeDev(t, lonelyKeyFile)
		lonelyBox := newBox(t, lonelyPTY)
		lonelyPTY.Disconnect()

		waitForSSH(t, lonelyBox, lonelyKeyFile)

		repl := sshToExeDev(t, lonelyKeyFile)
		repl.SendLine("share add " + lonelyBox + " team")
		repl.Want("not in a team")
		repl.WantPrompt()

		// Cleanup
		repl.SendLine("rm " + lonelyBox)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.SendLine("rm " + ownerBox)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})
}
