// This file contains tests for HTTP proxy functionality.

package e1e

import (
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"exe.dev/e1e/testinfra"
)

func TestHTTPProxy(t *testing.T) {
	t.Parallel()
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	pty, cookies, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.disconnect()
	waitForSSH(t, box, keyFile)

	// Make an index.html file to serve.
	makeIndex := boxSSHCommand(t, box, keyFile, "echo", "alive", ">", "/home/exedev/index.html")
	if err := makeIndex.Run(); err != nil {
		t.Fatalf("failed to create index.html: %v\n", err)
	}

	writeCGI := boxSSHCommand(t, box, keyFile, "sh", "-c", `set -e
mkdir -p /home/exedev/cgi-bin
cat <<'EOF' >/home/exedev/cgi-bin/headers
#!/bin/sh
echo "Content-Type: text/plain"
echo
env
EOF
chmod +x /home/exedev/cgi-bin/headers
`)
	writeCGI.Stdout = t.Output()
	writeCGI.Stderr = t.Output()
	if err := writeCGI.Run(); err != nil {
		t.Fatalf("failed to configure header CGI: %v\n", err)
	}

	// startedServers tracks ports where httpd is already running (to avoid double-starting).
	startedServers := make(map[int]bool)
	serveHTTP := func(t *testing.T, port int) {
		t.Helper()
		if startedServers[port] {
			return
		}
		startHTTPServer(t, box, keyFile, port)
		startedServers[port] = true
	}

	t.Run("default_port", func(t *testing.T) {
		serveHTTP(t, 8080)
		httpPort := Env.servers.Exed.HTTPPort

		// TODO: do the auth dance to test private routes too.

		t.Run("private_redirect", func(t *testing.T) {
			resp, err := doProxyRequest(t, box, httpPort)
			if err != nil {
				t.Fatalf("failed to make proxy request: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusTemporaryRedirect {
				t.Errorf("expected redirect for private route, got status %d", resp.StatusCode)
			}
		})

		t.Run("mark_public", func(t *testing.T) {
			exeShell := sshToExeDev(t, keyFile)
			exeShell.sendLine(fmt.Sprintf("share port %s 8080", box))
			exeShell.want("Route updated successfully")
			exeShell.wantPrompt()

			exeShell.sendLine(fmt.Sprintf("share set-public %s", box))
			exeShell.want("Route updated successfully")
			exeShell.wantPrompt()

			exeShell.sendLine(fmt.Sprintf("share port %s", box))
			exeShell.want("Port: 8080")
			exeShell.want("Share: public")
			exeShell.wantPrompt()
			exeShell.disconnect()
		})

		t.Run("port_validation", func(t *testing.T) {
			exeShell := sshToExeDev(t, keyFile)

			// Port below range
			exeShell.sendLine(fmt.Sprintf("share port %s 2999", box))
			exeShell.want("port must be between 3000 and 9999")
			exeShell.wantPrompt()

			// Port above range
			exeShell.sendLine(fmt.Sprintf("share port %s 10000", box))
			exeShell.want("port must be between 3000 and 9999")
			exeShell.wantPrompt()

			// Invalid port
			exeShell.sendLine(fmt.Sprintf("share port %s abc", box))
			exeShell.want("port must be between 3000 and 9999")
			exeShell.wantPrompt()

			exeShell.disconnect()
		})

		t.Run("public_route", func(t *testing.T) {
			sleepTimes := []time.Duration{
				0, 100 * time.Millisecond,
				200 * time.Millisecond, 300 * time.Millisecond, 500 * time.Millisecond,
				1 * time.Second, 1 * time.Second, 1 * time.Second, 1 * time.Second,
				2 * time.Second, 2 * time.Second,
			}
			var resp *http.Response
			var body []byte
			for _, sleepTime := range sleepTimes {
				time.Sleep(sleepTime)
				var err error
				resp, err = doProxyRequest(t, box, httpPort)
				if err != nil {
					continue
				}
				body, err = io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != http.StatusOK {
					continue
				}
				if strings.Contains(string(body), "alive") {
					break
				}
			}
			if resp == nil {
				t.Fatal("never received HTTP response from proxy")
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("expected HTTP 200 from public route, got %d", resp.StatusCode)
			}
			if !strings.Contains(string(body), "alive") {
				t.Fatalf("expected body to contain 'alive', got %s", body)
			}
		})

		t.Run("error_responses", func(t *testing.T) {
			httpPort := Env.servers.Exed.HTTPPort
			const (
				unreachablePort = 9091
				defaultPort     = 8080
			)

			setProxyPort := func(port int) {
				t.Helper()
				configureProxyRoute(t, keyFile, box, port, "public")
			}

			defer setProxyPort(defaultPort)
			setProxyPort(unreachablePort)

			resp, err := doProxyRequest(t, box, httpPort)
			if err != nil {
				t.Fatalf("failed to make proxy request: %v", err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("failed to read proxy response body: %v", err)
			}
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("expected HTTP 503 for unreachable route, got %d (body: %s)", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), "Service Unavailable") {
				t.Fatalf("expected response body to contain 'Service Unavailable', got %s", body)
			}

			missingBox := fmt.Sprintf("%s-missing", box)
			resp, err = doProxyRequest(t, missingBox, httpPort)
			if err != nil {
				t.Fatalf("failed to make proxy request for missing box: %v", err)
			}
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("failed to read missing box response body: %v", err)
			}
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected HTTP 401 for missing box, got %d (body: %s)", resp.StatusCode, body)
			}
			if !strings.Contains(string(body), "Access required") {
				t.Fatalf("expected response body to contain 'Access required', got %s", body)
			}
		})
	})

	t.Run("auth_confirm_owner_skip", func(t *testing.T) {
		altPort := Env.servers.Exed.ExtraPorts[0]
		serveHTTP(t, altPort)
		fixture := newProxyAuthFixture(t, box, altPort, cookies)
		jar := fixture.newJar()
		fixture.loginThroughProxy(jar)
		fixture.authCookie(jar) // will fail if no auth cookie issued
	})

	t.Run("logout_flow", func(t *testing.T) {
		altPort := Env.servers.Exed.ExtraPorts[0]
		serveHTTP(t, altPort)
		fixture := newProxyAuthFixture(t, box, altPort, cookies)
		requireLogoutUI(t, fixture.logoutURL, fixture.expectedLogout, fixture.port)

		ownerJar := fixture.newJar()
		ownerClient := fixture.loginThroughProxy(ownerJar)
		staleCookie := fixture.authCookie(ownerJar)

		otherJar := fixture.newJar()
		otherClient := fixture.loginThroughProxy(otherJar)

		fixture.logoutSession(ownerClient, ownerJar)
		fixture.requireLoginRedirect(ownerClient)
		fixture.requireLoginRedirectWithCookie(staleCookie)
		fixture.requireOtherSessionStillAuthed(otherClient, otherJar)
	})

	t.Run("forwarded_headers", func(t *testing.T) {
		const internalPort = 8080
		httpPort := Env.servers.Exed.HTTPPort

		serveHTTP(t, internalPort)
		configureProxyRoute(t, keyFile, box, internalPort, "public")

		client := noRedirectClient(nil)

		req := makeProxyRequestWithPath(t, box, httpPort, "/cgi-bin/headers")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request for forwarded headers: %v", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("expected HTTP 200 from forwarded header request, got %d (body: %s)", resp.StatusCode, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			resp.Body.Close()
			t.Fatalf("failed to read forwarded header response: %v", err)
		}
		resp.Body.Close()
		envMap := parseCGIEnv(body)

		gotProto := envMap["HTTP_X_FORWARDED_PROTO"]
		if gotProto != "http" {
			t.Fatalf("expected X-Forwarded-Proto=http, got %q", gotProto)
		}

		expectedHost := fmt.Sprintf("%s.exe.cloud:%d", box, httpPort)
		if gotHost := envMap["HTTP_X_FORWARDED_HOST"]; gotHost != expectedHost {
			t.Fatalf("expected X-Forwarded-Host=%q, got %q", expectedHost, gotHost)
		}

		initialXFF := strings.TrimSpace(envMap["HTTP_X_FORWARDED_FOR"])
		if initialXFF == "" {
			t.Fatalf("expected X-Forwarded-For to be populated in initial request")
		}
		initialChain := parseForwardedFor(initialXFF)
		clientIP := initialChain[len(initialChain)-1]
		if net.ParseIP(clientIP) == nil {
			t.Fatalf("expected final X-Forwarded-For hop to be an IP, got %q", clientIP)
		}

		req = makeProxyRequestWithPath(t, box, httpPort, "/cgi-bin/headers")
		priorXFF := "10.0.0.10"
		req.Header.Set("X-Forwarded-For", priorXFF)
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request with existing X-Forwarded-For: %v", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("expected HTTP 200 from appended header request, got %d (body: %s)", resp.StatusCode, string(body))
		}

		body, err = io.ReadAll(resp.Body)
		if err != nil {
			resp.Body.Close()
			t.Fatalf("failed to read appended forwarded header response: %v", err)
		}
		resp.Body.Close()

		mergedEnv := parseCGIEnv(body)

		gotXFF := strings.TrimSpace(mergedEnv["HTTP_X_FORWARDED_FOR"])
		if !strings.HasPrefix(gotXFF, priorXFF+",") {
			t.Fatalf("expected merged X-Forwarded-For to start with %q, got %q", priorXFF+",", gotXFF)
		}
		mergedChain := parseForwardedFor(gotXFF)
		if mergedChain[len(mergedChain)-1] != clientIP {
			t.Fatalf("expected merged X-Forwarded-For to end with %q, got %q", clientIP, mergedChain[len(mergedChain)-1])
		}
	})

	t.Run("reject_synthetic_exedev_headers", func(t *testing.T) {
		const internalPort = 8080
		httpPort := Env.servers.Exed.HTTPPort

		serveHTTP(t, internalPort)
		configureProxyRoute(t, keyFile, box, internalPort, "public")

		client := noRedirectClient(nil)
		req := makeProxyRequestWithPath(t, box, httpPort, "/cgi-bin/headers")
		req.Header.Set("X-ExeDev-UserID", "spoofed-user")
		req.Header.Set("X-ExeDev-Email", "spoofed@example.com")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request for spoofed header test: %v", err)
		}
		t.Cleanup(func() { resp.Body.Close() })

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected HTTP 200 from spoofed header request, got %d (body: %s)", resp.StatusCode, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read spoofed header response: %v", err)
		}

		envMap := parseCGIEnv(body)
		if val, ok := envMap["HTTP_X_EXEDEV_USERID"]; ok {
			t.Fatalf("expected X-ExeDev-UserID to be stripped, got %q", val)
		}
		if val, ok := envMap["HTTP_X_EXEDEV_EMAIL"]; ok {
			t.Fatalf("expected X-ExeDev-Email to be stripped, got %q", val)
		}
	})

	t.Run("alternate_ports", func(t *testing.T) {
		serveHTTP(t, Env.servers.Exed.ExtraPorts[0])

		expectedRedirect := fmt.Sprintf("http://%s.exe.cloud:%d/__exe.dev/login?redirect=http%%3A%%2F%%2F%s.exe.cloud%%3A%d%%2F%%3Ffoo%%3D1", box, Env.servers.Exed.ExtraPorts[0], box, Env.servers.Exed.ExtraPorts[0])
		proxyAssert(t, box, proxyExpectation{
			name:             "altport without auth redirects",
			httpPort:         Env.servers.Exed.ExtraPorts[0],
			cookies:          nil,
			httpCode:         http.StatusTemporaryRedirect,
			redirectLocation: expectedRedirect,
		})
		proxyAssert(t, box, proxyExpectation{
			name:     "altport with auth succeeds",
			httpPort: Env.servers.Exed.ExtraPorts[0],
			cookies:  cookies,
			httpCode: http.StatusOK,
		})

		proxyAssert(t, box, proxyExpectation{
			name:     "other altport with auth fails",
			httpPort: Env.servers.Exed.ExtraPorts[1],
			cookies:  cookies,
			httpCode: http.StatusBadGateway,
		})
	})

	t.Run("cookie_port_isolation", func(t *testing.T) {
		// Verify that a proxy auth cookie for one port doesn't work for another port.
		// This tests that cookies are named "login-with-exe-<port>" rather than
		// a shared name like "exe-proxy-auth".
		portA := Env.servers.Exed.ExtraPorts[0]
		portB := Env.servers.Exed.ExtraPorts[1]
		serveHTTP(t, portA)
		serveHTTP(t, portB)

		// Login through proxy on port A
		fixtureA := newProxyAuthFixture(t, box, portA, cookies)
		jarA := fixtureA.newJar()
		fixtureA.loginThroughProxy(jarA)
		proxyAuthCookieA := fixtureA.authCookie(jarA)

		// Verify the cookie works on port A
		clientA := noRedirectClient(jarA)
		reqA, err := localhostRequestWithHostHeader("GET", fixtureA.proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request for port A: %v", err)
		}
		respA, err := clientA.Do(reqA)
		if err != nil {
			t.Fatalf("failed to make request on port A: %v", err)
		}
		bodyA, _ := io.ReadAll(respA.Body)
		respA.Body.Close()
		if respA.StatusCode != http.StatusOK {
			t.Fatalf("cookie should work on port A, got status %d: %s", respA.StatusCode, bodyA)
		}

		// Try using the port A cookie on port B - should fail with redirect to login
		fixtureB := newProxyAuthFixture(t, box, portB, nil) // no main domain cookies
		jarB, _ := cookiejar.New(nil)
		// Manually add port A's cookie to port B's jar
		portBURL := mustParseURL(fmt.Sprintf("http://localhost:%d", portB))
		jarB.SetCookies(portBURL, []*http.Cookie{proxyAuthCookieA})

		clientB := noRedirectClient(jarB)
		reqB, err := localhostRequestWithHostHeader("GET", fixtureB.proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request for port B: %v", err)
		}
		respB, err := clientB.Do(reqB)
		if err != nil {
			t.Fatalf("failed to make request on port B: %v", err)
		}
		respB.Body.Close()

		// Should redirect to login because port A's cookie shouldn't work on port B
		if respB.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("port A cookie should NOT work on port B, expected redirect (307), got status %d", respB.StatusCode)
		}
		location, err := respB.Location()
		if err != nil {
			t.Fatalf("expected redirect location: %v", err)
		}
		if !strings.Contains(location.Path, "/__exe.dev/login") {
			t.Fatalf("expected redirect to login, got %s", location.String())
		}
	})

	// Cleanup
	cleanupBox(t, keyFile, box)
}

