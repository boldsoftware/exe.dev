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
	"crypto/tls"
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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"exe.dev/tracing"
	"exe.dev/wildcardcert"
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
	// MetadataHTTPSPort is the local listen port for integration proxy TLS
	// termination. We use 2443 instead of 443 to avoid conflicting with
	// exeprox, which binds 0.0.0.0:443, and to stay outside the proxy
	// port range (3000-9999) and socat SSH port range (10000-20000).
	// The iptables DNAT rule translates 169.254.169.254:443 → bridge_ip:2443
	// so VMs still connect on 443.
	MetadataHTTPSPort = 2443
)

// CertRefreshInterval is how often the exelet re-fetches the integration
// wildcard certificate from exed. Exported so tests can shorten it.
var CertRefreshInterval = 1 * time.Hour

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
	certCachePath  string // path to cache the integration wildcard cert on disk (empty = no caching)
	gatewayDev     bool   // true in local/test stages; relaxes outbound local-address checks

	// integrationHostSuffixes is the list of domain suffixes for integration proxy
	// requests (e.g., [".int.exe.xyz", ".int.exe.cloud"]). The first entry is the
	// primary suffix; additional entries provide backward compatibility.
	integrationHostSuffixes []string

	gatewayRequests *prometheus.CounterVec

	// tlsCertMu protects tlsCert.
	tlsCertMu sync.RWMutex
	tlsCert   *tls.Certificate

	// tlsServer is the HTTPS listener (port 443), started only when a
	// certificate is available.
	tlsServer *http.Server

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
	target    *url.URL
	headers   map[string]string
	basicAuth *basicAuthConfig
	fetchedAt time.Time
}

type basicAuthConfig struct {
	User string `json:"user"`
	Pass string `json:"pass"`
}

// IntegrationCacheTTL is the default cache duration for integration configs.
// Exported so tests can shorten it.
var IntegrationCacheTTL = 1 * time.Minute

// NewService creates a new metadata service.
// listenAddr is the IP:port to bind to (e.g., "192.168.1.1:80").
// integrationHostSuffixes is the list of domain suffixes for integration proxy
// requests (e.g., [".int.exe.xyz", ".int.exe.cloud"]).
// certCachePath is the directory where the integration wildcard cert is cached
// on disk (e.g., "/data/exelet/certs"). Empty disables disk caching.
// gatewayDev relaxes outbound local-address checks for dev environments
// where connections legitimately route through private interfaces.
func NewService(log *slog.Logger, computeSvc InstanceLookup, exedURL, listenAddr string, integrationHostSuffixes []string, certCachePath string, gatewayDev bool, registry *prometheus.Registry) (*Service, error) {
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
		log:                     log,
		instanceLookup:          computeSvc,
		exedURL:                 exedURL,
		exedTargetURL:           targetURL,
		listenAddr:              listenAddr,
		certCachePath:           certCachePath,
		integrationHostSuffixes: integrationHostSuffixes,
		gatewayDev:              gatewayDev,
		gatewayRequests:         gatewayRequests,
		integrationCache:        make(map[integrationCacheKey]*integrationCacheEntry),
	}

	return s, nil
}

// Start starts the metadata HTTP server and, if a TLS certificate can be
// fetched from exed, an HTTPS server on port 443 for integration proxy.
func (s *Service) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/{$}", s.handleRoot)

	// Add gateway proxy handler
	mux.HandleFunc("/gateway/llm/", s.handleGatewayProxy)

	// Add email proxy handler
	mux.HandleFunc("POST /gateway/email/send", s.handleEmailProxy)

	// Wrap the mux with integration proxy routing.
	// Requests matching an integration host suffix (e.g. *.int.exe.xyz)
	// are routed to the integration proxy handler; everything else goes
	// to the normal mux.
	var mainHandler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if name, ok := s.integrationHostName(r.Host); ok {
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

	// In production, start the HTTPS listener and fetch the integration
	// wildcard cert from exed asynchronously. The TLS server uses a
	// GetCertificate callback so it will serve once a cert is available.
	// Skip entirely in dev/test stages where there are no real certs.
	if !s.gatewayDev {
		s.startTLSServer(ctx, handler)
		go s.refreshCertLoop(ctx)
	}

	return nil
}

// startTLSServer starts the HTTPS listener using the current TLS certificate.
func (s *Service) startTLSServer(ctx context.Context, handler http.Handler) {
	host, _, err := net.SplitHostPort(s.listenAddr)
	if err != nil {
		s.log.ErrorContext(ctx, "cannot derive HTTPS listen addr", "error", err)
		return
	}
	httpsAddr := net.JoinHostPort(host, fmt.Sprintf("%d", MetadataHTTPSPort))

	s.tlsServer = &http.Server{
		Addr:    httpsAddr,
		Handler: handler,
		TLSConfig: &tls.Config{
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				s.tlsCertMu.RLock()
				cert := s.tlsCert
				s.tlsCertMu.RUnlock()
				if cert == nil {
					return nil, fmt.Errorf("no TLS certificate available")
				}
				return cert, nil
			},
		},
	}

	s.log.InfoContext(ctx, "starting metadata HTTPS service", "addr", httpsAddr)

	go func() {
		// TLSConfig is set, so ListenAndServeTLS with empty cert/key files
		// uses the GetCertificate callback.
		if err := s.tlsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			s.log.ErrorContext(ctx, "metadata HTTPS service error", "err", err)
		}
	}()
}

