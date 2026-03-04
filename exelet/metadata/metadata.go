// Package metadata implements an HTTP metadata service for VMs running in the exelet.
//
// # Networking
//
// The metadata service listens on 169.254.169.254:80, which is added as a secondary
// IP address to the NAT bridge interface (br-exe). VMs are configured with a route
// that sends traffic destined for 169.254.169.254 through their default gateway,
// which directs it to the bridge where the metadata service listens.
//
// Each VM connects to the bridge via an isolated TAP device, ensuring that:
// - VMs cannot communicate with each other
// - Source IP addresses cannot be spoofed
// - The metadata service can reliably identify which VM made a request
//
// # API
//
// The metadata service currently provides a single endpoint:
//
//	GET /  - Returns metadata about the requesting VM in JSON format
//
// Response format:
//
//	{
//	  "name": "<box-name>",
//	  "source_ip": "<ip-address>"
//	}
//
// If the source IP cannot be mapped to an instance (e.g., during VM startup),
// name will be an empty string.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"exe.dev/tracing"
	"github.com/prometheus/client_golang/prometheus"
	sloghttp "github.com/samber/slog-http"
	"tailscale.com/net/tsaddr"
	"tailscale.com/util/singleflight"
)

const (
	// MetadataIP is the IP address where the metadata service listens
	MetadataIP = "169.254.169.254"
	// MetadataPort is the port where the metadata service listens
	MetadataPort = 80
)

// InstanceLookup provides a method to look up instances by IP address
type InstanceLookup interface {
	GetInstanceByIP(ctx context.Context, ip string) (id, name string, err error)
}

// Service provides a metadata HTTP service for VMs to query
type Service struct {
	log            *slog.Logger
	server         *http.Server
	instanceLookup InstanceLookup
	exedURL        string
	exedTargetURL  *url.URL
	listenAddr     string // actual address to bind to (may differ from MetadataIP for isolation)
	gatewayDev     bool   // true in local/test stages; relaxes outbound local-address checks

	gatewayRequests *prometheus.CounterVec

	integrationCacheMu sync.Mutex
	integrationCache   map[integrationCacheKey]*integrationCacheEntry
	integrationSF      singleflight.Group[integrationCacheKey, *integrationCacheEntry]
}

type integrationCacheKey struct {
	vmName          string
	integrationName string
}

type integrationCacheEntry struct {
	ok        bool
	typ       string
	config    string
	fetchedAt time.Time
}

// IntegrationCacheTTL is the default cache duration for integration configs.
// Exported so tests can shorten it.
var IntegrationCacheTTL = 1 * time.Minute

// NewService creates a new metadata service.
// listenAddr is the IP:port to bind to (e.g., "192.168.1.1:80").
// gatewayDev relaxes outbound local-address checks for dev environments
// where connections legitimately route through private interfaces.
func NewService(log *slog.Logger, computeSvc InstanceLookup, exedURL, listenAddr string, gatewayDev bool, registry *prometheus.Registry) (*Service, error) {
	if exedURL == "" {
		return nil, fmt.Errorf("exedURL is required")
	}
	if listenAddr == "" {
		return nil, fmt.Errorf("listenAddr is required")
	}

	targetURL, err := url.Parse(exedURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse exed URL: %w", err)
	}

	gatewayRequests := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "exelet",
			Subsystem: "metadata",
			Name:      "gateway_requests_total",
			Help:      "Total LLM gateway proxy requests by outcome.",
		},
		[]string{"outcome"},
	)
	if registry != nil {
		registry.MustRegister(gatewayRequests)
	}

	s := &Service{
		log:              log,
		instanceLookup:   computeSvc,
		exedURL:          exedURL,
		exedTargetURL:    targetURL,
		listenAddr:       listenAddr,
		gatewayDev:       gatewayDev,
		gatewayRequests:  gatewayRequests,
		integrationCache: make(map[integrationCacheKey]*integrationCacheEntry),
	}

	return s, nil
}

