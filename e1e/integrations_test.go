package e1e

import (
	"fmt"
	"testing"
)

// TestIntegrationsCommand tests the hidden integrations command.
func TestIntegrationsCommand(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	pty, _, _, _ := registerForExeDev(t)

	// List with no integrations.
	pty.SendLine("integrations list")
	pty.Want("No integrations configured.")
	pty.WantPrompt()

	// Add an http-proxy integration.
	pty.SendLine("integrations add http-proxy --name=myproxy --target=https://example.com --header=X-Auth:secret123")
	pty.Want("Added integration myproxy")
	pty.Want("attach it to a VM first")
	pty.Want("integrations attach myproxy vm:<vm-name>")
	pty.Want("curl http://myproxy.int.exe.cloud/")
	pty.WantPrompt()

	// List should now show the integration by name.
	pty.SendLine("integrations list")
	pty.Want("myproxy")
	pty.Want("http-proxy")
	pty.Want("target=https://example.com")
	pty.WantPrompt()

	// Add another integration.
	pty.SendLine("integrations add http-proxy --name=otherproxy --target=https://other.com --header=Authorization:Bearer-tok")
	pty.Want("Added integration otherproxy")
	pty.WantPrompt()

	// List should show two (most recent first).
	pty.SendLine("integrations list")
	pty.Want("otherproxy")
	pty.Want("myproxy")
	pty.WantPrompt()

	// Remove by name.
	pty.SendLine("integrations remove myproxy")
	pty.Want("Removed integration myproxy")
	pty.WantPrompt()

	// List should show one remaining.
	pty.SendLine("integrations list")
	pty.Want("otherproxy")
	pty.WantPrompt()

	// Try removing a non-existent integration.
	pty.SendLine("integrations remove doesnotexist")
	pty.Want("not found")
	pty.WantPrompt()

	// Test validation: missing --name.
	pty.SendLine("integrations add http-proxy --target=https://x.com --header=X-Foo:bar")
	pty.Want("--name is required")
	pty.WantPrompt()

	// Test validation: missing --target.
	pty.SendLine("integrations add http-proxy --name=bad --header=X-Foo:bar")
	pty.Want("--target is required")
	pty.WantPrompt()

	// Test validation: missing --header.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://x.com")
	pty.Want("--header (or --bearer) is required")
	pty.WantPrompt()

	// Test validation: unknown type.
	pty.SendLine("integrations add unknown-type")
	pty.Want("unknown integration type")
	pty.WantPrompt()

	// Test validation: duplicate name.
	pty.SendLine("integrations add http-proxy --name=otherproxy --target=https://dup.com --header=X:y")
	pty.Want("already in use")
	pty.WantPrompt()

	// Test validation: invalid name (uppercase).
	pty.SendLine("integrations add http-proxy --name=BadName --target=https://x.com --header=X-Foo:bar")
	pty.Want("invalid name")
	pty.WantPrompt()

	// Test validation: invalid name (leading hyphen).
	pty.SendLine("integrations add http-proxy --name=-bad --target=https://x.com --header=X-Foo:bar")
	pty.Want("invalid name")
	pty.WantPrompt()

	// Test validation: invalid target URL (no scheme).
	pty.SendLine("integrations add http-proxy --name=bad --target=example.com --header=X-Foo:bar")
	pty.Want("scheme must be https")
	pty.WantPrompt()

	// Test validation: http instead of https.
	pty.SendLine("integrations add http-proxy --name=bad --target=http://example.com --header=X-Foo:bar")
	pty.Want("scheme must be https")
	pty.WantPrompt()

	// Non-standard HTTPS port is allowed.
	pty.SendLine("integrations add http-proxy --name=customport --target=https://example.com:8080 --header=X-Foo:bar")
	pty.Want("Added integration customport")
	pty.WantPrompt()
	pty.SendLine("integrations remove customport")
	pty.Want("Removed")
	pty.WantPrompt()

	// Test validation: invalid header (no colon).
	pty.SendLine("integrations add http-proxy --name=bad --target=https://x.com --header=novalue")
	pty.Want("invalid header")
	pty.WantPrompt()

	// Test validation: reserved header.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://x.com --header=X-Exedev-Box:evil")
	pty.Want("reserved")
	pty.WantPrompt()

	// Test validation: bare IP address.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://192.168.1.1 --header=X-Foo:bar")
	pty.Want("hostname, not an IP")
	pty.WantPrompt()

	// Test validation: extra args to add.
	pty.SendLine("integrations add http-proxy extraarg --name=bad --target=https://x.com --header=X-Foo:bar")
	pty.Want("usage: integrations add")
	pty.WantPrompt()

	// Test validation: extra args to setup.
	pty.SendLine("integrations setup github extraarg")
	pty.Want("usage: integrations setup")
	pty.WantPrompt()

	// Test help.
	pty.SendLine("integrations")
	pty.Want("list")
	pty.Want("add")
	pty.Want("remove")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations remove otherproxy")
	pty.Want("Removed")
	pty.WantPrompt()
}