// refreshCertLoop fetches the integration certificate immediately and then
// periodically re-fetches it from exed.
func (s *Service) refreshCertLoop(ctx context.Context) {
	// Fetch immediately on startup.
	if err := s.fetchAndStoreIntegrationCert(ctx); err != nil {
		s.log.WarnContext(ctx, "initial integration cert fetch failed", "error", err)
	} else {
		s.log.InfoContext(ctx, "integration cert loaded")
	}

	ticker := time.NewTicker(CertRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.fetchAndStoreIntegrationCert(ctx); err != nil {
				s.log.WarnContext(ctx, "integration cert refresh failed", "error", err)
			} else {
				s.log.InfoContext(ctx, "integration cert refreshed")
			}
		}
	}
}

// fetchAndStoreIntegrationCert fetches the PEM-encoded wildcard certificate
// from exed's /_/integration-cert endpoint and stores it for TLS serving.
// On success the PEM is cached to disk so that a subsequent startup can use
// the cached cert if exed is unreachable.
// If the fetch fails, the method tries to load a previously cached cert.
func (s *Service) fetchAndStoreIntegrationCert(ctx context.Context) error {
	pemBytes, fetchErr := s.fetchIntegrationCertPEM(ctx)
	if fetchErr != nil {
		// Try loading a cached cert from disk.
		cached, diskErr := s.loadCachedCert()
		if diskErr != nil {
			return fmt.Errorf("fetch failed (%w) and no cached cert available (%v)", fetchErr, diskErr)
		}
		s.tlsCertMu.Lock()
		s.tlsCert = cached
		s.tlsCertMu.Unlock()
		s.log.WarnContext(ctx, "using cached integration cert from disk", "fetch_error", fetchErr)
		return nil
	}

	cert, err := wildcardcert.DecodeCertificate(pemBytes)
	if err != nil {
		return fmt.Errorf("decode cert: %w", err)
	}

	s.tlsCertMu.Lock()
	s.tlsCert = cert
	s.tlsCertMu.Unlock()

	// Best-effort write to disk cache.
	if err := s.writeCachedCert(pemBytes); err != nil {
		s.log.WarnContext(ctx, "failed to cache integration cert to disk", "error", err)
	}

	return nil
}

// fetchIntegrationCertPEM does the HTTP request to exed's /_/integration-cert.
func (s *Service) fetchIntegrationCertPEM(ctx context.Context) ([]byte, error) {
	u := strings.TrimRight(s.exedURL, "/") + "/_/integration-cert"
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
	}

	return io.ReadAll(resp.Body)
}

const certCacheFile = "integration-cert.pem"

// writeCachedCert writes the PEM cert to the disk cache directory.
func (s *Service) writeCachedCert(pem []byte) error {
	if s.certCachePath == "" {
		return nil
	}
	if err := os.MkdirAll(s.certCachePath, 0o700); err != nil {
		return err
	}
	// Write atomically via temp file + rename to avoid partial reads.
	tmp := filepath.Join(s.certCachePath, certCacheFile+".tmp")
	if err := os.WriteFile(tmp, pem, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.certCachePath, certCacheFile))
}

// loadCachedCert reads and decodes a previously cached cert from disk.
func (s *Service) loadCachedCert() (*tls.Certificate, error) {
	if s.certCachePath == "" {
		return nil, fmt.Errorf("no cert cache path configured")
	}
	data, err := os.ReadFile(filepath.Join(s.certCachePath, certCacheFile))
	if err != nil {
		return nil, err
	}
	return wildcardcert.DecodeCertificate(data)
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

// Stop stops the metadata HTTP and HTTPS servers.
func (s *Service) Stop(ctx context.Context) error {
	var firstErr error
	if s.tlsServer != nil {
		if err := s.tlsServer.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.server != nil {
		if err := s.server.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
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
		FlushInterval: -1, // Flush immediately for SSE streaming
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

// integrationHostName extracts the integration name from a Host header like
// "myproxy.int.exe.xyz" or "myproxy.int.exe.cloud:80".
// Returns the name and true if the host matches any configured suffix,
// or ("", false) otherwise.
func (s *Service) integrationHostName(host string) (string, bool) {
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
	for _, suffix := range s.integrationHostSuffixes {
		if strings.HasSuffix(h, suffix) {
			name := strings.TrimSuffix(h, suffix)
			if name != "" {
				return name, true
			}
		}
	}
	return "", false
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

	proxy := &httputil.ReverseProxy{
		Transport: s.integrationTransport(),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(cfg.target)
			if r.URL.Path != "" && r.URL.Path != "/" {
				pr.Out.URL.Path = strings.TrimSuffix(cfg.target.Path, "/") + r.URL.Path
			} else {
				pr.Out.URL.Path = cfg.target.Path
			}
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = cfg.target.Host
			for k, v := range cfg.headers {
				pr.Out.Header.Set(k, v)
			}
			if cfg.basicAuth != nil {
				pr.Out.SetBasicAuth(cfg.basicAuth.User, cfg.basicAuth.Pass)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) {
				return
			}
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
		OK        bool              `json:"ok"`
		Target    string            `json:"target"`
		Headers   map[string]string `json:"headers"`
		BasicAuth *basicAuthConfig  `json:"basic_auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		s.log.ErrorContext(ctx, "integration config: decode failed", "error", err)
		return negative
	}
	if !result.OK {
		return negative
	}

	targetURL, err := url.Parse(result.Target)
	if err != nil {
		s.log.ErrorContext(ctx, "integration config: bad target URL", "error", err, "target", result.Target)
		return negative
	}

	return &integrationCacheEntry{
		ok:        true,
		target:    targetURL,
		headers:   result.Headers,
		basicAuth: result.BasicAuth,
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
