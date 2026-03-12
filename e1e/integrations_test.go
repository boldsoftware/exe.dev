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
	pty.Want("ssh <vm> curl http://myproxy.int.exe.cloud/")
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
	pty.Want("already be in use")
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