type proxyExpectation struct {
	name             string
	httpPort         int
	cookies          []*http.Cookie
	httpCode         int
	redirectLocation string // Expected Location header for redirects (optional)
}

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(fmt.Sprintf("parse %q: %v", raw, err))
	}
	return u
}

type proxyAuthFixture struct {
	t                  testing.TB
	port               int
	proxyURL           string
	logoutURL          string
	expectedLogout     string
	expectedReturnHost string
	cookieURL          *url.URL
	localCookieAddr    string
	localCookieURL     *url.URL
	cookies            []*http.Cookie
}

func newProxyAuthFixture(t *testing.T, box string, port int, cookies []*http.Cookie) proxyAuthFixture {
	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, port)
	cookieURL := mustParseURL(proxyURL)
	localCookieAddr := fmt.Sprintf("http://localhost:%d", port)
	localCookieURL := mustParseURL(localCookieAddr)
	return proxyAuthFixture{
		t:                  t,
		port:               port,
		proxyURL:           proxyURL,
		logoutURL:          fmt.Sprintf("http://%s.exe.cloud:%d/__exe.dev/logout", box, port),
		expectedLogout:     fmt.Sprintf("http://localhost:%d/logged-out", Env.servers.Exed.HTTPPort),
		expectedReturnHost: fmt.Sprintf("%s.exe.cloud:%d", box, port),
		cookieURL:          cookieURL,
		localCookieAddr:    localCookieAddr,
		localCookieURL:     localCookieURL,
		cookies:            cookies,
	}
}

