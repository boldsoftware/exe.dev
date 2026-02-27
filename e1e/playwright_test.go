package e1e

import (
	"fmt"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
	"github.com/playwright-community/playwright-go"
)

// skipIfNoPlaywright skips the test if Playwright is not available.
func skipIfNoPlaywright(t *testing.T) {
	t.Helper()
	if !testinfra.PlaywrightAvailable() {
		t.Skip("playwright not available (run with -playwright flag)")
	}
}

// TestProxyLoginFlow_Playwright tests the full proxy login-via-email flow
// using a real browser. This exercises the cross-origin redirect chain that
// occurs when an unauthenticated browser hits a private proxy route:
//
//  1. Register user via SSH, create VM, start httpd, configure private proxy
//  2. Fresh browser navigates to http://boxname.exe.cloud:<httpPort>/
//  3. Browser follows redirects to auth page on localhost
//  4. Browser fills email form, submits
//  5. Go waits for email, extracts verification URL
//  6. Browser navigates to verification URL (auto-submits via JS)
//  7. Assert: browser ends up on boxname.exe.cloud showing VM content ("alive")
//
// Without the 307→303 fix in redirectAfterAuthWithParams, step 6-7 fails
// because the cross-origin POST redirect is blocked by CrossOriginProtection.
func TestProxyLoginFlow_Playwright(t *testing.T) {
	skipIfNoPlaywright(t)
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.servers.Exed.HTTPPort

	// Step 1: Register user via SSH, create VM, start httpd, configure private proxy route.
	pty, _, keyFile, email := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash", NoEmail: true})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	makeIndex := boxSSHCommand(t, box, keyFile, "sh", "-c", "'echo alive > /home/exedev/index.html'")
	if err := makeIndex.Run(); err != nil {
		t.Fatalf("failed to create index.html: %v", err)
	}
	startHTTPServer(t, box, keyFile, 8080)
	configureProxyRoute(t, keyFile, box, 8080, "private")

	// Step 2: Fresh browser navigates to proxy URL.
	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)

	page, err := testinfra.NewPage()
	if err != nil {
		t.Fatalf("failed to create page: %v", err)
	}
	defer page.Close()

	resp, err := page.Goto(proxyURL)
	if err != nil {
		t.Fatalf("failed to navigate to proxy URL: %v", err)
	}

	// Step 3: Browser should have followed redirects to auth page.
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	if err != nil {
		t.Fatalf("failed to wait for load: %v", err)
	}

	if resp.Status() != 401 {
		content, _ := page.Content()
		t.Fatalf("expected 401 Access required page, got status %d: %s", resp.Status(), content[:min(500, len(content))])
	}

	// Step 4: Fill email form and submit.
	emailInput := page.Locator("input[name=email]")
	if err := emailInput.Fill(email); err != nil {
		t.Fatalf("failed to fill email input: %v", err)
	}

	submitButton := page.Locator("button[type=submit]").First()
	if err := submitButton.Click(); err != nil {
		t.Fatalf("failed to click submit: %v", err)
	}

	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	if err != nil {
		t.Fatalf("failed to wait for load after submit: %v", err)
	}

	checkVisible, err := page.Locator("text=Check").First().IsVisible()
	if err != nil || !checkVisible {
		content, _ := page.Content()
		t.Fatalf("expected check-email page, got: %s", content[:min(500, len(content))])
	}

	// Step 5: Go waits for email and extracts verification URL.
	emailMsg, err := Env.servers.Email.WaitForEmail(email)
	if err != nil {
		t.Fatalf("failed to wait for email: %v", err)
	}
	verifyURL, err := testinfra.ExtractVerificationToken(emailMsg.Body)
	if err != nil {
		t.Fatalf("failed to extract verification URL: %v", err)
	}

	// Step 6: Navigate to verification URL.
	// The page contains an inline JS auto-submit that POSTs to /verify-email,
	// which triggers a redirect chain: /verify-email → /auth/confirm → boxname.exe.cloud/__exe.dev/auth.
	//
	// Use WaitUntil: commit so Goto returns immediately without blocking on
	// external CDN scripts that race with the inline auto-submit JS.
	_, err = page.Goto(verifyURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateCommit,
	})
	if err != nil {
		t.Fatalf("failed to navigate to verification URL: %v", err)
	}

	// Wait for the auto-submit JS redirect chain to settle.
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	})
	if err != nil {
		t.Fatalf("failed to wait for network idle after verification: %v", err)
	}

	// Step 7: Assert browser ended up on boxname.exe.cloud showing VM content.
	// Check for CSRF error first — this is the specific regression we're testing for.
	content, err := page.Content()
	if err != nil {
		t.Fatalf("failed to get page content: %v", err)
	}
	if strings.Contains(content, "cross-origin") {
		t.Fatal(content)
	}
	if !strings.Contains(content, "alive") {
		t.Fatalf("expected page to contain 'alive', got: %s", content[:min(500, len(content))])
	}

	pageURL := page.URL()
	expectedHost := fmt.Sprintf("%s.exe.cloud:%d", box, httpPort)
	if !strings.Contains(pageURL, expectedHost) {
		t.Fatalf("expected URL to contain %s, got %s", expectedHost, pageURL)
	}

	// Cleanup
	cleanupBox(t, keyFile, box)
}
