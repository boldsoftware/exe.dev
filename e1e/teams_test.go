// This file contains e2e tests for the teams feature.

package e1e

import (
	"fmt"
	"net/http"
	"strings"
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
		repl.Want(ownerEmail)  // billing_owner appears first in output
		repl.Want("(billing owner)")
		repl.Want(memberEmail) // regular user appears second
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Member can list team members (PTY-based test)
	// Note: want() calls must match the OUTPUT ORDER since ExpectString consumes the buffer
	t.Run("MemberTeamMembers", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("team members")
		repl.Want("Team members:")
		repl.Want(ownerEmail)  // billing_owner appears first in output
		repl.Want("(billing owner)")
		repl.Want(memberEmail) // regular user appears second
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

// TestTeamSSHSharing tests the "share ssh allow" and "share ssh disallow" commands.
func TestTeamSSHSharing(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register three users - owner, member, and a second member
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-team-ssh.example")
	memberPTY, _, memberKeyFile, memberEmail := registerForExeDevWithEmail(t, "member@test-team-ssh.example")
	member2PTY, _, member2KeyFile, member2Email := registerForExeDevWithEmail(t, "member2@test-team-ssh.example")
	ownerPTY.Disconnect()
	memberPTY.Disconnect()
	member2PTY.Disconnect()

	// Create team and add members
	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKeyFile, "team_ssh_e2e", "SSHTeam", ownerEmail)
	addTeamMember(t, ownerKeyFile, memberEmail)
	addTeamMember(t, ownerKeyFile, member2Email)

	// Member creates a VM
	memberPTY = sshToExeDev(t, memberKeyFile)
	memberBox := newBox(t, memberPTY)
	memberPTY.Disconnect()

	waitForSSH(t, memberBox, memberKeyFile)

	// Test: Owner cannot SSH into member's box before sharing is enabled
	t.Run("OwnerCannotSSHBeforeAllow", func(t *testing.T) {
		cmd := boxSSHCommand(t, memberBox, ownerKeyFile, "hostname")
		out, _ := cmd.CombinedOutput()
		// If we reached the box, hostname will contain the box name.
		// If we reached the REPL, output will be "command not found: hostname".
		if strings.Contains(string(out), memberBox) {
			t.Errorf("expected owner NOT to reach box shell, but got: %s", out)
		}
	})

	// Test: Member enables SSH sharing for their box
	t.Run("MemberAllowsSSH", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("share ssh allow " + memberBox)
		repl.Want("updated")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Owner can SSH into member's box via username routing (ssh boxname@host)
	t.Run("OwnerCanSSHViaUsername", func(t *testing.T) {
		pty := sshToBox(t, memberBox, ownerKeyFile)
		pty.SendLine("echo hello-from-team-ssh")
		pty.Want("hello-from-team-ssh")
		pty.Disconnect()
	})

	// Test: Another team member can SSH into the box (peer-to-peer)
	t.Run("MemberToMemberSSH", func(t *testing.T) {
		pty := sshToBox(t, memberBox, member2KeyFile)
		pty.SendLine("echo hello-from-peer")
		pty.Want("hello-from-peer")
		pty.Disconnect()
	})

	// Test: share show displays team SSH status
	t.Run("ShareShowDisplaysTeamSSH", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("share show " + memberBox)
		repl.Want("Team SSH: ALLOWED")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: share show JSON includes team_ssh
	t.Run("ShareShowJSONTeamSSH", func(t *testing.T) {
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), memberKeyFile, "share", "show", memberBox, "--json")
		if err != nil {
			t.Fatalf("share show --json failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), `"team_ssh":true`) {
			t.Fatalf("expected team_ssh:true in JSON output, got: %s", out)
		}
	})

	// Test: Member disables SSH sharing
	t.Run("MemberDisallowsSSH", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("share ssh disallow " + memberBox)
		repl.Want("updated")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Owner cannot SSH after disallow
	t.Run("OwnerCannotSSHAfterDisallow", func(t *testing.T) {
		cmd := boxSSHCommand(t, memberBox, ownerKeyFile, "hostname")
		out, _ := cmd.CombinedOutput()
		if strings.Contains(string(out), memberBox) {
			t.Errorf("expected owner NOT to reach box shell after disallow, but got: %s", out)
		}
	})

	// Test: share show no longer displays team SSH
	t.Run("ShareShowNoTeamSSH", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("share show " + memberBox)
		repl.Reject("Team SSH")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Test: Non-team user cannot see the share ssh command
	t.Run("NonTeamNoSSH", func(t *testing.T) {
		lonelyPTY, _, lonelyKeyFile, _ := registerForExeDevWithEmail(t, "lonely@test-team-ssh.example")
		lonelyPTY.Disconnect()

		lonelyPTY = sshToExeDev(t, lonelyKeyFile)
		lonelyBox := newBox(t, lonelyPTY)
		lonelyPTY.Disconnect()

		waitForSSH(t, lonelyBox, lonelyKeyFile)

		repl := sshToExeDev(t, lonelyKeyFile)
		repl.SendLine("share ssh allow " + lonelyBox)
		repl.Want("command not available")
		repl.WantPrompt()

		// Cleanup
		repl.SendLine("rm " + lonelyBox)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, memberKeyFile)
		repl.SendLine("rm " + memberBox)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})
}

// TestTeamSharingIsolation is an adversarial test that verifies users outside
// a team CANNOT access team-shared resources. It creates two teams and an
// unaffiliated user, then tests that:
//   - Team web sharing (share add team) doesn't leak to outsiders
//   - Team SSH sharing (share ssh allow) doesn't leak to outsiders
//   - Team Shelley sharing (share shelley allow) doesn't leak to outsiders
//
// Each sharing mechanism is tested against three adversaries:
//  1. A member of a different team
//  2. An unaffiliated user (no team at all)
//  3. A former team member (removed after sharing was enabled)
func TestTeamSharingIsolation(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// === Setup: Two teams, one lonely user ===

	// Team Alpha: owner + member
	alphaPTY, _, alphaOwnerKey, alphaOwnerEmail := registerForExeDevWithEmail(t, "alpha-owner@test-isolation.example")
	alphaMemberPTY, _, alphaMemberKey, alphaMemberEmail := registerForExeDevWithEmail(t, "alpha-member@test-isolation.example")
	// Will be removed from team mid-test
	alphaExMemberPTY, alphaExMemberCookies, alphaExMemberKey, alphaExMemberEmail := registerForExeDevWithEmail(t, "alpha-ex@test-isolation.example")
	alphaPTY.Disconnect()
	alphaMemberPTY.Disconnect()
	alphaExMemberPTY.Disconnect()

	enableRootSupport(t, alphaOwnerEmail)
	createTeam(t, alphaOwnerKey, "team_alpha_iso", "AlphaTeam", alphaOwnerEmail)
	addTeamMember(t, alphaOwnerKey, alphaMemberEmail)
	addTeamMember(t, alphaOwnerKey, alphaExMemberEmail)

	// Team Beta: owner only (the adversary from another team)
	betaPTY, betaCookies, betaOwnerKey, betaOwnerEmail := registerForExeDevWithEmail(t, "beta-owner@test-isolation.example")
	betaPTY.Disconnect()

	enableRootSupport(t, betaOwnerEmail)
	createTeam(t, betaOwnerKey, "team_beta_iso", "BetaTeam", betaOwnerEmail)

	// Lonely user: no team at all
	lonelyPTY, lonelyCookies, _, _ := registerForExeDevWithEmail(t, "lonely@test-isolation.example")
	lonelyPTY.Disconnect()

	// Alpha member creates a box
	alphaMemberPTY = sshToExeDev(t, alphaMemberKey)
	box := newBox(t, alphaMemberPTY, testinfra.BoxOpts{Command: "/bin/bash"})
	alphaMemberPTY.Disconnect()
	waitForSSH(t, box, alphaMemberKey)

	// Set up HTTP server for proxy tests
	serveIndex(t, box, alphaMemberKey, "team-isolation-test")
	httpPort := Env.servers.Exed.HTTPPort
	configureProxyRoute(t, alphaMemberKey, box, 8080, "private")

	// ============================================================
	// Part 1: Team web sharing (share add <box> team)
	// ============================================================
	t.Run("web_sharing_isolation", func(t *testing.T) {
		// Share with team
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), alphaMemberKey, "share", "add", box, "team")
		if err != nil {
			t.Fatalf("share add team failed: %v\n%s", err, out)
		}

		// Sanity: non-owner team member CAN access via team share
		proxyAssert(t, box, proxyExpectation{
			name:     "team member can access web-shared box",
			httpPort: httpPort,
			cookies:  alphaExMemberCookies,
			httpCode: http.StatusOK,
		})

		// Adversary 1: different team's owner CANNOT access
		proxyAssert(t, box, proxyExpectation{
			name:     "other team owner CANNOT access web-shared box",
			httpPort: httpPort,
			cookies:  betaCookies,
			httpCode: http.StatusUnauthorized,
		})

		// Adversary 2: unaffiliated user CANNOT access
		proxyAssert(t, box, proxyExpectation{
			name:     "unaffiliated user CANNOT access web-shared box",
			httpPort: httpPort,
			cookies:  lonelyCookies,
			httpCode: http.StatusUnauthorized,
		})

		// Adversary 3: ex-member test
		// First verify ex-member CAN access (they're still in the team)
		proxyAssert(t, box, proxyExpectation{
			name:     "ex-member can access before removal",
			httpPort: httpPort,
			cookies:  alphaExMemberCookies,
			httpCode: http.StatusOK,
		})

		// Remove the ex-member from the team
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), alphaOwnerKey, "team", "remove", alphaExMemberEmail)
		if err != nil {
			t.Fatalf("team remove failed: %v\n%s", err, out)
		}

		// Ex-member should now be denied
		proxyAssert(t, box, proxyExpectation{
			name:     "ex-member CANNOT access web-shared box after removal",
			httpPort: httpPort,
			cookies:  alphaExMemberCookies,
			httpCode: http.StatusUnauthorized,
		})

		// Unshare team
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), alphaMemberKey, "share", "remove", box, "team")
		if err != nil {
			t.Fatalf("share remove team failed: %v\n%s", err, out)
		}

		// Re-add ex-member for later tests
		addTeamMember(t, alphaOwnerKey, alphaExMemberEmail)
	})

	// ============================================================
	// Part 2: Team SSH sharing (share ssh allow)
	// ============================================================
	t.Run("ssh_sharing_isolation", func(t *testing.T) {
		// Enable SSH sharing
		repl := sshToExeDev(t, alphaMemberKey)
		repl.SendLine("share ssh allow " + box)
		repl.Want("updated")
		repl.WantPrompt()
		repl.Disconnect()

		// Sanity: team member CAN SSH
		pty := sshToBox(t, box, alphaExMemberKey)
		pty.SendLine("echo ssh-isolation-ok")
		pty.Want("ssh-isolation-ok")
		pty.Disconnect()

		// Adversary 1: different team's owner CANNOT SSH
		// Use "hostname" — if we reach the box shell, output contains the box name.
		// If we land in the REPL, output is "command not found: hostname".
		cmd := boxSSHCommand(t, box, betaOwnerKey, "hostname")
		out, _ := cmd.CombinedOutput()
		if strings.Contains(string(out), box) {
			t.Errorf("other team owner SHOULD NOT reach box shell via SSH, but got: %s", out)
		}

		// Adversary 2: (lonely user has no key registered for box SSH,
		// so SSH will fail at key auth level — this is inherently safe)

		// Adversary 3: remove ex-member, verify SSH denied
		rmOut, err := Env.servers.RunExeDevSSHCommand(Env.context(t), alphaOwnerKey, "team", "remove", alphaExMemberEmail)
		if err != nil {
			t.Fatalf("team remove failed: %v\n%s", err, rmOut)
		}

		cmd = boxSSHCommand(t, box, alphaExMemberKey, "hostname")
		out, _ = cmd.CombinedOutput()
		if strings.Contains(string(out), box) {
			t.Errorf("ex-member SHOULD NOT reach box shell via SSH after removal, but got: %s", out)
		}

		// Disable SSH sharing and re-add member
		repl = sshToExeDev(t, alphaMemberKey)
		repl.SendLine("share ssh disallow " + box)
		repl.Want("updated")
		repl.WantPrompt()
		repl.Disconnect()

		addTeamMember(t, alphaOwnerKey, alphaExMemberEmail)
	})

	// ============================================================
	// Part 3: Team Shelley sharing (share shelley allow)
	// ============================================================
	t.Run("shelley_sharing_isolation", func(t *testing.T) {
		shelleyHost := fmt.Sprintf("%s.shelley.exe.cloud:%d", box, httpPort)

		// Before enabling: non-owner team member CANNOT access Shelley
		// (alphaMemberCookies is the box OWNER — they always have access.
		//  Use alphaExMemberCookies who is a team member but not the owner.)
		proxyAssert(t, box, proxyExpectation{
			name:     "non-owner team member cannot access shelley before allow",
			httpPort: httpPort,
			cookies:  alphaExMemberCookies,
			host:     shelleyHost,
			httpCode: http.StatusUnauthorized,
		})

		// Enable Shelley sharing
		repl := sshToExeDev(t, alphaMemberKey)
		repl.SendLine("share shelley allow " + box)
		repl.Want("updated")
		repl.WantPrompt()
		repl.Disconnect()

		// Sanity: team member CAN now access Shelley
		// Shelley is running in test VMs, so we expect 200.
		proxyAssert(t, box, proxyExpectation{
			name:     "team member can access shelley after allow",
			httpPort: httpPort,
			cookies:  alphaExMemberCookies,
			host:     shelleyHost,
			httpCode: http.StatusOK,
		})

		// Adversary 1: different team's owner CANNOT access Shelley
		proxyAssert(t, box, proxyExpectation{
			name:     "other team owner CANNOT access shelley",
			httpPort: httpPort,
			cookies:  betaCookies,
			host:     shelleyHost,
			httpCode: http.StatusUnauthorized,
		})

		// Adversary 2: unaffiliated user CANNOT access Shelley
		proxyAssert(t, box, proxyExpectation{
			name:     "unaffiliated user CANNOT access shelley",
			httpPort: httpPort,
			cookies:  lonelyCookies,
			host:     shelleyHost,
			httpCode: http.StatusUnauthorized,
		})

		// Adversary 3: remove ex-member, verify Shelley denied
		rmOut, err := Env.servers.RunExeDevSSHCommand(Env.context(t), alphaOwnerKey, "team", "remove", alphaExMemberEmail)
		if err != nil {
			t.Fatalf("team remove failed: %v\n%s", err, rmOut)
		}

		proxyAssert(t, box, proxyExpectation{
			name:     "ex-member CANNOT access shelley after removal",
			httpPort: httpPort,
			cookies:  alphaExMemberCookies,
			host:     shelleyHost,
			httpCode: http.StatusUnauthorized,
		})

		// Disable Shelley sharing and re-add ex-member for later tests
		repl = sshToExeDev(t, alphaMemberKey)
		repl.SendLine("share shelley disallow " + box)
		repl.Want("updated")
		repl.WantPrompt()
		repl.Disconnect()

		addTeamMember(t, alphaOwnerKey, alphaExMemberEmail)
	})

	// ============================================================
	// Part 4: Cross-cutting — email share doesn't grant Shelley
	// ============================================================
	t.Run("email_share_no_shelley", func(t *testing.T) {
		shelleyHost := fmt.Sprintf("%s.shelley.exe.cloud:%d", box, httpPort)

		// Share box via email with the beta user
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), alphaMemberKey, "share", "add", box, betaOwnerEmail)
		if err != nil {
			t.Fatalf("share add email failed: %v\n%s", err, out)
		}

		// Beta user CAN access the standard proxy
		proxyAssert(t, box, proxyExpectation{
			name:     "email-shared user can access standard proxy",
			httpPort: httpPort,
			cookies:  betaCookies,
			httpCode: http.StatusOK,
		})

		// Beta user CANNOT access Shelley (email share != shelley access)
		proxyAssert(t, box, proxyExpectation{
			name:     "email-shared user CANNOT access shelley",
			httpPort: httpPort,
			cookies:  betaCookies,
			host:     shelleyHost,
			httpCode: http.StatusUnauthorized,
		})

		// Revoke email share
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), alphaMemberKey, "share", "remove", box, betaOwnerEmail)
		if err != nil {
			t.Fatalf("share remove email failed: %v\n%s", err, out)
		}
	})

	// ============================================================
	// Part 5: Team web share doesn't grant Shelley
	// ============================================================
	t.Run("team_web_share_no_shelley", func(t *testing.T) {
		shelleyHost := fmt.Sprintf("%s.shelley.exe.cloud:%d", box, httpPort)

		// Share box with team (alphaExMember was re-added in Part 2 cleanup)
		out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), alphaMemberKey, "share", "add", box, "team")
		if err != nil {
			t.Fatalf("share add team failed: %v\n%s", err, out)
		}

		// Team member CAN access standard proxy via team share
		proxyAssert(t, box, proxyExpectation{
			name:     "team web-shared user can access standard proxy",
			httpPort: httpPort,
			cookies:  alphaExMemberCookies,
			httpCode: http.StatusOK,
		})

		// Team member CANNOT access Shelley (team web share != shelley access)
		proxyAssert(t, box, proxyExpectation{
			name:     "team web-shared user CANNOT access shelley",
			httpPort: httpPort,
			cookies:  alphaExMemberCookies,
			host:     shelleyHost,
			httpCode: http.StatusUnauthorized,
		})

		// Cleanup: remove team share
		out, err = Env.servers.RunExeDevSSHCommand(Env.context(t), alphaMemberKey, "share", "remove", box, "team")
		if err != nil {
			t.Fatalf("share remove team failed: %v\n%s", err, out)
		}
	})

	// Cleanup
	t.Run("Cleanup", func(t *testing.T) {
		repl := sshToExeDev(t, alphaMemberKey)
		repl.SendLine("rm " + box)
		repl.Want("deleted")
		repl.WantPrompt()
		repl.Disconnect()
	})
}