// Start starts the metadata HTTP server
func (s *Service) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", s.handleRoot)

	// Add gateway proxy handler
	mux.HandleFunc("/gateway/llm/", s.handleGatewayProxy)

	// Add email proxy handler
	mux.HandleFunc("POST /gateway/email/send", s.handleEmailProxy)

	// Wrap the mux with integration proxy routing.
	// Requests to *.int.exe.cloud are routed to the integration proxy handler;
	// everything else goes to the normal mux.
	var mainHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, ok := integrationHostName(r.Host); ok {
			s.handleIntegrationProxy(w, r, name)
			return
		}
		mux.ServeHTTP(w, r)
	})

	// Build the handler with logging middleware chain.
	// The middleware chain (in order of execution) is:
	//  1. tracing.HTTPMiddleware - generates trace_id and adds to context
	//  2. sloghttp middleware - captures request/response and logs
	//  3. customAttrsMiddleware - adds custom attributes after handler runs
	handler := s.loggerMiddleware(mainHandler)

	s.server = &http.Server{
		Addr:    s.listenAddr,
		Handler: handler,
	}

	s.log.InfoContext(ctx, "starting metadata service", "addr", s.listenAddr)

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.ErrorContext(ctx, "metadata service error", "err", err)
		}
	}()

	return nil
}

// loggerMiddleware builds the logging middleware chain.
// This is a "CANONICAL LOG LINE". If at all possible, don't filter these,
// so taht we can reliably see what's going on in HTTP based on these.
func (s *Service) loggerMiddleware(next http.Handler) http.Handler {
	slogConfig := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
		WithRequestID:    false,
	}

	// Build chain from inside out: 3 -> 2 -> 1
	h := s.customAttrsMiddleware(next)
	h = sloghttp.NewWithConfig(s.log, slogConfig)(h)
	h = tracing.HTTPMiddleware(h)
	return h
}

// customAttrsMiddleware adds custom log attributes after the handler runs.
func (s *Service) customAttrsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		sloghttp.AddCustomAttributes(r, slog.String("log_type", "http_request"))
		sloghttp.AddCustomAttributes(r, slog.String("method", r.Method))
		if uri := r.URL.RequestURI(); uri != "" {
			sloghttp.AddCustomAttributes(r, slog.String("uri", uri))
		}
		if remoteIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			sloghttp.AddCustomAttributes(r, slog.String("remote_ip", remoteIP))
		}
	})
}

