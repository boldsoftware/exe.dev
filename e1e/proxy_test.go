// This file contains tests for HTTP proxy functionality.

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

	"exe.dev/vouch"
)

// TestHTTPProxyBasic tests basic HTTP proxy functionality with public and private routes
func TestHTTPProxyBasic(t *testing.T) {
	vouch.For("philip")
	t.Parallel()

	pty, cookies, keyFile, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	pty.disconnect()

	// Start HTTP server on port 80 in background
	pythonCmd := boxSSHCommand(t, boxName, keyFile, "sudo", "python3", "-m", "http.server", "80", ">", "/tmp/http_server.log", "2>&1")
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
		t.Log("Testing private route without auth")
		resp, err := makeProxyRequest(t, boxName, httpPort)
		if err != nil {
			t.Fatalf("failed to make proxy request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Errorf("expected redirect for private route, got status %d", resp.StatusCode)
		}
	}

	// Private route should be accessible with proxy authentication cookies
	{
		t.Log("Getting proxy auth cookies for private route access")
		proxyCookies := getProxyAuthCookies(t, boxName, cookies)

		t.Log("Testing private route with proxy auth cookies")
		// It's unclear why to me, but the HTTP server take a while, so we retry a few times.
		sleepTimes := []time.Duration{0 * time.Second, 100 * time.Millisecond, 200 * time.Millisecond, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
		var resp *http.Response
		var err error
		var body []byte
		for i, sleepTime := range sleepTimes {
			if sleepTime > 0 {
				t.Logf("Retrying after %v...", sleepTime)
				time.Sleep(sleepTime)
			}
			resp, err = makeProxyRequest(t, boxName, httpPort, proxyCookies)
			if err != nil {
				t.Logf("Attempt %d: failed to make authenticated proxy request: %v", i+1, err)
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
			t.Fatalf("failed to make authenticated proxy request after retries: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 OK for private route with auth, got status %d", resp.StatusCode)
		}
		bodyStr := string(body)
		if !strings.Contains(bodyStr, "Directory listing") && !strings.Contains(bodyStr, "Index of") {
			t.Errorf("did not get expected directory listing from Python HTTP server, got:\n%s", bodyStr)
		}
	}

	// Make route public via SSH command interface
	{
		t.Log("Making route public")
		exeShell := sshToExeDev(t, keyFile)
		exeShell.sendLine(fmt.Sprintf("route %s --port=80 --public", boxName))
		exeShell.want("Route updated successfully")
		exeShell.wantPrompt()

		// Verify route is public
		exeShell.sendLine(fmt.Sprintf("route %s", boxName))
		exeShell.want("Port: 80")
		exeShell.want("Share: public")
		exeShell.wantPrompt()
		exeShell.disconnect()
	}

	// Public route should allow direct access (with retry logic)
	{
		t.Log("Testing public route")
		// It's unclear why to me, but the HTTP server take a while, so we retry a few times.
		sleepTimes := []time.Duration{0 * time.Second, 100 * time.Millisecond, 200 * time.Millisecond, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
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
// If cookies is provided and non-empty, they will be used for authentication.
func makeProxyRequest(t *testing.T, boxName string, httpPort int, cookies ...[]*http.Cookie) (*http.Response, error) {
	t.Helper()

	// Always create a cookie jar and add any cookies we have
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %v", err)
	}

	// Add all provided cookies
	if len(cookies) > 0 {
		proxyURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
		parsedURL, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("failed to parse proxy URL: %v", err)
		}
		jar.SetCookies(parsedURL, cookies[0])
	}

	client := &http.Client{Jar: jar}

	// Don't follow redirects
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	req, err := http.NewRequest("GET", proxyURL, nil)
	if err != nil {
		return nil, err
	}
	req.Host = fmt.Sprintf("%s.localhost", boxName)
	return client.Do(req)
}
