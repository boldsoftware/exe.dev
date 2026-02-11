package execore

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"exe.dev/stage"
	"exe.dev/tslog"
	"github.com/prometheus/client_golang/prometheus"
)

type bootStripeState struct {
	mu sync.Mutex

	requests int

	products map[string]bool
	prices   map[string]string

	productCreates int
	priceCreates   int
}

func newBootStripeServer(t *testing.T) (*httptest.Server, *bootStripeState) {
	t.Helper()

	state := &bootStripeState{
		products: make(map[string]bool),
		prices:   make(map[string]string),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		state.mu.Lock()
		state.requests++
		state.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/products/prod_individual":
			state.mu.Lock()
			_, ok := state.products["prod_individual"]
			state.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"No such product"}}`)
				return
			}
			io.WriteString(w, `{"id":"prod_individual","object":"product","name":"Individual"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/products":
			id := r.Form.Get("id")
			state.mu.Lock()
			state.products[id] = true
			state.productCreates++
			state.mu.Unlock()
			io.WriteString(w, `{"id":"`+id+`","object":"product"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/prices":
			lookupKey := r.Form.Get("lookup_keys[0]")
			state.mu.Lock()
			priceID, ok := state.prices[lookupKey]
			state.mu.Unlock()
			if ok {
				io.WriteString(w, fmt.Sprintf(`{"object":"list","data":[{"id":"%s","object":"price","lookup_key":"%s","product":"prod_individual"}],"has_more":false,"url":"/v1/prices"}`,
					priceID, lookupKey,
				))
				return
			}
			io.WriteString(w, `{"object":"list","data":[],"has_more":false,"url":"/v1/prices"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/prices":
			lookupKey := r.Form.Get("lookup_key")
			priceID := "price_" + lookupKey
			state.mu.Lock()
			state.prices[lookupKey] = priceID
			state.priceCreates++
			state.mu.Unlock()
			io.WriteString(w, fmt.Sprintf(`{"id":"%s","object":"price","lookup_key":"%s","product":"prod_individual"}`,
				priceID, lookupKey,
			))
		default:
			t.Errorf("unexpected fake Stripe request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"type":"invalid_request_error","code":"resource_missing","message":"not found"}}`)
		}
	}))

	t.Cleanup(srv.Close)
	return srv, state
}

func TestNewServerInstallPricesWhenBillingEnabled(t *testing.T) {
	fakeStripe, state := newBootStripeServer(t)

	env := stage.Test()
	env.SkipBilling = false
	env.StripeURL = fakeStripe.URL

	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
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
		MetricsRegistry:    prometheus.NewRegistry(),
		LMTPSocketPath:     "",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { s.Stop() })

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.productCreates != 1 {
		t.Fatalf("product creates = %d, want 1", state.productCreates)
	}
	if state.priceCreates != 1 {
		t.Fatalf("price creates = %d, want 1", state.priceCreates)
	}
}

func TestNewServerSkipsInstallPricesWhenBillingDisabled(t *testing.T) {
	fakeStripe, state := newBootStripeServer(t)

	env := stage.Test()
	env.SkipBilling = true
	env.StripeURL = fakeStripe.URL

	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
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
		MetricsRegistry:    prometheus.NewRegistry(),
		LMTPSocketPath:     "",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { s.Stop() })

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.requests != 0 {
		t.Fatalf("Stripe requests = %d, want 0", state.requests)
	}
}