// TestIntegrationsBearerFlag tests the --bearer shorthand flag.
func TestIntegrationsBearerFlag(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	pty, _, _, _ := registerForExeDev(t)

	// Add with --bearer instead of --header.
	pty.SendLine("integrations add http-proxy --name=bearerproxy --target=https://example.com --bearer=my-secret-token")
	pty.Want("Added integration bearerproxy")
	pty.WantPrompt()

	// List should show the expanded Authorization:Bearer header.
	pty.SendLine("integrations list")
	pty.Want("bearerproxy")
	pty.Want("header=Authorization:Bearer my-secret-token")
	pty.WantPrompt()

	// Add a second integration with --bearer to verify it works consistently.
	pty.SendLine("integrations add http-proxy --name=bearerproxy2 --target=https://other.com --bearer=another-token-456")
	pty.Want("Added integration bearerproxy2")
	pty.WantPrompt()

	pty.SendLine("integrations list")
	pty.Want("header=Authorization:Bearer another-token-456")
	pty.WantPrompt()

	// Error: --bearer and --header together.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://x.com --header=X-Foo:bar --bearer=tok")
	pty.Want("mutually exclusive")
	pty.WantPrompt()

	// Error: neither --header nor --bearer.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://x.com")
	pty.Want("--header (or --bearer) is required")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations remove bearerproxy")
	pty.Want("Removed")
	pty.WantPrompt()

	pty.SendLine("integrations remove bearerproxy2")
	pty.Want("Removed")
	pty.WantPrompt()
}

// TestIntegrationsAttachDetach tests the attach and detach subcommands.
func TestIntegrationsAttachDetach(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	// Create a VM to attach to.
	bn := boxName(t)
	pty.SendLine(fmt.Sprintf("new --name=%s", bn))
	pty.WantRE("Creating .*" + bn)
	pty.Want("Ready")
	pty.WantPrompt()

	// Add an integration.
	pty.SendLine("integrations add http-proxy --name=myproxy --target=https://example.com --header=X-Auth:secret123")
	pty.Want("Added integration myproxy")
	pty.WantPrompt()

	// Attach by name.
	pty.SendLine(fmt.Sprintf("integrations attach myproxy vm:%s", bn))
	pty.Want("Attached myproxy to vm:" + bn)
	pty.WantPrompt()

	// Detach by name.
	pty.SendLine(fmt.Sprintf("integrations detach myproxy vm:%s", bn))
	pty.Want("Detached myproxy from vm:" + bn)
	pty.WantPrompt()

	// Error: attach nonexistent integration.
	pty.SendLine(fmt.Sprintf("integrations attach nope vm:%s", bn))
	pty.Want("not found")
	pty.WantPrompt()

	// Error: attach to nonexistent VM.
	pty.SendLine("integrations attach myproxy vm:nonexistent-vm-abc")
	pty.Want("not found")
	pty.WantPrompt()

	// Error: detach nonexistent integration.
	pty.SendLine(fmt.Sprintf("integrations detach nope vm:%s", bn))
	pty.Want("not found")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations remove myproxy")
	pty.Want("Removed")
	pty.WantPrompt()

	cleanupBox(t, keyFile, bn)
}

