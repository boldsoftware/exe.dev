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
	"syscall"

	"exe.dev/tracing"
	"github.com/prometheus/client_golang/prometheus"
	sloghttp "github.com/samber/slog-http"
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

	gatewayRequests *prometheus.CounterVec
}

// NewService creates a new metadata service
// listenAddr is the IP:port to bind to (e.g., "192.168.1.1:80")
func NewService(log *slog.Logger, computeSvc InstanceLookup, exedURL, listenAddr string, registry *prometheus.Registry) (*Service, error) {
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
		log:             log,
		instanceLookup:  computeSvc,
		exedURL:         exedURL,
		exedTargetURL:   targetURL,
		listenAddr:      listenAddr,
		gatewayRequests: gatewayRequests,
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

	// Build the handler with logging middleware chain.
	// The middleware chain (in order of execution) is:
	//  1. tracing.HTTPMiddleware - generates trace_id and adds to context
	//  2. sloghttp middleware - captures request/response and logs
	//  3. customAttrsMiddleware - adds custom attributes after handler runs
	handler := s.loggerMiddleware(mux)

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
