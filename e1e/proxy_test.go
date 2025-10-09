// This file contains tests for HTTP proxy functionality.

package e1e

import (
	"fmt"
	"io"
	"net"
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

func TestHTTPProxyForAlternateProxyPorts(t *testing.T) {
	vouch.For("philip")
	// Empirically, this test does not play well with others.
	// It would be really good to confirm that this does not reflect
	// a real bug, but for now, skip parallel execution.
	// t.Parallel()

	pty, cookies, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, BoxOpts{Command: "/bin/bash"})
	pty.disconnect()

	altPort := Env.exed.ExtraPorts[0]

	// You might want to use "busybox httpd" but Go's http client doesn't like it (gets an EOF),
	// so don't do that.
	p := boxSSHCommand(t, box, keyFile, "python3", "-m", "http.server", strconv.Itoa(altPort))
	if err := p.Start(); err != nil {
		t.Fatalf("failed to start python HTTP server: %v\n", err)
	}
	t.Cleanup(func() {
		p.Process.Kill()
		p.Wait()
	})

	waitCmd := boxSSHCommand(t, box, keyFile, "timeout", "20", "sh", "-c",
		fmt.Sprintf("while ! curl http://localhost:%d/; do sleep 0.5; done", altPort))
	if err := waitCmd.Start(); err != nil {
		t.Fatalf("failed to start wait command: %v\n", err)
	}
	waitCmd.Wait()

	proxyAssert(t, box, proxyExpectation{
		name:     "altport without auth redirects",
		httpPort: altPort,
		cookies:  nil,
		httpCode: http.StatusTemporaryRedirect,
	})
	proxyAssert(t, box, proxyExpectation{
		name:     "altport with auth succeeds",
		httpPort: altPort,
		cookies:  cookies,
		httpCode: http.StatusOK,
	})

	proxyAssert(t, box, proxyExpectation{
		name:     "other altport with auth fails",
		httpPort: Env.exed.ExtraPorts[1],
		cookies:  cookies,
		httpCode: http.StatusBadGateway,
	})
}

type proxyExpectation struct {
	name     string
	httpPort int
	cookies  []*http.Cookie
	httpCode int
}

func localhostRequestWithHostHeader(method string, urlS string, body io.Reader) (*http.Request, error) {
	url, err := url.Parse(urlS)
	if err != nil {
		return nil, err
	}
	// Split the host and port
	host, _, err := net.SplitHostPort(url.Host)
	if err != nil {
		return nil, err
	}

	if strings.HasSuffix(host, ".localhost") {
		originalUrlHost := url.Host
		url.Host = strings.Replace(url.Host, host, "localhost", 1)
		req, err := http.NewRequest(method, url.String(), body)
		if err != nil {
			return nil, err
		}
		req.Host = originalUrlHost
		return req, nil
	} else {
		return http.NewRequest(method, url.String(), body)
	}
}

func proxyAssert(t *testing.T, boxName string, exp proxyExpectation) {
	t.Helper()
	t.Logf("Testing proxy expectation: %s port %d expected http status %d", exp.name, exp.httpPort, exp.httpCode)
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

	// We put in a GET parameter here to ensure that all the redirects preserve the parameters.
	proxyURL := fmt.Sprintf("http://%s.localhost:%d/?foo=1", boxName, exp.httpPort)
	req, err := localhostRequestWithHostHeader("GET", proxyURL, nil)
	if err != nil {
		t.Errorf("failed to make http request: %v", err)
		return
	}
	req.Host = fmt.Sprintf("%s.localhost:%d", boxName, exp.httpPort)
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
		resp, err = client.Do(req)
		if err != nil {
			t.Errorf("failed to do http request: %v", err)
			return
		}
		// Now finally we should have a new cookie and should be back at the beginning!
		// Here we have to do a little bit of sleight of hand since the final redirect is /...
		// without an http://..., and Go doesn't do this with the foo.localhost stuff... So:
		location := resp.Header.Get("Location")
		if location == "" {
			t.Fatalf("failed to get redirect location: %v", err)
			return
		}
		origUrl, err := url.Parse(proxyURL)
		if err != nil {
			t.Fatalf("failed to parse original URL: %v", err)
			return
		}
		u, err = origUrl.Parse(location)
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
}

