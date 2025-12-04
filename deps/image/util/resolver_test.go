package util

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryTransport_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRetryTransport_RetryOn429(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("rate limited"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if attempts.Load() != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts.Load())
	}
}

func TestRetryTransport_RetryOn503(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("service unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestRetryTransport_NoRetryOn4xx(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	if attempts.Load() != 1 {
		t.Errorf("expected 1 attempt (no retry), got %d", attempts.Load())
	}
}

func TestRetryTransport_ContextCancellation(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay to allow at least one attempt
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	_, err := client.Do(req)

	if err == nil {
		t.Fatal("expected error due to context cancellation")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context canceled error, got: %v", err)
	}
}

func TestRetryTransport_PreservesBody(t *testing.T) {
	var attempts atomic.Int32
	var receivedBodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBodies = append(receivedBodies, string(body))
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	body := "test request body"
	resp, err := client.Post(server.URL, "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if len(receivedBodies) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(receivedBodies))
	}
	for i, received := range receivedBodies {
		if received != body {
			t.Errorf("attempt %d: expected body %q, got %q", i+1, body, received)
		}
	}
}

func TestRetryTransport_RetryOnNetworkError(t *testing.T) {
	var attempts atomic.Int32

	// Create a transport that fails on first attempt
	failingTransport := &failOnceTransport{
		base:      http.DefaultTransport,
		failCount: 1,
		attempts:  &attempts,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	// Point failingTransport to server for successful requests
	failingTransport.serverURL = server.URL

	transport := &retryTransport{base: failingTransport}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestRetryTransport_NoRetryWithoutGetBody(t *testing.T) {
	// When a request has a body but no GetBody function (streaming body),
	// we cannot retry because we can't replay the body.
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}

	// Create a request with a body but without GetBody (simulating a streaming body)
	req, _ := http.NewRequest("POST", server.URL, &nonReplayableReader{data: []byte("streaming data")})
	req.ContentLength = 14

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Should return 503 without retrying since body can't be replayed
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", resp.StatusCode)
	}
	if attempts.Load() != 1 {
		t.Errorf("expected 1 attempt (no retry for non-replayable body), got %d", attempts.Load())
	}
}

// nonReplayableReader is a reader that doesn't support GetBody
type nonReplayableReader struct {
	data []byte
	pos  int
}

func (r *nonReplayableReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *nonReplayableReader) Close() error { return nil }

func TestRetryTransport_RespectsRetryAfterSeconds(t *testing.T) {
	var attempts atomic.Int32
	var requestTimes []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestTimes = append(requestTimes, time.Now())
		if attempts.Add(1) < 2 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("rate limited"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if len(requestTimes) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(requestTimes))
	}

	// Verify we waited at least 1 second (the Retry-After value)
	elapsed := requestTimes[1].Sub(requestTimes[0])
	if elapsed < 1*time.Second {
		t.Errorf("expected to wait at least 1s (Retry-After), but only waited %v", elapsed)
	}
}

func TestRetryTransport_RetryAfterExceedsDeadline(t *testing.T) {
	// When Retry-After exceeds our deadline, we should give up
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "120") // 120 seconds - way beyond 30s deadline
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer server.Close()

	transport := &retryTransport{base: http.DefaultTransport}
	client := &http.Client{Transport: transport}

	start := time.Now()
	resp, err := client.Get(server.URL)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	// Should return 429 because we can't wait that long
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
	// Should have given up quickly, not waited 120s
	if elapsed > 5*time.Second {
		t.Errorf("should have given up quickly, but took %v", elapsed)
	}
	// Should have made 2 attempts (initial + final attempt after giving up on retry)
	if attempts.Load() != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts.Load())
	}
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected time.Duration
	}{
		{"empty", "", 0},
		{"seconds", "60", 60 * time.Second},
		{"zero seconds", "0", 0},
		{"malformed", "invalid", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRetryAfter(tt.value)
			if got != tt.expected {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.value, got, tt.expected)
			}
		})
	}
}

// failOnceTransport is a test transport that fails the first N requests
type failOnceTransport struct {
	base      http.RoundTripper
	failCount int32
	attempts  *atomic.Int32
	serverURL string
}

func (t *failOnceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	attempt := t.attempts.Add(1)
	if attempt <= t.failCount {
		return nil, &tempError{msg: "connection refused"}
	}
	return t.base.RoundTrip(req)
}

type tempError struct {
	msg string
}

func (e *tempError) Error() string   { return e.msg }
func (e *tempError) Temporary() bool { return true }
