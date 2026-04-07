package e1e

import (
	"testing"
)

// TestSetRegion tests the set-region command end-to-end.
func TestSetRegion(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, keyFile, _ := registerForExeDev(t)

	t.Run("NoArgs", func(t *testing.T) {
		pty.SendLine("set-region")
		pty.Want("usage")
		pty.Want("available")
		pty.WantPrompt()
	})

	t.Run("InvalidRegion", func(t *testing.T) {
		pty.SendLine("set-region mars")
		pty.Want("not available")
		pty.Want("choose from")
		pty.WantPrompt()
	})

	t.Run("Success", func(t *testing.T) {
		// lax is open to all users (!RequiresUserMatch), always available.
		pty.SendLine("set-region lax")
		pty.Want("lax")
		pty.WantPrompt()

		// Confirm whoami reflects the new region.
		pty.SendLine("whoami")
		pty.Want("lax")
		pty.WantPrompt()
	})

	t.Run("JSONOutput", func(t *testing.T) {
		type setRegionOutput struct {
			Region        string `json:"region"`
			RegionDisplay string `json:"region_display"`
		}
		out := runParseExeDevJSON[setRegionOutput](t, keyFile, "set-region", "--json", "fra")
		if out.Region != "fra" {
			t.Errorf("region = %q, want %q", out.Region, "fra")
		}
		if out.RegionDisplay == "" {
			t.Error("region_display should not be empty")
		}
	})

	t.Run("RestrictedRegionBlocked", func(t *testing.T) {
		// pdx requires user match; a fresh user assigned to lax cannot pick it.
		pty.SendLine("set-region pdx")
		pty.Want("not available")
		pty.WantPrompt()
	})
}
