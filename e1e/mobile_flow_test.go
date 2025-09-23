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
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Create VM") {
		t.Fatalf("/m unexpected: status=%d contains-Create? %v", resp.StatusCode, strings.Contains(string(body), "Create VM"))
	}

	// 2) POST /m/create-vm (logged-out) → email auth page
	form := url.Values{}
	form.Set("hostname", host)
	form.Set("description", "e2e mobile flow")
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
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Check your email") {
		t.Fatalf("unexpected email sent page: status=%d body=%q", resp.StatusCode, string(body))
	}

	// 4) Click verify link from email (uses the mobile /m/verify-code?token=... link)
	emailMsg := Env.email.waitForEmail(t, email)
	// Extract first URL to /m/verify-code?token=...
	re := regexp.MustCompile(`http://localhost:\d+/m/verify-code\?token=[a-f0-9]+`)
	m := re.FindString(emailMsg.Body)
	if m == "" {
		t.Fatalf("did not find mobile verify link in email: %q", emailMsg.Body)
	}
	// Use a fresh client+jar to follow redirects and retain cookies
	jar2, _ := cookiejar.New(nil)
	client2 := &http.Client{Jar: jar2, Timeout: 5 * time.Minute}
	verifyResp, err := client2.Get(m)
	if err != nil {
		t.Fatalf("GET verify link: %v", err)
	}
	io.ReadAll(verifyResp.Body)
	verifyResp.Body.Close()
	if verifyResp.StatusCode != http.StatusOK && verifyResp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("verify response status: %d", verifyResp.StatusCode)
	}

	// 5) Visit creating page and connect to SSE stream
	createURL := base + "/m/creating?hostname=" + url.QueryEscape(host)
	pageResp, err := client2.Get(createURL)
	if err != nil {
		t.Fatalf("GET creating page: %v", err)
	}
	io.ReadAll(pageResp.Body)
	pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusOK {
		t.Fatalf("creating page status: %d", pageResp.StatusCode)
	}

	// Stream via POST since creation is a write action
	sseResp, err := client2.PostForm(base+"/m/creating/stream", url.Values{"hostname": {host}})
	if err != nil {
		t.Fatalf("GET SSE stream: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(sseResp.Header.Get("Content-Type")), "text/event-stream") {
		b, _ := io.ReadAll(sseResp.Body)
		t.Fatalf("SSE stream unexpected: status=%d ct=%q body=%q", sseResp.StatusCode, sseResp.Header.Get("Content-Type"), string(b))
	}

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

	// 6) VM page
	vmURL := base + "/m/box/" + url.PathEscape(host)
	vmResp, err := client2.Get(vmURL)
	if err != nil {
		t.Fatalf("GET VM page: %v", err)
	}
	vmBody, _ := io.ReadAll(vmResp.Body)
	vmResp.Body.Close()
	if vmResp.StatusCode != http.StatusOK || !strings.Contains(string(vmBody), host+".exe.dev") {
		t.Fatalf("VM page unexpected: status=%d body=%q", vmResp.StatusCode, string(vmBody))
	}
}
