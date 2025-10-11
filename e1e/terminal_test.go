package e1e

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"exe.dev/vouch"
)

func TestTerminalPermissions(t *testing.T) {
	e1eTestsOnlyRunOnce(t)
	vouch.For("philip")
	t.Parallel()

	// Create user and box
	pty, cookies, _, _ := registerForExeDev(t)
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

	// Test 2b: Terminal favicon is served for authenticated user
	t.Run("favicon_served", func(t *testing.T) {
		client := createAuthenticatedTerminalClient(t, box, cookies)
		faviconURL := fmt.Sprintf("http://%s.xterm.localhost:%d/favicon.ico", box, Env.exed.HTTPPort)
		req, err := localhostRequestWithHostHeader("GET", faviconURL, nil)
		if err != nil {
			t.Fatalf("failed to make http request: %v", err)
		}
		req.Host = fmt.Sprintf("%s.xterm.localhost:%d", box, Env.exed.HTTPPort)

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
		var client *http.Client
		var outputChan chan string
		var errChan chan error
		var readyChan chan bool

		retryTimeout := time.After(15 * time.Second)
		retryTicker := time.NewTicker(100 * time.Millisecond)
		defer retryTicker.Stop()

		connected := false
		for !connected {
			select {
			case <-retryTimeout:
				t.Fatal("timeout waiting for box SSH to be ready")
			case <-retryTicker.C:
				// Create authenticated client for terminal subdomain
				client = createAuthenticatedTerminalClient(t, box, cookies)

				// Start listening to terminal events in a goroutine
				outputChan = make(chan string, 100)
				errChan = make(chan error, 1)
				readyChan = make(chan bool, 1)
				go streamTerminalEventsWithClient(t, box, client, outputChan, errChan, readyChan)

				// Wait for the terminal connection to be established
				select {
				case <-readyChan:
					// Terminal is ready
					connected = true
				case err := <-errChan:
					t.Logf("failed to connect to terminal, retrying: %v", err)
					time.Sleep(200 * time.Millisecond)
				case <-time.After(500 * time.Millisecond):
					t.Logf("terminal connection attempt timed out, retrying")
				}
			}
		}

		// Send a command
		sendTerminalInputWithClient(t, box, client, "echo hello\n")

		// Wait for output
		timeout := time.After(5 * time.Second)
		var foundHello bool
		for !foundHello {
			select {
			case output := <-outputChan:
				if strings.Contains(output, "hello") {
					foundHello = true
				}
			case err := <-errChan:
				t.Fatalf("error reading terminal events: %v", err)
			case <-timeout:
				t.Fatal("timeout waiting for 'hello' in terminal output")
			}
		}
	})
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
		ur, err := url.Parse(u)
		if err != nil {
			t.Fatalf("failed to parse URL %q: %v", u, err)
		}
		jar.SetCookies(ur, slices.Clone(cookies))
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	terminalURL := fmt.Sprintf("http://%s.xterm.localhost:%d/", boxName, Env.exed.HTTPPort)
	req, err := localhostRequestWithHostHeader("GET", terminalURL, nil)
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.xterm.localhost:%d", boxName, Env.exed.HTTPPort)
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
	ur, err := url.Parse(u)
	if err != nil {
		t.Fatalf("failed to parse URL %q: %v", u, err)
	}
	jar.SetCookies(ur, slices.Clone(cookies))

	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	terminalURL := fmt.Sprintf("http://%s.xterm.localhost:%d/", boxName, Env.exed.HTTPPort)
	req, err := localhostRequestWithHostHeader("GET", terminalURL, nil)
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.xterm.localhost:%d", boxName, Env.exed.HTTPPort)
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

		// Auto-confirm by adding action=confirm
		q := location.Query()
		q.Set("action", "confirm")
		location.RawQuery = q.Encode()

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
	parsedMainURL, _ := url.Parse(mainURL)
	jar.SetCookies(parsedMainURL, baseCookies)

	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Start with terminal page request
	terminalURL := fmt.Sprintf("http://%s.xterm.localhost:%d/", boxName, Env.exed.HTTPPort)
	req, _ := localhostRequestWithHostHeader("GET", terminalURL, nil)
	req.Host = fmt.Sprintf("%s.xterm.localhost:%d", boxName, Env.exed.HTTPPort)

	// Follow the redirect chain
	for i := 0; i < 10; i++ {
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

// streamTerminalEventsWithClient connects to the SSE endpoint and streams terminal output
func streamTerminalEventsWithClient(t *testing.T, boxName string, client *http.Client, outputChan chan string, errChan chan error, readyChan chan bool) {
	t.Helper()

	// Use a random terminal ID
	terminalID := "test-terminal-1"
	eventsURL := fmt.Sprintf("http://%s.xterm.localhost:%d/terminal/events/%s", boxName, Env.exed.HTTPPort, terminalID)

	req, err := localhostRequestWithHostHeader("GET", eventsURL, nil)
	if err != nil {
		errChan <- err
		return
	}
	req.Host = fmt.Sprintf("%s.xterm.localhost:%d", boxName, Env.exed.HTTPPort)

	resp, err := client.Do(req)
	if err != nil {
		errChan <- err
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errChan <- fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
		return
	}

	// Signal that we're connected
	readyChan <- true

	// Read SSE events
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			// Decode base64
			decoded, err := base64.StdEncoding.DecodeString(data)
			if err != nil {
				continue
			}
			outputChan <- string(decoded)
		}
	}

	if err := scanner.Err(); err != nil {
		errChan <- err
	}
}

// sendTerminalInputWithClient sends input to the terminal
func sendTerminalInputWithClient(t *testing.T, boxName string, client *http.Client, input string) {
	t.Helper()

	// Use the same terminal ID
	terminalID := "test-terminal-1"
	inputURL := fmt.Sprintf("http://%s.xterm.localhost:%d/terminal/input/%s", boxName, Env.exed.HTTPPort, terminalID)

	req, err := localhostRequestWithHostHeader("POST", inputURL, bytes.NewBufferString(input))
	if err != nil {
		t.Fatalf("failed to make http request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.xterm.localhost:%d", boxName, Env.exed.HTTPPort)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to send terminal input: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected status %d when sending input: %s", resp.StatusCode, string(body))
	}
}
