package e1e

import (
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestExeletsCommand(t *testing.T) {
	t.Parallel()
	noGolden(t)

	// Create two users: a regular user and a support user
	regularPTY, _, _, _ := registerForExeDevWithEmail(t, "regular@test-exelets-cmd.example")
	supportPTY, _, supportKeyFile, supportEmail := registerForExeDevWithEmail(t, "support@test-exelets-cmd.example")

	// Test that regular user without root_support gets the sudoers joke error
	t.Run("denied_without_root_support", func(t *testing.T) {
		regularPTY.SendLine("exelets")
		regularPTY.Want("is not in the sudoers file")
		regularPTY.Want("This incident will be reported")
		regularPTY.WantPrompt()
	})

	// Enable root_support for the support user
	t.Run("enable_root_support", func(t *testing.T) {
		enableRootSupport(t, supportEmail)
	})

	// Test that support user with root_support can use the exelets command
	t.Run("allowed_with_root_support", func(t *testing.T) {
		supportPTY.SendLine("exelets")
		supportPTY.Reject("is not in the sudoers file")
		supportPTY.Want("healthy")
		supportPTY.WantPrompt()
	})

	// Test JSON output via SSH command (non-PTY)
	t.Run("json_output", func(t *testing.T) {
		type exeletsOutput struct {
			Exelets []struct {
				Address       string `json:"address"`
				Host          string `json:"host"`
				Version       string `json:"version"`
				Arch          string `json:"arch"`
				Status        string `json:"status"`
				IsPreferred   bool   `json:"is_preferred"`
				InstanceCount int    `json:"instance_count"`
				Error         string `json:"error,omitempty"`
			} `json:"exelets"`
		}

		result, err := testinfra.RunParseExeDevJSON[exeletsOutput](Env.context(t), Env.servers, supportKeyFile, "exelets", "--json")
		if err != nil {
			t.Fatalf("failed to run exelets --json: %v", err)
		}

		if len(result.Exelets) == 0 {
			t.Fatal("expected at least one exelet in JSON output")
		}

		// Check that we have an address, host, and status
		for _, e := range result.Exelets {
			if e.Address == "" {
				t.Error("expected exelet address to be non-empty")
			}
			if e.Host == "" {
				t.Error("expected exelet host to be non-empty")
			}
			if e.Status != "healthy" && e.Status != "error" {
				t.Errorf("unexpected exelet status: %q", e.Status)
			}
		}
	})

	// Test that the command doesn't appear in help for regular users
	t.Run("hidden_from_help", func(t *testing.T) {
		regularPTY.SendLine("help")
		regularPTY.Reject("exelets")
		regularPTY.WantPrompt()
	})

	regularPTY.Disconnect()
	supportPTY.Disconnect()
}
