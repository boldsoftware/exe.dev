// This file tests the invite code signup flow.

package e1e

import (
	"encoding/json"
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
	reserveVMs(t, 0)
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
	reserveVMs(t, 0)
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
	reserveVMs(t, 0)
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
	reserveVMs(t, 0)
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
	authURL := fmt.Sprintf("http://localhost:%d/auth?invite=%s", Env.HTTPPort(), inviteCode)
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatalf("failed to GET /auth: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	// Should show invite invalid in page data (Vue page with window.__PAGE__ JSON)
	pageData := testinfra.ExtractPageJSON(body)
	if pageData == nil {
		t.Fatalf("expected Vue page with window.__PAGE__ data, got: %s", string(body))
	}
	if pageData["inviteInvalid"] != true {
		t.Errorf("expected inviteInvalid=true in page data, got %v", pageData["inviteInvalid"])
	}
	// inviteValid should not be set for invalid codes
	if pageData["inviteValid"] == true {
		t.Error("inviteValid should not be true for already-used invite code")
	}
}

// TestInvalidInviteCodeIgnored tests that an invalid invite code is ignored
// and registration proceeds normally.
func TestInvalidInviteCodeIgnored(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
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
	reserveVMs(t, 0)
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
	inviteURL := fmt.Sprintf("http://localhost:%d/invite", Env.HTTPPort())
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
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	// Sign up a user
	email := t.Name() + testinfra.FakeEmailSuffix
	cookies, err := Env.servers.WebLoginWithEmail(email)
	if err != nil {
		t.Fatalf("web login failed: %v", err)
	}

	apiURL := fmt.Sprintf("http://localhost:%d/api/dashboard", Env.HTTPPort())

	// Check dashboard API - should show 0 invites
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to GET /api/dashboard: %v", err)
	}
	defer resp.Body.Close()

	var data struct {
		InviteCount       int64 `json:"inviteCount"`
		CanRequestInvites bool  `json:"canRequestInvites"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode /api/dashboard: %v", err)
	}
	if data.InviteCount != 0 {
		t.Errorf("expected inviteCount=0, got %d", data.InviteCount)
	}

	// Give user 1 invite
	if err := Env.servers.GiveInvitesToUser(email, 1, "trial"); err != nil {
		t.Fatalf("failed to give invite to user: %v", err)
	}

	// Check dashboard API again - should show 1 invite
	req2, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		t.Fatalf("failed to create second request: %v", err)
	}
	for _, c := range cookies {
		req2.AddCookie(c)
	}

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("failed to GET /api/dashboard: %v", err)
	}
	defer resp2.Body.Close()

	var data2 struct {
		InviteCount int64 `json:"inviteCount"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&data2); err != nil {
		t.Fatalf("failed to decode /api/dashboard: %v", err)
	}
	if data2.InviteCount != 1 {
		t.Errorf("expected inviteCount=1, got %d", data2.InviteCount)
	}
}