// proxyAuthCookieName returns the cookie name for proxy authentication on a specific port.
func proxyAuthCookieName(port int) string {
	return fmt.Sprintf("login-with-exe-%d", port)
}

func (f proxyAuthFixture) authCookies(jar *cookiejar.Jar) []*http.Cookie {
	f.t.Helper()
	cookieName := proxyAuthCookieName(f.port)
	var found []*http.Cookie
	for _, u := range []*url.URL{f.cookieURL, f.localCookieURL} {
		for _, c := range jar.Cookies(u) {
			if c.Name == cookieName && c.Value != "" {
				copy := *c
				found = append(found, &copy)
			}
		}
	}
	return found
}

func (f proxyAuthFixture) newJar() *cookiejar.Jar {
	jar, _ := cookiejar.New(nil) // no error possible
	setCookiesForJar(f.t, jar, f.localCookieAddr, f.cookies)
	return jar
}

func (f proxyAuthFixture) loginThroughProxy(jar *cookiejar.Jar) *http.Client {
	f.t.Helper()
	client := noRedirectClient(jar)
	req, err := localhostRequestWithHostHeader("GET", f.proxyURL, nil)
	if err != nil {
		f.t.Fatalf("failed to create request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatalf("failed to do request: %v", err)
	}
	defer resp.Body.Close()

	redirectCount := 0
	sawConfirmRedirect := false
	for resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusSeeOther {
		if redirectCount > 10 {
			f.t.Fatalf("too many redirects")
		}

		location, err := resp.Location()
		if err != nil {
			body, _ := io.ReadAll(resp.Body)
			f.t.Fatalf("failed to get redirect location: %v (status %d, body %s)", err, resp.StatusCode, body)
		}

		req, err = localhostRequestWithHostHeader("GET", location.String(), nil)
		if err != nil {
			f.t.Fatalf("failed to create redirect request: %v", err)
		}

		isConfirm := strings.Contains(location.Path, "/auth/confirm")

		resp.Body.Close()
		resp, err = client.Do(req)
		if err != nil {
			f.t.Fatalf("failed to follow redirect: %v", err)
		}

		if isConfirm {
			f.assertOwnerConfirmRedirect(resp)
			sawConfirmRedirect = true
		}

		redirectCount++
	}

	if !sawConfirmRedirect {
		resp.Body.Close()
		f.t.Fatalf("owner flow never hit /auth/confirm redirect")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		f.t.Fatalf("failed to read final response body: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("expected final status 200, got %d. Body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "alive") {
		f.t.Fatalf("expected final body to contain 'alive', got %s", body)
	}

	return client
}

func (f proxyAuthFixture) assertOwnerConfirmRedirect(resp *http.Response) {
	f.t.Helper()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		f.t.Fatalf("owner flow should skip confirmation page, but status %d returned: %s", resp.StatusCode, body)
	}
	confirmLocation, err := resp.Location()
	if err != nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		f.t.Fatalf("owner flow confirm redirect missing Location header: %v (body: %s)", err, body)
	}
	if confirmLocation.Scheme != "http" {
		f.t.Fatalf("expected owner confirm redirect scheme http, got %s", confirmLocation.Scheme)
	}
	if confirmLocation.Host != f.expectedReturnHost {
		f.t.Fatalf("expected owner confirm redirect host %s, got %s", f.expectedReturnHost, confirmLocation.Host)
	}
	if confirmLocation.Path != "/__exe.dev/auth" {
		f.t.Fatalf("expected owner confirm redirect path /__exe.dev/auth, got %s", confirmLocation.Path)
	}
	query := confirmLocation.Query()
	if query.Get("secret") == "" {
		f.t.Fatalf("owner confirm redirect missing secret query parameter")
	}
	if query.Get("redirect") == "" {
		f.t.Fatalf("owner confirm redirect missing redirect query parameter")
	}
}

