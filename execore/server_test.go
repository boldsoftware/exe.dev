package execore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"exe.dev/billing"
	"exe.dev/billing/stripetest"
	"exe.dev/exedb"
	"exe.dev/stage"
	"exe.dev/tslog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stripe/stripe-go/v82"
)

// testSSHPubKey is a valid SSH public key for use in tests.
const testSSHPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKmvM4PVNt905k8sp9UYnPzlFgR8J6k64U3qIFkJvvy8 test@example.com"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := newUnstartedServer(t)
	s.startAndAwaitReady()
	return s
}

func newBillingTestServer(t *testing.T) *Server {
	t.Helper()
	s := newUnstartedBillingServer(t)
	s.startAndAwaitReady()
	return s
}

func newStripeClient(baseURL string) *stripe.Client {
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		URL: &baseURL,
	})
	return stripe.NewClient(billing.TestAPIKey, stripe.WithBackends(backends))
}

func newUnstartedBillingServer(t testing.TB) *Server {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}
	env := stage.Test()
	registry := prometheus.NewRegistry()
	cassette := filepath.Join("testdata", "stripe", strings.ReplaceAll(t.Name(), "/", "_")+".httprr")
	s, err := NewServer(ServerConfig{
		Logger:             tslog.Slogger(t),
		HTTPAddr:           ":0",
		HTTPSAddr:          ":0",
		SSHAddr:            ":0",
		PluginAddr:         ":0",
		ExeproxServicePort: 0,
		DBPath:             dbPath,
		FakeEmailServer:    "",
		PiperdPort:         2222,
		GHWhoAmIPath:       "",
		ExeletAddresses:    nil,
		Env:                env,
		Billing: &billing.Manager{
			Client: stripetest.Record(t, cassette),
		},
		MetricsRegistry: registry,
		LMTPSocketPath:  "",
		MetricsdURL:     "",
	})
	if err != nil {
		t.Fatalf("failed to create billing test server: %v", err)
	}
	t.Cleanup(func() { s.Stop() })
	return s
}

// httpURL returns the base HTTP URL for the test server (e.g., "http://127.0.0.1:12345").
func (s *Server) httpURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", s.httpPort())
}

func (s *Server) startAndAwaitReady() {
	go s.Start()
	s.ready.Wait()
}

func newUnstartedServer(t testing.TB) *Server {
	t.Helper()

	// Create a fake Stripe server that handles common billing endpoints.
	// This allows tests to run without hitting real Stripe.
	fakeStripe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/subscriptions":
			// Cancelation poller endpoint
			json.NewEncoder(w).Encode(map[string]any{
				"data":     []map[string]any{},
				"has_more": false,
			})
		case r.Method == "GET" && r.URL.Path == "/v1/events":
			// Subscription event poller endpoint
			json.NewEncoder(w).Encode(map[string]any{
				"data":     []map[string]any{},
				"has_more": false,
			})
		case r.Method == "GET" && r.URL.Path == "/v1/prices":
			// Price lookup for checkout
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "price_test123", "lookup_key": "individual"},
				},
			})
		case r.Method == "POST" && r.URL.Path == "/v1/customers":
			// Create customer
			json.NewEncoder(w).Encode(map[string]any{
				"id":    "cus_test123",
				"email": r.FormValue("email"),
			})
		case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
			// Create checkout session
			// Enforce Stripe's 5000-character limit on URLs.
			for _, param := range []string{"success_url", "cancel_url"} {
				if u := r.FormValue(param); len(u) > 5000 {
					w.WriteHeader(http.StatusBadRequest)
					json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"type":    "invalid_request_error",
							"message": "Invalid URL: URL must be 5000 characters or less.",
							"param":   param,
						},
					})
					return
				}
			}
			json.NewEncoder(w).Encode(map[string]any{
				"id":  "cs_test_session",
				"url": "https://checkout.stripe.com/pay/cs_test_session",
			})
		case r.Method == "POST" && r.URL.Path == "/v1/billing_portal/sessions":
			// Create billing portal session
			json.NewEncoder(w).Encode(map[string]any{
				"id":  "bps_test_session",
				"url": "https://billing.stripe.com/session/bps_test_session",
			})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
			// Retrieve checkout session (used by VerifyCheckout).
			// Only return success for the session ID we actually created.
			sessionID := strings.TrimPrefix(r.URL.Path, "/v1/checkout/sessions/")
			if sessionID != "cs_test_session" {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{
						"type":    "invalid_request_error",
						"message": "No such checkout session: " + sessionID,
					},
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{
				"id":             sessionID,
				"status":         "complete",
				"payment_status": "paid",
				"customer":       map[string]any{"id": "cus_test123"},
			})
		case r.Method == "GET" && r.URL.Path == "/v1/payment_intents":
			// List payment intents (used by SyncCredits)
			json.NewEncoder(w).Encode(map[string]any{
				"data":     []map[string]any{},
				"has_more": false,
			})
		case r.Method == "GET" && r.URL.Path == "/v1/charges":
			// List charges (used by ReceiptURLs for credit purchase receipts)
			json.NewEncoder(w).Encode(map[string]any{
				"data":     []map[string]any{},
				"has_more": false,
			})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/products/"):
			// Get product (used by install_prices at startup)
			productID := strings.TrimPrefix(r.URL.Path, "/v1/products/")
			json.NewEncoder(w).Encode(map[string]any{
				"id":     productID,
				"object": "product",
				"name":   "Individual",
			})
		case r.Method == "GET" && r.URL.Path == "/v1/prices":
			// List prices (used by install_prices at startup)
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{
					"id":         "price_test123",
					"object":     "price",
					"lookup_key": "individual",
					"product":    "prod_individual",
				}},
				"has_more": false,
			})
		default:
			t.Errorf("unhandled fake Stripe request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":"not_found","message":"Not found"}`)
		}
	}))
	t.Cleanup(fakeStripe.Close)

	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	if err := exedb.CopyTemplateDB(tslog.Slogger(t), dbPath); err != nil {
		t.Fatalf("failed to copy template database: %v", err)
	}
	env := stage.Test()
	registry := prometheus.NewRegistry()
	s, err := NewServer(ServerConfig{
		Logger:             tslog.Slogger(t),
		HTTPAddr:           ":0",
		HTTPSAddr:          ":0",
		SSHAddr:            ":0",
		PluginAddr:         ":0",
		ExeproxServicePort: 0,
		DBPath:             dbPath,
		FakeEmailServer:    "",
		PiperdPort:         2222,
		GHWhoAmIPath:       "",
		ExeletAddresses:    nil,
		Env:                env,
		Billing: &billing.Manager{
			Client: newStripeClient(fakeStripe.URL),
		},
		MetricsRegistry: registry,
		LMTPSocketPath:  "",
		MetricsdURL:     "",
	})
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	t.Cleanup(func() { s.Stop() }) // Ensure server is stopped when test ends (even if not started)
	return s
}

// BenchmarkNewTestServer benchmarks the creation of a new test server.
// This is directly proportional to the time it takes to run these tests, which is an ongoing pain point.
func BenchmarkNewTestServer(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		s := newUnstartedServer(b)
		s.startAndAwaitReady()
		s.Stop()
	}
}
