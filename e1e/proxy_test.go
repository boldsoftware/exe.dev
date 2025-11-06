// This file contains tests for HTTP proxy functionality.

package e1e

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"exe.dev/vouch"
)

func TestHTTPProxy(t *testing.T) {
	vouch.For("philip")
	t.Parallel()
	e1eTestsOnlyRunOnce(t)

	pty, cookies, keyFile, _ := registerForExeDev(t)
	box := newBox(t, pty, BoxOpts{Command: "/bin/bash"})
	pty.disconnect()

	// Make an index.html file to serve.
	makeIndex := boxSSHCommand(t, box, keyFile, "echo", "alive", ">", "/home/exedev/index.html")
	if err := makeIndex.Run(); err != nil {
		t.Fatalf("failed to create index.html: %v\n", err)
	}

	serveHTTP := func(t *testing.T, port int) {
		t.Helper()

		httpdCmd := boxSSHCommand(t, box, keyFile, "busybox", "httpd", "-f", "-p", strconv.Itoa(port), "-h", "/home/exedev")
		httpdCmd.Stdout = t.Output()
		httpdCmd.Stderr = t.Output()
		if err := httpdCmd.Start(); err != nil {
			t.Fatalf("failed to start busybox HTTP server: %v\n", err)
		}
		t.Cleanup(func() {
			httpdCmd.Process.Kill()
			httpdCmd.Wait()
		})

		// TODO: arguably we shouldn't do this waiting.
		// Instead, exed should be responsible for handling it for us.
		// Early versions of this test accidentally did just that,
		// and it was slow and flaky...but best would be to fix _that_
		// and completely remove this stanza.
		waitCmd := boxSSHCommand(t, box, keyFile, "timeout", "20", "sh", "-c",
			fmt.Sprintf("'while ! curl -s http://localhost:%d/; do sleep 0.5; done'", port))
		waitCmd.Stdout = t.Output()
		waitCmd.Stderr = t.Output()
		if err := waitCmd.Run(); err != nil {
			t.Fatalf("failed http server unavailable: %v\n", err)
		}
	}

	t.Run("default_port", func(t *testing.T) {
		serveHTTP(t, 8080)
		httpPort := Env.exed.HTTPPort

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
			exeShell.sendLine(fmt.Sprintf("proxy %s --port=8080 --public", box))
			exeShell.want("Route updated successfully")
			exeShell.wantPrompt()

			exeShell.sendLine(fmt.Sprintf("proxy %s", box))
			exeShell.want("Port: 8080")
			exeShell.want("Share: public")
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
	})

	t.Run("auth_confirm_owner_skip", func(t *testing.T) {
		altPort := Env.exed.ExtraPorts[0]
		serveHTTP(t, altPort)

		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatalf("failed to create cookie jar: %v", err)
		}
		setCookiesForJar(t, jar, fmt.Sprintf("http://localhost:%d", altPort), cookies)

		client := noRedirectClient(jar)
		proxyURL := fmt.Sprintf("http://%s.localhost:%d/", box, altPort)

		req, err := localhostRequestWithHostHeader(http.MethodGet, proxyURL, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to do request: %v", err)
		}

		redirectCount := 0
		for resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusSeeOther {
			if redirectCount > 10 {
				resp.Body.Close()
				t.Fatalf("too many redirects")
			}

			location, err := resp.Location()
			if err != nil {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				t.Fatalf("failed to get redirect location: %v (status %d, body %s)", err, resp.StatusCode, body)
			}
			resp.Body.Close()

			req, err = localhostRequestWithHostHeader(http.MethodGet, location.String(), nil)
			if err != nil {
				t.Fatalf("failed to create redirect request: %v", err)
			}

			isConfirm := strings.Contains(location.Path, "/auth/confirm")
			resp, err = client.Do(req)
			if err != nil {
				t.Fatalf("failed to follow redirect: %v", err)
			}
			if isConfirm && resp.StatusCode == http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				t.Fatalf("owner flow should skip confirmation page, but confirmation page rendered: %s", body)
			}

			redirectCount++
		}

		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read final response body: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected final status 200, got %d. Body: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "alive") {
			t.Fatalf("expected final body to contain 'alive', got %s", body)
		}
	})

	t.Run("basic_auth", func(t *testing.T) {
		httpPort := Env.exed.HTTPPort
		serveHTTP(t, 8080)

		exeShell := sshToExeDev(t, keyFile)
		exeShell.sendLine(fmt.Sprintf("proxy %s --port=8080 --private", box))
		exeShell.want("Route updated successfully")
		exeShell.wantPrompt()
		exeShell.disconnect()

		resp, err := doProxyRequest(t, box, httpPort)
		if err != nil {
			t.Fatalf("failed to make proxy request without auth: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("expected redirect for private route, got status %d", resp.StatusCode)
		}

		type proxyTokenOutput struct {
			BoxName string `json:"box_name"`
			Token   string `json:"token"`
		}
		tokenResp := runParseExeDevJSON[proxyTokenOutput](t, keyFile, "proxy-token", box, "--json")
		token := tokenResp.Token
		if token == "" {
			t.Fatal("proxy bearer token output empty")
		}

		client := noRedirectClient(nil)
		req := makeProxyRequest(t, box, httpPort)
		setBasicAuthUserinfo(req, "bad-token")
		invalidResp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request with invalid basic auth: %v", err)
		}
		invalidResp.Body.Close()
		if invalidResp.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("expected HTTP 307 for invalid basic auth, got %d", invalidResp.StatusCode)
		}

		req = makeProxyRequest(t, box, httpPort)
		setBasicAuthUserinfo(req, token)
		authResp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request with basic auth: %v", err)
		}
		defer authResp.Body.Close()
		body, err := io.ReadAll(authResp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}
		if authResp.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTP 200 from basic auth request, got %d (body: %s)", authResp.StatusCode, body)
		}
		if !strings.Contains(string(body), "alive") {
			t.Fatalf("expected body to contain 'alive', got %s", body)
		}

		req = makeProxyRequest(t, box, httpPort)
		setBasicAuthHeader(req, "bad-token")
		invalidHeaderResp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request with invalid header auth: %v", err)
		}
		invalidHeaderResp.Body.Close()
		if invalidHeaderResp.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("expected HTTP 307 for invalid header basic auth, got %d", invalidHeaderResp.StatusCode)
		}

		req = makeProxyRequest(t, box, httpPort)
		req.Header.Set("Authorization", "Bearer bad-token")
		invalidBearerResp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request with invalid bearer auth: %v", err)
		}
		invalidBearerResp.Body.Close()
		if invalidBearerResp.StatusCode != http.StatusTemporaryRedirect {
			t.Fatalf("expected HTTP 307 for invalid bearer auth, got %d", invalidBearerResp.StatusCode)
		}

		req = makeProxyRequest(t, box, httpPort)
		req.Header.Set("Authorization", "bearer "+token)
		lowerBearerResp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request with lowercase bearer auth: %v", err)
		}
		defer lowerBearerResp.Body.Close()
		body, err = io.ReadAll(lowerBearerResp.Body)
		if err != nil {
			t.Fatalf("failed to read lowercase bearer response body: %v", err)
		}
		if lowerBearerResp.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTP 200 from lowercase bearer auth request, got %d (body: %s)", lowerBearerResp.StatusCode, body)
		}
		if !strings.Contains(string(body), "alive") {
			t.Fatalf("expected body to contain 'alive' via lowercase bearer auth, got %s", body)
		}

		req = makeProxyRequest(t, box, httpPort)
		setBasicAuthHeader(req, token)
		authHeaderResp, err := client.Do(req)
		if err != nil {
			t.Fatalf("failed to make proxy request with header auth: %v", err)
		}
		defer authHeaderResp.Body.Close()
		body, err = io.ReadAll(authHeaderResp.Body)
		if err != nil {
			t.Fatalf("failed to read header-auth response body: %v", err)
		}
		if authHeaderResp.StatusCode != http.StatusOK {
			t.Fatalf("expected HTTP 200 from header basic auth request, got %d (body: %s)", authHeaderResp.StatusCode, body)
		}
		if !strings.Contains(string(body), "alive") {
			t.Fatalf("expected body to contain 'alive' via header auth, got %s", body)
		}
	})

	t.Run("alternate_ports", func(t *testing.T) {
		serveHTTP(t, Env.exed.ExtraPorts[0])

		expectedRedirect := fmt.Sprintf("http://%s.localhost:%d/__exe.dev/login?redirect=http%%3A%%2F%%2F%s.localhost%%3A%d%%2F%%3Ffoo%%3D1", box, Env.exed.ExtraPorts[0], box, Env.exed.ExtraPorts[0])
		proxyAssert(t, box, proxyExpectation{
			name:             "altport without auth redirects",
			httpPort:         Env.exed.ExtraPorts[0],
			cookies:          nil,
			httpCode:         http.StatusTemporaryRedirect,
			redirectLocation: expectedRedirect,
		})
		proxyAssert(t, box, proxyExpectation{
			name:     "altport with auth succeeds",
			httpPort: Env.exed.ExtraPorts[0],
			cookies:  cookies,
			httpCode: http.StatusOK,
		})

		proxyAssert(t, box, proxyExpectation{
			name:     "other altport with auth fails",
			httpPort: Env.exed.ExtraPorts[1],
			cookies:  cookies,
			httpCode: http.StatusBadGateway,
		})
	})

	// Cleanup
	pty = sshToExeDev(t, keyFile)
	pty.deleteBox(box)
	pty.disconnect()
}

