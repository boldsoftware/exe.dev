// Package stripetest provides a client for testing Stripe API interactions.
package stripetest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"time"

	"exe.dev/backoff"
	"github.com/stripe/stripe-go/v82"
)

type roundTripperFunc func(req *http.Request) *http.Response

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

// Client arranges a Stripe client that routes requests to the provided HTTP
// handler called through a custom transport.
// The ResponseWriter will be an [httptest.ResponseRecorder].
func Client(h func(w http.ResponseWriter, r *http.Request)) *stripe.Client {
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		HTTPClient: &http.Client{
			Transport: roundTripperFunc(func(req *http.Request) *http.Response {
				w := httptest.NewRecorder()
				w.Header().Set("Request-Id", "req_test_"+req.URL.Path)
				h(w, req.Clone(req.Context()))
				return w.Result()
			}),
		},
	})
	return stripe.NewClient("sk_test_xxx", stripe.WithBackends(backends))
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
		Name:       new_(name),
		FrozenTime: new_(now.Unix()),
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
		FrozenTime: new_(c.now.Unix()),
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

func new_[T any](v T) *T {
	return &v
}
