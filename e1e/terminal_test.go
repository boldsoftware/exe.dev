package e1e

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"exe.dev/vouch"
)

func TestTerminalPermissions(t *testing.T) {
	t.Skip("skipping CI - looks flakey (https://github.com/boldsoftware/exe/issues/87)")
	e1eTestsOnlyRunOnce(t)
	vouch.For("philip")
	t.Parallel()
	noGolden(t)

	// Create user and box
	pty, cookies, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, BoxOpts{Command: "/bin/bash"})
	pty.disconnect()

	// Test 1: No authentication - should redirect to login
	t.Run("no_auth_redirects_to_login", func(t *testing.T) {
		resp, body := terminalRequest(t, box, nil)
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("expected redirect (307), got %d", resp.StatusCode)
		}
		location := resp.Header.Get("Location")
		if !strings.Contains(location, "/auth?") {
			t.Fatalf("expected redirect to /auth, got %s", location)
		}
		if !strings.Contains(location, "redirect=") {
			t.Fatalf("expected redirect URL to include return URL, got %s", location)
		}
		_ = body
	})

	// Test 2: Owner authenticated - should see their terminal page
	t.Run("owner_can_access", func(t *testing.T) {
		resp, body := terminalRequestWithAuth(t, box, cookies)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d: %s", resp.StatusCode, body)
		}
		if !strings.Contains(body, "terminal") {
			t.Fatalf("expected terminal page, got: %s", body)
		}
	})

	var client *http.Client
	t.Run("auth", func(t *testing.T) {
		client = createAuthenticatedTerminalClient(t, box, cookies)
	})

	// Test 2b: Terminal favicon is served for authenticated user
	t.Run("favicon_served", func(t *testing.T) {
		faviconURL := fmt.Sprintf("http://%s.xterm.exe.cloud:%d/favicon.ico", box, Env.exed.HTTPPort)
		req, err := localhostRequestWithHostHeader("GET", faviconURL, nil)
		if err != nil {
			t.Fatalf("failed to make http request: %v", err)
		}
		req.Host = fmt.Sprintf("%s.xterm.exe.cloud:%d", box, Env.exed.HTTPPort)

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to request favicon: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200 for favicon.ico, got %d: %s", resp.StatusCode, string(b))
		}

		ct := resp.Header.Get("Content-Type")
		if ct == "" || !strings.HasPrefix(ct, "image/") {
			t.Fatalf("expected image/* content type for favicon, got %q", ct)
		}
	})

	// Test 3: Non-existent box - should see access denied
	t.Run("nonexistent_box_shows_access_denied", func(t *testing.T) {
		resp, body := terminalRequestWithAuth(t, "nonexistent", cookies)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK for access denied page, got %d: %s", resp.StatusCode, body)
		}
		if !strings.Contains(body, "Access denied") {
			t.Fatalf("expected 'Access denied' in response, got: %s", body)
		}
		if !strings.Contains(body, "nonexistent") {
			t.Fatalf("expected box name 'nonexistent' in response, got: %s", body)
		}
		if !strings.Contains(body, "/~") {
			t.Fatalf("expected link to dashboard in response, got: %s", body)
		}
	})

	// Test 4: Terminal functionality - send command and receive output
	t.Run("terminal_send_and_receive", func(t *testing.T) {
		// Retry connecting to the terminal until successful or timeout
		var conn *websocket.Conn
		var ctx context.Context
		var cancel context.CancelFunc

		retryTimeout := time.After(time.Minute)
		retryTicker := time.NewTicker(500 * time.Millisecond)
		defer retryTicker.Stop()

		connected := false
		var lastErr error
		for !connected {
			select {
			case <-retryTimeout:
				t.Fatalf("timeout waiting for box SSH to be ready, last error: %v", lastErr)
			case <-retryTicker.C:
				// Try to connect and test if it works by sending a command
				testCtx, testCancel := context.WithTimeout(context.Background(), 3*time.Second)
				testConn, err := connectTerminalWebSocket(t, box, client)
				if err != nil {
					lastErr = err
					testCancel()
					continue
				}

				// Try to actually use the connection
				testOutputChan := make(chan string, 10)
				testErrChan := make(chan error, 1)
				go func() {
					for {
						var msg map[string]interface{}
						err := wsjson.Read(testCtx, testConn, &msg)
						if err != nil {
							if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
								testErrChan <- err
							}
							return
						}
						if msgType, ok := msg["type"].(string); ok && msgType == "output" {
							if dataStr, ok := msg["data"].(string); ok {
								decoded, _ := base64.StdEncoding.DecodeString(dataStr)
								testOutputChan <- string(decoded)
							}
						}
					}
				}()

				// Send a test command
				wsjson.Write(testCtx, testConn, map[string]interface{}{"type": "resize", "cols": 80, "rows": 24})
				wsjson.Write(testCtx, testConn, map[string]interface{}{"type": "input", "data": "echo test\n"})

				// Wait for output or error
				select {
				case <-testOutputChan:
					// Success! We got output
					testConn.Close(websocket.StatusNormalClosure, "")
					testCancel()
					connected = true
				case err := <-testErrChan:
					// Connection failed
					lastErr = fmt.Errorf("terminal connection test failed: %w", err)
					testConn.Close(websocket.StatusNormalClosure, "")
					testCancel()
				case <-testCtx.Done():
					// Timeout
					lastErr = fmt.Errorf("terminal connection test timed out")
					testConn.Close(websocket.StatusNormalClosure, "")
					testCancel()
				}
			}
		}

		// Now create the actual connection for the test
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
		var err error
		conn, err = connectTerminalWebSocket(t, box, client)
		if err != nil {
			t.Fatalf("failed to connect after successful test: %v", err)
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		// Start reading output in background
		outputChan := make(chan string, 100)
		errChan := make(chan error, 1)
		go func() {
			for {
				var msg map[string]interface{}
				err := wsjson.Read(ctx, conn, &msg)
				if err != nil {
					if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
						errChan <- err
					}
					return
				}

				if msgType, ok := msg["type"].(string); ok && msgType == "output" {
					if dataStr, ok := msg["data"].(string); ok {
						decoded, err := base64.StdEncoding.DecodeString(dataStr)
						if err == nil {
							outputChan <- string(decoded)
						}
					}
				}
			}
		}()

		// Send initial resize message
		if err := wsjson.Write(ctx, conn, map[string]interface{}{
			"type": "resize",
			"cols": 80,
			"rows": 24,
		}); err != nil {
			t.Fatalf("failed to send resize: %v", err)
		}

		// Send a command and wait for its output
		if err := wsjson.Write(ctx, conn, map[string]interface{}{
			"type": "input",
			"data": "echo hello\n",
		}); err != nil {
			t.Fatalf("failed to send input: %v", err)
		}

		// Wait for the "hello" output
		timeout := time.After(10 * time.Second)
		var foundHello bool
		for !foundHello {
			select {
			case output := <-outputChan:
				if strings.Contains(output, "hello") {
					foundHello = true
				}
			case err := <-errChan:
				t.Fatalf("error reading terminal output: %v", err)
			case <-timeout:
				t.Fatal("timeout waiting for 'hello' in terminal output")
			}
		}
	})

	// Cleanup
	pty = sshToExeDev(t, keyFile)
	pty.deleteBox(box)
	pty.disconnect()
}

