package llmgateway

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strings"
	"time"

	"exe.dev/domz"
	"exe.dev/exedb"
	"exe.dev/llmpricing"
	"exe.dev/sqlite"
	"exe.dev/stage"
	"github.com/prometheus/client_golang/prometheus"
	sloghttp "github.com/samber/slog-http"
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
	env           stage.Env
	testDebitDone chan bool // for testing -- if non-nil, best effort send every time a debit occurs
	log           *slog.Logger
	creditMgr     *CreditManager
}

type APIKeys struct {
	Anthropic string
	Fireworks string
	OpenAI    string
}

func NewGateway(log *slog.Logger, db *sqlite.DB, apiKeys APIKeys, env stage.Env) *llmGateway {
	ret := &llmGateway{
		now:       time.Now,
		db:        db,
		apiKeys:   apiKeys,
		env:       env,
		log:       log,
		creditMgr: NewCreditManager(db),
	}
	if apiKeys.Anthropic == "" || apiKeys.Fireworks == "" || apiKeys.OpenAI == "" {
		log.Warn("NewGateway: not all apiKeys are set", "apiKeys", apiKeys)
	}
	return ret
}

// httpError reports an error on w.
// userMsg is shown to the user; err (if non-nil) is logged but not shown.
func (m *llmGateway) httpError(w http.ResponseWriter, r *http.Request, userMsg string, code int, boxName string, err error) {
	http.Error(w, userMsg, code)
	var errStr string
	if err != nil {
		errStr = err.Error()
	}
	var logger func(context.Context, string, ...any)
	switch {
	case strings.Contains(errStr, "stream error"),
		strings.Contains(errStr, "unexpected end of JSON"),
		strings.Contains(errStr, "stream closed"),
		strings.Contains(errStr, "client disconnected"):
		// Client cancelled request (HTTP/2 stream cancel). Not an error.
		logger = m.log.InfoContext
	case code == http.StatusPaymentRequired:
		// Running out of LLM credit is not an error.
		logger = m.log.InfoContext
	case code == http.StatusNotFound:
		// This is probably a user poking around the gateway.
		// Possibly sketchy...but not necessarily an error.
		logger = m.log.WarnContext
	default:
		logger = m.log.ErrorContext
	}
	logger(r.Context(), "llmgateway.httpError", "method", r.Method, "path", r.URL.Path, "code", code, "error", userMsg, "boxName", boxName, "cause", err)
}

