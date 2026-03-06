package e1e

import (
	"fmt"
	"testing"
)

// TestIntegrationAttachmentSpecs tests the new attachment spec system:
// vm:<name>, tag:<name>, auto:all, and backward-compatible bare VM names.
func TestIntegrationAttachmentSpecs(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)
	defer pty.Disconnect()

	// Create a VM to work with.
	bn := boxName(t)
	pty.SendLine(fmt.Sprintf("new --name=%s", bn))
	pty.WantRE("Creating .*" + bn)
	pty.Want("Ready")
	pty.WantPrompt()
	waitForSSH(t, bn, keyFile)

	// Add a tag to the box.
	pty.SendLine(fmt.Sprintf("tag %s prod", bn))
	pty.Want("Added")
	pty.WantPrompt()

	// Add an integration.
	pty.SendLine("integrations add http-proxy --name=testint --target=https://example.com --header=X-Auth:secret")
	pty.Want("Added integration testint")
	pty.WantPrompt()

	// List should show (none) for attachments.
	pty.SendLine("integrations list")
	pty.Want("testint")
	pty.Want("(none)")
	pty.WantPrompt()

	// --- vm: spec ---

	t.Run("AttachVMSpec", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		defer repl.Disconnect()

		repl.SendLine(fmt.Sprintf("integrations attach testint vm:%s", bn))
		repl.Want("Attached testint to vm:" + bn)
		repl.WantPrompt()

		// List should show the attachment.
		repl.SendLine("integrations list")
		repl.Want("vm:" + bn)
		repl.WantPrompt()

		// Duplicate attach should fail.
		repl.SendLine(fmt.Sprintf("integrations attach testint vm:%s", bn))
		repl.Want("already attached")
		repl.WantPrompt()

		// Detach.
		repl.SendLine(fmt.Sprintf("integrations detach testint vm:%s", bn))
		repl.Want("Detached testint from vm:" + bn)
		repl.WantPrompt()
	})

	// --- Bare VM name backward compat ---

	t.Run("AttachBareVMName", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		defer repl.Disconnect()

		// Bare VM name should auto-prefix with vm:
		repl.SendLine(fmt.Sprintf("integrations attach testint %s", bn))
		repl.Want("Attached testint to vm:" + bn)
		repl.WantPrompt()

		// List should show vm: prefix.
		repl.SendLine("integrations list")
		repl.Want("vm:" + bn)
		repl.WantPrompt()

		// Detach with bare name.
		repl.SendLine(fmt.Sprintf("integrations detach testint %s", bn))
		repl.Want("Detached testint from vm:" + bn)
		repl.WantPrompt()
	})

	// --- tag: spec ---

	t.Run("AttachTagSpec", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		defer repl.Disconnect()

		repl.SendLine("integrations attach testint tag:prod")
		repl.Want("Attached testint to tag:prod")
		repl.WantPrompt()

		// List should show the tag attachment.
		repl.SendLine("integrations list")
		repl.Want("tag:prod")
		repl.WantPrompt()

		// Duplicate should fail.
		repl.SendLine("integrations attach testint tag:prod")
		repl.Want("already attached")
		repl.WantPrompt()

		// Detach.
		repl.SendLine("integrations detach testint tag:prod")
		repl.Want("Detached testint from tag:prod")
		repl.WantPrompt()
	})

	// --- auto:all spec ---

	t.Run("AttachAutoAll", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		defer repl.Disconnect()

		repl.SendLine("integrations attach testint auto:all")
		repl.Want("Attached testint to auto:all")
		repl.WantPrompt()

		// List should show auto:all.
		repl.SendLine("integrations list")
		repl.Want("auto:all")
		repl.WantPrompt()

		// Duplicate should fail.
		repl.SendLine("integrations attach testint auto:all")
		repl.Want("already attached")
		repl.WantPrompt()

		// Detach.
		repl.SendLine("integrations detach testint auto:all")
		repl.Want("Detached testint from auto:all")
		repl.WantPrompt()
	})

	// --- Multiple attachments ---

	t.Run("MultipleAttachments", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		defer repl.Disconnect()

		repl.SendLine(fmt.Sprintf("integrations attach testint vm:%s", bn))
		repl.Want("Attached")
		repl.WantPrompt()

		repl.SendLine("integrations attach testint tag:prod")
		repl.Want("Attached")
		repl.WantPrompt()

		repl.SendLine("integrations attach testint auto:all")
		repl.Want("Attached")
		repl.WantPrompt()

		// List should show all three.
		repl.SendLine("integrations list")
		repl.Want("vm:" + bn)
		repl.Want("tag:prod")
		repl.Want("auto:all")
		repl.WantPrompt()

		// Detach one at a time.
		repl.SendLine("integrations detach testint tag:prod")
		repl.Want("Detached")
		repl.WantPrompt()

		repl.SendLine("integrations detach testint auto:all")
		repl.Want("Detached")
		repl.WantPrompt()

		repl.SendLine(fmt.Sprintf("integrations detach testint vm:%s", bn))
		repl.Want("Detached")
		repl.WantPrompt()

		// Should be empty now.
		repl.SendLine("integrations list")
		repl.Want("(none)")
		repl.WantPrompt()
	})

	// --- Validation errors ---

	t.Run("InvalidSpecs", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		defer repl.Disconnect()

		// Invalid tag name.
		repl.SendLine("integrations attach testint tag:INVALID")
		repl.Want("invalid tag name")
		repl.WantPrompt()

		// Invalid spec format.
		repl.SendLine("integrations attach testint foo:bar")
		repl.Want("invalid attachment spec")
		repl.WantPrompt()

		// vm: with nonexistent VM.
		repl.SendLine("integrations attach testint vm:nonexistent-vm-xyz")
		repl.Want("not found")
		repl.WantPrompt()

		// Detach something that's not attached.
		repl.SendLine("integrations detach testint tag:notattached")
		repl.Want("not attached")
		repl.WantPrompt()

		// Wrong arg count.
		repl.SendLine("integrations attach testint")
		repl.Want("usage")
		repl.WantPrompt()

		repl.SendLine("integrations detach testint")
		repl.Want("usage")
		repl.WantPrompt()
	})

	// Clean up.
	pty.SendLine("integrations remove testint")
	pty.Want("Removed")
	pty.WantPrompt()

	cleanupBox(t, keyFile, bn)
}
