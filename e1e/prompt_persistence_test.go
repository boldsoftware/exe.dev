package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
	"github.com/playwright-community/playwright-go"
)

// TestPromptPersistence_LoggedIn verifies that when a logged-in user
// types a prompt on the landing page and clicks "Start building",
// the prompt survives the redirect through /auth to /new.
func TestPromptPersistence_LoggedIn(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	base := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())

	// Register a user and get cookies.
	pty, cookies, _, _ := registerForExeDev(t)
	pty.Disconnect()

	// Use a client that captures the final redirect URL without following it.
	jar, _ := cookiejar.New(nil)
	setCookiesForJar(t, jar, base, cookies)
	// Also set cookies for the direct exed port (exeprox may redirect there).
	exedBase := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
	setCookiesForJar(t, jar, exedBase, cookies)

	var finalRedirect *url.URL
	client := &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow redirects between servers (exeprox→exed) but capture
			// the redirect that goes to /new.
			if req.URL.Path == "/new" {
				finalRedirect = req.URL
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	testPrompt := "Build a blog with comments and RSS"

	// GET /auth?prompt=... as a logged-in user.
	resp, err := client.Get(base + "/auth?prompt=" + url.QueryEscape(testPrompt))
	if err != nil {
		t.Fatalf("GET /auth?prompt=...: %v", err)
	}
	resp.Body.Close()

	if finalRedirect == nil {
		t.Fatalf("expected redirect to /new, got status %d at %s", resp.StatusCode, resp.Request.URL)
	}

	gotPrompt := finalRedirect.Query().Get("prompt")
	if gotPrompt != testPrompt {
		t.Fatalf("prompt not preserved in redirect: got %q, want %q", gotPrompt, testPrompt)
	}
}

// TestPromptPersistence_LoggedOut verifies that when a not-logged-in user
// types a prompt on the landing page and goes through email auth, the
// prompt survives the full auth flow and ends up on /new.
func TestPromptPersistence_LoggedOut(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	base := fmt.Sprintf("http://localhost:%d", Env.HTTPPort())
	email := t.Name() + testinfra.FakeEmailSuffix

	// Step 1: GET /auth?prompt=... as a logged-out user.
	// The auth form should show with a redirect to /new?prompt=... stashed.
	resp, err := http.Get(base + "/auth?prompt=" + url.QueryEscape("Build a blog with comments"))
	if err != nil {
		t.Fatalf("GET /auth?prompt=...: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// The auth form should contain a hidden redirect field pointing to /new?prompt=...
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "/new?") {
		t.Fatalf("auth form should contain redirect to /new, body: %s", bodyStr[:min(500, len(bodyStr))])
	}

	// Step 2: POST to /auth with email (carrying the redirect).
	// Extract hidden fields from the auth form.
	formData := testinfra.ExtractFormFields(body)
	formData.Set("email", email)

	resp, err = http.PostForm(base+"/auth", formData)
	if err != nil {
		t.Fatalf("POST /auth: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (check email page), got %d", resp.StatusCode)
	}

	// Step 3: Verify email and check that the redirect lands on /new?prompt=...
	emailMsg, err := Env.servers.Email.WaitForEmail(email)
	if err != nil {
		t.Fatalf("failed to wait for email: %v", err)
	}
	verifyURL, err := testinfra.ExtractVerificationToken(emailMsg.Body)
	if err != nil {
		t.Fatalf("failed to extract verification URL: %v", err)
	}

	// GET the verification page
	getResp, err := http.Get(verifyURL)
	if err != nil {
		t.Fatalf("GET verify: %v", err)
	}
	htmlBody, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()

	// Extract form fields and POST to /verify-email
	verifyForm := testinfra.ExtractFormFields(htmlBody)
	actionPath := testinfra.ExtractFormAction(htmlBody, "/verify-email")
	if !strings.HasPrefix(actionPath, "/") {
		actionPath = "/" + actionPath
	}

	jar, _ := cookiejar.New(nil)
	var finalRedirect *url.URL
	client := &http.Client{
		Jar:     jar,
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Path == "/new" {
				finalRedirect = req.URL
				return http.ErrUseLastResponse
			}
			return nil
		},
	}

	postURL := fmt.Sprintf("http://localhost:%d%s", Env.servers.Exed.HTTPPort, actionPath)
	postResp, err := client.PostForm(postURL, verifyForm)
	if err != nil {
		t.Fatalf("POST verify: %v", err)
	}
	postResp.Body.Close()

	if finalRedirect == nil {
		t.Fatalf("expected redirect to /new, got status %d at %s", postResp.StatusCode, postResp.Request.URL)
	}

	gotPrompt := finalRedirect.Query().Get("prompt")
	if gotPrompt != "Build a blog with comments" {
		t.Fatalf("prompt not preserved: got %q, want %q", gotPrompt, "Build a blog with comments")
	}
}