func (m *llmGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.log.InfoContext(r.Context(), "llmGateway.ServeHTTP", "r.URL.Path", r.URL.Path)

	// Authenticate request
	// The request must come from a Tailscale IP (or be in devMode),
	// AND must have "X-Exedev-Box: <boxname>" header.

	boxName := r.Header.Get("X-Exedev-Box")
	host := domz.StripPort(r.RemoteAddr)
	remoteIP, err := netip.ParseAddr(host)
	if !m.env.GatewayDev && (err != nil || !tsaddr.IsTailscaleIP(remoteIP)) {
		// This is super sketchy.
		// Someone on the public internet is trying to access our gateway.
		m.httpError(w, r, "hey go away", http.StatusUnauthorized, boxName, nil)
		return
	}

	if boxName == "" {
		m.httpError(w, r, "X-Exedev-Box header required", http.StatusUnauthorized, boxName, nil)
		return
	}

	// Look up the box to get the user ID for logging and metrics
	box, err := exedb.WithRxRes1(m.db, r.Context(), (*exedb.Queries).BoxNamed, boxName)
	if errors.Is(err, sql.ErrNoRows) {
		m.httpError(w, r, "VM not found", http.StatusUnauthorized, boxName, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		return // Client disconnected
	}
	if err != nil {
		m.httpError(w, r, "internal server error", http.StatusInternalServerError, boxName, fmt.Errorf("failed to look up box: %w", err))
		return
	}
	userID := box.CreatedByUserID
	if userID == "" {
		m.httpError(w, r, "user not found", http.StatusInternalServerError, boxName, fmt.Errorf("could not determine user ID for box %s", boxName))
		return
	}

	// Strip the header before forwarding
	r.Header.Del("X-Exedev-Box")

	// Extract Shelley conversation ID and version for logging
	conversationID := r.Header.Get("Shelley-Conversation-Id")
	userAgent := r.Header.Get("User-Agent")
	shelleyVersion := parseShelleyVersion(userAgent)
	if conversationID != "" {
		sloghttp.AddCustomAttributes(r, slog.String("conversation_id", conversationID))
	}
	if userAgent != "" {
		sloghttp.AddCustomAttributes(r, slog.String("user_agent", userAgent))
	}
	if shelleyVersion != "" {
		sloghttp.AddCustomAttributes(r, slog.String("shelley_version", shelleyVersion))
	}

	// Handle /ready endpoint after authentication
	// This ensures /ready validates that auth is working correctly
	if r.URL.Path == "/_/gateway/ready" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK\n"))
		return
	}

	// Check credit before allowing LLM request
	creditInfo, err := m.creditMgr.CheckAndRefreshCredit(r.Context(), userID)
	if errors.Is(err, ErrInsufficientCredit) {
		m.log.WarnContext(r.Context(), "insufficient LLM credit", "user_id", userID, "box", boxName, "available_usd", creditInfo.Available, "plan", creditInfo.Plan.Name)
		m.httpError(w, r, creditInfo.Plan.CreditExhaustedError, http.StatusPaymentRequired, boxName, nil)
		return
	}
	if errors.Is(err, context.Canceled) {
		return // Client disconnected
	}
	if err != nil {
		m.httpError(w, r, "failed to check gateway credit", http.StatusInternalServerError, boxName, err)
		return
	}

	endpointPath := strings.TrimPrefix(r.URL.Path, "/_/gateway/")
	parts := strings.Split(endpointPath, "/")
	alias := parts[0]
	remainder := endpointPath[len(alias):]
	m.log.InfoContext(r.Context(), "llmGateway.ServeHTTP", "alias", alias, "remaimder", remainder, "method", r.Method, "url", r.URL, "boxname", boxName)

	requestsCounter.WithLabelValues("attempted", alias).Inc()

	// Check if this endpoint is blocked (e.g., image generation with per-image pricing)
	if isBlockedEndpoint(remainder) {
		m.httpError(w, r, "endpoint not supported: "+remainder, http.StatusForbidden, boxName, nil)
		return
	}

	// Map alias to provider
	var provider llmpricing.Provider
	switch alias {
	case "anthropic":
		provider = llmpricing.ProviderAnthropic
	case "openai":
		provider = llmpricing.ProviderOpenAI
	case "fireworks":
		provider = llmpricing.ProviderFireworks
	default:
		m.httpError(w, r, "unrecognized origin alias "+alias, http.StatusNotFound, boxName, nil)
		return
	}

	// Extract model from request body and validate it's allowed
	// We need to buffer the body to read it and then replay it for the proxy
	model, bodyBytes, err := extractModelFromRequest(r)
	if err != nil {
		m.httpError(w, r, "failed to parse request body: "+err.Error(), http.StatusBadRequest, boxName, err)
		return
	}

	// Check if the model is in our allowlist (only if a model was specified)
	if model != "" {
		if !llmpricing.IsModelAllowed(provider, model) {
			// TODO: reject unknown models once we have complete coverage
			// For now, log and allow through
			m.log.WarnContext(r.Context(), "model not in allowlist (allowing for now)",
				"provider", provider,
				"model", model,
				"user_id", userID,
				"box", boxName,
			)
			sloghttp.AddCustomAttributes(r, slog.String("unknown_model", model))
			sloghttp.AddCustomAttributes(r, slog.Bool("model_not_in_allowlist", true))
		}
		sloghttp.AddCustomAttributes(r, slog.String("requested_model", model))
	}

	// Restore the body for the proxy
	if bodyBytes != nil {
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
	}

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
	}
	if proxyErr != nil {
		m.httpError(w, r, "proxy configuration error", http.StatusInternalServerError, boxName, proxyErr)
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
		provider:     llmpricing.ProviderAnthropic,
		log:          m.log,
		creditMgr:    m.creditMgr,
		incomingReq:  incomingReq,
		boxName:      boxName,
		userID:       userID,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			r.Out.Header.Set("X-API-Key", m.apiKeys.Anthropic)
			r.Out.Header.Set("Accept-Encoding", "gzip")
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
			m.httpError(w, r, "anthropic api gateway error", http.StatusBadGateway, boxName, err)
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
		provider:     llmpricing.ProviderOpenAI,
		log:          m.log,
		creditMgr:    m.creditMgr,
		incomingReq:  incomingReq,
		boxName:      boxName,
		userID:       userID,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			m.log.InfoContext(r.In.Context(), "ReverseProxy.Rewrite", "r.Out.URL", r.Out.URL, "r.Out.Host", r.Out.Host, "r.Out.Header", r.Out.Header)
			r.Out.Header.Set("Authorization", "Bearer "+m.apiKeys.OpenAI)
			r.Out.Header.Set("X-API-Key", m.apiKeys.OpenAI)
			r.Out.Header.Set("Accept-Encoding", "gzip")
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
			m.httpError(w, r, "openai api gateway error", http.StatusBadGateway, boxName, err)
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
		provider:     llmpricing.ProviderFireworks,
		log:          m.log,
		creditMgr:    m.creditMgr,
		incomingReq:  incomingReq,
		boxName:      boxName,
		userID:       userID,
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.Header.Del("Authorization") // Remove our bearer token so we don't leak them to the origin server.
			m.log.InfoContext(r.In.Context(), "ReverseProxy.Rewrite", "r.Out.URL", r.Out.URL, "r.Out.Host", r.Out.Host, "r.Out.Header", r.Out.Header)
			r.Out.Header.Set("Authorization", "Bearer "+m.apiKeys.Fireworks)
			r.Out.Header.Set("X-API-Key", m.apiKeys.Fireworks)
			r.Out.Header.Set("Accept-Encoding", "gzip")
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
			m.httpError(w, r, "fireworks api gateway error", http.StatusBadGateway, boxName, err)
		},
	}

	return proxy, transport, nil
}

