package e1e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestUsagePage(t *testing.T) {
	t.Parallel()
	reserveVMs(t, 1)
	e1eTestsOnlyRunOnce(t)

	if Env.servers.Metricsd == nil {
		t.Skip("metricsd not configured")
	}

	// Register a user and create a box for subtests that need it.
	pty, cookies, _, _ := registerForExeDev(t)
	boxName := newBox(t, pty)
	defer pty.Disconnect()
	defer pty.deleteBox(boxName)

	t.Run("unauthenticated_redirect", func(t *testing.T) {
		noGolden(t)

		// Follow redirects, recording each URL, until we get a non-redirect
		// or find /auth in the chain.
		client := &http.Client{
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: 10 * time.Second,
		}
		cur := fmt.Sprintf("http://localhost:%d/usage", Env.HTTPPort())
		var foundAuth bool
		for range 10 {
			resp, err := client.Get(cur)
			if err != nil {
				t.Fatalf("GET %s failed: %v", cur, err)
			}
			resp.Body.Close()
			loc := resp.Header.Get("Location")
			if strings.Contains(loc, "/auth") {
				foundAuth = true
				break
			}
			if resp.StatusCode/100 != 3 {
				break
			}
			cur = loc
		}
		if !foundAuth {
			t.Errorf("expected redirect chain to reach /auth")
		}
	})

	t.Run("unauthenticated_api_401", func(t *testing.T) {
		noGolden(t)

		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/usage-api?vm_names=fake&hours=24", Env.HTTPPort()))
		if err != nil {
			t.Fatalf("GET /usage-api failed: %v", err)
		}
		defer resp.Body.Close()

		// Unauthenticated API requests should get 401 or a redirect.
		if resp.StatusCode != http.StatusUnauthorized &&
			resp.StatusCode != http.StatusTemporaryRedirect &&
			resp.StatusCode != http.StatusFound &&
			resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("expected 401 or redirect, got %d", resp.StatusCode)
		}
	})

	t.Run("authenticated_page_loads", func(t *testing.T) {
		noGolden(t)

		client := newClientWithCookies(t, cookies)
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/usage", Env.HTTPPort()))
		if err != nil {
			t.Fatalf("GET /usage failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}
		if !strings.Contains(string(body), "usage-charts.js") {
			t.Errorf("expected page to reference usage-charts.js, got:\n%s", body)
		}
	})

	t.Run("authenticated_api_returns_json", func(t *testing.T) {
		noGolden(t)

		client := newClientWithCookies(t, cookies)
		resp, err := client.Get(fmt.Sprintf("http://localhost:%d/usage-api?vm_names=%s&hours=24", Env.HTTPPort(), boxName))
		if err != nil {
			t.Fatalf("GET /usage-api failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		// The response should be valid JSON.
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("response is not valid JSON: %v", err)
		}
	})

	t.Run("debug_api_works", func(t *testing.T) {
		noGolden(t)

		// Debug endpoints don't require auth.
		resp, err := http.Get(fmt.Sprintf("http://localhost:%d/debug/usage-api?vm_names=%s&hours=24", Env.servers.Exed.HTTPPort, boxName))
		if err != nil {
			t.Fatalf("GET /debug/usage-api failed: %v", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		// The response should be valid JSON.
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("response is not valid JSON: %v\nbody: %s", err, body[:min(len(body), 500)])
		}
	})

	t.Run("api_rejects_other_users_vms", func(t *testing.T) {
		noGolden(t)

		// Register a second user (user B).
		ptyB, cookiesB, _, _ := registerForExeDev(t)
		ptyB.Disconnect()

		// User B queries user A's box via /usage-api.
		clientB := newClientWithCookies(t, cookiesB)
		resp, err := clientB.Get(fmt.Sprintf("http://localhost:%d/usage-api?vm_names=%s&hours=24", Env.HTTPPort(), boxName))
		if err != nil {
			t.Fatalf("GET /usage-api as user B failed: %v", err)
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("failed to read response body: %v", err)
		}

		// The API should return 200 but not include user A's box data.
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}

		// The response should not contain user A's box name as a key.
		var result map[string]any
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("response is not valid JSON: %v", err)
		}
		if _, ok := result[boxName]; ok {
			t.Errorf("user B should not see user A's box %q in response, got: %s", boxName, body)
		}
	})
}