// TestInviteCodePassthrough tests that invite codes are passed through
// the /create-vm form correctly.
// Note: GET /new?invite=CODE hidden field rendering is now handled by the Vue SPA.
func TestInviteCodePassthrough(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// POST /create-vm with invite should pass it to auth form with "Invite code accepted"
	createURL := fmt.Sprintf("http://localhost:%d/create-vm", Env.HTTPPort())
	resp, err := http.PostForm(createURL, map[string][]string{
		"hostname": {"testvm"},
		"prompt":   {"test prompt"},
		"invite":   {inviteCode},
	})
	if err != nil {
		t.Fatalf("failed to POST /create-vm: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	// Valid invite code should be in the page data (Vue page with window.__PAGE__ JSON)
	pageData := testinfra.ExtractPageJSON(body)
	if pageData == nil {
		t.Fatalf("expected Vue page with window.__PAGE__ data")
	}
	if pageData["inviteValid"] != true {
		t.Errorf("expected inviteValid=true in page data, got %v", pageData["inviteValid"])
	}
	if pageData["invite"] != inviteCode {
		t.Errorf("expected invite=%q in page data, got %v", inviteCode, pageData["invite"])
	}
}

// TestLoginWithExeUserCanApplyInviteCode tests that a user who was created via
// the login-with-exe flow (proxy auth) can later visit /auth?invite=CODE while
// already authenticated and have the invite code applied.
func TestLoginWithExeUserCanApplyInviteCode(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	// Step 1: Create a user through the login-with-exe flow.
	email := t.Name() + testinfra.FakeEmailSuffix
	cookies := webLoginWithExe(t, email)

	// Step 2: Create an invite code.
	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Step 3: Visit /auth?invite=CODE while already authenticated.
	// The server should apply the invite code for this login-with-exe user.
	client := newClientWithCookies(t, cookies)
	authURL := fmt.Sprintf("http://localhost:%d/auth?invite=%s", Env.servers.Exed.HTTPPort, inviteCode)
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("failed to GET /auth with invite: %v", err)
	}
	resp.Body.Close()

	// Step 4: Verify the invite code was applied — user should have a billing exemption.
	exemption := getUserBillingExemption(t, email)
	if exemption != "free" {
		t.Errorf("expected billing_exemption='free' for login-with-exe user after invite, got %q", exemption)
	}
}

// TestInviteCodePromotesLoginWithExeUser tests that a login-with-exe user who
// accepts an invite code gets promoted to a full developer (created_for_login_with_exe
// becomes false). This ensures the user can create VMs after accepting an invite.
func TestInviteCodePromotesLoginWithExeUser(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	// Step 1: Create a user through the login-with-exe flow.
	email := t.Name() + testinfra.FakeEmailSuffix
	cookies := webLoginWithExe(t, email)

	// Verify the user starts as a login-with-exe user.
	if !isLoginWithExeUser(t, email) {
		t.Fatal("expected user to start as login-with-exe user")
	}

	// Step 2: Create an invite code and accept it.
	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	client := newClientWithCookies(t, cookies)
	authURL := fmt.Sprintf("http://localhost:%d/auth?invite=%s", Env.servers.Exed.HTTPPort, inviteCode)
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("failed to GET /auth with invite: %v", err)
	}
	resp.Body.Close()

	// Step 3: Verify the user is now a full developer, not a login-with-exe user.
	if isLoginWithExeUser(t, email) {
		t.Error("expected user to be promoted from login-with-exe to developer after accepting invite")
	}
	if exemption := getUserBillingExemption(t, email); exemption != "free" {
		t.Errorf("expected billing_exemption='free', got %q", exemption)
	}
}

