// This file tests the credit/gift system end-to-end.
// Credits can be gifted via debug UI, SSH commands, and automatically on plan signup.

package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// getUserIDByEmail looks up a user ID by email via the debug API.
func getUserIDByEmail(t *testing.T, email string) string {
	t.Helper()
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
	for _, u := range users {
		if u.Email == email {
			return u.UserID
		}
	}
	t.Fatalf("user %s not found in users list", email)
	return ""
}

// giftCreditsViaDebug gifts credits to a user via the debug HTTP endpoint.
func giftCreditsViaDebug(t *testing.T, userID string, amountUSD float64, note string) {
	t.Helper()
	form := url.Values{}
	form.Add("user_id", userID)
	form.Add("amount", fmt.Sprintf("%.2f", amountUSD))
	form.Add("note", note)

	// Use a no-redirect client since the endpoint redirects to /debug/billing.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.PostForm(
		fmt.Sprintf("http://localhost:%d/debug/users/gift-credits", Env.servers.Exed.HTTPPort),
		form,
	)
	if err != nil {
		t.Fatalf("failed to gift credits via debug: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status gifting credits: %d, body: %s", resp.StatusCode, body)
	}
}

// TestGiftCreditsViaDebug verifies that gifting credits via the debug HTTP endpoint
// results in the gift appearing in sudo-exe llm-credits output.
func TestGiftCreditsViaDebug(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	noGolden(t)

	_, _, keyFile, email := registerForExeDevWithEmail(t, "giftdebug@test-credits.example")

	// Enable root support so we can run sudo-exe commands.
	enableRootSupport(t, email)

	userID := getUserIDByEmail(t, email)

	// Gift $10 via debug endpoint.
	giftCreditsViaDebug(t, userID, 10.00, "e1e test gift via debug")

	// Verify gift appears in llm-credits output.
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "sudo-exe", "llm-credits", email)
	if err != nil {
		t.Fatalf("sudo-exe llm-credits failed: %v\noutput: %s", err, out)
	}
	outStr := string(out)

	if !strings.Contains(outStr, "10.00") {
		t.Errorf("expected gift amount $10.00 in llm-credits output, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "e1e test gift via debug") {
		t.Errorf("expected gift note in llm-credits output, got:\n%s", outStr)
	}
}

// TestGiftCreditsViaSSH verifies that gifting credits via sudo-exe add-gift
// results in the gift appearing in the credit state.
func TestGiftCreditsViaSSH(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	noGolden(t)

	_, _, keyFile, email := registerForExeDevWithEmail(t, "giftssh@test-credits.example")

	// Enable root support so we can run sudo-exe commands.
	enableRootSupport(t, email)

	// Gift $25 via SSH add-gift command.
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "sudo-exe", "add-gift", email, "25", "test gift via ssh")
	if err != nil {
		t.Fatalf("sudo-exe add-gift failed: %v\noutput: %s", err, out)
	}
	outStr := string(out)
	if !strings.Contains(outStr, "Gift credited successfully") {
		t.Errorf("expected success message in add-gift output, got:\n%s", outStr)
	}

	// Verify gift appears in llm-credits output.
	credOut, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "sudo-exe", "llm-credits", email)
	if err != nil {
		t.Fatalf("sudo-exe llm-credits failed: %v\noutput: %s", err, credOut)
	}
	credStr := string(credOut)

	if !strings.Contains(credStr, "25.00") {
		t.Errorf("expected gift amount $25.00 in llm-credits output, got:\n%s", credStr)
	}
	if !strings.Contains(credStr, "test gift via ssh") {
		t.Errorf("expected gift note in llm-credits output, got:\n%s", credStr)
	}
}