// Stop stops the metadata HTTP server
func (s *Service) Stop(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// MetadataResponse is the JSON response returned by the metadata service
type MetadataResponse struct {
	Name     string `json:"name"`
	SourceIP string `json:"source_ip"`
}

// handleRoot handles requests to the root endpoint
func (s *Service) handleRoot(w http.ResponseWriter, r *http.Request) {
	sourceIP := requestSourceIP(r)
	response := MetadataResponse{
		SourceIP: sourceIP,
	}

	// Look up instance information
	// TODO(philip): Beware that GetInstanceByIP is linear in reading some JSON files!
	if s.instanceLookup != nil {
		if _, name, err := s.instanceLookup.GetInstanceByIP(r.Context(), sourceIP); err == nil {
			response.Name = name
			sloghttp.AddCustomAttributes(r, slog.String("vm_name", name))
		} else {
			s.log.DebugContext(r.Context(), "failed to lookup instance", "ip", sourceIP, "error", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(response); err != nil {
		s.log.ErrorContext(r.Context(), "failed to encode JSON response", "error", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleGatewayProxy proxies requests to the LLM gateway on exed
func (s *Service) handleGatewayProxy(w http.ResponseWriter, r *http.Request) {
	// Look up the box name for this connection
	sourceIP := requestSourceIP(r)
	_, boxName, err := s.instanceLookup.GetInstanceByIP(r.Context(), sourceIP)
	if err != nil {
		s.log.ErrorContext(r.Context(), "failed to lookup box by IP", "ip", sourceIP, "error", err)
		http.Error(w, "Failed to identify box", http.StatusInternalServerError)
		return
	}
	if boxName == "" {
		s.log.ErrorContext(r.Context(), "no box found for IP", "ip", sourceIP)
		http.Error(w, "No box found for this IP", http.StatusForbidden)
		return
	}

	// Rewrite the path to match the exed gateway endpoint
	// /gateway/llm/anthropic/... -> /_/gateway/anthropic/...
	originalPath := r.URL.Path
	// The path that comes in is /gateway/llm/FOO or /gateway/llm/_/gateway/FOO
	// We want to rewrite it to /_/gateway/FOO
	newPath1 := strings.Replace(originalPath, "/gateway/llm/_/gateway", "/gateway/llm", 1)
	newPath2 := strings.Replace(newPath1, "/gateway/llm", "/_/gateway", 1)
	r.URL.Path = newPath2

	sloghttp.AddCustomAttributes(r, slog.String("vm_name", boxName))
	sloghttp.AddCustomAttributes(r, slog.String("original_path", originalPath))
	sloghttp.AddCustomAttributes(r, slog.String("new_path", newPath2))

	// Create a reverse proxy for this request
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(s.exedTargetURL)
			pr.Out.URL.Path = pr.In.URL.Path
			pr.Out.Host = s.exedTargetURL.Host
			// Add header to identify the box making the request
			pr.Out.Header.Set("X-Exedev-Box", boxName)
			// Propagate trace_id to downstream service
			tracing.SetTraceIDHeader(pr.In.Context(), pr.Out.Header)
		},
		ModifyResponse: func(resp *http.Response) error {
			s.gatewayRequests.WithLabelValues("success").Inc()
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) {
				return
			}
			http.Error(w, "gateway proxy error: "+err.Error(), http.StatusBadGateway)
			switch {
			case errors.Is(err, syscall.ECONNREFUSED):
				s.gatewayRequests.WithLabelValues("conn_refused").Inc()
				// This typically happens in bursts when we restart exed. Warn only.
				s.log.WarnContext(r.Context(), "gateway proxy conn refused", "error", err, "box", boxName)
			default:
				s.gatewayRequests.WithLabelValues("unknown_error").Inc()
				s.log.ErrorContext(r.Context(), "gateway proxy error", "error", err, "box", boxName)
			}
		},
	}

	proxy.ServeHTTP(w, r)
}

// handleEmailProxy proxies email send requests to exed
func (s *Service) handleEmailProxy(w http.ResponseWriter, r *http.Request) {
	// Look up the box name for this connection
	sourceIP := requestSourceIP(r)
	_, boxName, err := s.instanceLookup.GetInstanceByIP(r.Context(), sourceIP)
	if err != nil {
		s.log.ErrorContext(r.Context(), "failed to lookup box by IP", "ip", sourceIP, "error", err)
		http.Error(w, "Failed to identify box", http.StatusInternalServerError)
		return
	}
	if boxName == "" {
		s.log.ErrorContext(r.Context(), "no box found for IP", "ip", sourceIP)
		http.Error(w, "No box found for this IP", http.StatusForbidden)
		return
	}

	sloghttp.AddCustomAttributes(r, slog.String("vm_name", boxName))

	// Create a reverse proxy for this request
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(s.exedTargetURL)
			pr.Out.URL.Path = "/_/gateway/email/send"
			pr.Out.Host = s.exedTargetURL.Host
			// Add header to identify the box making the request
			pr.Out.Header.Set("X-Exedev-Box", boxName)
			// Propagate trace_id to downstream service
			tracing.SetTraceIDHeader(pr.In.Context(), pr.Out.Header)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) {
				return
			}
			http.Error(w, "email proxy error: "+err.Error(), http.StatusBadGateway)
			s.log.ErrorContext(r.Context(), "email proxy error", "error", err, "box", boxName)
		},
	}

	proxy.ServeHTTP(w, r)
}

// IntegrationHostSuffix is the domain suffix for integration proxy requests.
const IntegrationHostSuffix = ".int.exe.cloud"

// integrationHostName extracts the integration name from a Host header like
// "myproxy.int.exe.cloud" or "myproxy.int.exe.cloud:80".
// Returns the name and true if the host matches, or ("", false) otherwise.
func integrationHostName(host string) (string, bool) {
	// Strip port if present.
	h := host
	if i := strings.LastIndex(h, ":"); i != -1 {
		// Only strip if what's after : looks like a port (all digits).
		potentialPort := h[i+1:]
		allDigits := len(potentialPort) > 0
		for _, c := range potentialPort {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			h = h[:i]
		}
	}
	if !strings.HasSuffix(h, IntegrationHostSuffix) {
		return "", false
	}
	name := strings.TrimSuffix(h, IntegrationHostSuffix)
	if name == "" {
		return "", false
	}
	return name, true
}

// isValidIntegrationName checks whether name matches the creation-time rules
// (lowercase letters, digits, hyphens, 1-63 chars, no leading/trailing hyphen).
// This is the same validation as execore.validateIntegrationName but returns
// a bool (no error details needed at proxy time). Checked before any
// cache/network operations to prevent cache flooding.
func isValidIntegrationName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i, c := range name {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		if c == '-' && i > 0 && i < len(name)-1 {
			continue
		}
		return false
	}
	return true
}

