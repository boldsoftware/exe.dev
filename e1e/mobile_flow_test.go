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

	// Build client with cookie jar
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Minute}

	base := fmt.Sprintf("http://localhost:%d", Env.exed.HTTPPort)

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
	emailMsg := Env.email.waitForEmail(t, email)
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
	for i := 0; i < 50; i++ {
		sseResp, err = client2.Get(streamURL)
		if err != nil {
			t.Fatalf("GET SSE stream: %v", err)
		}
		if sseResp.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(sseResp.Header.Get("Content-Type")), "text/event-stream") {
			break
		}
		sseResp.Body.Close()
		if i == 49 {
			t.Fatalf("SSE stream not ready after retries")
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer sseResp.Body.Close()

	// Read SSE until we see event: done
	scanner := bufio.NewScanner(sseResp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
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
		} else if strings.HasPrefix(line, "data: ") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			if curEvent == "done" {
				doneData = data
				done = true
				break
			}
		}
	}
	if !done || doneData == "" {
		t.Fatalf("did not receive done event; last data: %q", doneData)
	}
	parts := strings.Split(doneData, "|")
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "http") || !strings.HasPrefix(parts[1], "http") {
		t.Fatalf("unexpected done payload: %q", doneData)
	}

	// 6) Dashboard page should show the box
	dashURL := base + "/~"
	dashResp, err := client2.Get(dashURL)
	if err != nil {
		t.Fatalf("GET dashboard: %v", err)
	}
	dashBody, _ := io.ReadAll(dashResp.Body)
	dashResp.Body.Close()
	if dashResp.StatusCode != http.StatusOK || !strings.Contains(string(dashBody), host) {
		t.Fatalf("Dashboard unexpected: status=%d contains host? %v", dashResp.StatusCode, strings.Contains(string(dashBody), host))
	}

	// 7) Register SSH key and cleanup
	// Now that the account exists via email, add an SSH key
	keyFile, _ := genSSHKey(t)
	pty := sshToExeDev(t, keyFile)
	pty.want(banner)
	pty.want("Please enter your email")
	pty.sendLine(email)
	pty.wantRe("Verification email sent to")
	pty.wantRe("Pairing code:")

	// Click verification link from email
	emailMsg2 := Env.email.waitForEmail(t, email)
	clickVerifyLinkInEmail(t, emailMsg2)

	pty.want("Email verified successfully")
	pty.want("Registration complete")
	pty.wantRe("key.*added")
	pty.want("Press any key to continue")
	pty.sendLine("")
	pty.wantPrompt()

	// Cleanup
	pty.sendLine("delete " + host)
	pty.want("Deleting")
	pty.wantPrompt()
	pty.disconnect()
}
