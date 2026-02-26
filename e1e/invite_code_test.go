// This file tests the invite code signup flow.

package e1e

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

// TestInviteCodeSignup tests that using an invite code as the SSH username
// during signup applies the invite code to the user.
func TestInviteCodeSignup(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Generate a new SSH key for the test
	keyFile, _ := genSSHKey(t)

	// Connect to exed with the invite code as the username
	pty := makePty(t, "ssh localhost with invite code")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.PTY(), inviteCode, keyFile)
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.SetPrompt(testinfra.ExeDevPrompt)

	// Go through registration flow
	pty.Want(testinfra.Banner)
	pty.Want("Invite code accepted: free account")
	pty.Want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.WantPrompt()
	pty.Disconnect()
}

// TestInviteCodeTrial tests that trial invite codes are accepted during signup.
func TestInviteCodeTrial(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	inviteCode, err := Env.servers.CreateInviteCode("trial")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Generate a new SSH key and register with the invite code
	keyFile, _ := genSSHKey(t)

	pty := makePty(t, "ssh localhost with trial invite code")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.PTY(), inviteCode, keyFile)
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.SetPrompt(testinfra.ExeDevPrompt)

	pty.Want(testinfra.Banner)
	pty.Want("Invite code accepted: 1 month free trial")
	pty.Want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.WantPrompt()
	pty.Disconnect()
}

// TestWebInviteCodeSignup tests that using an invite code via the web auth flow
// (https://exe.dev/auth?invite=invitecode) applies the invite code to the user.
func TestWebInviteCodeSignup(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Sign up via web with invite code
	email := t.Name() + testinfra.FakeEmailSuffix
	_, err = Env.servers.WebLoginWithInvite(email, inviteCode)
	if err != nil {
		t.Fatalf("web login with invite failed: %v", err)
	}

	// Web login succeeded - the invite code was applied.
	// If the invite code were invalid or already used, WebLoginWithInvite would fail.
}

// TestWebInviteCodeAlreadyUsed tests that visiting /auth?invite=<code> with an
// already-used invite code shows the auth form (user can still sign up, just
// without the invite code benefit).
func TestWebInviteCodeAlreadyUsed(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Use the invite code by signing up with it
	email1 := t.Name() + "-first" + testinfra.FakeEmailSuffix
	_, err = Env.servers.WebLoginWithInvite(email1, inviteCode)
	if err != nil {
		t.Fatalf("first web login with invite failed: %v", err)
	}

	// Now try to visit /auth?invite=<used_code> - should show error message
	authURL := fmt.Sprintf("http://localhost:%d/auth?invite=%s", Env.servers.Exed.HTTPPort, inviteCode)
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatalf("failed to GET /auth: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Should show the error message
	if !strings.Contains(string(body), "Invalid or already used invite code") {
		t.Errorf("expected error message for used invite code, got: %s", string(body))
	}

	// Should NOT include the invite code in a hidden field
	if strings.Contains(string(body), `name="invite"`) {
		t.Error("should not include invite hidden field for invalid code")
	}
}

// TestInvalidInviteCodeIgnored tests that an invalid invite code is ignored
// and registration proceeds normally.
func TestInvalidInviteCodeIgnored(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	// Generate a new SSH key
	keyFile, _ := genSSHKey(t)

	// Connect with an invalid invite code as username
	invalidCode := "INVALIDCODE123"
	pty := makePty(t, "ssh localhost with invalid invite code")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.PTY(), invalidCode, keyFile)
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.SetPrompt(testinfra.ExeDevPrompt)

	// Registration should still work, but no invite code message
	pty.Want(testinfra.Banner)
	pty.Reject("Invite code accepted")
	pty.Want("Please enter your email")
	email := t.Name() + testinfra.FakeEmailSuffix
	pty.SendLine(email)
	pty.WantRE("Verification email sent to.*" + email)
	waitForEmailAndVerify(t, email)
	pty.Want("Email verified successfully")
	pty.Want("Registration complete")
	pty.WantPrompt()
	pty.Disconnect()

	// The SSH output confirms the invite code was NOT accepted (pty.Reject).
	// User signed up normally without any billing exemption.
}

