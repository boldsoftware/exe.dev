// This file tests the request-access flow for private boxes.

package e1e

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"exe.dev/e1e/testinfra"
)

func TestRequestAccess(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	noGolden(t)

	// Register owner and create a private box.
	ownerPTY, _, ownerKeyFile, ownerEmail := registerForExeDevWithEmail(t, "owner@test-request-access.example")
	box := newBox(t, ownerPTY, testinfra.BoxOpts{Command: "/bin/bash", NoEmail: true})
	ownerPTY.Disconnect()
	waitForSSH(t, box, ownerKeyFile)

	const boxInternalPort = 8080
	serveIndex(t, box, ownerKeyFile, "alive")

	httpPort := Env.HTTPPort()
	configureProxyRoute(t, ownerKeyFile, box, boxInternalPort, "private")

	// Register a guest via the proxy login-with-exe flow.
	guestEmail := "guest@test-request-access.example"
	guestCookies := webLoginWithExe(t, guestEmail)

	// Guest cannot access the private box — sees "You need access" page.
	t.Run("guest_sees_request_access_page", func(t *testing.T) {
		noGolden(t)
		proxyAssert(t, box, proxyExpectation{
			name:     "guest sees request access page",
			httpPort: httpPort,
			cookies:  guestCookies,
			httpCode: http.StatusUnauthorized,
		})
	})

	// Guest submits the request-access form → owner gets email with reply-to.
	t.Run("request_access_sends_email_with_reply_to", func(t *testing.T) {
		noGolden(t)

		// Complete the auth dance to get subdomain cookies, then POST.
		jar, _ := cookiejar.New(nil)
		boxURL := fmt.Sprintf("http://localhost:%d", httpPort)
		setCookiesForJar(t, jar, boxURL, guestCookies)
		client := noRedirectClient(jar)

		// Do a GET to trigger the auth dance and get subdomain cookies.
		proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)
		initReq, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create init request: %v", err)
		}
		initResp, err := client.Do(initReq)
		if err != nil {
			t.Fatalf("failed to do init request: %v", err)
		}
		// Follow the auth dance (redirects + CONFIRM LOGIN) to establish subdomain cookies.
		authResp := followAuthDance(t, client, initReq, initResp)
		authResp.Body.Close()

		// POST to the request-access endpoint with a message.
		requestURL := fmt.Sprintf("http://%s.exe.cloud:%d/__exe.dev/request-access", box, httpPort)
		req, err := localhostRequestWithHostHeader("POST", requestURL, strings.NewReader(url.Values{
			"message": {"Please let me collaborate!"},
		}.Encode()))
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to post request-access: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d; body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "Request sent") {
			t.Fatalf("expected 'Request sent' in body, got: %s", body)
		}

		// Verify the email sent to the owner.
		emailMsg, err := Env.servers.Email.WaitForEmail(ownerEmail)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(emailMsg.Subject, guestEmail) {
			t.Errorf("expected guest email %q in subject, got: %q", guestEmail, emailMsg.Subject)
		}
		if !strings.Contains(emailMsg.Subject, box) {
			t.Errorf("expected box name %q in subject, got: %q", box, emailMsg.Subject)
		}
		if !strings.Contains(emailMsg.Body, "Please let me collaborate!") {
			t.Errorf("expected message in body, got: %q", emailMsg.Body)
		}
		if !strings.Contains(emailMsg.Body, "share_vm="+box) {
			t.Errorf("expected share_vm param in body, got: %q", emailMsg.Body)
		}
		if !strings.Contains(emailMsg.Body, "share_email="+url.QueryEscape(guestEmail)) {
			t.Errorf("expected share_email param in body, got: %q", emailMsg.Body)
		}
		if emailMsg.ReplyTo != guestEmail {
			t.Errorf("expected reply-to %q, got %q", guestEmail, emailMsg.ReplyTo)
		}
	})

	cleanupBox(t, ownerKeyFile, box)
}
