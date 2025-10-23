// This file tests box sharing functionality.

package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"exe.dev/vouch"
)

func TestBoxSharingWithWebServer(t *testing.T) {
	vouch.For("philip")
	t.Parallel()

	// User 1: Create a box and start a web server
	// Use lowercase emails since share command normalizes emails
	pty1, cookies1, keyFile1, email1 := registerForExeDevWithEmail(t, strings.ToLower(t.Name())+"-user1@example.com")
	box := newBox(t, pty1, BoxOpts{Command: "/bin/bash"})
	pty1.disconnect() // Done with interactive session

	// Start a simple HTTP server on port 8080 inside the box
	boxInternalPort := 8080
	p := boxSSHCommand(t, box, keyFile1, "python3", "-m", "http.server", strconv.Itoa(boxInternalPort))
	if err := p.Start(); err != nil {
		t.Fatalf("failed to start python HTTP server: %v\n", err)
	}
	t.Cleanup(func() {
		p.Process.Kill()
		p.Wait()
	})

	// Wait for server to be ready
	waitCmd := boxSSHCommand(t, box, keyFile1, "timeout", "20", "sh", "-c",
		fmt.Sprintf("while ! curl http://localhost:%d/; do sleep 0.5; done", boxInternalPort))
	if err := waitCmd.Start(); err != nil {
		t.Fatalf("failed to start wait command: %v\n", err)
	}
	waitCmd.Wait()

	// httpPort is the exed HTTP proxy port, not the port inside the box
	httpPort := Env.exed.HTTPPort

	// Configure the proxy to use port 8080 and make it private
	out, err := runExeDevSSHCommand(t, keyFile1, "proxy", box, fmt.Sprintf("--port=%d", boxInternalPort), "--private")
	if err != nil {
		t.Fatalf("failed to configure proxy: %v\n%s", err, out)
	}

	// Verify user1 can access the box via HTTPS proxy
	proxyAssert(t, box, proxyExpectation{
		name:     "user1 can access their own box",
		httpPort: httpPort,
		cookies:  cookies1,
		httpCode: http.StatusOK,
	})

	// User 2: Register a second user
	pty2, cookies2, _, email2 := registerForExeDevWithEmail(t, strings.ToLower(t.Name())+"-user2@example.com")

	// Verify user2 cannot access the box (it's private)
	proxyAssert(t, box, proxyExpectation{
		name:     "user2 cannot access private box",
		httpPort: httpPort,
		cookies:  cookies2,
		httpCode: http.StatusNotFound, // Should get 404 to not leak box existence
	})

	// User 1: Share the box with user2 via email
	out, err = runExeDevSSHCommand(t, keyFile1, "share", "add", box, email2, "--message=Welcome")
	if err != nil {
		t.Fatalf("failed to share box: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Invitation sent to "+email2) {
		t.Fatalf("Expected 'Invitation sent to %s' in output, got: %s", email2, out)
	}

	// User 2 should receive an email
	emailMsg := Env.email.waitForEmail(t, email2)
	if !strings.Contains(emailMsg.Body, "shared a box with you") {
		t.Fatalf("Expected share invitation email, got: %s", emailMsg.Body)
	}
	if !strings.Contains(emailMsg.Body, "Welcome") {
		t.Fatalf("Expected custom message in email, got: %s", emailMsg.Body)
	}

	// User 2 should now be able to access the box
	proxyAssert(t, box, proxyExpectation{
		name:     "user2 can access shared box",
		httpPort: httpPort,
		cookies:  cookies2,
		httpCode: http.StatusOK,
	})

	// User 2: Check dashboard for shared boxes via HTTP
	jar2, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to create cookie jar: %v", err)
	}
	// Add cookies to jar
	for _, cookie := range cookies2 {
		cookie.Domain = "localhost"
		jar2.SetCookies(&url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", Env.exed.HTTPPort)}, []*http.Cookie{cookie})
	}
	client2 := &http.Client{
		Jar:     jar2,
		Timeout: 10 * time.Second,
	}
	resp, err := client2.Get(fmt.Sprintf("http://localhost:%d/", Env.exed.HTTPPort))
	if err != nil {
		t.Fatalf("failed to get dashboard: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read dashboard: %v", err)
	}
	dashboard := string(body)

	// Check that the shared box appears in the dashboard
	if !strings.Contains(dashboard, "Shared with Me") {
		t.Errorf("Expected 'Shared with me' section in dashboard")
	}
	if !strings.Contains(dashboard, box) {
		t.Errorf("Expected box name %s in dashboard", box)
	}
	if !strings.Contains(dashboard, email1) {
		t.Errorf("Expected owner email %s in dashboard", email1)
	}

	// User 1: Check share status via SSH
	shareOut, err := runExeDevSSHCommand(t, keyFile1, "share", "show", box, "--json")
	if err != nil {
		t.Fatalf("failed to run share command: %v\n%s", err, shareOut)
	}
	var shareInfo struct {
		BoxName string `json:"box_name"`
		Users   []struct {
			Email  string `json:"email"`
			Status string `json:"status"`
		} `json:"users"`
		Links []struct {
			Token string `json:"token"`
		} `json:"links"`
	}
	if err = json.Unmarshal(shareOut, &shareInfo); err != nil {
		t.Fatalf("failed to parse share info JSON: %v\n%s", err, shareOut)
	}
	if shareInfo.BoxName != box {
		t.Errorf("Expected box name %s, got %s", box, shareInfo.BoxName)
	}
	if len(shareInfo.Users) != 1 {
		t.Errorf("Expected 1 shared user, got %d", len(shareInfo.Users))
	}
	if len(shareInfo.Users) > 0 && shareInfo.Users[0].Email != email2 {
		t.Errorf("Expected shared user %s, got %s", email2, shareInfo.Users[0].Email)
	}
	if len(shareInfo.Users) > 0 && shareInfo.Users[0].Status != "active" {
		t.Errorf("Expected status 'active', got %s", shareInfo.Users[0].Status)
	}

	// User 1: Revoke access
	out, err = runExeDevSSHCommand(t, keyFile1, "share", "remove", box, email2)
	if err != nil {
		t.Fatalf("failed to remove share: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Removed "+email2+"'s access") {
		t.Fatalf("Expected 'Removed %s's access' in output, got: %s", email2, out)
	}

	// User 2 should no longer be able to access the box
	proxyAssert(t, box, proxyExpectation{
		name:     "user2 cannot access after revoked",
		httpPort: httpPort,
		cookies:  cookies2,
		httpCode: http.StatusNotFound,
	})

	// Cleanup
	pty1 = sshToExeDev(t, keyFile1)
	pty1.sendLine("rm " + box)
	pty1.want("Deleting")
	pty1.wantPrompt()
	pty1.disconnect()

	// Clean up pty sessions
	pty2.disconnect()
}

func TestShareLinkAccess(t *testing.T) {
	vouch.For("philip")
	t.Parallel()

	// User 1: Create a box and start a web server
	pty1, _, keyFile1, _ := registerForExeDevWithEmail(t, strings.ToLower(t.Name())+"-user1@example.com")
	box := newBox(t, pty1, BoxOpts{Command: "/bin/bash"})
	pty1.disconnect() // Done with interactive session

	boxInternalPort := 8080
	p := boxSSHCommand(t, box, keyFile1, "python3", "-m", "http.server", strconv.Itoa(boxInternalPort))
	if err := p.Start(); err != nil {
		t.Fatalf("failed to start python HTTP server: %v\n", err)
	}
	t.Cleanup(func() {
		p.Process.Kill()
		p.Wait()
	})

	// Wait for server to be ready
	waitCmd := boxSSHCommand(t, box, keyFile1, "timeout", "20", "sh", "-c",
		fmt.Sprintf("while ! curl http://localhost:%d/; do sleep 0.5; done", boxInternalPort))
	if err := waitCmd.Start(); err != nil {
		t.Fatalf("failed to start wait command: %v\n", err)
	}
	waitCmd.Wait()

	// httpPort is the exed HTTP proxy port, not the port inside the box
	httpPort := Env.exed.HTTPPort

	// Configure the proxy to use port 8080 and make it private
	out, err := runExeDevSSHCommand(t, keyFile1, "proxy", box, fmt.Sprintf("--port=%d", boxInternalPort), "--private")
	if err != nil {
		t.Fatalf("failed to configure proxy: %v\n%s", err, out)
	}

	// User 1: Create a share link
	linkOut, err := runExeDevSSHCommand(t, keyFile1, "share", "add-share-link", box, "--json")
	if err != nil {
		t.Fatalf("failed to run share add-share-link command: %v\n%s", err, linkOut)
	}
	var linkInfo struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}
	if err = json.Unmarshal(linkOut, &linkInfo); err != nil {
		t.Fatalf("failed to parse link info JSON: %v\n%s", err, linkOut)
	}
	if linkInfo.Token == "" {
		t.Fatalf("Expected share token, got empty string")
	}
	// Canonicalize the share token for golden files
	Env.addCanonicalization(linkInfo.Token, "SHARE_TOKEN")

	// User 2: Register a second user
	_, cookies2, _, _ := registerForExeDevWithEmail(t, strings.ToLower(t.Name())+"-user2@example.com")

	// User 2 should be able to access via share link
	proxyAssertWithQuery(t, box, proxyExpectation{
		name:     "user2 can access via share link",
		httpPort: httpPort,
		cookies:  cookies2,
		httpCode: http.StatusOK,
	}, fmt.Sprintf("share=%s", linkInfo.Token))

	// After accessing via share link, user2 should get an email-based share
	// So they can access even without the share token
	time.Sleep(1 * time.Second) // Give the auto-create share time to complete
	proxyAssert(t, box, proxyExpectation{
		name:     "user2 can access without share link after first access",
		httpPort: httpPort,
		cookies:  cookies2,
		httpCode: http.StatusOK,
	})

	// User 1: Revoke the share link
	out, err = runExeDevSSHCommand(t, keyFile1, "share", "remove-share-link", box, linkInfo.Token)
	if err != nil {
		t.Fatalf("failed to remove share link: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Removed share link") {
		t.Fatalf("Expected 'Removed share link' in output, got: %s", out)
	}

	// User 2 should still be able to access (because they have email-based share now)
	proxyAssert(t, box, proxyExpectation{
		name:     "user2 still has access via email share",
		httpPort: httpPort,
		cookies:  cookies2,
		httpCode: http.StatusOK,
	})

	// Cleanup
	pty1 = sshToExeDev(t, keyFile1)
	pty1.sendLine("rm " + box)
	pty1.want("Deleting")
	pty1.wantPrompt()
	pty1.disconnect()
}

// proxyAssertWithQuery is like proxyAssert but adds a query string
func proxyAssertWithQuery(t *testing.T, box string, exp proxyExpectation, query string) {
	t.Helper()
	t.Logf("Testing proxy expectation: %s port %d expected http status %d", exp.name, exp.httpPort, exp.httpCode)
	if exp.httpPort == 0 {
		exp.httpPort = Env.exed.HTTPPort
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	if exp.cookies != nil {
		u := fmt.Sprintf("http://localhost:%d", exp.httpPort)
		ur, err := url.Parse(u)
		if err != nil {
			t.Fatalf("failed to parse URL %q: %v", u, err)
			return
		}
		// SetCookies seems to modify the cookies, so copy them:
		cookies := slices.Clone(exp.cookies)
		for i, c := range cookies {
			c2 := *c
			cookies[i] = &c2
		}
		jar.SetCookies(ur, exp.cookies)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Build URL with custom query string
	proxyURL := fmt.Sprintf("http://%s.localhost:%d/?%s", box, exp.httpPort, query)
	req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
	if err != nil {
		t.Errorf("failed to make http request: %v", err)
		return
	}
	req.Host = fmt.Sprintf("%s.localhost:%d", box, exp.httpPort)
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("failed to do http request: %v", err)
		return
	}
	// If we get a redirect (and we have a cookie), we want to do the auth dance. Remember,
	// we have a cookie to exe.dev, so we use that to get a series of redirections that will
	// get us back to the original URL. Ouchie.
	if resp.StatusCode == http.StatusTemporaryRedirect && exp.httpCode != http.StatusTemporaryRedirect && exp.cookies != nil {
		// We got a redirect when we weren't expecting it, but we have cookies,
		// so maybe we're just trying to do an auth dance. Let's do the auth dance!
		u, err := resp.Location()
		t.Logf("Got redirect to %s", u.String())
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
			return
		}

		// First redirect should be to /__exe.dev/login
		if !strings.Contains(u.String(), "/__exe.dev/login?") {
			t.Errorf("expected first redirect to /__exe.dev/login, got %s", u.String())
		}

		// Follow the login redirect, preserving the original Host header
		// The Location header is a relative URL, so the host should be the same
		originalHost := req.Host
		req, err = localhostRequestWithHostHeader("GET", u.String(), nil)
		if err != nil {
			t.Errorf("failed to make http request: %v", err)
			return
		}
		// localhostRequestWithHostHeader may not preserve the Host for plain localhost
		// so we explicitly set it to match the original request
		req.Host = originalHost
		resp, err = client.Do(req)
		if err != nil {
			t.Errorf("failed to do http request: %v", err)
			return
		}
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("expected redirect during auth dance, got status %d", resp.StatusCode)
		}
		u, err = resp.Location()
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
			return
		}
		t.Logf("Got redirect to main domain auth: %s", u.String())
		// Follow the redirect to /auth (which should then redirect to /auth/confirm with a secret)
		req, err = http.NewRequest("GET", u.String(), nil)
		if err != nil {
			t.Errorf("failed to make http request: %v", err)
			return
		}
		resp, err = client.Do(req)
		if err != nil {
			t.Errorf("failed to do http request: %v", err)
			return
		}
		// If we got the expected status code during the auth dance, we're done
		// (e.g., 404 when access is denied)
		if resp.StatusCode == exp.httpCode {
			// Auth dance failed with expected status - this is success
			return
		}
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("expected redirect to /auth/confirm, got status %d", resp.StatusCode)
		}
		u, err = resp.Location()
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
			return
		}
		t.Logf("Got redirect to confirm page: %s", u.String())

		// Now we scream through the confirm screen by adding "action=confirm" to the URL
		q := u.Query()
		q.Set("action", "confirm")
		u.RawQuery = q.Encode()
		req, err = http.NewRequest("GET", u.String(), nil)
		if err != nil {
			t.Errorf("failed to make http request: %v", err)
			return
		}
		resp, err = client.Do(req)
		if err != nil {
			t.Errorf("failed to do http request: %v", err)
			return
		}
		t.Logf("Last request was to: %s", req.URL.String())

		// Now we should get a redirect to /__exe.dev/auth
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("expected redirect during auth dance, got status %d", resp.StatusCode)
		}
		u, err = resp.Location()
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
			return
		}
		t.Logf("Got redirect to %s", u.String())
		if !strings.Contains(u.String(), "__exe.dev/auth?secret=") {
			t.Errorf("expected redirect to __exe.dev/auth, got %s", u.String())
		}
		req, err = localhostRequestWithHostHeader("GET", u.String(), nil)
		if err != nil {
			t.Errorf("failed to make http request: %v", err)
			return
		}
		req.Host = originalHost
		resp, err = client.Do(req)
		if err != nil {
			t.Errorf("failed to do http request: %v", err)
			return
		}
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("expected redirect during auth dance, got status %d", resp.StatusCode)
		}
		u, err = resp.Location()
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
			return
		}
		t.Logf("Final redirect to: %s", u.String())
		// Follow the final redirect
		req, err = localhostRequestWithHostHeader("GET", u.String(), nil)
		if err != nil {
			t.Errorf("failed to make http request: %v", err)
			return
		}
		req.Host = originalHost
		resp, err = client.Do(req)
		if err != nil {
			t.Errorf("failed to do http request: %v", err)
			return
		}
	}

	if resp.StatusCode != exp.httpCode {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("%s: expected HTTP %d, got %d. Body: %s", exp.name, exp.httpCode, resp.StatusCode, string(body))
	}

	if exp.redirectLocation != "" {
		if location := resp.Header.Get("Location"); location != exp.redirectLocation {
			t.Errorf("%s: expected redirect to %s, got %s", exp.name, exp.redirectLocation, location)
		}
	}
}