// handleIntegrationProxy looks up integration config from exed (cached 1 min)
// and proxies the request directly to the target.
func (s *Service) handleIntegrationProxy(w http.ResponseWriter, r *http.Request, integrationName string) {
	if !isValidIntegrationName(integrationName) {
		http.Error(w, "invalid integration name", http.StatusBadRequest)
		return
	}

	sourceIP := requestSourceIP(r)
	_, vmName, err := s.instanceLookup.GetInstanceByIP(r.Context(), sourceIP)
	if err != nil {
		s.log.ErrorContext(r.Context(), "integration proxy: failed to lookup box by IP", "ip", sourceIP, "error", err)
		http.Error(w, "Failed to identify box", http.StatusInternalServerError)
		return
	}
	if vmName == "" {
		s.log.ErrorContext(r.Context(), "integration proxy: no box found for IP", "ip", sourceIP)
		http.Error(w, "No box found for this IP", http.StatusForbidden)
		return
	}

	sloghttp.AddCustomAttributes(r, slog.String("vm_name", vmName))
	sloghttp.AddCustomAttributes(r, slog.String("integration", integrationName))

	cfg, ok := s.getIntegrationConfig(r.Context(), vmName, integrationName)
	if !ok {
		http.Error(w, "integration not found or not attached to this VM", http.StatusForbidden)
		return
	}

	sloghttp.AddCustomAttributes(r, slog.String("integration_type", cfg.typ))

	if cfg.typ != "http-proxy" {
		s.log.WarnContext(r.Context(), "integration proxy: unsupported type", "type", cfg.typ)
		http.Error(w, "unsupported integration type", http.StatusBadRequest)
		return
	}

	s.proxyHTTPIntegration(w, r, vmName, integrationName, cfg.config)
}

// httpProxyConfig matches the JSON config stored in the integrations table
// for type "http-proxy".
type httpProxyConfig struct {
	Target string `json:"target"`
	Header string `json:"header"`
}

