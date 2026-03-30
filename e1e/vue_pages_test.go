package e1e

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
	"github.com/playwright-community/playwright-go"
)

// waitForVue is a helper that waits for a Vue-rendered text to appear on page.
// Returns an error suitable for t.Fatalf.
func waitForVue(page playwright.Page, text string, timeoutMs float64) error {
	return page.Locator("text=" + text).First().WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(timeoutMs),
	})
}

// TestVuePages_Playwright verifies that Vue SFC pages mount and render
// correctly in a real browser. The server injects window.__PAGE__ JSON
// data into the HTML; the Vue app reads it and renders client-side.
// If Playwright can see the rendered text, it proves the Vue pipeline works.
func TestVuePages_Playwright(t *testing.T) {
	skipIfNoPlaywright(t)
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.HTTPPort()
	base := fmt.Sprintf("http://localhost:%d", httpPort)

	// ---- auth_form ----
	t.Run("auth_form", func(t *testing.T) {
		page, err := testinfra.NewPage()
		if err != nil {
			t.Fatalf("failed to create page: %v", err)
		}
		defer page.Close()

		_, err = page.Goto(base + "/auth")
		if err != nil {
			t.Fatalf("failed to navigate to /auth: %v", err)
		}

		if err := waitForVue(page, "Login (or create an account)", 10000); err != nil {
			content, _ := page.Content()
			t.Fatalf("heading not rendered: %v\n%s", err, truncate(content, 800))
		}

		for _, sel := range []string{"input[name=email]", "button[type=submit]"} {
			visible, err := page.Locator(sel).IsVisible()
			if err != nil || !visible {
				content, _ := page.Content()
				t.Fatalf("%s not visible: err=%v\n%s", sel, err, truncate(content, 800))
			}
		}
	})

	// ---- email_sent ----
	t.Run("email_sent", func(t *testing.T) {
		page, err := testinfra.NewPage()
		if err != nil {
			t.Fatalf("failed to create page: %v", err)
		}
		defer page.Close()

		_, err = page.Goto(base + "/auth")
		if err != nil {
			t.Fatalf("failed to navigate to /auth: %v", err)
		}

		emailInput := page.Locator("input[name=email]")
		if err := emailInput.WaitFor(playwright.LocatorWaitForOptions{Timeout: playwright.Float(10000)}); err != nil {
			t.Fatalf("email input not ready: %v", err)
		}

		email := strings.ReplaceAll(t.Name(), "/", ".") + testinfra.FakeEmailSuffix
		if err := emailInput.Fill(email); err != nil {
			t.Fatalf("failed to fill email: %v", err)
		}
		if err := page.Locator("button[type=submit]").First().Click(); err != nil {
			t.Fatalf("failed to click submit: %v", err)
		}

		if err := waitForVue(page, "Check your email", 15000); err != nil {
			content, _ := page.Content()
			t.Fatalf("email-sent page not rendered: %v\n%s", err, truncate(content, 800))
		}
	})

	// ---- verify_flow ----
	// Full flow: AuthForm → EmailSent → EmailVerificationForm → EmailVerified
	t.Run("verify_flow", func(t *testing.T) {
		page, err := testinfra.NewPage()
		if err != nil {
			t.Fatalf("failed to create page: %v", err)
		}
		defer page.Close()

		_, err = page.Goto(base + "/auth")
		if err != nil {
			t.Fatalf("failed to navigate to /auth: %v", err)
		}

		emailInput := page.Locator("input[name=email]")
		if err := emailInput.WaitFor(playwright.LocatorWaitForOptions{Timeout: playwright.Float(10000)}); err != nil {
			t.Fatalf("email input not ready: %v", err)
		}

		email := strings.ReplaceAll(t.Name(), "/", ".") + testinfra.FakeEmailSuffix
		if err := emailInput.Fill(email); err != nil {
			t.Fatalf("failed to fill email: %v", err)
		}
		if err := page.Locator("button[type=submit]").First().Click(); err != nil {
			t.Fatalf("failed to click submit: %v", err)
		}

		if err := waitForVue(page, "Check your email", 15000); err != nil {
			content, _ := page.Content()
			t.Fatalf("email-sent page not rendered: %v\n%s", err, truncate(content, 800))
		}

		// Retrieve verification URL from test email server.
		emailMsg, err := Env.servers.Email.WaitForEmail(email)
		if err != nil {
			t.Fatalf("failed to wait for email: %v", err)
		}
		verifyURL, err := testinfra.ExtractVerificationToken(emailMsg.Body)
		if err != nil {
			t.Fatalf("failed to extract verification URL: %v", err)
		}

		// Navigate to verification URL (auto-submit chain).
		_, err = page.Goto(verifyURL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateCommit,
		})
		if err != nil {
			t.Fatalf("failed to navigate to verification URL: %v", err)
		}

		err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateNetworkidle,
		})
		if err != nil {
			t.Fatalf("failed to wait for network idle after verification: %v", err)
		}

		content, err := page.Content()
		if err != nil {
			t.Fatalf("failed to get page content: %v", err)
		}
		if !strings.Contains(content, "VERIFIED") && !strings.Contains(content, "WELCOME") {
			t.Fatalf("email-verified page not rendered:\n%s", truncate(content, 1200))
		}
		if !strings.Contains(content, email) {
			t.Fatalf("email-verified page does not show email %q:\n%s", email, truncate(content, 1200))
		}
	})

	// ---- logged_out ----
	t.Run("logged_out", func(t *testing.T) {
		page, err := testinfra.NewPage()
		if err != nil {
			t.Fatalf("failed to create page: %v", err)
		}
		defer page.Close()

		_, err = page.Goto(base + "/logged-out")
		if err != nil {
			t.Fatalf("failed to navigate to /logged-out: %v", err)
		}

		if err := waitForVue(page, "Logged out", 10000); err != nil {
			content, _ := page.Content()
			t.Fatalf("logged-out page not rendered: %v\n%s", err, truncate(content, 800))
		}

		visible, err := page.Locator("a[href='/']").IsVisible()
		if err != nil || !visible {
			content, _ := page.Content()
			t.Fatalf("return link not visible: err=%v\n%s", err, truncate(content, 800))
		}
	})

	// ---- app_token_code_entry ----
	// POST /auth with response_mode=app_token triggers the code-entry flow.
	t.Run("app_token_code_entry", func(t *testing.T) {
		page, err := testinfra.NewPage()
		if err != nil {
			t.Fatalf("failed to create page: %v", err)
		}
		defer page.Close()

		_, err = page.Goto(base + "/auth")
		if err != nil {
			t.Fatalf("failed to navigate: %v", err)
		}

		emailInput := page.Locator("input[name=email]")
		if err := emailInput.WaitFor(playwright.LocatorWaitForOptions{Timeout: playwright.Float(10000)}); err != nil {
			t.Fatalf("email input not ready: %v", err)
		}

		email := strings.ReplaceAll(t.Name(), "/", ".") + testinfra.FakeEmailSuffix

		// Submit via HTTP with app_token params (can't set hidden fields via the Vue form).
		form := url.Values{}
		form.Set("email", email)
		form.Set("response_mode", "app_token")
		form.Set("callback_uri", "exedev-app://auth")
		resp, err := http.PostForm(base+"/auth", form)
		if err != nil {
			t.Fatalf("POST /auth: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /auth: expected 200, got %d", resp.StatusCode)
		}

		// Now navigate the browser to the same form; the server already created
		// a verification record. Re-submit to get the code-entry page.
		_, err = page.Goto(base+"/auth?response_mode=app_token&callback_uri="+url.QueryEscape("exedev-app://auth"), playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		})
		if err != nil {
			t.Fatalf("failed to navigate: %v", err)
		}

		// Fill email and submit via browser.
		emailInput = page.Locator("input[name=email]")
		if err := emailInput.WaitFor(playwright.LocatorWaitForOptions{Timeout: playwright.Float(10000)}); err != nil {
			t.Fatalf("email input not ready: %v", err)
		}
		if err := emailInput.Fill(email); err != nil {
			t.Fatalf("failed to fill email: %v", err)
		}
		if err := page.Locator("button[type=submit]").First().Click(); err != nil {
			t.Fatalf("failed to click submit: %v", err)
		}

		// The code entry page shows "Check your email" with a code input.
		if err := waitForVue(page, "Check your email", 15000); err != nil {
			content, _ := page.Content()
			t.Fatalf("code entry page heading not rendered: %v\n%s", err, truncate(content, 800))
		}

		// Verify code input is visible.
		codeInput := page.Locator("input[name=code]")
		visible, err := codeInput.IsVisible()
		if err != nil || !visible {
			content, _ := page.Content()
			t.Fatalf("code input not visible: err=%v\n%s", err, truncate(content, 800))
		}

		// Verify the email address is shown.
		content, _ := page.Content()
		if !strings.Contains(content, email) {
			t.Fatalf("code entry page does not show email %q:\n%s", email, truncate(content, 800))
		}
	})

	// ---- device_verification ----
	// Register a user with one SSH key, then connect with a new key.
	// The server creates a pending SSH key and sends a device verification email.
	// Navigate to the verification URL → DeviceVerification page.
	// POST the form → DeviceVerified page.
	t.Run("device_verification", func(t *testing.T) {
		// Register with first key.
		email := t.Name() + testinfra.FakeEmailSuffix
		keyFile1, _ := genSSHKey(t)
		pty := sshToExeDev(t, keyFile1)
		pty.Want(testinfra.Banner)
		pty.Want("Please enter your email")
		pty.SendLine(email)
		pty.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))
		waitForEmailAndVerify(t, email)
		pty.Want("Email verified successfully")
		pty.Want("Registration complete")
		pty.WantPrompt()
		pty.Disconnect()

		// Connect with second key → triggers device verification.
		keyFile2, _ := genSSHKey(t)
		pty2 := sshToExeDev(t, keyFile2)
		pty2.Want(testinfra.Banner)
		pty2.Want("Please enter your email")
		pty2.SendLine(email)
		pty2.WantRE("Verification email sent to.*" + regexp.QuoteMeta(email))

		// Get device verification email.
		msg, err := Env.servers.Email.WaitForEmail(email)
		if err != nil {
			t.Fatalf("failed to get device verification email: %v", err)
		}
		verifyURL, err := testinfra.ExtractVerificationToken(msg.Body)
		if err != nil {
			t.Fatalf("failed to extract verification URL: %v", err)
		}
		// Canonicalize the device verification token for golden file stability.
		if u, err := url.Parse(verifyURL); err == nil {
			if tok := u.Query().Get("token"); tok != "" {
				testinfra.AddCanonicalization(tok, "DEVICE_VERIFICATION_TOKEN")
			}
		}

		// Open verification page in browser.
		page, err := testinfra.NewPage()
		if err != nil {
			t.Fatalf("failed to create page: %v", err)
		}
		defer page.Close()

		_, err = page.Goto(verifyURL)
		if err != nil {
			t.Fatalf("failed to navigate to verification URL: %v", err)
		}

		// DeviceVerification page: "Authorize SSH key" heading.
		if err := waitForVue(page, "Authorize SSH key", 10000); err != nil {
			content, _ := page.Content()
			t.Fatalf("device verification page not rendered: %v\n%s", err, truncate(content, 800))
		}

		// Verify email and SSH KEY code block are shown.
		content, _ := page.Content()
		if !strings.Contains(content, email) {
			t.Fatalf("device verification page does not show email: %s", truncate(content, 800))
		}
		if !strings.Contains(content, "SSH KEY") {
			t.Fatalf("device verification page does not show SSH KEY label: %s", truncate(content, 800))
		}

		// Verify Authorize button is visible.
		authorizeBtn := page.Locator("button[type=submit]")
		visible, err := authorizeBtn.IsVisible()
		if err != nil || !visible {
			t.Fatalf("authorize button not visible")
		}

		// Click Authorize → DeviceVerified page.
		if err := authorizeBtn.Click(); err != nil {
			t.Fatalf("failed to click authorize: %v", err)
		}

		if err := waitForVue(page, "KEY AUTHORIZED", 10000); err != nil {
			content, _ := page.Content()
			t.Fatalf("device verified page not rendered: %v\n%s", err, truncate(content, 800))
		}

		// Verify subtitle.
		content, _ = page.Content()
		if !strings.Contains(content, "return to your terminal") {
			t.Fatalf("device verified page missing subtitle: %s", truncate(content, 800))
		}

		pty2.Disconnect()
	})

	// ---- auth_error ----
	// Trigger AuthError by exhausting verification code attempts.
	// The app_token flow allows 5 attempts. We exhaust all 5 via HTTP,
	// then on the 6th the record is gone and the handler returns "no
	// verification" error. Instead, we exhaust 4, then on the 5th the
	// handler itself shows "Too many attempts" via showAuthError.
	// We navigate the browser to the code entry page, fill the wrong
	// code in the real form, and submit.
	t.Run("auth_error", func(t *testing.T) {
		email := strings.ReplaceAll(t.Name(), "/", ".") + testinfra.FakeEmailSuffix

		// Start app_token flow via browser to get the code-entry page.
		page, err := testinfra.NewPage()
		if err != nil {
			t.Fatalf("failed to create page: %v", err)
		}
		defer page.Close()

		// Navigate and submit auth form with app_token params.
		_, err = page.Goto(base + "/auth?response_mode=app_token&callback_uri=" + url.QueryEscape("exedev-app://auth"))
		if err != nil {
			t.Fatalf("failed to navigate: %v", err)
		}
		emailInput := page.Locator("input[name=email]")
		if err := emailInput.WaitFor(playwright.LocatorWaitForOptions{Timeout: playwright.Float(10000)}); err != nil {
			t.Fatalf("email input not ready: %v", err)
		}
		if err := emailInput.Fill(email); err != nil {
			t.Fatalf("fill email: %v", err)
		}
		if err := page.Locator("button[type=submit]").First().Click(); err != nil {
			t.Fatalf("click submit: %v", err)
		}

		// Wait for code entry page.
		codeInput := page.Locator("input[name=code]")
		if err := codeInput.WaitFor(playwright.LocatorWaitForOptions{Timeout: playwright.Float(15000)}); err != nil {
			content, _ := page.Content()
			t.Fatalf("code entry page not rendered: %v\n%s", err, truncate(content, 800))
		}

		// Submit 4 wrong codes via HTTP to burn attempts.
		// Use the direct exed port to avoid exeprox routing issues.
		exedBase := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		for i := range 4 {
			f := url.Values{}
			f.Set("email", email)
			f.Set("code", "00000000")
			req, _ := http.NewRequest("POST", exedBase+"/auth/verify-code", strings.NewReader(f.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Host = fmt.Sprintf("localhost:%d", Env.servers.Exed.HTTPPort)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("POST /auth/verify-code attempt %d: %v", i+1, err)
			}
			t.Logf("attempt %d: status=%d", i+1, resp.StatusCode)
			resp.Body.Close()
		}

		// 5th wrong code via the browser form → auth error page.
		if err := codeInput.Fill("00000000"); err != nil {
			t.Fatalf("fill code: %v", err)
		}
		if err := page.Locator("button[type=submit]").First().Click(); err != nil {
			t.Fatalf("click verify: %v", err)
		}

		// Auth error page shows "Error" heading.
		if err := waitForVue(page, "Error", 10000); err != nil {
			content, _ := page.Content()
			t.Fatalf("auth error page not rendered: %v\n%s", err, truncate(content, 800))
		}

		content, _ := page.Content()
		if !strings.Contains(content, "Too many attempts") {
			t.Fatalf("auth error page missing error message: %s", truncate(content, 800))
		}

		// Verify "Try again" link.
		visible, err := page.Locator("text=Try again").First().IsVisible()
		if err != nil || !visible {
			t.Fatalf("'Try again' link not visible")
		}
	})

	// ---- billing_success ----
	// BillingSuccess is shown after Stripe checkout completes. We can't
	// do a real Stripe checkout in e1e, but we can test that the billing
	// success page renders by going through the dev-bypass path.
	t.Run("billing_success", func(t *testing.T) {
		// Register a user and get cookies.
		pty, cookies, _, _ := registerForExeDevWithoutBilling(t)
		pty.Disconnect()

		pwCookies := testinfra.HTTPCookiesToPlaywright(base, cookies)
		page, err := testinfra.NewPageWithCookies(base, pwCookies)
		if err != nil {
			t.Fatalf("failed to create page with cookies: %v", err)
		}
		defer page.Close()

		// dev bypass skips Stripe verification; source=exemenu renders the page
		// instead of redirecting.
		_, err = page.Goto(base + "/billing/success?dev_bypass=1&source=exemenu")
		if err != nil {
			t.Fatalf("failed to navigate: %v", err)
		}

		if err := waitForVue(page, "READY", 10000); err != nil {
			content, _ := page.Content()
			t.Fatalf("billing success page not rendered: %v\n%s", err, truncate(content, 800))
		}

		content, _ := page.Content()
		if !strings.Contains(content, "payment info has been saved") {
			t.Fatalf("billing success page missing expected text: %s", truncate(content, 800))
		}

		// source=exemenu shows "Return to your terminal" instead of the link.
		if !strings.Contains(content, "Return to your terminal") {
			t.Fatalf("billing success page missing terminal text: %s", truncate(content, 800))
		}
	})
}