// TestIntegrationsRename tests the rename subcommand.
func TestIntegrationsRename(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// Add an integration.
	pty.SendLine("integrations add http-proxy --name=myproxy --target=https://example.com --header=X-Auth:secret123")
	pty.Want("Added integration myproxy")
	pty.WantPrompt()

	// Rename it.
	pty.SendLine("integrations rename myproxy betterproxy")
	pty.Want("Renamed integration myproxy to betterproxy")
	pty.WantPrompt()

	// List to verify name changed.
	pty.SendLine("integrations list")
	pty.Want("betterproxy")
	pty.Want("http-proxy")
	pty.WantPrompt()

	// Old name should not work.
	pty.SendLine("integrations remove myproxy")
	pty.Want("not found")
	pty.WantPrompt()

	// Error: rename nonexistent.
	pty.SendLine("integrations rename doesnotexist somename")
	pty.Want("not found")
	pty.WantPrompt()

	// Error: invalid new name.
	pty.SendLine("integrations rename betterproxy BAD-NAME")
	pty.Want("invalid name")
	pty.WantPrompt()

	// Error: wrong arg count.
	pty.SendLine("integrations rename")
	pty.Want("usage")
	pty.WantPrompt()

	pty.SendLine("integrations rename onlyonearg")
	pty.Want("usage")
	pty.WantPrompt()

	// Clean up.
	pty.SendLine("integrations remove betterproxy")
	pty.Want("Removed")
	pty.WantPrompt()
}