// TestHTTPProxyBasic tests basic HTTP proxy functionality with public and private routes
// TODO: This doesn't do the auth dance so it's only testing the public stuff right now.
func TestHTTPProxyBasic(t *testing.T) {
	vouch.For("philip")
	t.Parallel()

	pty, _, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.disconnect()

	// Start HTTP server on port 8080 in background (non-privileged port)
	pythonCmd := boxSSHCommand(t, boxName, keyFile, "python3", "-m", "http.server", "8080", ">", "/tmp/http_server.log", "2>&1")
	if err := pythonCmd.Start(); err != nil {
		t.Fatalf("failed to start python HTTP server: %v\n", err)
	}
	t.Cleanup(func() {
		pythonCmd.Process.Kill()
		pythonCmd.Wait()
	})

	// Now test proxy functionality
	// Get the HTTP port for the test server
	// TODO(josh): use the url returned by newBox, once it exists.
	httpPort := Env.exed.HTTPPort

	// Default route should be private (redirect to auth)
	{
		t.Log("Testing private route")
		resp, err := makeProxyRequest(t, boxName, httpPort)
		if err != nil {
			t.Fatalf("failed to make proxy request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("expected redirect for private route, got status %d", resp.StatusCode)
		}
	}

	// Make route public via SSH command interface
	{
		t.Log("Making route public")
		exeShell := sshToExeDev(t, keyFile)
		exeShell.sendLine(fmt.Sprintf("proxy %s --port=8080 --public", boxName))
		exeShell.want("Route updated successfully")
		exeShell.wantPrompt()

		// Verify route is public
		exeShell.sendLine(fmt.Sprintf("proxy %s", boxName))
		exeShell.want("Port: 8080")
		exeShell.want("Share: public")
		exeShell.wantPrompt()
		exeShell.disconnect()
	}

	// Public route should allow direct access (with retry logic)
	{
		t.Log("Testing public route")
		// The HTTP server takes a while to start up, so we retry a few times.
		sleepTimes := []time.Duration{
			0, 100 * time.Millisecond,
			200 * time.Millisecond, 300 * time.Millisecond, 500 * time.Millisecond, 1 * time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second,
			8 * time.Second, 16 * time.Second,
		}
		var resp *http.Response
		var err error
		var body []byte
		for i, sleepTime := range sleepTimes {
			if sleepTime > 0 {
				t.Logf("Retrying after %v...", sleepTime)
				time.Sleep(sleepTime)
			}
			resp, err = makeProxyRequest(t, boxName, httpPort)
			if err != nil {
				t.Logf("Attempt %d: failed to make proxy request: %v", i+1, err)
				continue
			}
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Logf("Attempt %d: failed to read response body: %v", i+1, err)
				continue
			}
			if resp.StatusCode == http.StatusOK &&
				(strings.Contains(string(body), "Directory listing") || strings.Contains(string(body), "Index of")) {
				// Success
				break
			}
			t.Logf("Attempt %d: unexpected status %d or body", i+1, resp.StatusCode)
		}
		if resp == nil || err != nil {
			t.Fatalf("failed to make proxy request after retries: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for public route, got status %d", resp.StatusCode)
		}
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "Directory listing") && !strings.Contains(bodyStr, "Index of") {
			t.Errorf("did not get expected directory listing from Python HTTP server, got:\n%s", bodyStr)
		}
	}
}

// makeProxyRequest makes an HTTP request to boxName, available at httpPort.
func makeProxyRequest(t *testing.T, boxName string, httpPort int) (*http.Response, error) {
	t.Helper()
	client := &http.Client{}
	// don't follow redirects
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	req, err := http.NewRequest("GET", proxyURL, nil)
	if err != nil {
		return nil, err
	}
	req.Host = fmt.Sprintf("%s.localhost:%d", boxName, httpPort)
	return client.Do(req)
}