// proxyHTTPIntegration handles the actual HTTP reverse-proxy for an http-proxy integration.
func (s *Service) proxyHTTPIntegration(w http.ResponseWriter, r *http.Request, vmName, integrationName, configJSON string) {
	var cfg httpProxyConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		s.log.ErrorContext(r.Context(), "integration proxy: bad config JSON", "error", err)
		http.Error(w, "invalid integration config", http.StatusInternalServerError)
		return
	}

	targetURL, err := url.Parse(cfg.Target)
	if err != nil {
		s.log.ErrorContext(r.Context(), "integration proxy: bad target URL", "error", err)
		http.Error(w, "invalid integration target", http.StatusInternalServerError)
		return
	}

	headerName, headerValue, ok := strings.Cut(cfg.Header, ":")
	if !ok {
		s.log.ErrorContext(r.Context(), "integration proxy: bad header format", "header", cfg.Header)
		http.Error(w, "invalid integration header config", http.StatusInternalServerError)
		return
	}
	headerValue = strings.TrimSpace(headerValue)

	proxy := &httputil.ReverseProxy{
		Transport: s.integrationTransport(),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(targetURL)
			if r.URL.Path != "" && r.URL.Path != "/" {
				pr.Out.URL.Path = strings.TrimSuffix(targetURL.Path, "/") + r.URL.Path
			} else {
				pr.Out.URL.Path = targetURL.Path
			}
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = targetURL.Host

			// Forward URL credentials as HTTP Basic Auth.
			if targetURL.User != nil {
				password, _ := targetURL.User.Password()
				pr.Out.SetBasicAuth(targetURL.User.Username(), password)
			}

			pr.Out.Header.Set(headerName, headerValue)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) {
				return
			}
			// Log the full error server-side; return a generic message to the VM
			// so we don't leak target hostnames, IPs, or DNS errors.
			s.log.WarnContext(r.Context(), "integration proxy upstream error",
				"error", err, "vm_name", vmName, "integration", integrationName)
			http.Error(w, "integration proxy: upstream request failed", http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
}

// getIntegrationConfig returns cached config, fetching from exed on miss.
// Concurrent requests for the same key coalesce via singleflight.
func (s *Service) getIntegrationConfig(ctx context.Context, vmName, integrationName string) (integrationCacheEntry, bool) {
	key := integrationCacheKey{vmName: vmName, integrationName: integrationName}

	s.integrationCacheMu.Lock()
	if e, ok := s.integrationCache[key]; ok && time.Since(e.fetchedAt) < IntegrationCacheTTL {
		s.integrationCacheMu.Unlock()
		return *e, e.ok
	}
	s.integrationCacheMu.Unlock()

	entry, _, _ := s.integrationSF.Do(key, func() (*integrationCacheEntry, error) {
		e := s.fetchIntegrationConfig(ctx, vmName, integrationName)
		s.integrationCacheMu.Lock()
		s.integrationCache[key] = e
		s.integrationCacheMu.Unlock()
		return e, nil
	})

	return *entry, entry.ok
}

