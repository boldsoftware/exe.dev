// Helper functions for terminal websocket tests.
// The actual tests are in box_management_test.go (xterm_websocket subtest of TestVanillaBox).
package e1e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

// terminalRequest makes a request to the terminal page without authentication
func terminalRequest(t *testing.T, boxName string, cookies []*http.Cookie) (*http.Response, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	if cookies != nil {
		u := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
		setCookiesForJar(t, jar, u, cookies)
	}
	client := noRedirectClient(jar)

	terminalURL := fmt.Sprintf("http://%s.xterm.exe.cloud:%d/", boxName, Env.servers.Exed.HTTPPort)
	req, err := localhostRequestWithHostHeader("GET", terminalURL, nil)
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.xterm.exe.cloud:%d", boxName, Env.servers.Exed.HTTPPort)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to do http request: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return resp, string(body)
}

// terminalRequestWithAuth makes a request to the terminal page with authentication
// and follows the auth dance if needed
func terminalRequestWithAuth(t *testing.T, boxName string, cookies []*http.Cookie) (*http.Response, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	// Set cookies for the main domain
	u := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
	setCookiesForJar(t, jar, u, cookies)

	client := noRedirectClient(jar)

	terminalURL := fmt.Sprintf("http://%s.xterm.exe.cloud:%d/", boxName, Env.servers.Exed.HTTPPort)
	req, err := localhostRequestWithHostHeader("GET", terminalURL, nil)
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.xterm.exe.cloud:%d", boxName, Env.servers.Exed.HTTPPort)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to do http request: %v", err)
	}

	// If we get a redirect, follow the auth dance
	if resp.StatusCode == http.StatusTemporaryRedirect {
		location, err := resp.Location()
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
		}
		t.Logf("Following auth redirect to %s", location.String())

		// First redirect to auth page on main domain
		req, err = http.NewRequest("GET", location.String(), nil)
		if err != nil {
			t.Fatalf("failed to make http request: %v", err)
		}
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("failed to do http request: %v", err)
		}

		if resp.StatusCode != http.StatusTemporaryRedirect {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected redirect during auth dance, got status %d: %s", resp.StatusCode, string(body))
		}

		// Second redirect to confirm page
		location, err = resp.Location()
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
		}
		t.Logf("Following confirm redirect to %s", location.String())

		// Follow the /auth/confirm redirect - it redirects directly for users with access
		req, err = localhostRequestWithHostHeader("GET", location.String(), nil)
		if err != nil {
			t.Fatalf("failed to make http request: %v", err)
		}
		req.Host = location.Host
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("failed to do http request: %v", err)
		}

		if resp.StatusCode != http.StatusTemporaryRedirect {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected redirect after confirm, got status %d: %s", resp.StatusCode, string(body))
		}

		// Final redirect back to terminal page with magic auth
		location, err = resp.Location()
		if err != nil {
			t.Fatalf("failed to get redirect location: %v", err)
		}
		t.Logf("Following magic auth redirect to %s", location.String())

		req, err = localhostRequestWithHostHeader("GET", location.String(), nil)
		if err != nil {
			t.Fatalf("failed to make http request: %v", err)
		}
		// Parse the Host from the location URL
		req.Host = location.Host
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("failed to do http request: %v", err)
		}

		// After magic auth, should redirect back to original page
		if resp.StatusCode != http.StatusTemporaryRedirect {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected redirect after magic auth, got status %d: %s", resp.StatusCode, string(body))
		}

		locationHeader := resp.Header.Get("Location")
		if locationHeader == "" {
			t.Fatalf("no location header in redirect")
		}

		// Resolve relative URL against the previous location
		finalURL, err := location.Parse(locationHeader)
		if err != nil {
			t.Fatalf("failed to parse final location: %v", err)
		}
		t.Logf("Following final redirect to %s", finalURL.String())

		req, err = localhostRequestWithHostHeader("GET", finalURL.String(), nil)
		if err != nil {
			t.Fatalf("failed to make http request: %v", err)
		}
		req.Host = finalURL.Host
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("failed to do http request: %v", err)
		}
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	return resp, string(body)
}

