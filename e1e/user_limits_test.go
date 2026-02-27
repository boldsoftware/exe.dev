// This file tests per-user resource limit overrides.
// Users can have custom max_memory, max_disk, and max_cpus limits set,
// which allow them to create VMs with resources beyond the default limits.

package e1e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

// TestUserLimits tests that per-user resource limit overrides work correctly.
func TestUserLimits(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	// Create a regular user
	regularPTY, _, _, regularEmail := registerForExeDevWithEmail(t, "regular@test-userlimits.example")

	// First, verify that without custom limits, user can't exceed defaults
	t.Run("no-lim", func(t *testing.T) {
		bn := boxName(t)
		// Test env default memory is 1GB (stage.Test()), asking for 3GB should fail
		regularPTY.SendLine(fmt.Sprintf("new --name=%s --memory=3GB", bn))
		regularPTY.Want("--memory cannot exceed")
		regularPTY.WantPrompt()
	})

	// Now set custom limits for the user via debug endpoint
	t.Run("set-lim", func(t *testing.T) {
		setUserLimits(t, regularEmail, `{"max_memory": 4000000000, "max_disk": 30000000000, "max_cpus": 4}`)
	})

	// Now the user should be able to create with higher memory
	t.Run("with-lim", func(t *testing.T) {
		bn := boxName(t)
		// 3GB should now be allowed (within our 4GB limit)
		regularPTY.SendLine(fmt.Sprintf("new --name=%s --memory=3GB", bn))
		regularPTY.WantRE("Creating")
		regularPTY.Want("Ready")
		regularPTY.WantPrompt()
		regularPTY.deleteBox(bn)
	})

	// But user still can't exceed their custom limit
	t.Run("capped", func(t *testing.T) {
		bn := boxName(t)
		// 5GB exceeds the 4GB custom limit
		regularPTY.SendLine(fmt.Sprintf("new --name=%s --memory=5GB", bn))
		regularPTY.Want("--memory cannot exceed")
		regularPTY.WantPrompt()
	})

	// Clear the limits
	t.Run("clear", func(t *testing.T) {
		setUserLimits(t, regularEmail, "")
	})

	// Now user should be back to defaults
	t.Run("reset", func(t *testing.T) {
		bn := boxName(t)
		regularPTY.SendLine(fmt.Sprintf("new --name=%s --memory=3GB", bn))
		regularPTY.Want("--memory cannot exceed")
		regularPTY.WantPrompt()
	})

	regularPTY.Disconnect()
}

// TestUserLimitsCp tests that cp command also respects user limits.
func TestUserLimitsCp(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 2)
	noGolden(t)

	// Create a user
	pty, _, keyFile, email := registerForExeDevWithEmail(t, "cpuser@test-userlimits.example")

	// Create a source box with default resources
	sourceBox := newBox(t, pty)
	pty.Disconnect()
	waitForSSH(t, sourceBox, keyFile)

	// Try to copy with higher memory - should fail without custom limits
	t.Run("no-lim", func(t *testing.T) {
		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("cp %s copied-test --memory=3GB", sourceBox))
		repl.Want("--memory cannot exceed")
		repl.WantPrompt()
		repl.Disconnect()
	})

	// Set custom limits
	t.Run("set-lim", func(t *testing.T) {
		setUserLimits(t, email, `{"max_memory": 4000000000}`)
	})

	// Now copy with higher memory should work
	t.Run("with-lim", func(t *testing.T) {
		copiedBox := "copied-with-mem"
		repl := sshToExeDev(t, keyFile)
		repl.SendLine(fmt.Sprintf("cp %s %s --memory=3GB", sourceBox, copiedBox))
		repl.Want("Copying")
		repl.WantPrompt()
		repl.Disconnect()

		// Wait for copied box and cleanup
		waitForSSH(t, copiedBox, keyFile)
		cleanupBox(t, keyFile, copiedBox)
	})

	// Cleanup
	cleanupBox(t, keyFile, sourceBox)
}

// setUserLimits sets resource limits for a user via the debug endpoint.
// Pass empty string to clear limits.
func setUserLimits(t *testing.T, email, limitsJSON string) {
	t.Helper()

	// Get user ID from email
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/debug/users?format=json", Env.servers.Exed.HTTPPort))
	if err != nil {
		t.Fatalf("failed to get users list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status getting users list: %d", resp.StatusCode)
	}

	type userInfo struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}
	var users []userInfo
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		t.Fatalf("failed to parse users JSON: %v", err)
	}

	var userID string
	for _, u := range users {
		if u.Email == email {
			userID = u.UserID
			break
		}
	}
	if userID == "" {
		t.Fatalf("user %s not found in users list", email)
	}

	form := url.Values{}
	form.Add("user_id", userID)
	form.Add("limits", limitsJSON)

	resp, err = http.PostForm(
		fmt.Sprintf("http://localhost:%d/debug/users/set-limits", Env.servers.Exed.HTTPPort),
		form,
	)
	if err != nil {
		t.Fatalf("failed to set user limits: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status setting user limits: %d", resp.StatusCode)
	}
}