// TestPromptPersistence_Playwright exercises the full browser flow:
// user types a prompt on the landing page, clicks "Start building",
// logs in via email, and ends up on /new with their prompt intact.
func TestPromptPersistence_Playwright(t *testing.T) {
	skipIfNoPlaywright(t)
	t.Parallel()
	reserveVMs(t, 0)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.HTTPPort()
	base := fmt.Sprintf("http://localhost:%d", httpPort)
	email := t.Name() + testinfra.FakeEmailSuffix

	page, err := testinfra.NewPage()
	if err != nil {
		t.Fatalf("failed to create page: %v", err)
	}
	defer page.Close()

	// Step 1: Navigate to the landing page.
	_, err = page.Goto(base + "/")
	if err != nil {
		t.Fatalf("failed to navigate to landing page: %v", err)
	}
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	if err != nil {
		t.Fatalf("failed to wait for load: %v", err)
	}

	// Step 2: Type a prompt into the textarea.
	testPrompt := "Build me a real-time chat app with WebSockets"
	promptInput := page.Locator("#prompt-input")
	if err := promptInput.Fill(testPrompt); err != nil {
		t.Fatalf("failed to fill prompt: %v", err)
	}

	// Step 3: Click "Start building".
	submitBtn := page.Locator(".prompt-submit")
	if err := submitBtn.Click(); err != nil {
		t.Fatalf("failed to click submit: %v", err)
	}

	// Should navigate to /auth with prompt param, then show auth form.
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	if err != nil {
		t.Fatalf("failed to wait for auth page load: %v", err)
	}

	// Step 4: Fill in email and submit auth form.
	emailInput := page.Locator("input[name=email]")
	if err := emailInput.Fill(email); err != nil {
		t.Fatalf("failed to fill email: %v", err)
	}

	submitButton := page.Locator("button[type=submit]").First()
	if err := submitButton.Click(); err != nil {
		t.Fatalf("failed to click auth submit: %v", err)
	}

	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	})
	if err != nil {
		t.Fatalf("failed to wait for email sent page: %v", err)
	}

	// Step 5: Wait for email and navigate to verification URL.
	emailMsg, err := Env.servers.Email.WaitForEmail(email)
	if err != nil {
		t.Fatalf("failed to wait for email: %v", err)
	}
	verifyURL, err := testinfra.ExtractVerificationToken(emailMsg.Body)
	if err != nil {
		t.Fatalf("failed to extract verification URL: %v", err)
	}

	// Navigate to verification URL (auto-submits via JS).
	_, err = page.Goto(verifyURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateCommit,
	})
	if err != nil {
		t.Fatalf("failed to navigate to verification URL: %v", err)
	}

	// Wait for redirects to settle.
	err = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	})
	if err != nil {
		t.Fatalf("failed to wait for network idle after verification: %v", err)
	}

	// Step 6: Verify we ended up on /new with the prompt.
	// The page might be the Vue SPA, so wait a moment for the router to settle.
	time.Sleep(500 * time.Millisecond)

	pageURL := page.URL()
	parsedURL, err := url.Parse(pageURL)
	if err != nil {
		t.Fatalf("failed to parse page URL: %v", err)
	}

	if parsedURL.Path != "/new" {
		content, _ := page.Content()
		t.Fatalf("expected to be on /new, got %s\ncontent: %s", parsedURL.Path, content[:min(500, len(content))])
	}

	gotPrompt := parsedURL.Query().Get("prompt")
	if gotPrompt != testPrompt {
		t.Fatalf("prompt not preserved in URL: got %q, want %q", gotPrompt, testPrompt)
	}

	// Also verify the prompt text is visible in the textarea.
	promptTextarea := page.Locator(".prompt-input")
	val, err := promptTextarea.InputValue()
	if err != nil {
		t.Fatalf("failed to get prompt textarea value: %v", err)
	}
	if val != testPrompt {
		t.Fatalf("prompt textarea value: got %q, want %q", val, testPrompt)
	}
}
