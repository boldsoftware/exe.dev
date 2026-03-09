package xshelley

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"tailscale.com/util/singleflight"
)

// resetTestGlobals overrides package-level variables for isolated unit testing.
// Tests using this must NOT run in parallel since they mutate shared state.
func resetTestGlobals(t *testing.T, handler http.Handler) {
	t.Helper()

	origURL := latestReleaseURL
	origClient := httpClient
	origBaseWait := retryBaseWait

	ts := httptest.NewServer(handler)

	latestReleaseURL = ts.URL + "/releases/latest"
	httpClient = &http.Client{Timeout: 30 * time.Second}
	retryBaseWait = 10 * time.Millisecond
	cacheDirOnce = sync.Once{}
	cacheDir = t.TempDir()
	cacheDirOnce.Do(func() {}) // mark as done so getCacheDir returns our test dir
	sfGroup = &singleflight.Group[string, string]{}

	t.Cleanup(func() {
		ts.Close()
		latestReleaseURL = origURL
		httpClient = origClient
		retryBaseWait = origBaseWait
		cacheDirOnce = sync.Once{}
		cacheDir = ""
		cacheDirErr = nil
		sfGroup = &singleflight.Group[string, string]{}
	})
}

// serveRelease writes a mock GitHub releases JSON response with download URLs
// pointing back to the test server.
func serveRelease(w http.ResponseWriter, r *http.Request) {
	base := fmt.Sprintf("http://%s", r.Host)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ghRelease{
		TagName: "v1.0.0",
		Assets: []ghAsset{
			{Name: "shelley_linux_amd64", BrowserDownloadURL: base + "/download/shelley_linux_amd64"},
			{Name: "shelley_linux_arm64", BrowserDownloadURL: base + "/download/shelley_linux_arm64"},
		},
	})
}

// --- retryableGet tests ---

func TestRetryableGet_Retry5xx(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := calls.Add(1); n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	origClient, origWait := httpClient, retryBaseWait
	httpClient = &http.Client{Timeout: 10 * time.Second}
	retryBaseWait = time.Millisecond
	defer func() { httpClient = origClient; retryBaseWait = origWait }()

	resp, err := retryableGet(context.Background(), ts.URL, nil)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

func TestRetryableGet_429RetryAfterRespected(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := calls.Add(1); n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	origClient, origWait := httpClient, retryBaseWait
	httpClient = &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	retryBaseWait = time.Millisecond // much smaller than 1s Retry-After
	defer func() { httpClient = origClient; retryBaseWait = origWait }()

	synctest.Test(t, func(t *testing.T) {
		resp, err := retryableGet(context.Background(), ts.URL, nil)
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		resp.Body.Close()
		if got := calls.Load(); got != 2 {
			t.Errorf("expected 2 attempts, got %d", got)
		}
	})
}

func TestRetryableGet_403WithRetryAfterRetried(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := calls.Add(1); n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	origClient, origWait := httpClient, retryBaseWait
	httpClient = &http.Client{
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	retryBaseWait = time.Millisecond
	defer func() { httpClient = origClient; retryBaseWait = origWait }()

	synctest.Test(t, func(t *testing.T) {
		resp, err := retryableGet(context.Background(), ts.URL, nil)
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if got := calls.Load(); got != 2 {
			t.Errorf("expected 2 attempts, got %d", got)
		}
	})
}

func TestRetryableGet_403WithoutRetryAfterNotRetried(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	origClient, origWait := httpClient, retryBaseWait
	httpClient = &http.Client{Timeout: 10 * time.Second}
	retryBaseWait = time.Millisecond
	defer func() { httpClient = origClient; retryBaseWait = origWait }()

	resp, err := retryableGet(context.Background(), ts.URL, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 pass-through, got %d", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("expected 1 attempt (no retry for bare 403), got %d", got)
	}
}

func TestRetryableGet_AllRetriesFail(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	origClient, origWait := httpClient, retryBaseWait
	httpClient = &http.Client{Timeout: 10 * time.Second}
	retryBaseWait = time.Millisecond
	defer func() { httpClient = origClient; retryBaseWait = origWait }()

	_, err := retryableGet(context.Background(), ts.URL, nil)
	if err == nil {
		t.Fatal("expected error after all retries exhausted")
	}
	if got := calls.Load(); got != int32(maxRetries) {
		t.Errorf("expected %d attempts, got %d", maxRetries, got)
	}
}

func TestRetryableGet_ContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // trigger retry
	}))
	defer ts.Close()

	origClient, origWait := httpClient, retryBaseWait
	httpClient = &http.Client{Timeout: 10 * time.Second}
	retryBaseWait = 5 * time.Second // long backoff so cancellation fires during wait
	defer func() { httpClient = origClient; retryBaseWait = origWait }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := retryableGet(ctx, ts.URL, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
	if elapsed > 1*time.Second {
		t.Errorf("expected prompt return on cancellation, took %v", elapsed)
	}
}