// fetchIntegrationConfig does the actual HTTP request to exed.
func (s *Service) fetchIntegrationConfig(ctx context.Context, vmName, integrationName string) *integrationCacheEntry {
	negative := &integrationCacheEntry{ok: false, fetchedAt: time.Now()}

	u := fmt.Sprintf("%s/_/integration-config?vm_name=%s&integration=%s",
		strings.TrimRight(s.exedURL, "/"),
		url.QueryEscape(vmName),
		url.QueryEscape(integrationName),
	)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		s.log.ErrorContext(ctx, "integration config: request build failed", "error", err)
		return negative
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.log.ErrorContext(ctx, "integration config: request failed", "error", err)
		return negative
	}
	defer resp.Body.Close()

	var result struct {
		OK     bool   `json:"ok"`
		Type   string `json:"type"`
		Config string `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		s.log.ErrorContext(ctx, "integration config: decode failed", "error", err)
		return negative
	}

	return &integrationCacheEntry{
		ok:        result.OK,
		typ:       result.Type,
		config:    result.Config,
		fetchedAt: time.Now(),
	}
}

// integrationTransport returns an http.Transport with a dial guard that
// rejects connections to private/internal IP addresses.
//
// Three layers of protection:
//  1. Pre-dial: resolve DNS (IPv4 only) and check every IP
//  2. Dial: connect to the validated IP directly (not hostname) to
//     prevent DNS-rebinding TOCTOU attacks
//  3. Post-connect: verify the TCP connection's actual remote IP
//     in case of any routing/NAT surprises
//
// Also enforces that connections go to port 443 only.
func (s *Service) integrationTransport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("invalid address %q: %w", addr, err)
			}
			if port != "443" {
				return nil, fmt.Errorf("integration targets must use port 443")
			}

			// IPv4 only. Resolving "ip4" avoids IPv6 entirely.
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
			if err != nil {
				return nil, fmt.Errorf("DNS lookup failed for %q: %w", host, err)
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("DNS returned no addresses for %q", host)
			}
			for _, ip := range ips {
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("integration target resolves to private IP")
				}
			}

			// Dial the resolved IP directly to prevent DNS rebinding.
			var d net.Dialer
			conn, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(ips[0].String(), port))
			if err != nil {
				return nil, err
			}

			// Post-connect verification: check the actual remote IP (catches
			// DNS rebinding) and the local IP (catches routing through the
			// Tailscale interface to reach internal services).
			if remoteAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
				remoteIP, ok := netip.AddrFromSlice(remoteAddr.IP)
				if !ok || isPrivateIP(remoteIP.Unmap()) {
					conn.Close()
					return nil, fmt.Errorf("integration target connected to private IP")
				}
			}
			if err := s.checkLocalAddr(conn); err != nil {
				conn.Close()
				return nil, err
			}

			return conn, nil
		},
	}
}

// checkLocalAddr validates the local (source) address of an outbound integration
// connection. In production the local address must be a global unicast, non-private,
// non-Tailscale IP — i.e. the connection must leave through a real internet-facing
// interface. In dev mode (GatewayDev) we only reject loopback, since dev machines
// legitimately route through private IPs like 192.168.x.x or 10.x.x.
func (s *Service) checkLocalAddr(conn net.Conn) error {
	tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr)
	if !ok {
		return nil
	}
	localIP, ok := netip.AddrFromSlice(tcpAddr.IP)
	if !ok {
		return nil
	}
	localIP = localIP.Unmap()

	if s.gatewayDev {
		if localIP.IsLoopback() {
			return fmt.Errorf("integration target routed through loopback interface")
		}
		return nil
	}

	// Production: must be global unicast and not private/tailscale.
	if !localIP.IsGlobalUnicast() {
		return fmt.Errorf("integration target routed through non-global-unicast interface (%s)", localIP)
	}
	if localIP.IsPrivate() {
		return fmt.Errorf("integration target routed through private interface (%s)", localIP)
	}
	if tsaddr.IsTailscaleIP(localIP) {
		return fmt.Errorf("integration target routed through tailscale interface")
	}
	return nil
}

// isPrivateIP reports whether ip is non-public. Uses an allowlist approach:
// only globally-routable unicast addresses that aren't in known special-use
// ranges are considered safe.
func isPrivateIP(ip netip.Addr) bool {
	// IsGlobalUnicast is false for loopback, link-local, multicast,
	// unspecified, etc. — block all of those.
	if !ip.IsGlobalUnicast() {
		return true
	}
	// IsGlobalUnicast is true for RFC1918, CGNAT, etc. — block those too.
	if ip.IsPrivate() {
		return true
	}
	if tsaddr.IsTailscaleIP(ip) {
		return true
	}
	for _, p := range nonRoutableRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

var nonRoutableRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT (includes Tailscale 100.x)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF Protocol Assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // Benchmark testing
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // Reserved (Class E)
}

// requestSourceIP extracts the source IP address from the HTTP request.
func requestSourceIP(r *http.Request) string {
	sourceIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		sourceIP = r.RemoteAddr
	}
	// Unmap IPv6-mapped IPv4 addresses (e.g., "::ffff:10.42.0.2" -> "10.42.0.2")
	if addr, err := netip.ParseAddr(sourceIP); err == nil {
		sourceIP = addr.Unmap().String()
	}
	return sourceIP
}
