package execore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"exe.dev/stage"
	"exe.dev/tslog"
	"github.com/prometheus/client_golang/prometheus"
)

// testSSHPubKey is a valid SSH public key for use in tests.
const testSSHPubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKmvM4PVNt905k8sp9UYnPzlFgR8J6k64U3qIFkJvvy8 test@example.com"

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s := newUnstartedServer(t)
	s.startAndAwaitReady()
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
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":"not_found","message":"Not found"}`)
		}
	}))
	t.Cleanup(fakeStripe.Close)

	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	env := stage.Test()
	env.StripeURL = fakeStripe.URL
	registry := prometheus.NewRegistry()
	s, err := NewServer(ServerConfig{
		Logger:          tslog.Slogger(t),
		HTTPAddr:        ":0",
		HTTPSAddr:       ":0",
		SSHAddr:         ":0",
		PluginAddr:      ":0",
		DBPath:          dbPath,
		FakeEmailServer: "",
		PiperdPort:      2222,
		GHWhoAmIPath:    "",
		ExeletAddresses: nil,
		Env:             env,
		MetricsRegistry: registry,
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