// TestShareCommands tests the share command SSH interface and captures output for golden files
func TestShareCommands(t *testing.T) {
	vouch.For("philip")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	// User 1: Create a box
	pty1, _, keyFile1, email1 := registerForExeDev(t)
	box := newBox(t, pty1, BoxOpts{Command: "/bin/bash"})
	pty1.wantPrompt()

	// Show initial share status (should be empty)
	pty1.sendLine(fmt.Sprintf("share show %s", box))
	pty1.want("Sharing for box")
	pty1.want(box)
	pty1.want("No shares configured")
	pty1.wantPrompt()

	// User 1: Share the box with a user (will be pending since they're not registered)
	email2 := "friend@example.com"
	pty1.sendLine(fmt.Sprintf("share add %s %s --message='Welcome to my box'", box, email2))
	pty1.want("Invitation sent to " + email2)
	pty1.want("will receive an email")
	pty1.wantPrompt()

	// Show updated share status (should show pending share)
	pty1.sendLine(fmt.Sprintf("share show %s", box))
	pty1.want("Sharing for box")
	pty1.want(box)
	pty1.want(email2)
	pty1.wantPrompt()

	// Create a share link
	pty1.sendLine(fmt.Sprintf("share add-share-link %s", box))
	pty1.want("Share link created")
	pty1.want("http://")
	pty1.want("share=")
	pty1.wantPrompt()

	// Show status with share link
	pty1.sendLine(fmt.Sprintf("share show %s", box))
	pty1.want("Sharing for box")
	pty1.want(box)
	pty1.want(email2)
	pty1.want("Share links:") // Share link section
	pty1.wantPrompt()

	// Remove the email share
	pty1.sendLine(fmt.Sprintf("share remove %s %s", box, email2))
	pty1.want("Removed " + email2 + "'s access")
	pty1.wantPrompt()

	// Show status after removal
	pty1.sendLine(fmt.Sprintf("share show %s", box))
	pty1.want("Sharing for box")
	pty1.want("Share links:") // Still has share link
	pty1.wantPrompt()

	// Test help command
	pty1.sendLine("help share")
	pty1.want("Command: share")
	pty1.want("Subcommands:")
	pty1.want("show")
	pty1.want("add")
	pty1.want("remove")
	pty1.want("add-link")
	pty1.want("remove-link")
	pty1.wantPrompt()

	// Cleanup
	pty1.sendLine("rm " + box)
	pty1.want("Deleting")
	pty1.wantPrompt()
	pty1.disconnect()

	// Don't need to clean up - test tracks keyFile and email for canonicalization
	_ = keyFile1
	_ = email1
}