func (f proxyAuthFixture) authCookie(jar *cookiejar.Jar) *http.Cookie {
	f.t.Helper()
	cookies := f.authCookies(jar)
	if len(cookies) != 1 {
		f.t.Fatalf("expected one proxy auth cookie to be set after login, got %d", len(cookies))
	}
	return cookies[0]
}

func (f proxyAuthFixture) logoutSession(client *http.Client, jar *cookiejar.Jar) {
	f.t.Helper()
	logoutReq, err := localhostRequestWithHostHeader("POST", f.logoutURL, nil)
	if err != nil {
		f.t.Fatalf("failed to build logout request: %v", err)
	}
	resp, err := client.Do(logoutReq)
	if err != nil {
		f.t.Fatalf("failed to send logout request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("expected logout redirect, got status %d: %s", resp.StatusCode, body)
	}
	location, err := resp.Location()
	if err != nil {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("expected logout redirect location, got error: %v (body: %s)", err, body)
	}
	if location.String() != f.expectedLogout {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("expected logout redirect to %s, got %s (body: %s)", f.expectedLogout, location.String(), body)
	}
	if len(f.authCookies(jar)) > 0 {
		f.t.Fatalf("expected proxy auth cookie to be cleared, still present")
	}
}

func (f proxyAuthFixture) requireLoginRedirect(client *http.Client) {
	f.t.Helper()

	req, err := localhostRequestWithHostHeader("GET", f.proxyURL, nil)
	if err != nil {
		f.t.Fatalf("failed to rebuild proxy request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatalf("failed to make post-logout proxy request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("expected redirect after logout, got %d (body: %s)", resp.StatusCode, body)
	}
	location, err := resp.Location()
	if err != nil {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("missing redirect location after logout: %v (body: %s)", err, body)
	}
	if location == nil || !strings.Contains(location.Path, "/__exe.dev/login") {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("expected redirect to login after logout, got %v (body: %s)", location, body)
	}
}

func (f proxyAuthFixture) requireLoginRedirectWithCookie(staleCookie *http.Cookie) {
	f.t.Helper()

	client := noRedirectClient(nil)
	req, err := localhostRequestWithHostHeader("GET", f.proxyURL, nil)
	if err != nil {
		f.t.Fatalf("failed to create stale cookie request: %v", err)
	}
	req.AddCookie(staleCookie)
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatalf("failed to make request with stale cookie: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTemporaryRedirect {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("expected redirect for stale cookie request, got %d (body: %s)", resp.StatusCode, body)
	}
	location, err := resp.Location()
	if err != nil {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("missing redirect location for stale cookie request: %v (body: %s)", err, body)
	}
	if location == nil || !strings.Contains(location.Path, "/__exe.dev/login") {
		body, _ := io.ReadAll(resp.Body)
		f.t.Fatalf("expected login redirect for stale cookie, got %v (body: %s)", location, body)
	}
}

func (f proxyAuthFixture) requireOtherSessionStillAuthed(client *http.Client, jar *cookiejar.Jar) {
	f.t.Helper()

	req, err := localhostRequestWithHostHeader("GET", f.proxyURL, nil)
	if err != nil {
		f.t.Fatalf("failed to create proxy request for second session: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		f.t.Fatalf("failed to make proxy request from second session: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatalf("failed to read proxy response for second session: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		f.t.Fatalf("expected second session to retain auth, got %d (body: %s)", resp.StatusCode, body)
	}
	if len(f.authCookies(jar)) == 0 {
		f.t.Fatalf("expected second session cookie to remain after other session logout")
	}
}

func requireLogoutUI(t *testing.T, logoutURL, expectedLogout string, port int) {
	t.Helper()

	client := noRedirectClient(nil)
	logoutGetReq, err := localhostRequestWithHostHeader("GET", logoutURL, nil)
	if err != nil {
		t.Fatalf("failed to build logout GET request: %v", err)
	}
	logoutGetResp, err := client.Do(logoutGetReq)
	if err != nil {
		t.Fatalf("failed to send logout GET request: %v", err)
	}
	logoutGetBody, err := io.ReadAll(logoutGetResp.Body)
	logoutGetResp.Body.Close()
	if err != nil {
		t.Fatalf("failed to read logout GET body: %v", err)
	}
	if logoutGetResp.StatusCode != http.StatusOK {
		t.Fatalf("expected logout GET 200, got %d", logoutGetResp.StatusCode)
	}
	if !strings.Contains(string(logoutGetBody), "Are you sure you want to log out?") {
		t.Fatalf("expected logout confirmation form, got %s", logoutGetBody)
	}
	if !strings.Contains(string(logoutGetBody), `<form method="POST"`) {
		t.Fatalf("expected logout confirmation to include POST form")
	}

	logoutPostReq, err := localhostRequestWithHostHeader(http.MethodPost, logoutURL, nil)
	if err != nil {
		t.Fatalf("failed to build logout POST request: %v", err)
	}
	logoutPostResp, err := client.Do(logoutPostReq)
	if err != nil {
		t.Fatalf("failed to send logout POST request: %v", err)
	}
	defer logoutPostResp.Body.Close()

	if logoutPostResp.StatusCode != http.StatusTemporaryRedirect {
		body, _ := io.ReadAll(logoutPostResp.Body)
		t.Fatalf("expected logout POST redirect, got status %d: %s", logoutPostResp.StatusCode, body)
	}
	logoutPostLocation, err := logoutPostResp.Location()
	if err != nil {
		body, _ := io.ReadAll(logoutPostResp.Body)
		t.Fatalf("expected logout POST redirect location, got error: %v (body: %s)", err, body)
	}
	if logoutPostLocation.String() != expectedLogout {
		body, _ := io.ReadAll(logoutPostResp.Body)
		t.Fatalf("expected logout POST redirect to %s, got %s (body: %s)", expectedLogout, logoutPostLocation.String(), body)
	}

	cookieName := proxyAuthCookieName(port)
	logoutCookieCleared := false
	for _, c := range logoutPostResp.Cookies() {
		if c.Name == cookieName && c.Value == "" && c.MaxAge == -1 {
			logoutCookieCleared = true
			break
		}
	}
	if !logoutCookieCleared {
		t.Fatalf("expected logout POST to clear %s cookie", cookieName)
	}
}

func localhostRequestWithHostHeader(method, urlS string, body io.Reader) (*http.Request, error) {
	url, err := url.Parse(urlS)
	if err != nil {
		return nil, err
	}
	// Split the host and port
	host, _, err := net.SplitHostPort(url.Host)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(host, ".exe.cloud") {
		return http.NewRequest(method, url.String(), body)
	}
	originalUrlHost := url.Host
	url.Host = strings.Replace(url.Host, host, "localhost", 1)
	req, err := http.NewRequest(method, url.String(), body)
	if err != nil {
		return nil, err
	}
	req.Host = originalUrlHost
	return req, nil
}

func proxyAssert(t *testing.T, boxName string, exp proxyExpectation, query ...string) {
	t.Helper()
	// t.Logf("Testing proxy expectation: %s port %d expected http status %d", exp.name, exp.httpPort, exp.httpCode)
	jar, _ := cookiejar.New(nil) // no error possible
	if exp.cookies != nil {
		u := fmt.Sprintf("http://localhost:%d", exp.httpPort)
		setCookiesForJar(t, jar, u, exp.cookies)
	}
	client := noRedirectClient(jar)

	// We put in a GET parameter here to ensure that all the redirects preserve the parameters.
	q := "foo=1"
	if len(query) > 0 {
		q = query[0]
	}
	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/?%s", boxName, exp.httpPort, q)
	req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
	if err != nil {
		t.Errorf("failed to make http request: %v", err)
		return
	}
	req.Host = fmt.Sprintf("%s.exe.cloud:%d", boxName, exp.httpPort)
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
		// t.Logf("Got redirect to main domain auth: %s", u.String())
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
			t.Errorf("expected redirect after auth, got status %d", resp.StatusCode)
		}
		u, err = resp.Location()
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
			return
		}
		// t.Logf("Got redirect to confirm page: %s", u.String())

		// Check if we got /auth/confirm or directly to /__exe.dev/auth (owner skip)
		if strings.Contains(u.String(), "/auth/confirm") {
			// Follow the /auth/confirm redirect - it will either:
			// - Redirect directly for owners
			// - Show confirmation page (200 OK) for non-owners
			// - Return 401 for users without access
			req, err = localhostRequestWithHostHeader("GET", u.String(), nil)
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
			// If we got the expected status (like 401), we're done
			if resp.StatusCode == exp.httpCode {
				return
			}
			// Handle confirmation page for non-owners (200 OK with CONFIRM LOGIN page)
			if resp.StatusCode == http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if strings.Contains(string(body), "CONFIRM LOGIN") {
					// Extract Continue URL from confirmation page
					confirmURLRe := regexp.MustCompile(`href="([^"]*__exe\.dev/auth[^"]*)"`)
					matches := confirmURLRe.FindStringSubmatch(string(body))
					if len(matches) < 2 {
						t.Fatalf("could not find Continue URL in confirmation page")
						return
					}
					u, err = url.Parse(html.UnescapeString(matches[1]))
					if err != nil {
						t.Fatalf("failed to parse Continue URL: %v", err)
						return
					}
					t.Logf("Confirmation page shown, following Continue URL: %s", u.String())
				} else {
					t.Errorf("expected confirmation page or redirect, got 200 with unexpected body")
					return
				}
			} else if resp.StatusCode == http.StatusTemporaryRedirect {
				u, err = resp.Location()
				if err != nil {
					t.Fatalf("failed to get redirect location: %v", err)
					return
				}
			} else {
				t.Errorf("expected redirect or confirmation page after /auth/confirm, got status %d", resp.StatusCode)
				return
			}
		}
		// At this point u should be the /__exe.dev/auth URL
		t.Logf("Got redirect to %s", u.String())
		if !strings.Contains(u.String(), "__exe.dev/auth?secret=") {
			t.Errorf("expected redirect to __exe.dev/auth, got %s", u.String())
		}
		req, err = localhostRequestWithHostHeader("GET", u.String(), nil)
		if err != nil {
			t.Errorf("failed to make http request: %v", err)
			return
		}
		resp, err = client.Do(req)
		if err != nil {
			t.Errorf("failed to do http request: %v", err)
			return
		}
		// Now finally we should have a new cookie and should be back at the beginning!
		// Here we have to do a little bit of sleight of hand since the final redirect is /...
		// without an http://..., and Go doesn't do this with the foo.exe.cloud stuff... So:
		location := resp.Header.Get("Location")
		if location == "" {
			t.Fatalf("failed to get redirect location: %v", err)
			return
		}
		origURL := mustParseURL(proxyURL)
		u, err = origURL.Parse(location)
		if err != nil {
			t.Fatalf("failed to parse final redirect URL: %v", err)
			return
		}
		t.Logf("Got redirect to %s", u.String())
		if u.String() != proxyURL {
			t.Errorf("expected redirect to %s, got %s", proxyURL, u.String())
		}
		req, err = localhostRequestWithHostHeader("GET", u.String(), nil)
		if err != nil {
			t.Errorf("failed to make http request: %v", err)
			return
		}
		resp, err = client.Do(req)
		if err != nil {
			if resp != nil && resp.Body != nil {
				b, err := io.ReadAll(resp.Body)
				if err == nil {
					t.Logf("Response body: %s", string(b))
				}
			}
			t.Errorf("failed to do http request: %v", err)
			return
		}
	}

	if resp.StatusCode != exp.httpCode {
		body := ""
		if resp.Body != nil {
			b, err := io.ReadAll(resp.Body)
			if err == nil {
				body = string(b)
			}
		}
		t.Errorf("expected status %d, got %d, body: %s", exp.httpCode, resp.StatusCode, body)
		return
	}

	// Check redirect location if expected
	if exp.redirectLocation != "" {
		location := resp.Header.Get("Location")
		if location == "" {
			t.Errorf("expected redirect location %q, but no Location header found", exp.redirectLocation)
			return
		}
		if location != exp.redirectLocation {
			t.Errorf("expected redirect location %q, got %q", exp.redirectLocation, location)
			return
		}
		t.Logf("Redirect location matches expected: %s", location)
	}
}

