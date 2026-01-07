// This file tests the --exelet flag for the new command, which allows
// support users to specify which exelet a VM should be created on.

package e1e

import (
	"fmt"
	"testing"
)

func TestHostOverride(t *testing.T) {
	t.Parallel()
	noGolden(t)

	// Get the first exelet address for testing
	if len(Env.servers.Exelets) == 0 {
		t.Skip("no exelets available for testing")
	}
	exeletAddr := Env.servers.Exelets[0].Address

	// Create two users: a regular user and a support user
	regularPTY, _, _, _ := registerForExeDevWithEmail(t, "regular@test-exelet-override.example")
	supportPTY, _, _, supportEmail := registerForExeDevWithEmail(t, "support@test-exelet-override.example")

	// Test that regular user without root_support gets the sudoers joke error
	t.Run("denied_without_root_support", func(t *testing.T) {
		bn := boxName(t)
		regularPTY.sendLine(fmt.Sprintf("new --name=%s --exelet=%s", bn, exeletAddr))
		regularPTY.want("is not in the sudoers file")
		regularPTY.want("This incident will be reported")
		regularPTY.wantPrompt()
	})

	// Enable root_support for the support user
	t.Run("enable_root_support", func(t *testing.T) {
		enableRootSupport(t, supportEmail)
	})

	// Test that support user with root_support can use --exelet flag
	t.Run("allowed_with_root_support", func(t *testing.T) {
		// TODO: enable this test when e1e supports multiple exelets
		t.Skip("no point in this test yet, because e1e always uses a single exelet")
		bn := boxName(t)
		supportPTY.sendLine(fmt.Sprintf("new --name=%s --exelet=%s", bn, exeletAddr))
		supportPTY.reject("is not in the sudoers file")
		supportPTY.wantRe("Creating .*" + bn)
		supportPTY.want("Ready")
		supportPTY.wantPrompt()
		supportPTY.deleteBox(bn)
	})

	// Test that invalid exelet shows the list of valid exelets
	t.Run("invalid_exelet_shows_list", func(t *testing.T) {
		bn := boxName(t)
		supportPTY.sendLine(fmt.Sprintf("new --name=%s --exelet=tcp://invalid:9999", bn))
		supportPTY.want("not found")
		supportPTY.want("Available exelets")
		supportPTY.wantPrompt()
	})

	regularPTY.disconnect()
	supportPTY.disconnect()
}