// terminalRequest makes a request to the terminal page without authentication
func terminalRequest(t *testing.T, boxName string, cookies []*http.Cookie) (*http.Response, string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	if cookies != nil {
		u := fmt.Sprintf("http://localhost:%d", Env.exed.HTTPPort)
		setCookiesForJar(t, jar, u, cookies)
	}
	client := noRedirectClient(jar)

	terminalURL := fmt.Sprintf("http://%s.xterm.exe.cloud:%d/", boxName, Env.exed.HTTPPort)
	req, err := localhostRequestWithHostHeader("GET", terminalURL, nil)
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.xterm.exe.cloud:%d", boxName, Env.exed.HTTPPort)
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
	u := fmt.Sprintf("http://localhost:%d", Env.exed.HTTPPort)
	setCookiesForJar(t, jar, u, cookies)

	client := noRedirectClient(jar)

	terminalURL := fmt.Sprintf("http://%s.xterm.exe.cloud:%d/", boxName, Env.exed.HTTPPort)
	req, err := localhostRequestWithHostHeader("GET", terminalURL, nil)
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.xterm.exe.cloud:%d", boxName, Env.exed.HTTPPort)
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
	mainURL := fmt.Sprintf("http://localhost:%d", Env.exed.HTTPPort)
	setCookiesForJar(t, jar, mainURL, baseCookies)

	client := noRedirectClient(jar)

	// Start with terminal page request
	terminalURL := fmt.Sprintf("http://%s.xterm.exe.cloud:%d/", boxName, Env.exed.HTTPPort)
	req, _ := localhostRequestWithHostHeader("GET", terminalURL, nil)
	req.Host = fmt.Sprintf("%s.xterm.exe.cloud:%d", boxName, Env.exed.HTTPPort)

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

		if resp.StatusCode != http.StatusTemporaryRedirect {
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

// connectTerminalWebSocket connects to the terminal WebSocket endpoint
func connectTerminalWebSocket(t *testing.T, boxName string, client *http.Client) (*websocket.Conn, error) {
	t.Helper()

	if client == nil {
		return nil, fmt.Errorf("client is nil")
	}

	// Use a session name
	sessionName := "test-session"
	terminalID := "test-terminal-1"
	// Use localhost in the URL but set the Host header to the subdomain
	wsURL := fmt.Sprintf("ws://localhost:%d/terminal/ws/%s?name=%s", Env.exed.HTTPPort, terminalID, sessionName)
	originalHost := fmt.Sprintf("%s.xterm.exe.cloud:%d", boxName, Env.exed.HTTPPort)

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
