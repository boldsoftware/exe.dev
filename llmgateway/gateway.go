package llmgateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strings"
	"time"

	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/sqlite"
	"github.com/prometheus/client_golang/prometheus"
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
		[]string{"token_type", "model", "api_type", "vm_name", "user_id"},
	)

	// Counter for cost in USD by model
	costUSDCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_cost_usd_total",
			Help: "Total cost in USD by model",
		},
		[]string{"model", "api_type", "vm_name", "user_id"},
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
// - Authenticates client requests via X-Exedev-Box header from Tailscale IP addresses.
// - Performs account balance checks before allowing requests to continue.
// - Designed to work with client applications that have configurable API endpoints and auth headers.
type llmGateway struct {
	now           func() time.Time
	db            *sqlite.DB
	apiKeys       APIKeys
	devMode       bool      // if true, accept requests from any IP with X-Exedev-Box header
	testDebitDone chan bool // for testing -- if non-nil, best effort send every time a debit occurs
	log           *slog.Logger
	creditMgr     *CreditManager
}

type APIKeys struct {
	Anthropic string
	Fireworks string
	OpenAI    string
}

func NewGateway(log *slog.Logger, db *sqlite.DB, apiKeys APIKeys, devMode bool) *llmGateway {
	ret := &llmGateway{
		now:       time.Now,
		db:        db,
		apiKeys:   apiKeys,
		devMode:   devMode,
		log:       log,
		creditMgr: NewCreditManager(db),
	}
	if apiKeys.Anthropic == "" || apiKeys.Fireworks == "" || apiKeys.OpenAI == "" {
		log.Warn("NewGateway: not all apiKeys are set", "apiKeys", apiKeys)
	}
	return ret
}

func (m *llmGateway) httpError(w http.ResponseWriter, r *http.Request, errstr string, code int) {
	http.Error(w, errstr, code)
	boxName := r.Header.Get("X-Exedev-Box")
	m.log.ErrorContext(r.Context(), "llmgateway.httpError", "method", r.Method, "path", r.URL.Path, "code", code, "error", errstr, "boxName", boxName)
}

func (m *llmGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.log.InfoContext(r.Context(), "llmGateway.ServeHTTP", "r.URL.Path", r.URL.Path)

	// Authenticate request
	// The request must come from a Tailscale IP (or be in devMode),
	// AND must have "X-Exedev-Box: <boxname>" header.

	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !m.devMode && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
		// This is super sketchy.
		// Someone on the public internet is trying to access our gateway.
		m.httpError(w, r, "hey go away", http.StatusUnauthorized)
		return
	}

	boxName := r.Header.Get("X-Exedev-Box")
	if boxName == "" {
		m.httpError(w, r, "X-Exedev-Box header required", http.StatusUnauthorized)
		return
	}

	// Look up the box to get the user ID for logging and metrics
	userID := ""
	if err := m.db.Rx(r.Context(), func(ctx context.Context, rx *sqlite.Rx) error {
		box, err := exedb.New(rx.Conn()).BoxNamed(ctx, boxName)
		if err != nil {
			return err
		}
		userID = box.CreatedByUserID
		return nil
	}); err != nil {
		m.log.WarnContext(r.Context(), "failed to look up box for user ID", "box", boxName, "error", err)
	}

	// Strip the header before forwarding
	r.Header.Del("X-Exedev-Box")

	// Handle /ready endpoint after authentication
	// This ensures /ready validates that auth is working correctly
	if r.URL.Path == "/_/gateway/ready" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))
		return
	}

	// Check credit before allowing LLM request
	if userID != "" {
		creditInfo, err := m.creditMgr.CheckAndRefreshCredit(r.Context(), userID)
		if err != nil {
			if errors.Is(err, ErrInsufficientCredit) {
				m.log.WarnContext(r.Context(), "insufficient LLM credit", "user_id", userID, "box", boxName, "available_usd", creditInfo.Available)
				m.httpError(w, r, "insufficient gateway credit", http.StatusPaymentRequired)
				return
			}
			m.httpError(w, r, "failed to check gateway credit", http.StatusInternalServerError)
			return
		}
	}

	endpointPath := strings.TrimPrefix(r.URL.Path, "/_/gateway/")
	parts := strings.Split(endpointPath, "/")
	alias := parts[0]
	remainder := endpointPath[len(alias):]
	m.log.InfoContext(r.Context(), "llmGateway.ServeHTTP", "alias", alias, "remaimder", remainder, "method", r.Method, "url", r.URL, "boxname", boxName)

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
	var transport *accountingTransport
	var proxyErr error
	switch alias {
	case "anthropic":
		proxy, transport, proxyErr = m.createAnthropicProxy(r, boxName, userID)
	case "openai":
		proxy, transport, proxyErr = m.createOpenAIProxy(r, boxName, userID)
	case "fireworks":
		proxy, transport, proxyErr = m.createFireworksProxy(r, boxName, userID)
	default:
		m.httpError(w, r, "unrecognized origin alias "+alias, http.StatusNotFound)
		return
	}
	if proxyErr != nil {
		m.httpError(w, r, proxyErr.Error(), http.StatusInternalServerError)
		return
	}
	proxy.ServeHTTP(w, r)
	// For SSE responses, wait for processing to complete and add slog attributes
	transport.WaitAndAddSSEAttributes()
}

func (m *llmGateway) createAnthropicProxy(incomingReq *http.Request, boxName, userID string) (*httputil.ReverseProxy, *accountingTransport, error) {
	if m.apiKeys.Anthropic == "" {
		return nil, nil, fmt.Errorf("anthropic api key not configured")
	}
	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           m.db,
		apiType:      "anthropic",
		log:          m.log,
		creditMgr:    m.creditMgr,
		incomingReq:  incomingReq,
		boxName:      boxName,
		userID:       userID,
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
			if errors.Is(err, context.Canceled) || errors.Is(err, errBodyNotReplayable) {
				return
			}
			m.log.Error("anthropic api gateway", "error", err)
			m.httpError(w, r, "anthropic api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, transport, nil
}

func (m *llmGateway) createOpenAIProxy(incomingReq *http.Request, boxName, userID string) (*httputil.ReverseProxy, *accountingTransport, error) {
	if m.apiKeys.OpenAI == "" {
		return nil, nil, fmt.Errorf("openai api key not configured")
	}
	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           m.db,
		apiType:      "openai",
		log:          m.log,
		creditMgr:    m.creditMgr,
		incomingReq:  incomingReq,
		boxName:      boxName,
		userID:       userID,
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
			if errors.Is(err, context.Canceled) || errors.Is(err, errBodyNotReplayable) {
				return
			}
			m.log.Error("openai api gateway", "error", err)
			m.httpError(w, r, "openai api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, transport, nil
}

func (m *llmGateway) createFireworksProxy(incomingReq *http.Request, boxName, userID string) (*httputil.ReverseProxy, *accountingTransport, error) {
	if m.apiKeys.Fireworks == "" {
		return nil, nil, fmt.Errorf("fireworks api key not configured")
	}
	transport := &accountingTransport{
		RoundTripper: http.DefaultTransport,
		db:           m.db,
		apiType:      "fireworks",
		log:          m.log,
		creditMgr:    m.creditMgr,
		incomingReq:  incomingReq,
		boxName:      boxName,
		userID:       userID,
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
			if errors.Is(err, context.Canceled) || errors.Is(err, errBodyNotReplayable) {
				return
			}
			m.log.Error("fireworks api gateway", "error", err)
			m.httpError(w, r, "fireworks api gateway error: "+err.Error(), http.StatusBadGateway)
		},
	}

	return proxy, transport, nil
}
