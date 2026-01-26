// Package stripetest provides a client for testing Stripe API interactions.
package stripetest

import (
	"net/http"
	"net/http/httptest"

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
