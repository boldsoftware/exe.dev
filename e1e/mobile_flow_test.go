package e1e

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestMobileFlow_EndToEnd exercises the mobile creation flow with SSE using the default image.
func TestMobileFlow_EndToEnd(t *testing.T) {
	// Unique hostname for this test
	host := boxName(t)
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	// Build client with cookie jar
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Minute}

	base := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)

	// 1) GET /m (logged-out) shows create page
	resp, err := client.Get(base + "/m")
	if err != nil {
		t.Fatalf("GET /m: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Create") {
		t.Fatalf("/m unexpected: status=%d contains-Create? %v", resp.StatusCode, strings.Contains(string(body), "Create VM"))
	}

	// 2) POST /m/create-vm (logged-out) → email auth page
	form := url.Values{}
	form.Set("hostname", host)
	form.Set("prompt", "e2e mobile flow")
	resp, err = client.PostForm(base+"/m/create-vm", form)
	if err != nil {
		t.Fatalf("POST /m/create-vm: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "/m/email-auth") && !strings.Contains(string(body), "Enter your email") {
		t.Fatalf("unexpected email auth page: status=%d body=%q", resp.StatusCode, string(body))
	}

	// 3) POST /m/email-auth
	email := t.Name() + "@example.com"
	resp, err = client.PostForm(base+"/m/email-auth", url.Values{"email": {email}, "hostname": {host}})
	if err != nil {
		t.Fatalf("POST /m/email-auth: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Check Your Email") {
		t.Fatalf("unexpected email sent page: status=%d body=%q", resp.StatusCode, string(body))
	}

	// 4) Click verify link from email (uses the mobile /m/verify-token?token=... link)
	emailMsg, err := Env.servers.Email.WaitForEmail(email)
	if err != nil {
		t.Fatal(err)
	}
	// Extract first URL to /m/verify-token?token=...
	re := regexp.MustCompile(`http://localhost:\d+/m/verify-token\?token=[a-zA-Z0-9]+`)
	m := re.FindString(emailMsg.Body)
	if m == "" {
		t.Fatalf("did not find mobile verify link in email:\n%s", emailMsg.Body)
	}
	// Use a fresh client+jar to follow redirects and retain cookies
	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{Jar: jar2, Timeout: 5 * time.Minute}
	verifyResp, err := client2.Get(m)
	if err != nil {
		t.Fatalf("GET verify link: %v", err)
	}
	verifyRespBody, _ := io.ReadAll(verifyResp.Body)
	verifyResp.Body.Close()
	if verifyResp.StatusCode != http.StatusOK && verifyResp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("bad verify response status: %d\n%s", verifyResp.StatusCode, verifyRespBody)
	}

	// 5) Connect to SSE stream (creation already started in background after verification)
	// Retry until stream is available
	streamURL := base + "/m/creating/stream?hostname=" + url.QueryEscape(host)
	var sseResp *http.Response
	haveStream := false
	for range 50 {
		sseResp, err = client2.Get(streamURL)
		if err != nil {
			t.Fatalf("GET SSE stream: %v", err)
		}
		if sseResp.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(sseResp.Header.Get("Content-Type")), "text/event-stream") {
			haveStream = true
			break
		}
		sseResp.Body.Close()
		time.Sleep(100 * time.Millisecond)
	}
	if !haveStream {
		t.Fatalf("SSE stream not ready after retries")
	}
	defer sseResp.Body.Close()

	buf := new(strings.Builder)
	tee := io.TeeReader(sseResp.Body, buf)
	// Read SSE until we see event: done
	scanner := bufio.NewScanner(tee)
	var curEvent, doneData string
	done := false
	deadline := time.Now().Add(8 * time.Minute)
	for time.Now().Before(deadline) {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil && err != io.EOF {
				t.Fatalf("SSE read error: %v", err)
			}
			break
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if curEvent == "done" {
				doneData = data
				done = true
				break
			}
		}
	}
	if !done || doneData == "" {
		t.Logf("full SSE stream:\n\n%s\n\n", buf.String())
		t.Fatalf("did not receive done event; last data: %q", doneData)
	}
	parts := strings.Split(doneData, "|")
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "http") || !strings.HasPrefix(parts[1], "http") {
		t.Logf("full SSE stream:\n\n%s\n\n", buf.String())
		t.Fatalf("unexpected done payload: %q", doneData)
	}

	// Verify box-created email was sent
	boxCreatedEmail, err := Env.servers.Email.WaitForEmail(email)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(boxCreatedEmail.Subject, host) {
		t.Errorf("expected box-created email subject to contain %q, got %q", host, boxCreatedEmail.Subject)
	}

	// 6) Dashboard page should show the box
	dashURL := base + "/"
	dashResp, err := client2.Get(dashURL)
	if err != nil {
		t.Fatalf("GET dashboard: %v", err)
	}
	dashBody, _ := io.ReadAll(dashResp.Body)
	dashResp.Body.Close()
	if dashResp.StatusCode != http.StatusOK {
		t.Fatalf("Dashboard unexpected status=%d", dashResp.StatusCode)
	}
	if !strings.Contains(string(dashBody), host) {
		t.Fatalf("Dashboard unexpected body, should contain host=%v\n\n%s", host, dashBody)
	}

	// 7) Register SSH key and cleanup
	// Now that the account exists via email, add an SSH key
	keyFile, _ := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(banner)
	pty.want("Please enter your email")
	pty.sendLine(email)
	pty.wantRe("Verification email sent to")
	// pty.wantRe("Pairing code:")

	// Click verification link from email
	waitForEmailAndVerify(t, email)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.wantRe("key.*added")
	pty.wantPrompt()

	// Cleanup
	pty.deleteBox(host)
	pty.disconnect()
}

// TestNewPageRendersLoggedInAndOut verifies that the /new page renders correctly
// both when logged out and when logged in. This ensures the topbar template
// has access to all required fields (like BasicUser) in both cases.
func TestNewPageRendersLoggedInAndOut(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	base := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)

	// Test 1: Logged out - GET /new should render the create box form
	t.Run("logged_out", func(t *testing.T) {
		resp, err := http.Get(base + "/new")
		if err != nil {
			t.Fatalf("GET /new: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /new returned status %d, want 200", resp.StatusCode)
		}
		if !strings.Contains(string(body), "Create") {
			t.Fatalf("GET /new should contain 'Create', got: %s", string(body))
		}
		// Verify topbar rendered without error (page would be blank/error if template failed)
		if !strings.Contains(string(body), "Sign in") {
			t.Fatalf("GET /new (logged out) should show 'Sign in' link, got: %s", string(body))
		}
	})

	// Test 2: Logged in - GET /new should render with full navigation
	t.Run("logged_in", func(t *testing.T) {
		pty, cookies, _, _ := registerForExeDev(t)
		pty.disconnect()

		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatalf("cookiejar.New: %v", err)
		}
		setCookiesForJar(t, jar, base, cookies)
		client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

		resp, err := client.Get(base + "/new")
		if err != nil {
			t.Fatalf("GET /new: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /new returned status %d, want 200", resp.StatusCode)
		}
		if !strings.Contains(string(body), "Create") {
			t.Fatalf("GET /new should contain 'Create', got: %s", string(body))
		}
		// Verify topbar rendered with logged-in navigation (Sign out link)
		if !strings.Contains(string(body), "Sign out") {
			t.Fatalf("GET /new (logged in) should show 'Sign out' link, got: %s", string(body))
		}
	})
}
