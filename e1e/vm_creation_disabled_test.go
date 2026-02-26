package e1e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestNewVMCreationDisabled(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, _, _, email := registerForExeDev(t)

	// Get user_id from debug endpoint
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/debug/users?format=json", Env.servers.Exed.HTTPPort))
	if err != nil {
		t.Fatalf("failed to get users: %v", err)
	}
	defer resp.Body.Close()

	var users []struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		t.Fatalf("failed to parse users: %v", err)
	}

	var userID string
	for _, u := range users {
		if u.Email == email {
			userID = u.UserID
			break
		}
	}
	if userID == "" {
		t.Fatalf("user %s not found", email)
	}

	// Disable VM creation for this user
	resp, err = http.PostForm(
		fmt.Sprintf("http://localhost:%d/debug/users/toggle-vm-creation", Env.servers.Exed.HTTPPort),
		url.Values{"user_id": {userID}, "disable": {"1"}},
	)
	if err != nil {
		t.Fatalf("failed to disable vm creation: %v", err)
	}
	resp.Body.Close()

	// Try to create a box - should fail
	pty.SendLine("new")
	pty.Want("not available for your account")
	pty.WantPrompt()
	pty.Disconnect()
}