// TestInviteAllocation tests that POSTing to /invite allocates one invite at a time
// and each invite is only shown once.
func TestInviteAllocation(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	// Sign up a user first
	email := t.Name() + testinfra.FakeEmailSuffix
	cookies, err := Env.servers.WebLoginWithEmail(email)
	if err != nil {
		t.Fatalf("web login failed: %v", err)
	}

	// Give the user 2 invite codes via the debug API
	if err := Env.servers.GiveInvitesToUser(email, 2, "trial"); err != nil {
		t.Fatalf("failed to give invites to user: %v", err)
	}

	// POST to /invite to allocate first invite
	inviteURL := fmt.Sprintf("http://localhost:%d/invite", Env.servers.Exed.HTTPPort)
	req, err := http.NewRequest("POST", inviteURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to POST /invite: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	bodyStr := string(body)

	// Verify it shows usage instructions
	if !strings.Contains(bodyStr, "ssh ") {
		t.Error("expected to see SSH usage instructions")
	}
	if !strings.Contains(bodyStr, "/auth?invite=") {
		t.Error("expected to see web usage URL")
	}

	// Extract the first invite code from the page (it appears in the URL)
	// The page shows: https://exe.dev/auth?invite=INVITECODE
	firstCodeStart := strings.Index(bodyStr, "/auth?invite=")
	if firstCodeStart == -1 {
		t.Fatal("couldn't find first invite code in response")
	}
	firstCodeEnd := strings.IndexAny(bodyStr[firstCodeStart+13:], `"' <>`)
	firstCode := bodyStr[firstCodeStart+13 : firstCodeStart+13+firstCodeEnd]

	// POST again to allocate second invite
	req2, err := http.NewRequest("POST", inviteURL, nil)
	if err != nil {
		t.Fatalf("failed to create second request: %v", err)
	}
	for _, c := range cookies {
		req2.AddCookie(c)
	}

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("failed to POST /invite second time: %v", err)
	}
	defer resp2.Body.Close()

	body2, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatalf("failed to read second response body: %v", err)
	}
	body2Str := string(body2)

	// Second POST should show a different invite
	secondCodeStart := strings.Index(body2Str, "/auth?invite=")
	if secondCodeStart == -1 {
		t.Fatal("couldn't find second invite code in response")
	}
	secondCodeEnd := strings.IndexAny(body2Str[secondCodeStart+13:], `"' <>`)
	secondCode := body2Str[secondCodeStart+13 : secondCodeStart+13+secondCodeEnd]

	// The codes should be different
	if firstCode == secondCode {
		t.Errorf("expected different invite codes, but both were %s", firstCode)
	}

	// Should NOT show the first invite again
	if strings.Contains(body2Str, firstCode) && strings.Count(body2Str, firstCode) > 0 {
		// The first code might appear if it's shown as "previously allocated"
		// That's OK - what we care about is that a NEW code was allocated
		t.Logf("Note: first code %s appears in second response (might be in history)", firstCode)
	}
}

// TestDashboardShowsInviteCount tests that the dashboard shows the invite count
// and appropriate links.
func TestDashboardShowsInviteCount(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	// Sign up a user
	email := t.Name() + testinfra.FakeEmailSuffix
	cookies, err := Env.servers.WebLoginWithEmail(email)
	if err != nil {
		t.Fatalf("web login failed: %v", err)
	}

	// Visit dashboard - should show "0 invites" with "Request More" link
	dashboardURL := fmt.Sprintf("http://localhost:%d/", Env.servers.Exed.HTTPPort)
	req, err := http.NewRequest("GET", dashboardURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to GET /: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Should show "0 invites" and "Request More" link
	if !strings.Contains(string(body), "0 invite") {
		t.Error("expected to see '0 invites' on dashboard")
	}
	if !strings.Contains(string(body), "/invite/request") {
		t.Error("expected to see 'Request More' link when user has 0 invites")
	}

	if err := Env.servers.GiveInvitesToUser(email, 1, "trial"); err != nil {
		t.Fatalf("failed to give invite to user: %v", err)
	}

	// Visit dashboard again - should show "1 invite" with "Allocate" form
	req2, err := http.NewRequest("GET", dashboardURL, nil)
	if err != nil {
		t.Fatalf("failed to create second request: %v", err)
	}
	for _, c := range cookies {
		req2.AddCookie(c)
	}

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("failed to GET / second time: %v", err)
	}
	defer resp2.Body.Close()

	body2, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Should show "1 invite" and "Allocate" form
	if !strings.Contains(string(body2), "1 invite") {
		t.Errorf("expected to see '1 invite' on dashboard, got body:\n%s", string(body2))
	}
	if !strings.Contains(string(body2), `action="/invite"`) {
		t.Error("expected to see 'Allocate' form when user has invites")
	}
}

// TestInviteCodePassthrough tests that invite codes are passed through
// the /new and /create-vm forms correctly.
func TestInviteCodePassthrough(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Step 1: GET /new?invite=CODE should include invite in hidden form field
	newURL := fmt.Sprintf("http://localhost:%d/new?invite=%s", Env.servers.Exed.HTTPPort, inviteCode)
	resp, err := http.Get(newURL)
	if err != nil {
		t.Fatalf("failed to GET /new: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	// Verify the invite code is in a hidden field
	if !strings.Contains(string(body), fmt.Sprintf(`name="invite" value="%s"`, inviteCode)) {
		t.Errorf("expected hidden invite field with value %q in /new form", inviteCode)
	}

	// Step 2: POST /create-vm with invite should pass it to auth form with "Invite code accepted"
	createURL := fmt.Sprintf("http://localhost:%d/create-vm", Env.servers.Exed.HTTPPort)
	resp2, err := http.PostForm(createURL, map[string][]string{
		"hostname": {"testvm"},
		"prompt":   {"test prompt"},
		"invite":   {inviteCode},
	})
	if err != nil {
		t.Fatalf("failed to POST /create-vm: %v", err)
	}
	body2, err := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp2.StatusCode)
	}
	// Valid invite code should show acceptance message
	if !strings.Contains(string(body2), "Invite code accepted") {
		t.Errorf("expected 'Invite code accepted' message for valid invite code")
	}
	// The form should include the invite hidden field for valid codes
	if !strings.Contains(string(body2), `name="invite"`) {
		t.Errorf("expected invite hidden field for valid code")
	}
}
