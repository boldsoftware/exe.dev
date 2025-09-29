package llmgateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"exe.dev/accounting"
	"exe.dev/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/ssh"
)

// Prometheus metrics for accounting
var (
	// Single counter for all token types with token_type label
	tokensCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_tokens_total",
			Help: "Total number of tokens by type, model and API type",
		},
		[]string{"token_type", "model", "api_type"},
	)

	// Counter for cost in USD by model
	costUSDCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_cost_usd_total",
			Help: "Total cost in USD by model",
		},
		[]string{"model", "api_type"},
	)

	// Counter for requests proxied
	requestsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_requests_total",
			Help: "Total number of requests proxied",
		},
		[]string{"status", "api_type"},
	)

	// Histogram for request latencies
	requestLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llm_request_duration_seconds",
			Help:    "Request latency distribution",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 10), // Start at 100ms, double 10 times
		},
		[]string{"model", "api_type"},
	)

	// Gauge for Anthropic rate limits
	anthropicRateLimitGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "anthropic_rate_limit_remaining",
			Help: "Remaining Anthropic rate limits by type",
		},
		[]string{"type"},
	)
)

// RegisterAccountingMetrics registers all accounting metrics with the provided registry
func RegisterAccountingMetrics(registry *prometheus.Registry) {
	registry.MustRegister(
		tokensCounter,
		costUSDCounter,
		requestsCounter,
		requestLatency,
		anthropicRateLimitGauge,
	)
}

// llmGateway is a proxy for API calls to various LLM services.
// - Authenticates client requests to verify that they are coming from known box names.
// - Performs account balance checks before allowing requests to continue.
// - Debits an associated billing account with the cost of handling the API call
// - Designed to work with client applications that have configurable API endpoints and auth headers.
type llmGateway struct {
	now             func() time.Time
	mux             http.ServeMux
	accountant      *accounting.Accountant
	db              *sqlite.DB
	boxKeyAuthority boxKeyAuthority
	apiKeys         APIKeys
	testDebitDone   chan bool // for testing -- if non-nil, best effort send every time a debit occurs
}

type APIKeys struct {
	Anthropic string
	Fireworks string
	OpenAI    string
}

type boxKeyAuthority interface {
	// SSHIdentityKeyForBox returns the public key portion of the ssh server identity for the given boxy name.
	SSHIdentityKeyForBox(ctx context.Context, name string) (ssh.PublicKey, error)
}

func NewGateway(accountant *accounting.Accountant, db *sqlite.DB, boxKeyAuthority boxKeyAuthority,
	apiKeys APIKeys,
) *llmGateway {
	ret := &llmGateway{
		now:             time.Now,
		accountant:      accountant,
		db:              db,
		boxKeyAuthority: boxKeyAuthority,
		apiKeys:         apiKeys,
	}
	if apiKeys.Anthropic == "" || apiKeys.Fireworks == "" || apiKeys.OpenAI == "" {
		slog.Warn("NewGateway: not all apiKeys are set", "apiKeys", apiKeys)
	}
	return ret
}

func httpError(w http.ResponseWriter, r *http.Request, errstr string, code int) {
	http.Error(w, errstr, code)
	slog.Error("llmgateway.httpError", "method", r.Method, "path", r.URL.Path, "code", code, "error", errstr)
}

func (a *llmGateway) checkCredits(ctx context.Context, billingAccountID string) error {
	// Get the current balance for the user
	var balance float64
	err := a.db.Rx(ctx, func(ctx context.Context, rx *sqlite.Rx) error {
		var err error
		balance, err = a.accountant.GetBalance(ctx, rx, billingAccountID)
		return err
	})
	if err != nil {
		slog.Error("accountingTransport.checkCredits: GetBalance failed", "error", err)
		// Fallback to allowing the request if we can't check balance
		return nil
	}

	// If balance is insufficient, reject the request
	if balance <= 0 {
		return fmt.Errorf("your account balance of $%.2f is insufficient - please purchase more credits at %s, and then ask the agent to continue", balance, "https://exe.dev/buy")
	}
	return nil
}

