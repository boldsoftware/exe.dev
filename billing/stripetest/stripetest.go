// Package stripetest provides a client for testing Stripe API interactions.
package stripetest

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"exe.dev/backoff"
	"exe.dev/billing/httprr"
	"github.com/stripe/stripe-go/v82"
)

// TestAPIKey is the Stripe test API key used for record/replay clients.
const TestAPIKey = "sk_test_51SzRtTKBUWL0n1QN0OSXVllXJLOeM2JfcFDRLNJHeMpKVTgjaif5cDBhZ1jIcCv8cZFRoMb1YBnbYeXedaD1oQ3w00tOHZd9cF"

type roundTripperFunc func(req *http.Request) *http.Response

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

type testingLogger struct {
	t testing.TB
}

func (l *testingLogger) logf(format string, args ...any) {
	l.t.Helper()
	fmt.Fprintf(l.t.Output(), format+"\n", args...)
}

func (l *testingLogger) Debugf(format string, args ...any) {
	l.logf("[DEBUG] "+format, args...)
}

func (l *testingLogger) Errorf(format string, args ...any) {
	l.logf("[ERROR] "+format, args...)
}

func (l *testingLogger) Infof(format string, args ...any) {
	l.logf("[INFO] "+format, args...)
}

func (l *testingLogger) Warnf(format string, args ...any) {
	l.logf("[WARN] "+format, args...)
}

// LeveledLogger returns a Stripe LeveledLoggerInterface that writes through
// the test's output stream.
func LeveledLogger(t testing.TB) stripe.LeveledLoggerInterface {
	t.Helper()
	return &testingLogger{t: t}
}

// Client arranges a Stripe client that routes requests to the provided HTTP
// handler called through a custom transport.
// The ResponseWriter will be an [httptest.ResponseRecorder].
func Client(t testing.TB, h func(w http.ResponseWriter, r *http.Request)) *stripe.Client {
	t.Helper()

	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) *http.Response {
				w := httptest.NewRecorder()
				w.Header().Set("Request-Id", "req_test_"+req.URL.Path)
				h(w, req.Clone(req.Context()))
				return w.Result()
			}),
		},
		LeveledLogger: LeveledLogger(t),
	})
	return stripe.NewClient("sk_test_xxx", stripe.WithBackends(backends))
}

// Record creates a Stripe client backed by httprr at file.
// Use -httprecord to refresh traces.
func Record(t testing.TB, file string) *stripe.Client {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(file), err)
	}

	rr, err := httprr.Open(file, http.DefaultTransport)
	if err != nil {
		t.Fatalf("httprr.Open(%q): %v", file, err)
	}
	t.Cleanup(func() {
		if err := rr.Close(); err != nil {
			t.Errorf("failed to close httprr: %v", err)
		}
	})

	var counter atomic.Int64
	rr.ScrubReq(func(r *http.Request) error {
		pathParts := strings.Split(r.URL.Path, "/")
		for i, part := range pathParts {
			if strings.HasPrefix(part, "exe_") {
				pathParts[i] = "exe_TEST"
			}
		}
		r.URL.Path = strings.Join(pathParts, "/")

		q := r.URL.Query()
		for _, key := range []string{"created[gt]", "created[gte]"} {
			if q.Has(key) {
				q.Set(key, "1")
			}
		}
		if customer := q.Get("customer"); strings.HasPrefix(customer, "exe_") {
			q.Set("customer", "exe_TEST")
		}
		r.URL.RawQuery = q.Encode()

		r.Header.Del("Authorization")
		r.Header.Del("Idempotency-Key")
		r.Header.Del("Stripe-Version")
		r.Header.Del("User-Agent")
		r.Header.Del("X-Stripe-Client-User-Agent")
		r.Header.Del("X-Stripe-Client-Telemetry")
		r.Header.Set("Replay-Id", fmt.Sprintf("req_%d", counter.Add(1)))

		if body, ok := r.Body.(*httprr.Body); ok {
			contentType := r.Header.Get("Content-Type")
			if strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
				form, err := url.ParseQuery(string(body.Data))
				if err == nil {
					if id := form.Get("id"); strings.HasPrefix(id, "exe_") {
						form.Set("id", "exe_TEST")
					}
					if customer := form.Get("customer"); strings.HasPrefix(customer, "exe_") {
						form.Set("customer", "exe_TEST")
					}
					for _, key := range []string{"success_url", "cancel_url", "return_url"} {
						if form.Has(key) {
							form.Set(key, "https://example.com/callback")
						}
					}
					body.Data = []byte(form.Encode())
					body.ReadOffset = 0
				}
			}
		}
		return nil
	})

	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient:    rr.Client(),
		LeveledLogger: LeveledLogger(t),
	})
	return stripe.NewClient(TestAPIKey, stripe.WithBackends(backends))
}

type Clock struct {
	sc  *stripe.Client
	id  string
	now time.Time
}

// Start starts a new test clock frozen to 2020-01-01 00:00:00.000 UTC using the provided
// Stripe client and returns a [Clock] that advances the clock with subsequent calls to [Clock.Sleep].
//
// It registers a t.Cleanup function that deletes the clock if the test
// passeed, or logs a Stripe dashboard link to assist debugging failures.
func Start(ctx context.Context, sc *stripe.Client, name string) (*Clock, error) {
	now := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	clock, err := sc.V1TestHelpersTestClocks.Create(ctx, &stripe.TestHelpersTestClockCreateParams{
		Name:       new(name),
		FrozenTime: new(now.Unix()),
	})
	if err != nil {
		return nil, err
	}
	return &Clock{sc, clock.ID, now}, nil
}

// Close deletes the test clock.
func (c *Clock) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := c.sc.V1TestHelpersTestClocks.Delete(ctx, c.id, nil)
	return err
}

// Link returns a URL to the Stripe dashboard for this test clock.
func (c *Clock) Link() string {
	return "https://dashboard.stripe.com/test/billing/subscriptions/test-clocks/" + c.id
}

// Now returns the current time of the test clock.
func (c *Clock) Now() time.Time {
	return c.now
}

// Sleep advances the test clock by the provided duration and waits for it to
// become ready.
func (c *Clock) Sleep(ctx context.Context, d time.Duration, onRetry func(status string)) error {
	// Update now
	c.now = c.now.Add(d)

	_, err := c.sc.V1TestHelpersTestClocks.Advance(ctx, c.id, &stripe.TestHelpersTestClockAdvanceParams{
		FrozenTime: new(c.now.Unix()),
	})
	if err != nil {
		return err
	}

	for range backoff.Loop(ctx, 1*time.Second) {
		info, err := c.sc.V1TestHelpersTestClocks.Retrieve(ctx, c.id, nil)
		if err != nil {
			return err
		}
		if info.Status == "ready" {
			return nil
		}
		onRetry(string(info.Status))
	}

	return ctx.Err()
}

// ID returns the clock_id as returned by Stripe.
func (c *Clock) ID() string {
	return c.id
}
