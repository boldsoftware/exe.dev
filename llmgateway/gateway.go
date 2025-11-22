package llmgateway

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strings"
	"time"

	"exe.dev/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/ssh"
	"tailscale.com/net/tsaddr"
)

// Prometheus metrics
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

// RegisterMetrics registers all metrics with the provided registry
func RegisterMetrics(registry *prometheus.Registry) {
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
// - Designed to work with client applications that have configurable API endpoints and auth headers.
type llmGateway struct {
	now             func() time.Time
	db              *sqlite.DB
	boxKeyAuthority boxKeyAuthority
	apiKeys         APIKeys
	devMode         bool      // if true, accept "dev.key" as a valid API key
	testDebitDone   chan bool // for testing -- if non-nil, best effort send every time a debit occurs
	log             *slog.Logger
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

func NewGateway(log *slog.Logger, db *sqlite.DB, boxKeyAuthority boxKeyAuthority, apiKeys APIKeys, devMode bool) *llmGateway {
	ret := &llmGateway{
		now:             time.Now,
		db:              db,
		boxKeyAuthority: boxKeyAuthority,
		apiKeys:         apiKeys,
		devMode:         devMode,
		log:             log,
	}
	if apiKeys.Anthropic == "" || apiKeys.Fireworks == "" || apiKeys.OpenAI == "" {
		log.Warn("NewGateway: not all apiKeys are set", "apiKeys", apiKeys)
	}
	return ret
}

func (m *llmGateway) httpError(w http.ResponseWriter, r *http.Request, errstr string, code int) {
	http.Error(w, errstr, code)
	m.log.Error("llmgateway.httpError", "method", r.Method, "path", r.URL.Path, "code", code, "error", errstr)
}

func (m *llmGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.log.Info("llmGateway.ServeHTTP", "r.URL.Path", r.URL.Path)

	// Authenticate request
	// If the request has "X-Exedev-Box: <boxname>" header,
	// AND if we're in dev mode OR the request is coming via our tailscale IP,
	// then we can consider it authenticated. Otherwise, use bearer auth.
	boxName := r.Header.Get("X-Exedev-Box")
	if boxName != "" {
		// Check if request is from dev mode or tailscale
		remoteAddr := r.RemoteAddr
		host, _, err := net.SplitHostPort(remoteAddr)
		if err != nil {
			host = remoteAddr
		}
		remoteIP, err := netip.ParseAddr(host)

		allowXExedevBox := false
		if err == nil {
			// Allow if in dev mode or coming from tailscale IP
			if m.devMode || tsaddr.IsTailscaleIP(remoteIP) {
				allowXExedevBox = true
			}
		}

		if allowXExedevBox {
			m.log.Info("authenticated via X-Exedev-Box header", "box", boxName, "remote_ip", host)
			// Strip the header before forwarding
			r.Header.Del("X-Exedev-Box")
		} else {
			// X-Exedev-Box header present but not authorized
			m.httpError(w, r, "X-Exedev-Box header not allowed from this IP", http.StatusUnauthorized)
			return
		}
	} else {
		// Fall back to bearer token authentication
		_, err := m.boxKeyAuth(r.Context(), r)
		if err != nil {
			m.httpError(w, r, "box key auth failed: "+err.Error(), http.StatusUnauthorized)
			return
		}
	}

	// Handle /ready endpoint after authentication
	// This ensures /ready validates that auth is working correctly
	if r.URL.Path == "/_/gateway/ready" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))
		return
	}

	m.log.Info("gateway proxying request -->", "method", r.Method, "url", r.URL)
	endpointPath := strings.TrimPrefix(r.URL.Path, "/_/gateway/")
	parts := strings.Split(endpointPath, "/")
	alias := parts[0]
	remainder := endpointPath[len(alias):]
	m.log.Info("llmGateway.ServeHTTP", "alias", alias, "remaimder", remainder)

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
	var err error
	switch alias {
	case "anthropic":
		proxy, err = m.createAnthropicProxy()
	case "openai":
		proxy, err = m.createOpenAIProxy()
	case "fireworks":
		proxy, err = m.createFireworksProxy()
	default:
		m.httpError(w, r, "unrecognized origin alias", http.StatusNotFound)
		return
	}
	if err != nil {
		m.httpError(w, r, err.Error(), http.StatusInternalServerError)
		return
	}
	proxy.ServeHTTP(w, r)
}

func (m *llmGateway) createAnthropicProxy() (*httputil.ReverseProxy, error) {
	if m.apiKeys.Anthropic == "" {
		return nil, fmt.Errorf("anthropic api key not configured")
	}
	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           m.db,
		apiType:      "anthropic",
		log:          m.log,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			m.log.Info("ReverseProxy.Rewrite", "r.Out.URL", r.Out.URL, "r.Out.Host", r.Out.Host, "r.Out.Header", r.Out.Header)
			r.Out.Header.Set("X-API-Key", m.apiKeys.Anthropic)
			r.Out.Host = "api.anthropic.com"
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = "api.anthropic.com"
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			m.log.Error("anthropic api gateway", "error", err)
			m.httpError(w, r, "anthropic api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, nil
}

func (m *llmGateway) createOpenAIProxy() (*httputil.ReverseProxy, error) {
	if m.apiKeys.OpenAI == "" {
		return nil, fmt.Errorf("anthropic api key not configured")
	}
	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           m.db,
		apiType:      "openai",
		log:          m.log,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			m.log.Info("ReverseProxy.Rewrite", "r.Out.URL", r.Out.URL, "r.Out.Host", r.Out.Host, "r.Out.Header", r.Out.Header)
			r.Out.Header.Set("Authorization", "Bearer "+m.apiKeys.OpenAI)
			r.Out.Header.Set("X-API-Key", m.apiKeys.OpenAI)
			r.Out.Host = "api.openai.com"
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = "api.openai.com"
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			m.log.Error("openai api gateway", "error", err)
			m.httpError(w, r, "openai api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, nil
}

func (m *llmGateway) createFireworksProxy() (*httputil.ReverseProxy, error) {
	if m.apiKeys.Fireworks == "" {
		return nil, fmt.Errorf("fireworks api key not configured")
	}
	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           m.db,
		apiType:      "fireworks",
		log:          m.log,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			m.log.Info("ReverseProxy.Rewrite", "r.Out.URL", r.Out.URL, "r.Out.Host", r.Out.Host, "r.Out.Header", r.Out.Header)
			r.Out.Header.Set("Authorization", "Bearer "+m.apiKeys.Fireworks)
			r.Out.Header.Set("X-API-Key", m.apiKeys.Fireworks)
			r.Out.Host = "api.fireworks.ai"
			r.Out.URL.Scheme = "https"
			r.Out.URL.Host = "api.fireworks.ai"
		},
		Transport:      transport,
		ModifyResponse: transport.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			m.log.Error("fireworks api gateway", "error", err)
			m.httpError(w, r, "fireworks api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, nil
}