// TestTeamIntegrations tests team integration CRUD, name uniqueness, cross-user
// visibility, and behavior when a member leaves the team.
func TestTeamIntegrations(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	// Register owner and member, create a team.
	ownerPTY, _, ownerKey, ownerEmail := registerForExeDevWithEmail(t, "teamint-owner@test.example")
	ownerPTY.Disconnect()
	memberPTY, _, memberKey, memberEmail := registerForExeDevWithEmail(t, "teamint-member@test.example")
	memberPTY.Disconnect()

	enableRootSupport(t, ownerEmail)
	createTeam(t, ownerKey, "team_int_crud", "IntTeam", ownerEmail)
	addTeamMember(t, "team_int_crud", memberEmail)

	// Also register a solo user who is NOT in a team.
	soloPTY, _, soloKey, _ := registerForExeDevWithEmail(t, "teamint-solo@test.example")
	soloPTY.Disconnect()

	t.Run("BasicCRUD", func(t *testing.T) {
		pty := sshToExeDev(t, ownerKey)

		// Add a team integration.
		pty.SendLine("integrations add http-proxy --team --name=shared-mcp --target=https://example.com --header=X-Auth:secret")
		pty.Want("Added team integration shared-mcp")
		pty.Want("tag:")
		pty.WantPrompt()

		// List should show the team integration with (team) label.
		pty.SendLine("integrations list")
		pty.Want("shared-mcp")
		pty.Want("(team)")
		pty.WantPrompt()

		// Attach team integration to a tag (no --team flag needed).
		pty.SendLine("integrations attach shared-mcp tag:production")
		pty.Want("Attached shared-mcp to tag:production")
		pty.WantPrompt()

		// Cannot attach team integration with vm: spec.
		pty.SendLine("integrations attach shared-mcp vm:myvm")
		pty.Want("team integrations only support tag:")
		pty.WantPrompt()

		// Cannot attach team integration with auto:all.
		pty.SendLine("integrations attach shared-mcp auto:all")
		pty.Want("team integrations only support tag:")
		pty.WantPrompt()

		// Detach team integration (no --team flag needed).
		pty.SendLine("integrations detach shared-mcp tag:production")
		pty.Want("Detached shared-mcp from tag:production")
		pty.WantPrompt()

		// Rename team integration (no --team flag needed).
		pty.SendLine("integrations rename shared-mcp better-mcp")
		pty.Want("Renamed integration shared-mcp to better-mcp")
		pty.WantPrompt()

		// List shows renamed.
		pty.SendLine("integrations list")
		pty.Want("better-mcp")
		pty.Want("(team)")
		pty.WantPrompt()

		// Remove team integration (no --team flag needed).
		pty.SendLine("integrations remove better-mcp")
		pty.Want("Removed integration better-mcp")
		pty.WantPrompt()

		// List should be empty now.
		pty.SendLine("integrations list")
		pty.Want("No integrations configured.")
		pty.WantPrompt()
		pty.Disconnect()
	})

	t.Run("NameUniqueness", func(t *testing.T) {
		pty := sshToExeDev(t, ownerKey)

		// Create a personal integration.
		pty.SendLine("integrations add http-proxy --name=myproxy --target=https://example.com --header=X-Auth:secret")
		pty.Want("Added integration myproxy")
		pty.WantPrompt()

		// Cannot create a team integration with the same name.
		pty.SendLine("integrations add http-proxy --team --name=myproxy --target=https://other.com --header=X-Other:val")
		pty.Want("already in use")
		pty.WantPrompt()

		// Clean up and do the reverse: team first, personal second.
		pty.SendLine("integrations remove myproxy")
		pty.Want("Removed")
		pty.WantPrompt()

		pty.SendLine("integrations add http-proxy --team --name=teamproxy --target=https://example.com --header=X-Auth:secret")
		pty.Want("Added team integration teamproxy")
		pty.WantPrompt()

		// Cannot create personal integration with the same name as team integration.
		pty.SendLine("integrations add http-proxy --name=teamproxy --target=https://other.com --header=X-Other:val")
		pty.Want("already in use")
		pty.WantPrompt()

		// Cannot rename personal integration to a team integration name.
		pty.SendLine("integrations add http-proxy --name=personal1 --target=https://example.com --header=X-Auth:s")
		pty.Want("Added integration personal1")
		pty.WantPrompt()
		pty.SendLine("integrations rename personal1 teamproxy")
		pty.Want("already in use")
		pty.WantPrompt()

		// Clean up.
		pty.SendLine("integrations remove personal1")
		pty.Want("Removed")
		pty.WantPrompt()
		pty.SendLine("integrations remove teamproxy")
		pty.Want("Removed")
		pty.WantPrompt()
		pty.Disconnect()
	})

	t.Run("CrossUserVisibility", func(t *testing.T) {
		// Owner creates a team integration.
		pty := sshToExeDev(t, ownerKey)
		pty.SendLine("integrations add http-proxy --team --name=team-shared --target=https://example.com --header=X-Auth:secret")
		pty.Want("Added team integration team-shared")
		pty.WantPrompt()
		pty.Disconnect()

		// Member should see it in their list.
		mpty := sshToExeDev(t, memberKey)
		mpty.SendLine("integrations list")
		mpty.Want("team-shared")
		mpty.Want("(team)")
		mpty.WantPrompt()

		// Member can attach it to a tag.
		mpty.SendLine("integrations attach team-shared tag:staging")
		mpty.Want("Attached team-shared to tag:staging")
		mpty.WantPrompt()

		// Member can detach it.
		mpty.SendLine("integrations detach team-shared tag:staging")
		mpty.Want("Detached team-shared from tag:staging")
		mpty.WantPrompt()

		// Member cannot create a personal integration with the same name.
		mpty.SendLine("integrations add http-proxy --name=team-shared --target=https://other.com --header=X:y")
		mpty.Want("already in use")
		mpty.WantPrompt()
		mpty.Disconnect()

		// Owner cleans up.
		pty = sshToExeDev(t, ownerKey)
		pty.SendLine("integrations remove team-shared")
		pty.Want("Removed")
		pty.WantPrompt()
		pty.Disconnect()
	})

	t.Run("MemberLeavesTeam", func(t *testing.T) {
		// Owner creates a team integration.
		pty := sshToExeDev(t, ownerKey)
		pty.SendLine("integrations add http-proxy --team --name=survive-leave --target=https://example.com --header=X-Auth:secret")
		pty.Want("Added team integration survive-leave")
		pty.WantPrompt()
		pty.Disconnect()

		// Verify member sees it.
		mpty := sshToExeDev(t, memberKey)
		mpty.SendLine("integrations list")
		mpty.Want("survive-leave")
		mpty.Want("(team)")
		mpty.WantPrompt()
		mpty.Disconnect()

		// Remove member from team.
		enableRootSupport(t, ownerEmail)
		pty = sshToExeDev(t, ownerKey)
		pty.SendLine("team remove " + memberEmail)
		pty.Want("Removed")
		pty.WantPrompt()
		pty.Disconnect()

		// Integration should still exist for the owner.
		pty = sshToExeDev(t, ownerKey)
		pty.SendLine("integrations list")
		pty.Want("survive-leave")
		pty.Want("(team)")
		pty.WantPrompt()

		// Member should no longer see it.
		mpty = sshToExeDev(t, memberKey)
		mpty.SendLine("integrations list")
		mpty.Want("No integrations configured.")
		mpty.WantPrompt()
		mpty.Disconnect()

		// Add member back and clean up.
		addTeamMember(t, "team_int_crud", memberEmail)
		pty.SendLine("integrations remove survive-leave")
		pty.Want("Removed")
		pty.WantPrompt()
		pty.Disconnect()
	})

	t.Run("NoTeamFlag", func(t *testing.T) {
		// Solo user (not in a team) cannot use --team.
		pty := sshToExeDev(t, soloKey)
		pty.SendLine("integrations add http-proxy --team --name=nope --target=https://example.com --header=X:y")
		pty.Want("--team requires being in a team")
		pty.WantPrompt()
		pty.Disconnect()
	})
}
