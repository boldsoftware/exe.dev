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
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register two users - one will be the owner, one will be a member
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-teams.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-teams.example")
	ownerPTY.disconnect()
	memberPTY.disconnect()

	// Create a team via SSH (requires root_support for `team create`)
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_test_e2e", "TestTeam", ownerEmail)

	// Add the member via SSH `team add`
	addTeamMember(t, ownerKeyFile, memberEmail)

	// Test: Owner can see team command and get team info
	t.Run("OwnerTeamInfo", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("team")
		repl.want("TestTeam")
		repl.want("owner")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Member can see team command and get team info
	t.Run("MemberTeamInfo", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.sendLine("team")
		repl.want("TestTeam")
		repl.want("user")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Owner can list team members (PTY-based test)
	// Note: want() calls must match the OUTPUT ORDER since ExpectString consumes the buffer
	t.Run("OwnerTeamMembers", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("team members")
		repl.want("Team members:")
		repl.want(memberEmail) // member appears first in output
		repl.want(ownerEmail)  // owner appears second
		repl.want("(owner)")   // (owner) indicator after owner email
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Member can list team members (PTY-based test)
	// Note: want() calls must match the OUTPUT ORDER since ExpectString consumes the buffer
	t.Run("MemberTeamMembers", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.sendLine("team members")
		repl.want("Team members:")
		repl.want(memberEmail) // member appears first in output
		repl.want(ownerEmail)  // owner appears second
		repl.want("(owner)")   // (owner) indicator after owner email
		repl.wantPrompt()
		repl.disconnect()
	})

	// Member creates a VM
	memberPTY = sshToExeDev(t, memberKeyFile)
	memberBox := newBox(t, memberPTY)
	memberPTY.disconnect()

	waitForSSH(t, memberBox, memberKeyFile)

	// Test: Owner can see member's VM in "Team VMs" section
	t.Run("OwnerSeesTeamVMs", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("ls")
		repl.want("Team VMs:")
		repl.want(memberBox)
		repl.want(memberEmail) // Should show creator email
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Member only sees their own VMs (no Team VMs section)
	t.Run("MemberSeesOnlyOwnVMs", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.sendLine("ls")
		repl.want("Your VMs:")
		repl.want(memberBox)
		repl.reject("Team VMs") // Member should NOT see Team VMs section
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Member cannot use team add (owner only)
	t.Run("MemberCannotTeamAdd", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.sendLine("team add someone@example.com")
		repl.want("command not available") // Command not available to members
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Member cannot use team remove (owner only)
	t.Run("MemberCannotTeamRemove", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.sendLine("team remove someone@example.com")
		repl.want("command not available") // Command not available to members
		repl.wantPrompt()
		repl.disconnect()
	})

	// Owner creates a VM
	ownerPTY = sshToExeDev(t, ownerKeyFile)
	ownerBox := newBox(t, ownerPTY)
	ownerPTY.disconnect()

	waitForSSH(t, ownerBox, ownerKeyFile)

	// Test: Member cannot rm owner's VM
	t.Run("MemberCannotRmOwnerVM", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.sendLine("rm " + ownerBox)
		repl.want("not found")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Member cannot rename owner's VM
	t.Run("MemberCannotRenameOwnerVM", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.sendLine("rename " + ownerBox + " stolen-box")
		repl.want("not found")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Member cannot cp owner's VM
	t.Run("MemberCannotCpOwnerVM", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.sendLine("cp " + ownerBox)
		repl.want("not found")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Owner CAN rm member's VM
	t.Run("OwnerCanRmMemberVM", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("rm " + memberBox)
		repl.want("deleted")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Cleanup: delete owner's box
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("rm " + ownerBox)
		repl.want("deleted")
		repl.wantPrompt()
		repl.disconnect()
	})
}

// TestTeamOwnerCanManageMemberVMs tests that team owners can perform
// various operations on team member VMs (rename, cp, ssh).
func TestTeamOwnerCanManageMemberVMs(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register two users
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-team-manage.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-team-manage.example")
	ownerPTY.disconnect()
	memberPTY.disconnect()

	// Create team and add member via SSH commands
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_manage_e2e", "ManageTeam", ownerEmail)
	addTeamMember(t, ownerKeyFile, memberEmail)

	// Member creates a VM
	memberPTY = sshToExeDev(t, memberKeyFile)
	memberBox := newBox(t, memberPTY)
	memberPTY.disconnect()

	waitForSSH(t, memberBox, memberKeyFile)

	// Test: Owner can rename member's VM
	t.Run("OwnerCanRenameMemberVM", func(t *testing.T) {
		newName := "renamed-" + memberBox[:8] + "-vm"
		testinfra.AddCanonicalization(newName, "RENAMED_BOX")
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("rename " + memberBox + " " + newName)
		repl.want("Renamed")
		repl.want(newName)
		repl.wantPrompt()
		repl.disconnect()

		// Update memberBox for subsequent tests
		memberBox = newName
	})

	// Test: Owner can cp member's VM
	t.Run("OwnerCanCpMemberVM", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("cp " + memberBox + " copied-vm")
		repl.want("Created")
		repl.want("copied-vm")
		repl.wantPrompt()

		// Clean up the copied VM
		repl.sendLine("rm copied-vm")
		repl.want("deleted")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("rm " + memberBox)
		repl.want("deleted")
		repl.wantPrompt()
		repl.disconnect()
	})
}

// TestTeamSharing tests the "share add team" and "share remove team" commands.
func TestTeamSharing(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register three users - owner, member1, member2
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-team-share.example")
	member1PTY, _, _, member1Email := registerForExeDevWithEmail(t, "member1@test-team-share.example")
	member2PTY, _, _, member2Email := registerForExeDevWithEmail(t, "member2@test-team-share.example")
	ownerPTY.disconnect()
	member1PTY.disconnect()
	member2PTY.disconnect()

	// Create team and add members via SSH commands
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_share_e2e", "ShareTeam", ownerEmail)
	addTeamMember(t, ownerKeyFile, member1Email)
	addTeamMember(t, ownerKeyFile, member2Email)

	// Owner creates a VM
	ownerPTY = sshToExeDev(t, ownerKeyFile)
	ownerBox := newBox(t, ownerPTY)
	ownerPTY.disconnect()

	waitForSSH(t, ownerBox, ownerKeyFile)

	// Test: Owner shares VM with team
	t.Run("OwnerSharesWithTeam", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("share add " + ownerBox + " team")
		repl.want("Shared")
		repl.want("ShareTeam") // team display name
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: share show displays team name
	t.Run("ShareShowDisplaysTeam", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("share show " + ownerBox)
		repl.want("Shared with teams:")
		repl.want("ShareTeam")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Sharing again shows "Already shared"
	t.Run("ShareAgainShowsAlready", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("share add " + ownerBox + " team")
		repl.want("Already shared")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Owner removes team sharing
	t.Run("OwnerRemovesTeamShare", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("share remove " + ownerBox + " team")
		repl.want("Removed team access")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: share show no longer displays team
	t.Run("ShareShowNoTeam", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("share show " + ownerBox)
		repl.reject("ShareTeam")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Test: Non-team user gets error when trying team
	t.Run("NonTeamUser", func(t *testing.T) {
		// Register a user not in any team
		lonelyPTY, _, lonelyKeyFile, _ := registerForExeDevWithEmail(t, "lonely@test-team-share.example")
		lonelyPTY.disconnect()

		// Create a VM for the lonely user
		lonelyPTY = sshToExeDev(t, lonelyKeyFile)
		lonelyBox := newBox(t, lonelyPTY)
		lonelyPTY.disconnect()

		waitForSSH(t, lonelyBox, lonelyKeyFile)

		repl := sshToExeDev(t, lonelyKeyFile)
		repl.sendLine("share add " + lonelyBox + " team")
		repl.want("not in a team")
		repl.wantPrompt()

		// Cleanup
		repl.sendLine("rm " + lonelyBox)
		repl.want("deleted")
		repl.wantPrompt()
		repl.disconnect()
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, ownerKeyFile)
		repl.sendLine("rm " + ownerBox)
		repl.want("deleted")
		repl.wantPrompt()
		repl.disconnect()
	})
}