// createAuthenticatedTerminalClient creates an HTTP client with cookies authenticated for the terminal subdomain
func createAuthenticatedTerminalClient(t *testing.T, boxName string, baseCookies []*http.Cookie) *http.Client {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}

	// Set base cookies for main domain
	mainURL := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
	setCookiesForJar(t, jar, mainURL, baseCookies)

	client := noRedirectClient(jar)

	// Start with terminal page request
	terminalURL := fmt.Sprintf("http://%s.xterm.exe.cloud:%d/", boxName, Env.servers.Exed.HTTPPort)
	req, _ := localhostRequestWithHostHeader("GET", terminalURL, nil)
	req.Host = fmt.Sprintf("%s.xterm.exe.cloud:%d", boxName, Env.servers.Exed.HTTPPort)

	// Follow the redirect chain
	for range 10 {
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("auth dance request failed: %v", err)
		}

		if resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}

		if resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusSeeOther {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("unexpected status %d in auth dance: %s", resp.StatusCode, string(body))
		}

		location, err := resp.Location()
		resp.Body.Close()
		if err != nil {
			t.Fatalf("failed to get location: %v", err)
		}

		// Auto-confirm if this is the confirm page
		if strings.Contains(location.String(), "/auth/confirm") {
			q := location.Query()
			q.Set("action", "confirm")
			location.RawQuery = q.Encode()
		}

		req, _ = localhostRequestWithHostHeader("GET", location.String(), nil)
		req.Host = location.Host
	}

	// Return the client with the jar that has all the cookies
	return client
}

// connectTerminalWebSocket connects to the terminal WebSocket endpoint with an optional working directory
func connectTerminalWebSocket(t *testing.T, boxName string, client *http.Client, workingDir string) (*websocket.Conn, error) {
	t.Helper()

	if client == nil {
		return nil, fmt.Errorf("client is nil")
	}

	// Use a session name
	sessionName := "test-session"
	terminalID := "test-terminal-1"
	// Use localhost in the URL but set the Host header to the subdomain
	wsURL := fmt.Sprintf("ws://localhost:%d/terminal/ws/%s?name=%s", Env.servers.Exed.HTTPPort, terminalID, sessionName)
	if workingDir != "" {
		wsURL += "&d=" + url.QueryEscape(workingDir)
	}
	originalHost := fmt.Sprintf("%s.xterm.exe.cloud:%d", boxName, Env.servers.Exed.HTTPPort)

	// Create context for the WebSocket connection
	ctx := context.Background()

	// Create a custom dialer that uses our authenticated client's cookies
	dialer := websocket.DialOptions{
		HTTPClient: client,
		Host:       originalHost,
		HTTPHeader: http.Header{},
	}

	// Copy cookies from the jar to the request
	if jar := client.Jar; jar != nil {
		cookieURL := fmt.Sprintf("http://%s", originalHost)
		parsedURL, _ := url.Parse(cookieURL)
		for _, cookie := range jar.Cookies(parsedURL) {
			dialer.HTTPHeader.Add("Cookie", fmt.Sprintf("%s=%s", cookie.Name, cookie.Value))
		}
	}

	conn, _, err := websocket.Dial(ctx, wsURL, &dialer)
	if err != nil {
		return nil, fmt.Errorf("failed to dial websocket: %w", err)
	}

	return conn, nil
}

// connectShellWebSocket connects to the exe.dev /shell/ws WebSocket endpoint
func connectShellWebSocket(t *testing.T, cookies []*http.Cookie) (*websocket.Conn, error) {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	// Set cookies for the main exe.dev domain
	mainURL := fmt.Sprintf("http://localhost:%d", Env.servers.Exed.HTTPPort)
	setCookiesForJar(t, jar, mainURL, cookies)

	client := noRedirectClient(jar)

	// Connect to /shell/ws on the main domain (WebHost, not BoxHost)
	wsURL := fmt.Sprintf("ws://localhost:%d/shell/ws", Env.servers.Exed.HTTPPort)
	originalHost := fmt.Sprintf("localhost:%d", Env.servers.Exed.HTTPPort)

	ctx := context.Background()

	dialer := websocket.DialOptions{
		HTTPClient: client,
		Host:       originalHost,
		HTTPHeader: http.Header{},
	}

	// Copy cookies from jar to request
	if jar := client.Jar; jar != nil {
		parsedURL, _ := url.Parse(mainURL)
		for _, cookie := range jar.Cookies(parsedURL) {
			dialer.HTTPHeader.Add("Cookie", fmt.Sprintf("%s=%s", cookie.Name, cookie.Value))
		}
	}

	conn, _, err := websocket.Dial(ctx, wsURL, &dialer)
	if err != nil {
		return nil, fmt.Errorf("failed to dial shell websocket: %w", err)
	}

	return conn, nil
}