type proxyExpectation struct {
	name             string
	httpPort         int
	cookies          []*http.Cookie
	httpCode         int
	redirectLocation string // Expected Location header for redirects (optional)
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
	if !strings.HasSuffix(host, ".localhost") {
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

func proxyAssert(t *testing.T, boxName string, exp proxyExpectation) {
	t.Helper()
	// t.Logf("Testing proxy expectation: %s port %d expected http status %d", exp.name, exp.httpPort, exp.httpCode)
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	if exp.cookies != nil {
		u := fmt.Sprintf("http://localhost:%d", exp.httpPort)
		setCookiesForJar(t, jar, u, exp.cookies)
	}
	client := noRedirectClient(jar)

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
		// (e.g., 404 when access is denied)
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
			// Not owner, need to manually confirm
			q := u.Query()
			q.Set("action", "confirm")
			u.RawQuery = q.Encode()
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
			if resp.StatusCode != http.StatusSeeOther {
				t.Errorf("expected StatusSeeOther (303) redirect after confirmation, got status %d", resp.StatusCode)
			}
			u, err = resp.Location()
			if err != nil {
				t.Fatalf("failed to get redirect location: %v", err)
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
	t.Helper()
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	req, err := http.NewRequest("GET", proxyURL, nil)
	if err != nil {
		t.Fatalf("failed to create proxy request: %v", err)
	}
	req.Host = fmt.Sprintf("%s.localhost:%d", boxName, httpPort)
	return req
}

func setBasicAuthHeader(req *http.Request, token string) {
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(token+":")))
}

func setBasicAuthUserinfo(req *http.Request, token string) {
	req.URL.User = url.UserPassword(token, "")
}
