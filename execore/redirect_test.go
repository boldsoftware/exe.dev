package execore

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"exe.dev/exedb"
	"exe.dev/sqlite"
)

func TestRedirectBasic(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	baseURL := server.httpURL()

	target := "https://accounts.google.com/o/oauth2/auth?client_id=long&state=abc123"
	key, err := server.createRedirect(t.Context(), target)
	if err != nil {
		t.Fatalf("createRedirect: %v", err)
	}
	if key == "" {
		t.Fatal("createRedirect returned empty key")
	}

	// GET /r/<key> should redirect to the target.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(baseURL + "/r/" + key)
	if err != nil {
		t.Fatalf("GET /r/%s: %v", key, err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d", resp.StatusCode)
	}
	got := resp.Header.Get("Location")
	if got != target {
		t.Fatalf("redirect target = %q, want %q", got, target)
	}
}

func TestRedirectNotFound(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	baseURL := server.httpURL()

	resp, err := http.Get(baseURL + "/r/nonexistent")
	if err != nil {
		t.Fatalf("GET /r/nonexistent: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRedirectExpired(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	baseURL := server.httpURL()

	// Insert a redirect with an already-expired TTL using the sqlc method
	// to ensure consistent time formatting.
	key := "expired-test-key"
	err := server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		return queries.InsertRedirect(ctx, exedb.InsertRedirectParams{
			Key:       key,
			Target:    "https://example.com",
			ExpiresAt: time.Now().Add(-time.Hour),
		})
	})
	if err != nil {
		t.Fatalf("insert expired redirect: %v", err)
	}

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(baseURL + "/r/" + key)
	if err != nil {
		t.Fatalf("GET /r/%s: %v", key, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for expired redirect, got %d", resp.StatusCode)
	}
}

func TestRedirectBareR(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	baseURL := server.httpURL()

	// /r/ with no key should 404.
	resp, err := http.Get(baseURL + "/r/")
	if err != nil {
		t.Fatalf("GET /r/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for /r/, got %d", resp.StatusCode)
	}
}

func TestRedirectURL(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	got := server.redirectURL("abc123")
	want := server.webBaseURLNoRequest() + "/r/abc123"
	if got != want {
		t.Fatalf("redirectURL = %q, want %q", got, want)
	}
}

func TestRedirectCleanup(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)

	// Insert two redirects: one expired, one valid.
	err := server.withTx(t.Context(), func(ctx context.Context, queries *exedb.Queries) error {
		if err := queries.InsertRedirect(ctx, exedb.InsertRedirectParams{
			Key:       "old-key",
			Target:    "https://old.example.com",
			ExpiresAt: time.Now().Add(-time.Hour),
		}); err != nil {
			return err
		}
		return queries.InsertRedirect(ctx, exedb.InsertRedirectParams{
			Key:       "fresh-key",
			Target:    "https://fresh.example.com",
			ExpiresAt: time.Now().Add(time.Hour),
		})
	})
	if err != nil {
		t.Fatalf("insert test redirects: %v", err)
	}

	// Create a new redirect, which triggers cleanup.
	_, err = server.createRedirect(t.Context(), "https://trigger-cleanup.example.com")
	if err != nil {
		t.Fatalf("createRedirect: %v", err)
	}

	// old-key should be cleaned up.
	var count int
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM redirects WHERE key = 'old-key'`).Scan(&count)
	})
	if err != nil {
		t.Fatalf("query old-key: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected old-key to be cleaned up, but count = %d", count)
	}

	// fresh-key should still exist.
	err = server.db.Rx(t.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		return rx.QueryRow(`SELECT COUNT(*) FROM redirects WHERE key = 'fresh-key'`).Scan(&count)
	})
	if err != nil {
		t.Fatalf("query fresh-key: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected fresh-key to exist, but count = %d", count)
	}
}

func TestRedirectE2EFullFlow(t *testing.T) {
	t.Parallel()
	server := newTestServer(t)
	baseURL := server.httpURL()

	// Simulate the full flow: create a redirect, GET it, verify we land at the target.
	longURL := "https://accounts.google.com/o/oauth2/auth?client_id=563092301298-g6atemvnid09dobubjm3ufctfpuaas93.apps.googleusercontent.com&login_hint=david.crawshaw%40gmail.com&redirect_uri=https%3A%2F%2Fexe.dev%2Foauth%2Fgoogle%2Fcallback&response_type=code&scope=openid+email&state=wn52vVAs3U9amgwSkDz8yEl82YROkUJrTkdD-dGoDGc%3D"

	key, err := server.createRedirect(t.Context(), longURL)
	if err != nil {
		t.Fatalf("createRedirect: %v", err)
	}

	shortURL := server.redirectURL(key)
	t.Logf("short URL: %s", shortURL)
	t.Logf("long URL length: %d, short URL length: %d", len(longURL), len(shortURL))

	if len(shortURL) >= len(longURL) {
		t.Fatalf("short URL (%d chars) should be shorter than long URL (%d chars)", len(shortURL), len(longURL))
	}

	// Follow the redirect and verify the Location header.
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// Use the actual test server URL, not the production URL.
	resp, err := client.Get(baseURL + "/r/" + key)
	if err != nil {
		t.Fatalf("GET short URL: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302, got %d; body: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Location"); got != longURL {
		t.Fatalf("Location = %q, want %q", got, longURL)
	}
}