// parseShelleyVersion extracts the version from a User-Agent header like "Shelley/abcd1234".
// Returns an empty string if the User-Agent doesn't match the expected format.
func parseShelleyVersion(userAgent string) string {
	const prefix = "Shelley/"
	if !strings.HasPrefix(userAgent, prefix) {
		return ""
	}
	version := strings.TrimPrefix(userAgent, prefix)
	// The version might have additional content after a space (e.g., "Shelley/abcd1234 other-stuff")
	if idx := strings.Index(version, " "); idx != -1 {
		version = version[:idx]
	}
	return version
}

// extractModelFromRequest extracts the model name from the request body.
// Returns the model name, the full body bytes (for replay), and any error.
// If no body or no model field, returns empty string with nil error.
func extractModelFromRequest(r *http.Request) (string, []byte, error) {
	if r.Body == nil {
		return "", nil, nil
	}

	// Read the body
	bodyBytes, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return "", nil, fmt.Errorf("failed to read request body: %w", err)
	}

	if len(bodyBytes) == 0 {
		return "", nil, nil
	}

	// Parse just the model field - both Anthropic and OpenAI/Fireworks use
	// {"model": "..."} at the top level of their request bodies
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return "", bodyBytes, fmt.Errorf("invalid JSON in request body: %w", err)
	}

	return req.Model, bodyBytes, nil
}

// isBlockedEndpoint returns true if the endpoint path should be blocked.
// Some endpoints (like image generation) have per-image pricing that we don't support.
func isBlockedEndpoint(path string) bool {
	blockedPrefixes := []string{
		"/v1/images/",      // OpenAI image generation
		"/v1/audio/speech", // OpenAI TTS (per-character pricing)
	}
	for _, prefix := range blockedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
