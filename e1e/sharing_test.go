// This file tests box sharing functionality.

package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBoxSharing(t *testing.T) {
	t.Parallel()
	noGolden(t)

	ownerPTY, ownerCookies, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-box-sharing.example")
	box := newBox(t, ownerPTY, BoxOpts{Command: "/bin/bash"})
	ownerPTY.disconnect()
	waitForSSH(t, box, ownerKeyFile)

	const boxInternalPort = 8080
	makeIndex := boxSSHCommand(t, box, ownerKeyFile, "echo", "alive", ">", "/home/exedev/index.html")
	if err := makeIndex.Run(); err != nil {
		t.Fatalf("failed to create index.html: %v\n", err)
	}

	httpdCmd := boxSSHCommand(t, box, ownerKeyFile, "busybox", "httpd", "-f", "-p", strconv.Itoa(boxInternalPort), "-h", "/home/exedev")
	httpdCmd.Stdout = t.Output()
	httpdCmd.Stderr = t.Output()
	if err := httpdCmd.Start(); err != nil {
		t.Fatalf("failed to start busybox HTTP server: %v\n", err)
	}
	t.Cleanup(func() {
		httpdCmd.Process.Kill()
		httpdCmd.Wait()
	})

	// Wait for server to be ready
	waitCmd := boxSSHCommand(t, box, ownerKeyFile, "timeout", "20", "sh", "-c",
		fmt.Sprintf("'while ! curl -s http://localhost:%d/; do sleep 0.5; done'", boxInternalPort))
	waitCmd.Stdout = t.Output()
	waitCmd.Stderr = t.Output()
	if err := waitCmd.Run(); err != nil {
		t.Fatalf("failed to wait for busybox to serve: %v\n", err)
	}

	// httpPort is the exed HTTP proxy port, not the port inside the box
	httpPort := Env.exed.HTTPPort

	// Configure the proxy to use port 8080 and ensure it is private
	out, err := runExeDevSSHCommand(t, ownerKeyFile, "share", "port", box, fmt.Sprintf("%d", boxInternalPort))
	if err != nil {
		t.Fatalf("failed to set proxy port: %v\n%s", err, out)
	}
	out, err = runExeDevSSHCommand(t, ownerKeyFile, "share", "set-private", box)
	if err != nil {
		t.Fatalf("failed to set proxy visibility: %v\n%s", err, out)
	}

	// Verify owner can access the box via HTTPS proxy
	proxyAssert(t, box, proxyExpectation{
		name:     "owner can access own box",
		httpPort: httpPort,
		cookies:  ownerCookies,
		httpCode: http.StatusOK,
	})

	t.Run("email_sharing", func(t *testing.T) {
		noGolden(t)
		// Register a guest user.
		guestPTY, guestCookies, _, guestEmail := registerForExeDevWithEmail(t, "guest@test-box-sharing.example")

		// Verify guest cannot access the box yet (it's private)
		proxyAssert(t, box, proxyExpectation{
			name:     "guest cannot access private box",
			httpPort: httpPort,
			cookies:  guestCookies,
			httpCode: http.StatusUnauthorized, // Should get 401 to not leak box existence
		})

		out, err := runExeDevSSHCommand(t, ownerKeyFile, "share", "add", box, guestEmail, "--message=Welcome")
		if err != nil {
			t.Fatalf("failed to share box: %v\n%s", err, out)
		}
		if want := "Invitation sent to " + guestEmail; !strings.Contains(string(out), want) {
			t.Fatalf("Expected %q in output, got: %q", want, out)
		}

		emailMsg := Env.email.waitForEmail(t, guestEmail)
		if !strings.Contains(emailMsg.Body, "has shared") {
			t.Fatalf("Expected share invitation email, got: %s", emailMsg.Body)
		}
		if !strings.Contains(emailMsg.Body, "Welcome") {
			t.Fatalf("Expected custom message in email, got: %s", emailMsg.Body)
		}

		proxyAssert(t, box, proxyExpectation{
			name:     "guest can access shared box",
			httpPort: httpPort,
			cookies:  guestCookies,
			httpCode: http.StatusOK,
		})

		jar2, err := cookiejar.New(nil)
		if err != nil {
			t.Fatalf("failed to create cookie jar: %v", err)
		}
		for _, cookie := range guestCookies {
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
		if !strings.Contains(dashboard, ownerEmail) {
			t.Errorf("Expected owner email %s in dashboard", ownerEmail)
		}

		// Owner: Check share status via SSH
		shareOut, err := runExeDevSSHCommand(t, ownerKeyFile, "share", "show", box, "--json")
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
		if len(shareInfo.Users) > 0 && shareInfo.Users[0].Email != guestEmail {
			t.Errorf("Expected shared user %s, got %s", guestEmail, shareInfo.Users[0].Email)
		}
		if len(shareInfo.Users) > 0 && shareInfo.Users[0].Status != "active" {
			t.Errorf("Expected status 'active', got %s", shareInfo.Users[0].Status)
		}

		// Owner: Revoke access
		out, err = runExeDevSSHCommand(t, ownerKeyFile, "share", "remove", box, guestEmail)
		if err != nil {
			t.Fatalf("failed to remove share: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Removed "+guestEmail+"'s access") {
			t.Fatalf("Expected 'Removed %s's access' in output, got: %s", guestEmail, out)
		}

		proxyAssert(t, box, proxyExpectation{
			name:     "guest cannot access after revoked",
			httpPort: httpPort,
			cookies:  guestCookies,
			httpCode: http.StatusUnauthorized,
		})

		guestPTY.disconnect()
	})

	t.Run("share_link", func(t *testing.T) {
		noGolden(t)
		linkOut, err := runExeDevSSHCommand(t, ownerKeyFile, "share", "add-share-link", box, "--json")
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

		// Register a guest user.
		_, guestCookies, _, _ := registerForExeDev(t)

		// Guest should be able to access via share link.
		proxyAssertWithQuery(t, box, proxyExpectation{
			name:     "guest can access via share link",
			httpPort: httpPort,
			cookies:  guestCookies,
			httpCode: http.StatusOK,
		}, fmt.Sprintf("share=%s", linkInfo.Token))

		time.Sleep(100 * time.Millisecond) // TODO: poll instead of unilaterally sleeping
		proxyAssert(t, box, proxyExpectation{
			name:     "guest can access without share link after first access",
			httpPort: httpPort,
			cookies:  guestCookies,
			httpCode: http.StatusOK,
		})

		out, err := runExeDevSSHCommand(t, ownerKeyFile, "share", "remove-share-link", box, linkInfo.Token)
		if err != nil {
			t.Fatalf("failed to remove share link: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Removed share link") {
			t.Fatalf("Expected 'Removed share link' in output, got: %s", out)
		}

		proxyAssert(t, box, proxyExpectation{
			name:     "guest still has access via email share",
			httpPort: httpPort,
			cookies:  guestCookies,
			httpCode: http.StatusOK,
		})
	})

	// Cleanup
	ownerCleanup := sshToExeDev(t, ownerKeyFile)
	ownerCleanup.deleteBox(box)
	ownerCleanup.disconnect()
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
		setCookiesForJar(t, jar, u, exp.cookies)
	}
	client := noRedirectClient(jar)

	// Build URL with custom query string
	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/?%s", box, exp.httpPort, query)
	req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
	if err != nil {
		t.Errorf("failed to make http request: %v", err)
		return
	}
	req.Host = fmt.Sprintf("%s.exe.cloud:%d", box, exp.httpPort)
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
		// (e.g., 401 when access is denied)
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

		// Follow redirect to /auth/confirm which should redirect to /__exe.dev/auth for users with access
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

		// Should get a redirect to /__exe.dev/auth (307 for users with access)
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("expected StatusTemporaryRedirect (307) redirect after confirm, got status %d", resp.StatusCode)
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
		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("expected StatusSeeOther (303) redirect from magic auth, got status %d", resp.StatusCode)
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
	pty1.deleteBox(box)
	pty1.disconnect()

	// Don't need to clean up - test tracks keyFile and email for canonicalization
	_ = keyFile1
	_ = email1
}

// TestPublicBoxAccessByLoggedInUser tests that a logged-in user can access a public box
// even without an explicit share. This is a regression test for the bug where the
// auth redirect flow would return 404 for users without shares, even for public boxes.
//
// Scenario: A box owner creates a public website but wants to identify visitors.
// They add a "login" link that sends users through the auth dance:
//
//	/auth?redirect=https://mybox.exe.dev/&return_host=mybox.exe.dev
//
// The user authenticates, and redirectAfterAuth should allow access to the public box
// even though the user has no explicit share.
func TestPublicBoxAccessByLoggedInUser(t *testing.T) {
	t.Parallel()
	noGolden(t)

	// Owner creates a box
	ownerPTY, _, ownerKeyFile, _ := registerForExeDevWithEmail(t, "owner@test-public-box.example")
	box := newBox(t, ownerPTY, BoxOpts{Command: "/bin/bash"})
	ownerPTY.disconnect()
	waitForSSH(t, box, ownerKeyFile)

	const boxInternalPort = 8080

	// Create index.html to serve
	makeIndex := boxSSHCommand(t, box, ownerKeyFile, "echo", "public-content", ">", "/home/exedev/index.html")
	if err := makeIndex.Run(); err != nil {
		t.Fatalf("failed to create index.html: %v\n", err)
	}

	// Start HTTP server in the box
	httpdCmd := boxSSHCommand(t, box, ownerKeyFile, "busybox", "httpd", "-f", "-p", strconv.Itoa(boxInternalPort), "-h", "/home/exedev")
	httpdCmd.Stdout = t.Output()
	httpdCmd.Stderr = t.Output()
	if err := httpdCmd.Start(); err != nil {
		t.Fatalf("failed to start busybox HTTP server: %v\n", err)
	}
	t.Cleanup(func() {
		httpdCmd.Process.Kill()
		httpdCmd.Wait()
	})

	// Wait for server to be ready
	waitCmd := boxSSHCommand(t, box, ownerKeyFile, "timeout", "20", "sh", "-c",
		fmt.Sprintf("'while ! curl -s http://localhost:%d/; do sleep 0.5; done'", boxInternalPort))
	waitCmd.Stdout = t.Output()
	waitCmd.Stderr = t.Output()
	if err := waitCmd.Run(); err != nil {
		t.Fatalf("failed to wait for busybox to serve: %v\n", err)
	}

	httpPort := Env.exed.HTTPPort

	// Configure proxy port and set the box to PUBLIC
	out, err := runExeDevSSHCommand(t, ownerKeyFile, "share", "port", box, fmt.Sprintf("%d", boxInternalPort))
	if err != nil {
		t.Fatalf("failed to set proxy port: %v\n%s", err, out)
	}
	out, err = runExeDevSSHCommand(t, ownerKeyFile, "share", "set-public", box)
	if err != nil {
		t.Fatalf("failed to set public visibility: %v\n%s", err, out)
	}

	// Register a guest user (no share to this box)
	_, guestCookies, _, _ := registerForExeDevWithEmail(t, "guest@test-public-box.example")

	// Simulate a "login to identify yourself" flow on a public box.
	// The box owner might have a link like:
	//   /auth?redirect=http://box.exe.cloud/&return_host=box.exe.cloud:port
	// This sends the user through the auth dance even though the box is public.
	// The bug was that redirectAfterAuth returned 404 for users without explicit shares.
	returnHost := fmt.Sprintf("%s.exe.cloud:%d", box, httpPort)
	redirectURL := fmt.Sprintf("http://%s/", returnHost)
	authURL := fmt.Sprintf("http://localhost:%d/auth?redirect=%s&return_host=%s",
		httpPort, url.QueryEscape(redirectURL), url.QueryEscape(returnHost))

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("failed to create cookie jar: %v", err)
	}
	setCookiesForJar(t, jar, fmt.Sprintf("http://localhost:%d", httpPort), guestCookies)
	client := noRedirectClient(jar)

	// Hit /auth with redirect params - this should trigger redirectAfterAuth
	req, err := http.NewRequest("GET", authURL, nil)
	if err != nil {
		t.Fatalf("failed to create auth request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to do auth request: %v", err)
	}

	// Should get a redirect (not a 404 error)
	if resp.StatusCode == http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("BUG: got 404 when accessing public box through auth dance. Body: %s", body)
	}
	if resp.StatusCode != http.StatusTemporaryRedirect {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("expected redirect from /auth, got status %d. Body: %s", resp.StatusCode, body)
	}
	resp.Body.Close()

	// Follow the redirect chain to complete the auth dance
	location, err := resp.Location()
	if err != nil {
		t.Fatalf("failed to get redirect location: %v", err)
	}
	t.Logf("Auth redirected to: %s", location.String())

	// The redirect should be to /auth/confirm (for non-owners) with the magic secret
	if !strings.Contains(location.Path, "/auth/confirm") {
		t.Fatalf("expected redirect to /auth/confirm, got %s", location.String())
	}

	// Cleanup
	ownerCleanup := sshToExeDev(t, ownerKeyFile)
	ownerCleanup.deleteBox(box)
	ownerCleanup.disconnect()
}