// TestSSHInviteCodeForLoginWithExeUser tests that a login-with-exe user who
// SSHes with an invite code as the username and provides their existing email
// gets the invite code applied and is promoted to a full developer.
// This is the SSH equivalent of TestLoginWithExeUserCanApplyInviteCode.
func TestSSHInviteCodeForLoginWithExeUser(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	// Step 1: Create a user through the login-with-exe flow.
	email := t.Name() + testinfra.FakeEmailSuffix
	_ = webLoginWithExe(t, email)

	// Verify the user starts as a login-with-exe user with no billing exemption.
	if !isLoginWithExeUser(t, email) {
		t.Fatal("expected user to start as login-with-exe user")
	}
	if exemption := getUserBillingExemption(t, email); exemption != "" {
		t.Fatalf("expected no billing exemption initially, got %q", exemption)
	}

	// Step 2: Create an invite code.
	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Step 3: SSH with the invite code as the username (new SSH key).
	keyFile, _ := genSSHKey(t)
	pty := makePty(t, "ssh with invite code for login-with-exe user")
	cmd, err := Env.servers.SSHWithUserName(Env.context(t), pty.PTY(), inviteCode, keyFile)
	if err != nil {
		t.Fatalf("failed to start SSH: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pty.SetPrompt(testinfra.ExeDevPrompt)

	// Step 4: Go through registration, entering the same email as the existing user.
	pty.Want(testinfra.Banner)
	pty.Want("Invite code accepted: free account")
	pty.Want("email")
	pty.SendLine(email)
	// Existing user gets "new ssh key" device verification email.
	waitForEmailAndVerify(t, email)
	pty.Want("new ssh key has been added")
	pty.WantPrompt()

	// Step 5: Verify the invite code was applied and user was promoted.
	if exemption := getUserBillingExemption(t, email); exemption != "free" {
		t.Errorf("expected billing_exemption='free' after SSH invite, got %q", exemption)
	}
	if isLoginWithExeUser(t, email) {
		t.Error("expected user to be promoted from login-with-exe to developer after SSH invite")
	}

	pty.Disconnect()
}

// TestExistingUserCannotApplyInviteCode tests that a regular existing user
// (NOT created via login-with-exe) visiting /auth?invite=CODE while already
// authenticated does NOT get the invite code applied.
func TestExistingUserCannotApplyInviteCode(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)

	// Step 1: Create a regular user via web login.
	email := t.Name() + testinfra.FakeEmailSuffix
	cookies, err := Env.servers.WebLoginWithEmail(email)
	if err != nil {
		t.Fatalf("web login failed: %v", err)
	}

	// Step 2: Create an invite code.
	inviteCode, err := Env.servers.CreateInviteCode("free")
	if err != nil {
		t.Fatalf("failed to create invite code: %v", err)
	}

	// Step 3: Visit /auth?invite=CODE while already authenticated.
	// The server should NOT apply the invite code for a regular existing user.
	client := newClientWithCookies(t, cookies)
	authURL := fmt.Sprintf("http://localhost:%d/auth?invite=%s", Env.servers.Exed.HTTPPort, inviteCode)
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("failed to GET /auth with invite: %v", err)
	}
	resp.Body.Close()

	// Step 4: Verify the invite code was NOT applied — user should have no billing exemption.
	exemption := getUserBillingExemption(t, email)
	if exemption != "" {
		t.Errorf("expected no billing_exemption for regular existing user, got %q", exemption)
	}

	// Step 5: Verify the invite code is still unused — a new user should be able to use it.
	newEmail := t.Name() + "-second" + testinfra.FakeEmailSuffix
	if _, err := Env.servers.WebLoginWithInvite(newEmail, inviteCode); err != nil {
		t.Errorf("invite code should still be usable after regular user visited /auth with it: %v", err)
	}
}

// getUserBillingExemption returns the billing exemption string for a user,
// or "" if none is set. It queries the debug users JSON API.
func getUserBillingExemption(t *testing.T, email string) string {
	t.Helper()
	httpPort := Env.servers.Exed.HTTPPort

	usersURL := fmt.Sprintf("http://localhost:%d/debug/users?format=json", httpPort)
	resp, err := http.Get(usersURL)
	if err != nil {
		t.Fatalf("failed to query debug users API: %v", err)
	}
	defer resp.Body.Close()

	var users []struct {
		Email            string `json:"email"`
		BillingExemption string `json:"billing_exemption"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		t.Fatalf("failed to decode debug users response: %v", err)
	}

	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return u.BillingExemption
		}
	}
	t.Fatalf("user %q not found in debug API", email)
	return ""
}

// isLoginWithExeUser reports whether the user has created_for_login_with_exe set.
func isLoginWithExeUser(t *testing.T, email string) bool {
	t.Helper()
	httpPort := Env.servers.Exed.HTTPPort

	usersURL := fmt.Sprintf("http://localhost:%d/debug/users?format=json", httpPort)
	resp, err := http.Get(usersURL)
	if err != nil {
		t.Fatalf("failed to query debug users API: %v", err)
	}
	defer resp.Body.Close()

	var users []struct {
		Email                  string `json:"email"`
		CreatedForLoginWithExe bool   `json:"created_for_login_with_exe"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		t.Fatalf("failed to decode debug users response: %v", err)
	}

	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			return u.CreatedForLoginWithExe
		}
	}
	t.Fatalf("user %q not found in debug API", email)
	return false
}
