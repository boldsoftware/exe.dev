// This file contains tests for HTTP proxy functionality.

package e1e

import (
	"encoding/json"
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
	"exe.dev/stage"
)

var confirmURLRe = regexp.MustCompile(`href="([^"]*__exe\.dev/auth[^"]*)"`)

func TestHTTPProxy(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.servers.Exeprox.HTTPPort
	extraPorts := Env.servers.Exeprox.ExtraPorts

	pty, cookies, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
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
		t.Cleanup(func() {
			delete(startedServers, port)
		})
	}

	t.Run("default_port", func(t *testing.T) {
		serveHTTP(t, 8080)

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
			exeShell.SendLine(fmt.Sprintf("share port %s 8080", box))
			exeShell.Want("Route updated successfully")
			exeShell.WantPrompt()

			exeShell.SendLine(fmt.Sprintf("share set-public %s", box))
			exeShell.Want("Route updated successfully")
			exeShell.WantPrompt()

			exeShell.SendLine(fmt.Sprintf("share port %s", box))
			exeShell.Want("Port: 8080")
			exeShell.Want("Share: public")
			exeShell.WantPrompt()
			exeShell.Disconnect()
		})

		t.Run("port_validation", func(t *testing.T) {
			exeShell := sshToExeDev(t, keyFile)

			// Port below range
			exeShell.SendLine(fmt.Sprintf("share port %s 2999", box))
			exeShell.Want("port must be between 3000 and 9999")
			exeShell.WantPrompt()

			// Port above range
			exeShell.SendLine(fmt.Sprintf("share port %s 10000", box))
			exeShell.Want("port must be between 3000 and 9999")
			exeShell.WantPrompt()

			// Invalid port
			exeShell.SendLine(fmt.Sprintf("share port %s abc", box))
			exeShell.Want("port must be between 3000 and 9999")
			exeShell.WantPrompt()

			exeShell.Disconnect()
		})

		t.Run("public_route", func(t *testing.T) {
			var resp *http.Response
			var body []byte
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				var err error
				resp, err = doProxyRequest(t, box, httpPort)
				if err != nil {
					time.Sleep(500 * time.Millisecond)
					continue
				}
				body, err = io.ReadAll(resp.Body)
				resp.Body.Close()
				if err == nil && resp.StatusCode == http.StatusOK && strings.Contains(string(body), "alive") {
					break
				}
				time.Sleep(500 * time.Millisecond)
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
			// Unauthenticated users get redirected to the main domain auth page.
			// The important thing is they don't get 200 (access granted).
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("missing box should not return 200, got: %s", body)
			}
		})
	})

	t.Run("auth_confirm_owner_skip", func(t *testing.T) {
		altPort := extraPorts[0]
		serveHTTP(t, altPort)
		fixture := newProxyAuthFixture(t, box, altPort, httpPort, cookies)
		jar := fixture.newJar()
		fixture.loginThroughProxy(jar)
		fixture.authCookie(jar) // will fail if no auth cookie issued
	})

	t.Run("logout_flow", func(t *testing.T) {
		altPort := extraPorts[0]
		serveHTTP(t, altPort)
		fixture := newProxyAuthFixture(t, box, altPort, httpPort, cookies)
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
		serveHTTP(t, extraPorts[0])

		expectedRedirect := fmt.Sprintf("http://%s.exe.cloud:%d/__exe.dev/login?redirect=%%2F%%3Ffoo%%3D1", box, extraPorts[0])
		proxyAssert(t, box, proxyExpectation{
			name:             "altport without auth redirects",
			httpPort:         extraPorts[0],
			cookies:          nil,
			httpCode:         http.StatusTemporaryRedirect,
			redirectLocation: expectedRedirect,
		})
		proxyAssert(t, box, proxyExpectation{
			name:     "altport with auth succeeds",
			httpPort: extraPorts[0],
			cookies:  cookies,
			httpCode: http.StatusOK,
		})

		proxyAssert(t, box, proxyExpectation{
			name:     "other altport with auth fails",
			httpPort: extraPorts[1],
			cookies:  cookies,
			httpCode: http.StatusBadGateway,
		})
	})

	t.Run("cookie_port_isolation", func(t *testing.T) {
		// Verify that a proxy auth cookie for one port doesn't work for another port.
		// This tests that cookies are named "login-with-exe-<port>" rather than
		// a shared name like "exe-proxy-auth".
		portA := extraPorts[0]
		portB := extraPorts[1]
		serveHTTP(t, portA)
		serveHTTP(t, portB)

		// Login through proxy on port A
		fixtureA := newProxyAuthFixture(t, box, portA, httpPort, cookies)
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
		fixtureB := newProxyAuthFixture(t, box, portB, httpPort, nil) // no main domain cookies
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

	t.Run("cookie_domain_isolation", func(t *testing.T) {
		const internalPort = 8080

		// Write a CGI script that sets cookies with various Domain attributes.
		writeCookieCGI := boxSSHShell(t, box, keyFile, fmt.Sprintf(`set -e
cat <<'ENDCGI' >/home/exedev/cgi-bin/setcookie
#!/bin/sh
echo "Content-Type: text/plain"
echo "Set-Cookie: hostonly=val1; Path=/"
echo "Set-Cookie: domainwide=val2; Domain=.exe.cloud; Path=/"
echo "Set-Cookie: subdomain=val3; Domain=%s.exe.cloud; Path=/"
echo
echo ok
ENDCGI
chmod +x /home/exedev/cgi-bin/setcookie`, box))
		writeCookieCGI.Stdout = t.Output()
		writeCookieCGI.Stderr = t.Output()
		if err := writeCookieCGI.Run(); err != nil {
			t.Fatalf("failed to create setcookie CGI: %v", err)
		}

		serveHTTP(t, internalPort)
		configureProxyRoute(t, keyFile, box, internalPort, "public")

		client := noRedirectClient(nil)
		req := makeProxyRequestWithPath(t, box, httpPort, "/cgi-bin/setcookie")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		// Check Set-Cookie headers: Domain attributes must be stripped
		// for requests on shared domains (*.exe.cloud).
		setCookies := resp.Header.Values("Set-Cookie")
		if len(setCookies) == 0 {
			t.Fatal("expected Set-Cookie headers in response, got none")
		}

		for _, sc := range setCookies {
			lower := strings.ToLower(sc)
			// Look for "; domain=" — the attribute form.
			// This avoids false positives from cookie names like "subdomain=...".
			if strings.Contains(lower, "; domain=") || strings.Contains(lower, ";domain=") {
				t.Errorf("Set-Cookie should not contain Domain attribute on shared domain, got: %s", sc)
			}
		}

		// Verify cookie name=value pairs survived the stripping.
		found := map[string]bool{}
		for _, sc := range setCookies {
			for _, name := range []string{"hostonly", "domainwide", "subdomain"} {
				if strings.HasPrefix(sc, name+"=") {
					found[name] = true
				}
			}
		}
		for _, name := range []string{"hostonly", "domainwide", "subdomain"} {
			if !found[name] {
				t.Errorf("cookie %q missing from response; Set-Cookie headers: %v", name, setCookies)
			}
		}
	})

	// Test that deleting a box also deletes its auth cookies.
	t.Run("delete_removes_auth_cookies", func(t *testing.T) {
		noGolden(t)

		// The auth_confirm_owner_skip and logout_flow subtests above already created
		// auth cookies for this box. Verify they appear in the profile API.
		client := newClientWithCookies(t, cookies)
		profileURL := fmt.Sprintf("http://localhost:%d/api/profile", httpPort)
		t.Logf("profileURL %q", profileURL)

		boxDomain := fmt.Sprintf("%s.exe.cloud", box)

		if !profileHasSiteSession(t, client, profileURL, boxDomain) {
			t.Fatalf("profile API should show box domain %q in site sessions before deletion", boxDomain)
		}

		// Delete the box
		cleanupBox(t, keyFile, box)

		// Verify the box domain is no longer in the profile API
		if profileHasSiteSession(t, client, profileURL, boxDomain) {
			t.Errorf("profile API should NOT show box domain %q in site sessions after deletion", boxDomain)
		}
	})
}

type proxyExpectation struct {
	name             string
	httpPort         int
	cookies          []*http.Cookie
	httpCode         int
	redirectLocation string // Expected Location header for redirects (optional)
	host             string // Custom host header (default: "<box>.exe.cloud:<port>")
}

// profileHasSiteSession checks if the /api/profile response includes a site session for the given domain.
func profileHasSiteSession(t testing.TB, client *http.Client, profileURL, domain string) bool {
	t.Helper()
	resp, err := client.Get(profileURL)
	if err != nil {
		t.Fatalf("failed to GET %s: %v", profileURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned status %d", profileURL, resp.StatusCode)
	}
	var profile struct {
		SiteSessions []struct {
			Domain string `json:"domain"`
		} `json:"siteSessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatalf("failed to decode %s: %v", profileURL, err)
	}
	for _, s := range profile.SiteSessions {
		if s.Domain == domain {
			return true
		}
	}
	return false
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

func newProxyAuthFixture(t *testing.T, box string, port, serverHTTPPort int, cookies []*http.Cookie) proxyAuthFixture {
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

	// Track the logical current URL so relative redirects resolve against the
	// correct origin. In tests the actual HTTP requests go to localhost with a
	// Host-header override, so resp.Location() (which resolves against the
	// transport URL) would lose track of which domain we're logically on.
	// Updating currentBase after every redirect keeps both main-domain and
	// box-subdomain relative redirects correct.
	currentBase := mustParseURL(f.proxyURL)
	redirectCount := 0
	sawConfirmRedirect := false
	for resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusSeeOther {
		if redirectCount > 10 {
			f.t.Fatalf("too many redirects")
		}

		rawLocation := resp.Header.Get("Location")
		if rawLocation == "" {
			body, _ := io.ReadAll(resp.Body)
			f.t.Fatalf("missing Location header (status %d, body %s)", resp.StatusCode, body)
		}
		location, err := currentBase.Parse(rawLocation)
		if err != nil {
			f.t.Fatalf("failed to parse redirect location %q: %v", rawLocation, err)
		}
		currentBase = location

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
	if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusTemporaryRedirect {
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
	host := exp.host
	if host == "" {
		host = fmt.Sprintf("%s.exe.cloud:%d", boxName, exp.httpPort)
	}
	proxyURL := fmt.Sprintf("http://%s/?%s", host, q)
	req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
	if err != nil {
		t.Errorf("failed to make http request: %v", err)
		return
	}
	req.Host = host
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
		if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusTemporaryRedirect {
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
			} else if resp.StatusCode == http.StatusSeeOther || resp.StatusCode == http.StatusTemporaryRedirect {
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

// followAuthDance follows the proxy auth redirect chain (307/303 hops and
// the CONFIRM LOGIN page) until a non-redirect, non-confirm response is
// reached. It returns that final response. The caller's client (and its
// cookie jar) accumulate the proxy-auth cookie along the way.
func followAuthDance(t *testing.T, client *http.Client, initReq *http.Request, resp *http.Response) *http.Response {
	t.Helper()
	currentBase := mustParseURL(initReq.URL.String())
	for i := 0; i < 20; i++ {
		switch {
		case resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusSeeOther:
			rawLoc := resp.Header.Get("Location")
			resp.Body.Close()
			if rawLoc == "" {
				t.Fatal("followAuthDance: missing Location header")
			}
			u, err := currentBase.Parse(rawLoc)
			if err != nil {
				t.Fatalf("followAuthDance: bad Location %q: %v", rawLoc, err)
			}
			currentBase = u
			req, err := localhostRequestWithHostHeader("GET", u.String(), nil)
			if err != nil {
				t.Fatalf("followAuthDance: %v", err)
			}
			resp, err = client.Do(req)
			if err != nil {
				t.Fatalf("followAuthDance: %v", err)
			}

		case resp.StatusCode == http.StatusOK:
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatalf("followAuthDance: reading body: %v", err)
			}
			if !strings.Contains(string(body), "CONFIRM LOGIN") {
				// Not a confirm page — reconstruct the response for the caller.
				resp.Body = io.NopCloser(strings.NewReader(string(body)))
				return resp
			}
			// Extract Continue URL from confirmation page.
			m := confirmURLRe.FindStringSubmatch(string(body))
			if len(m) < 2 {
				t.Fatal("followAuthDance: no Continue URL in confirmation page")
			}
			u, err := currentBase.Parse(html.UnescapeString(m[1]))
			if err != nil {
				t.Fatalf("followAuthDance: bad Continue URL: %v", err)
			}
			currentBase = u
			req, err := localhostRequestWithHostHeader("GET", u.String(), nil)
			if err != nil {
				t.Fatalf("followAuthDance: %v", err)
			}
			resp, err = client.Do(req)
			if err != nil {
				t.Fatalf("followAuthDance: %v", err)
			}

		default:
			return resp
		}
	}
	t.Fatal("followAuthDance: too many redirects")
	return nil
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

// TestProxyTokenNamespaceIsolation verifies that token-based proxy authentication
// enforces strict namespace isolation:
// - An API token must NOT work for VM proxy auth
// - A token for VM1 must NOT work for VM2
// - Only a token with the exact namespace (v0@vmname.BOXHOST) should work
func TestProxyTokenNamespaceIsolation(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.servers.Exeprox.HTTPPort

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Start HTTP server on the box
	startHTTPServer(t, box, keyFile, 8080)

	// Make the route private (requires auth)
	exeShell := sshToExeDev(t, keyFile)
	exeShell.SendLine(fmt.Sprintf("share port %s 8080", box))
	exeShell.Want("Route updated successfully")
	exeShell.WantPrompt()
	exeShell.SendLine(fmt.Sprintf("share set-private %s", box))
	exeShell.Want("Route updated successfully")
	exeShell.WantPrompt()
	exeShell.Disconnect()

	// Load signer for generating signed tokens
	ts := loadTestSigner(t, keyFile)

	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)

	// Helper to make a proxy request with a bearer token
	makeTokenRequest := func(token string) (*http.Response, error) {
		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return client.Do(req)
	}

	// Helper to make a proxy request with basic auth (token as password)
	makeBasicAuthRequest := func(token string) (*http.Response, error) {
		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth("anyuser", token)
		return client.Do(req)
	}

	t.Run("api_token_rejected_by_proxy", func(t *testing.T) {
		// A token signed for the API namespace should NOT work for VM proxy
		apiToken := generateToken(t, ts, `{}`, execAPINamespace)
		resp, err := makeTokenRequest(apiToken)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Should return 401 (Authorization header is present but token is invalid for this VM)
		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("API token should be rejected by proxy: expected 401, got %d: %s",
				resp.StatusCode, body)
		}
	})

	t.Run("wrong_vm_token_rejected", func(t *testing.T) {
		// A token for a different VM should NOT work
		wrongVMToken := generateToken(t, ts, `{}`, "v0@othervmname."+stage.Test().BoxHost)
		resp, err := makeTokenRequest(wrongVMToken)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Should return 401 (Authorization header is present but token is invalid for this VM)
		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("wrong VM token should be rejected: expected 401, got %d: %s",
				resp.StatusCode, body)
		}
	})

	t.Run("correct_vm_token_works_bearer", func(t *testing.T) {
		// A token with the correct namespace should work
		// The namespace format is v0@VMNAME.BOXHOST (using BoxHost from env)
		correctToken := generateToken(t, ts, `{}`, "v0@"+box+"."+stage.Test().BoxHost)
		resp, err := makeTokenRequest(correctToken)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Should get 200 (or at least not a redirect to login)
		if resp.StatusCode == http.StatusTemporaryRedirect {
			location, _ := resp.Location()
			if location != nil && strings.Contains(location.Path, "/__exe.dev/login") {
				t.Errorf("correct VM token should authenticate, but got login redirect")
			}
		}
		// Note: might get 502 if httpd isn't running, but that's OK - we're testing auth
		if resp.StatusCode == http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("correct VM token should authenticate: got 401: %s", body)
		}
	})

	t.Run("correct_vm_token_works_basic_auth", func(t *testing.T) {
		// Token as password in basic auth should also work
		correctToken := generateToken(t, ts, `{}`, "v0@"+box+"."+stage.Test().BoxHost)
		resp, err := makeBasicAuthRequest(correctToken)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Should not redirect to login
		if resp.StatusCode == http.StatusTemporaryRedirect {
			location, _ := resp.Location()
			if location != nil && strings.Contains(location.Path, "/__exe.dev/login") {
				t.Errorf("basic auth with correct token should authenticate, but got login redirect")
			}
		}
		if resp.StatusCode == http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("basic auth with correct token should authenticate: got 401: %s", body)
		}
	})

	t.Run("api_token_rejected_via_basic_auth", func(t *testing.T) {
		// API token via basic auth should also be rejected
		apiToken := generateToken(t, ts, `{}`, execAPINamespace)
		resp, err := makeBasicAuthRequest(apiToken)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Should return 401 (Authorization header is present but token is invalid for this VM)
		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("API token via basic auth should be rejected: expected 401, got %d: %s",
				resp.StatusCode, body)
		}
	})

	t.Run("token_payload_passed_to_server", func(t *testing.T) {
		noGolden(t)
		// Create a CGI script to echo headers
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
		if err := writeCGI.Run(); err != nil {
			t.Fatalf("failed to configure header CGI: %v", err)
		}

		// Generate a token with ctx field containing custom data
		// The ctx field is what gets passed to the VM's server
		correctToken := generateToken(t, ts, `{"ctx":{"scope":"repo:push","repo":"myrepo"}}`, "v0@"+box+"."+stage.Test().BoxHost)

		client := noRedirectClient(nil)
		cgiURL := fmt.Sprintf("http://%s.exe.cloud:%d/cgi-bin/headers", box, httpPort)
		req, err := localhostRequestWithHostHeader("GET", cgiURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+correctToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		// Check that the ctx field was passed as the token ctx header
		tokenCtx := envMap["HTTP_X_EXEDEV_TOKEN_CTX"]
		if tokenCtx == "" {
			t.Errorf("expected X-ExeDev-Token-Ctx header, not found in: %s", body)
		} else {
			// Verify the ctx contains our custom data
			var parsed map[string]any
			if err := json.Unmarshal([]byte(tokenCtx), &parsed); err != nil {
				t.Errorf("failed to parse token ctx: %v", err)
			} else {
				if parsed["scope"] != "repo:push" {
					t.Errorf("expected scope=repo:push, got %v", parsed["scope"])
				}
				if parsed["repo"] != "myrepo" {
					t.Errorf("expected repo=myrepo, got %v", parsed["repo"])
				}
			}
		}

		// Check that user headers were also set
		if envMap["HTTP_X_EXEDEV_USERID"] == "" {
			t.Errorf("expected X-ExeDev-UserID header")
		}
		if envMap["HTTP_X_EXEDEV_EMAIL"] == "" {
			t.Errorf("expected X-ExeDev-Email header")
		}
	})

	t.Run("token_ctx_passed_verbatim", func(t *testing.T) {
		// This test verifies that ONLY the ctx field is passed to the VM,
		// and that it's passed EXACTLY as it appears in the signed payload
		// (not re-serialized through a JSON parser).
		//
		// The ctx field has unusual but valid JSON formatting that would
		// not survive a json.Marshal round-trip:
		// - Multiple spaces after colons
		// - Tab characters
		// - Non-canonical key ordering (z before a)
		// Note: We avoid newlines since HTTP headers cannot contain them.
		weirdCtx := `{"z":   1,	"a":  2,  "foo":"bar"}`
		// The outer payload has other fields that should NOT be passed
		fullPayload := `{"exp":4000000000,"ctx":` + weirdCtx + `}`

		token := generateToken(t, ts, fullPayload, "v0@"+box+"."+stage.Test().BoxHost)

		client := noRedirectClient(nil)
		cgiURL := fmt.Sprintf("http://%s.exe.cloud:%d/cgi-bin/headers", box, httpPort)
		req, err := localhostRequestWithHostHeader("GET", cgiURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		received := envMap["HTTP_X_EXEDEV_TOKEN_CTX"]
		if received == "" {
			t.Fatalf("expected X-ExeDev-Token-Ctx header, not found in: %s", body)
		}

		// The header MUST contain ONLY the ctx field value, byte-for-byte identical
		if received != weirdCtx {
			t.Errorf("ctx was not passed verbatim\nexpected: %q\nreceived: %q", weirdCtx, received)
		}

		// Verify it's valid JSON and has the expected content
		var parsed map[string]any
		if err := json.Unmarshal([]byte(received), &parsed); err != nil {
			t.Fatalf("received ctx is not valid JSON: %v", err)
		}
		if parsed["z"] != float64(1) {
			t.Errorf("expected z=1, got %v", parsed["z"])
		}
		if parsed["a"] != float64(2) {
			t.Errorf("expected a=2, got %v", parsed["a"])
		}
		if parsed["foo"] != "bar" {
			t.Errorf("expected foo=bar, got %v", parsed["foo"])
		}

		// Verify that outer payload fields (like exp) are NOT present in ctx
		if _, hasExp := parsed["exp"]; hasExp {
			t.Errorf("outer payload field 'exp' should not be in ctx header")
		}
	})

	t.Run("token_without_ctx_no_header", func(t *testing.T) {
		// When ctx is not present in the token, no X-ExeDev-Token-Ctx header should be set
		payloadWithoutCtx := `{"exp":4000000000}`
		token := generateToken(t, ts, payloadWithoutCtx, "v0@"+box+"."+stage.Test().BoxHost)

		client := noRedirectClient(nil)
		cgiURL := fmt.Sprintf("http://%s.exe.cloud:%d/cgi-bin/headers", box, httpPort)
		req, err := localhostRequestWithHostHeader("GET", cgiURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		// When ctx is absent, the header should not be set
		if received := envMap["HTTP_X_EXEDEV_TOKEN_CTX"]; received != "" {
			t.Errorf("expected no X-ExeDev-Token-Ctx header when ctx absent, got: %q", received)
		}
	})

	// Cleanup
	cleanupBox(t, keyFile, box)
}

// TestProxyPrivateRouteTokenAuth verifies that token-based authentication
// (Bearer and Basic) works on private proxy routes without needing cookies.
// Private routes should:
// - Accept valid Bearer tokens and proxy with X-ExeDev-* headers
// - Accept valid Basic auth tokens and proxy with X-ExeDev-* headers
// - Return 401 for invalid tokens when Authorization header is present
// - Redirect (307) to login when no auth is provided
func TestProxyPrivateRouteTokenAuth(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.servers.Exeprox.HTTPPort

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Start HTTP server on the box
	startHTTPServer(t, box, keyFile, 8080)

	// Create a CGI script that echoes all env vars (including proxied headers)
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
	if err := writeCGI.Run(); err != nil {
		t.Fatalf("failed to configure header CGI: %v", err)
	}

	// Configure port 8080 as a PRIVATE route (private is default, but be explicit)
	configureProxyRoute(t, keyFile, box, 8080, "private")

	ts := loadTestSigner(t, keyFile)
	proxyURL := fmt.Sprintf("http://%s.exe.cloud:%d/cgi-bin/headers", box, httpPort)

	t.Run("bearer_token_200_with_headers", func(t *testing.T) {
		token := generateToken(t, ts, `{"ctx":{"role":"admin"}}`, "v0@"+box+"."+stage.Test().BoxHost)

		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		if envMap["HTTP_X_EXEDEV_USERID"] == "" {
			t.Errorf("expected X-ExeDev-UserID header, not found in: %s", body)
		}
		if envMap["HTTP_X_EXEDEV_EMAIL"] == "" {
			t.Errorf("expected X-ExeDev-Email header, not found in: %s", body)
		}

		payload := envMap["HTTP_X_EXEDEV_TOKEN_CTX"]
		if payload == "" {
			t.Fatalf("expected X-ExeDev-Token-Ctx header, not found in: %s", body)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			t.Fatalf("failed to parse token payload: %v", err)
		}
		if parsed["role"] != "admin" {
			t.Errorf("expected role=admin, got %v", parsed["role"])
		}
	})

	t.Run("basic_auth_token_200_with_headers", func(t *testing.T) {
		token := generateToken(t, ts, `{"ctx":{"role":"reader"}}`, "v0@"+box+"."+stage.Test().BoxHost)

		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.SetBasicAuth("anyuser", token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		if envMap["HTTP_X_EXEDEV_USERID"] == "" {
			t.Errorf("expected X-ExeDev-UserID header, not found in: %s", body)
		}
		if envMap["HTTP_X_EXEDEV_EMAIL"] == "" {
			t.Errorf("expected X-ExeDev-Email header, not found in: %s", body)
		}

		payload := envMap["HTTP_X_EXEDEV_TOKEN_CTX"]
		if payload == "" {
			t.Fatalf("expected X-ExeDev-Token-Ctx header, not found in: %s", body)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			t.Fatalf("failed to parse token payload: %v", err)
		}
		if parsed["role"] != "reader" {
			t.Errorf("expected role=reader, got %v", parsed["role"])
		}
	})

	t.Run("invalid_token_returns_401", func(t *testing.T) {
		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer exe0.bogus.dG9rZW4.invalid")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 401 for invalid token, got %d: %s", resp.StatusCode, body)
		}
	})

	t.Run("no_auth_redirects_to_login", func(t *testing.T) {
		client := noRedirectClient(nil)
		indexURL := fmt.Sprintf("http://%s.exe.cloud:%d/", box, httpPort)
		req, err := localhostRequestWithHostHeader("GET", indexURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusTemporaryRedirect {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("expected 307 redirect for unauthenticated request, got %d: %s", resp.StatusCode, body)
		}
	})

	cleanupBox(t, keyFile, box)
}

// TestProxyPublicRouteTokenCtx verifies token ctx behavior on public routes:
//   - A valid token for THIS public VM should have its ctx forwarded normally
//   - A token for a DIFFERENT VM fails namespace validation, so no ctx (or any
//     auth headers) should reach the container
func TestProxyPublicRouteTokenCtx(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.servers.Exeprox.HTTPPort

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Start HTTP server on the box
	startHTTPServer(t, box, keyFile, 8080)

	// Make the route public
	configureProxyRoute(t, keyFile, box, 8080, "public")

	// Create a CGI script that echoes all env vars (to inspect forwarded headers)
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
	if err := writeCGI.Run(); err != nil {
		t.Fatalf("failed to configure header CGI: %v", err)
	}

	ts := loadTestSigner(t, keyFile)
	cgiURL := fmt.Sprintf("http://%s.exe.cloud:%d/cgi-bin/headers", box, httpPort)

	t.Run("same_vm_token_ctx_forwarded_on_public_route", func(t *testing.T) {
		// A valid token for THIS VM on a public route should have ctx forwarded.
		// The token is namespace-scoped to this VM and signed by a registered key,
		// so there is no cross-container risk.
		token := generateToken(t, ts, `{"ctx":{"role":"admin","secret":"data"}}`, "v0@"+box+"."+stage.Test().BoxHost)

		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", cgiURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		// ctx should be forwarded for a valid same-VM token.
		received := envMap["HTTP_X_EXEDEV_TOKEN_CTX"]
		if received == "" {
			t.Fatalf("expected X-ExeDev-Token-Ctx on public route with valid same-VM token, not found in: %s", body)
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(received), &parsed); err != nil {
			t.Fatalf("failed to parse token ctx: %v", err)
		}
		if parsed["role"] != "admin" {
			t.Errorf("expected role=admin, got %v", parsed["role"])
		}
		if parsed["secret"] != "data" {
			t.Errorf("expected secret=data, got %v", parsed["secret"])
		}
	})

	t.Run("wrong_vm_token_ctx_not_forwarded_on_public_route", func(t *testing.T) {
		// A token signed for a DIFFERENT VM should not forward ctx.
		// The token validation fails (wrong namespace), so authResult is nil.
		wrongVMToken := generateToken(t, ts, `{"ctx":{"role":"admin","secret":"data"}}`, "v0@othervmname."+stage.Test().BoxHost)

		client := noRedirectClient(nil)
		req, err := localhostRequestWithHostHeader("GET", cgiURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+wrongVMToken)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Public route should still succeed (200) even with an invalid token.
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200 on public route with wrong-VM token, got %d: %s", resp.StatusCode, body)
		}

		body, _ := io.ReadAll(resp.Body)
		envMap := parseCGIEnv(body)

		// No ctx should be forwarded.
		if received := envMap["HTTP_X_EXEDEV_TOKEN_CTX"]; received != "" {
			t.Errorf("expected no X-ExeDev-Token-Ctx for wrong-VM token on public route, got: %q", received)
		}

		// No user identity headers should be forwarded either.
		if received := envMap["HTTP_X_EXEDEV_USERID"]; received != "" {
			t.Errorf("expected no X-ExeDev-UserID for wrong-VM token on public route, got: %q", received)
		}
	})

	cleanupBox(t, keyFile, box)
}

// TestProxyConcurrentRequests verifies that the HTTP proxy can handle
// a burst of concurrent requests without returning errors.
// This reproduces the issue where tools like Vite's HMR that make many
// simultaneous requests would see failures (502s, connection resets)
// because each proxy request created a fresh SSH transport.
func TestProxyConcurrentRequests(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.servers.Exeprox.HTTPPort

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Create static files to serve: a small index and a larger JS bundle.
	setupCmd := boxSSHShell(t, box, keyFile, `
set -e
cd /home/exedev
echo hello-concurrent > index.html
dd if=/dev/urandom bs=1024 count=100 2>/dev/null | base64 > bundle.js
for i in 1 2 3 4; do echo "module $i" > "mod${i}.js"; done
`)
	setupCmd.Stdout = t.Output()
	setupCmd.Stderr = t.Output()
	if err := setupCmd.Run(); err != nil {
		t.Fatalf("failed to create test files: %v", err)
	}

	// Start a concurrent-capable HTTP server.
	// busybox httpd forks per request, so it handles concurrency well.
	startHTTPServer(t, box, keyFile, 8080)

	// Configure the proxy route as public so we don't need auth cookies.
	configureProxyRoute(t, keyFile, box, 8080, "public")

	// First, verify a single request works.
	resp, err := doProxyGet(t, box, httpPort, "/")
	if err != nil {
		t.Fatalf("single request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("single request returned status %d, body: %s", resp.StatusCode, body)
	}

	// Simulate a Vite-like page load: many concurrent requests for
	// different resources (JS bundles, modules, etc.).
	// Browsers cap at ~6 concurrent connections per host.
	paths := []string{"/", "/bundle.js", "/mod1.js", "/mod2.js", "/mod3.js", "/mod4.js"}

	// Run multiple rounds to stress the proxy.
	// Each round fires all paths concurrently (simulating a page load),
	// then waits for the round to finish before starting the next.
	// Higher concurrency (more paths per round, or launching all
	// rounds at once) causes SSH channel open timeouts on CI.
	const rounds = 5
	totalRequests := rounds * len(paths)

	type result struct {
		status int
		err    error
	}

	var (
		successes  int
		failures   int
		errCounts  = make(map[string]int)
		statCounts = make(map[int]int)
	)
	for round := range rounds {
		results := make(chan result, len(paths))
		for _, path := range paths {
			go func(round int, path string) {
				resp, err := doProxyGet(t, box, httpPort, path)
				if err != nil {
					results <- result{err: err}
					return
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				results <- result{status: resp.StatusCode}
			}(round, path)
		}
		for range len(paths) {
			r := <-results
			if r.err != nil {
				failures++
				errCounts[r.err.Error()]++
			} else if r.status == 200 {
				successes++
				statCounts[r.status]++
			} else {
				failures++
				statCounts[r.status]++
			}
		}
	}

	t.Logf("Results: %d/%d succeeded, %d failed", successes, totalRequests, failures)
	for status, count := range statCounts {
		if status != 200 {
			t.Logf("  HTTP %d: %d requests", status, count)
		}
	}
	for errMsg, count := range errCounts {
		t.Logf("  Error: %s (%d requests)", errMsg, count)
	}

	if failures > 0 {
		t.Errorf("%d/%d requests failed under concurrent load", failures, totalRequests)
	}

	cleanupBox(t, keyFile, box)
}

// doProxyGet makes a GET request through the proxy to the given box.
func doProxyGet(t *testing.T, boxName string, httpPort int, path string) (*http.Response, error) {
	t.Helper()
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req := makeProxyRequestWithPath(t, boxName, httpPort, path)
	return client.Do(req)
}

// TestProxyDebugForwarding verifies that /debug requests addressed to a VM
// are proxied to the VM and not intercepted by exeprox's own debug handler.
func TestProxyDebugForwarding(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)
	noGolden(t)

	httpPort := Env.servers.Exeprox.HTTPPort

	pty, _, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, testinfra.BoxOpts{Command: "/bin/bash"})
	pty.Disconnect()
	waitForSSH(t, box, keyFile)

	// Create a file at /debug so the VM's httpd serves it,
	// giving us a marker to distinguish from exeprox's debug page.
	mkDebug := boxSSHCommand(t, box, keyFile, "sh", "-c",
		"'echo vm-debug-marker > /home/exedev/debug'")
	if err := mkDebug.Run(); err != nil {
		t.Fatalf("failed to create debug file: %v", err)
	}

	serveIndex(t, box, keyFile, "alive")
	configureProxyRoute(t, keyFile, box, 8080, "public")

	// Wait for the proxy to be ready by polling /.
	var lastErr error
	for _, d := range []time.Duration{
		0, 100 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond,
		1 * time.Second, 1 * time.Second, 2 * time.Second, 2 * time.Second,
	} {
		time.Sleep(d)
		resp, err := doProxyRequest(t, box, httpPort)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "alive") {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("status %d, body %q", resp.StatusCode, body)
	}
	if lastErr != nil {
		t.Fatalf("proxy not ready: %v", lastErr)
	}

	// Request /debug through the proxy — must reach the VM, not exeprox.
	req := makeProxyRequestWithPath(t, box, httpPort, "/debug")
	resp, err := noRedirectClient(nil).Do(req)
	if err != nil {
		t.Fatalf("proxy /debug request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if strings.Contains(string(body), "exeprox debug") {
		t.Fatal("/debug was intercepted by exeprox instead of being proxied to the VM")
	}
	if !strings.Contains(string(body), "vm-debug-marker") {
		t.Fatalf("/debug response not from VM; status=%d body=%q", resp.StatusCode, body)
	}

	cleanupBox(t, keyFile, box)
}
