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
	noGolden(t)

	pty, _, _, _ := registerForExeDev(t)

	// List with no integrations.
	pty.SendLine("integrations list")
	pty.Want("No integrations configured.")
	pty.WantPrompt()

	// Add an http-proxy integration.
	pty.SendLine("integrations add http-proxy --name=myproxy --target=https://example.com/api --header=X-Auth:secret123")
	pty.Want("Added integration myproxy")
	pty.WantPrompt()

	// List should now show the integration by name.
	pty.SendLine("integrations list")
	pty.Want("myproxy")
	pty.Want("http-proxy")
	pty.Want("target=https://example.com/api")
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
	pty.Want("--header is required")
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
	pty.SendLine("integrations add http-proxy --name=bad --target=http://example.com/api --header=X-Foo:bar")
	pty.Want("scheme must be https")
	pty.WantPrompt()

	// Test validation: non-443 port.
	pty.SendLine("integrations add http-proxy --name=bad --target=https://example.com:8080/api --header=X-Foo:bar")
	pty.Want("port 443")
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
	pty.SendLine("integrations add http-proxy --name=bad --target=https://192.168.1.1/api --header=X-Foo:bar")
	pty.Want("hostname, not an IP")
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
	pty.SendLine("integrations add http-proxy --name=myproxy --target=https://example.com/api --header=X-Auth:secret123")
	pty.Want("Added integration myproxy")
	pty.WantPrompt()

	// Attach by name.
	pty.SendLine(fmt.Sprintf("integrations attach myproxy %s", bn))
	pty.Want("Attached myproxy to " + bn)
	pty.WantPrompt()

	// Detach by name.
	pty.SendLine(fmt.Sprintf("integrations detach myproxy %s", bn))
	pty.Want("Detached myproxy from " + bn)
	pty.WantPrompt()

	// Error: attach nonexistent integration.
	pty.SendLine(fmt.Sprintf("integrations attach nope %s", bn))
	pty.Want("not found")
	pty.WantPrompt()

	// Error: attach to nonexistent VM.
	pty.SendLine("integrations attach myproxy nonexistent-vm-abc")
	pty.Want("not found")
	pty.WantPrompt()

	// Error: detach nonexistent integration.
	pty.SendLine(fmt.Sprintf("integrations detach nope %s", bn))
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
	pty.SendLine("integrations add http-proxy --name=myproxy --target=https://example.com/api --header=X-Auth:secret123")
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