func (m *llmGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slog.Info("llmGateway.ServeHTTP", "r.URL.Path", r.URL.Path)

	// authenticate request
	boxName, err := m.boxKeyAuth(r.Context(), r)
	if err != nil {
		httpError(w, r, "box key auth failed: "+err.Error(), http.StatusUnauthorized)
		return
	}

	var billingID string
	err = m.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		var err error
		billingID, err = m.accountant.BillingAccountForBox(ctx, rx, boxName)
		return err
	})
	if err != nil {
		slog.Error("llmGateway.ServeHTTP", "BillingAccountForBox error", err)
	}

	slog.Info("gateway proxying request -->", "method", r.Method, "url", r.URL)
	endpointPath := strings.TrimPrefix(r.URL.Path, "/_/gateway/")
	parts := strings.Split(endpointPath, "/")
	alias := parts[0]
	remainder := endpointPath[len(alias):]
	slog.Info("llmGateway.ServeHTTP", "alias", alias, "remaimder", remainder)

	requestsCounter.WithLabelValues("attempted", alias).Inc()

	// Construct filtered header to send to origin server
	hh := http.Header{}
	for hk := range r.Header {
		if hk == "X-Api-Key" || hk == "Authorization" { // filter out any auth tokens or API keys passed to us.
			continue
		}
		if hv, ok := r.Header[hk]; ok {
			hh[hk] = hv
		}
	}
	r.URL.Path = remainder
	var proxy *httputil.ReverseProxy
	switch alias {
	case "anthropic":
		proxy, err = m.createAnthropicProxy(billingID)
	case "openai":
		proxy, err = m.createOpenAIProxy(billingID)
	case "fireworks":
		proxy, err = m.createFireworksProxy(billingID)
	default:
		httpError(w, r, "unrecognized origin alias", http.StatusNotFound)
		return
	}
	if err != nil {
		httpError(w, r, err.Error(), http.StatusInternalServerError)
		return
	}
	proxy.ServeHTTP(w, r)
}

func (m *llmGateway) createAnthropicProxy(billingAccountID string) (*httputil.ReverseProxy, error) {
	transport := &accountingTransport{
		RoundTripper:     http.DefaultTransport,
		accountant:       m.accountant,
		db:               m.db,
		billingAccountID: billingAccountID,
		apiType:          "anthropic",
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			slog.Info("ReverseProxy.Rewrite", "r.Out.URL", r.Out.URL, "r.Out.Host", r.Out.Host, "r.Out.Header", r.Out.Header)
			r.Out.Header.Set("X-API-Key", m.apiKeys.Anthropic)
			r.Out.Host = "api.anthropic.com"
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = "api.anthropic.com"
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("anthropic api gateway", "error", err)
			httpError(w, r, "anthropic api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, nil
}

func (m *llmGateway) createOpenAIProxy(billingAccountID string) (*httputil.ReverseProxy, error) {
	transport := &accountingTransport{
		RoundTripper:     http.DefaultTransport,
		accountant:       m.accountant,
		db:               m.db,
		billingAccountID: billingAccountID,
		apiType:          "openai",
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			slog.Info("ReverseProxy.Rewrite", "r.Out.URL", r.Out.URL, "r.Out.Host", r.Out.Host, "r.Out.Header", r.Out.Header)
			r.Out.Header.Set("Authorization", "Bearer "+m.apiKeys.OpenAI)
			r.Out.Header.Set("X-API-Key", m.apiKeys.OpenAI)
			r.Out.Host = "api.openai.com"
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = "api.openai.com"
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("openai api gateway", "error", err)
			httpError(w, r, "openai api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, nil
}

func (m *llmGateway) createFireworksProxy(billingAccountID string) (*httputil.ReverseProxy, error) {
	transport := &accountingTransport{
		RoundTripper:     http.DefaultTransport,
		accountant:       m.accountant,
		db:               m.db,
		billingAccountID: billingAccountID,
		apiType:          "fireworks",
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			slog.Info("ReverseProxy.Rewrite", "r.Out.URL", r.Out.URL, "r.Out.Host", r.Out.Host, "r.Out.Header", r.Out.Header)
			r.Out.Header.Set("Authorization", "Bearer "+m.apiKeys.Fireworks)
			r.Out.Header.Set("X-API-Key", m.apiKeys.Fireworks)
			r.Out.Host = "api.fireworks.ai"
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = "api.fireworks.ai"
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("fireworks api gateway", "error", err)
			httpError(w, r, "fireworks api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, nil
}