// TestSignupBonusInLedger verifies that adding billing triggers a signup bonus
// which appears in the llm-credits gift section with the "signup:" prefix.
func TestSignupBonusInLedger(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	noGolden(t)

	// registerForExeDevWithEmail calls AddBillingForEmail, which hits the
	// /debug/users/add-billing endpoint. That endpoint creates an accounts row
	// and calls giftSignupBonus, so the signup bonus should appear.
	_, _, keyFile, email := registerForExeDevWithEmail(t, "signupbonus@test-credits.example")

	// Enable root support so we can run sudo-exe commands.
	enableRootSupport(t, email)

	// Run llm-credits to check for signup gift.
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "sudo-exe", "llm-credits", email)
	if err != nil {
		t.Fatalf("sudo-exe llm-credits failed: %v\noutput: %s", err, out)
	}
	outStr := string(out)

	// The signup gift should appear with the "signup:" prefix in the gift ID.
	if !strings.Contains(outStr, "signup:") {
		t.Errorf("expected signup: gift in llm-credits output, got:\n%s", outStr)
	}
}

// TestCreditDisplayOnDebugPage verifies that gifted credits appear on the
// /debug/billing page with gift history and correct amounts.
func TestCreditDisplayOnDebugPage(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	noGolden(t)

	_, _, _, email := registerForExeDevWithEmail(t, "debugpage@test-credits.example")

	userID := getUserIDByEmail(t, email)

	// Gift $15 via debug endpoint.
	giftCreditsViaDebug(t, userID, 15.00, "debug page test gift")

	// GET /debug/billing?userId=X and verify gift history is present.
	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/debug/billing?userId=%s", Env.servers.Exed.HTTPPort, url.QueryEscape(userID)))
	if err != nil {
		t.Fatalf("failed to GET /debug/billing: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status from /debug/billing: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read /debug/billing response body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Gift History") {
		t.Errorf("expected Gift History section in /debug/billing page, got:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "debug page test gift") {
		t.Errorf("expected gift note in /debug/billing page, got:\n%s", bodyStr)
	}
}

// TestMultipleGifts verifies that multiple gifts accumulate and all appear
// in the llm-credits output.
func TestMultipleGifts(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	noGolden(t)

	_, _, keyFile, email := registerForExeDevWithEmail(t, "multigift@test-credits.example")

	// Enable root support so we can run sudo-exe commands.
	enableRootSupport(t, email)

	userID := getUserIDByEmail(t, email)

	// Gift $5 first.
	giftCreditsViaDebug(t, userID, 5.00, "first gift")

	// Gift $20 second.
	giftCreditsViaDebug(t, userID, 20.00, "second gift")

	// Verify both gifts appear in llm-credits output.
	out, err := Env.servers.RunExeDevSSHCommand(Env.context(t), keyFile, "sudo-exe", "llm-credits", email)
	if err != nil {
		t.Fatalf("sudo-exe llm-credits failed: %v\noutput: %s", err, out)
	}
	outStr := string(out)

	if !strings.Contains(outStr, "first gift") {
		t.Errorf("expected first gift note in llm-credits output, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "second gift") {
		t.Errorf("expected second gift note in llm-credits output, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "5.00") {
		t.Errorf("expected first gift amount $5.00 in llm-credits output, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "20.00") {
		t.Errorf("expected second gift amount $20.00 in llm-credits output, got:\n%s", outStr)
	}
}

// TestGiftCreditsShowInProfile verifies that gifted credits appear in the
// user profile page under "Gift History".
func TestGiftCreditsShowInProfile(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	noGolden(t)

	_, cookies, _, email := registerForExeDevWithEmail(t, "profilegift@test-credits.example")

	userID := getUserIDByEmail(t, email)

	// Gift $30 via debug endpoint.
	giftCreditsViaDebug(t, userID, 30.00, "profile page test gift")

	// GET /user (the profile page) using auth cookies.
	client := newClientWithCookies(t, cookies)
	resp, err := client.Get(fmt.Sprintf("http://localhost:%d/user", Env.HTTPPort()))
	if err != nil {
		t.Fatalf("failed to GET /user: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status from /user profile page: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read /user response body: %v", err)
	}
	bodyStr := string(body)

	if !strings.Contains(bodyStr, "Gift History") {
		t.Errorf("expected Gift History section in /user profile page, got:\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "profile page test gift") {
		t.Errorf("expected gift note in /user profile page, got:\n%s", bodyStr)
	}
}