// doProxyRequest makes an HTTP request to boxName, available at httpPort.
func doProxyRequest(t *testing.T, boxName string, httpPort int) (*http.Response, error) {
	t.Helper()
	client := noRedirectClient(nil)
	req := makeProxyRequest(t, boxName, httpPort)
	return client.Do(req)
}

func makeProxyRequest(t *testing.T, boxName string, httpPort int) *http.Request {
	return makeProxyRequestWithPath(t, boxName, httpPort, "/")
}

func makeProxyRequestWithPath(t *testing.T, boxName string, httpPort int, path string) *http.Request {
	t.Helper()
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d%s", httpPort, path)
	req, err := http.NewRequest("GET", proxyURL, nil)
	if err != nil {
		t.Fatalf("failed to create proxy request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.exe.cloud:%d", boxName, httpPort)
	return req
}

func parseCGIEnv(body []byte) map[string]string {
	envMap := make(map[string]string)
	for line := range strings.Lines(string(body)) {
		line := strings.TrimSpace(line)
		if idx := strings.IndexRune(line, '='); idx != -1 {
			envMap[line[:idx]] = line[idx+1:]
		}
	}
	return envMap
}

func parseForwardedFor(header string) []string {
	parts := strings.Split(header, ",")
	var trimmed []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			trimmed = append(trimmed, part)
		}
	}
	if len(trimmed) == 0 {
		return []string{header}
	}
	return trimmed
}

