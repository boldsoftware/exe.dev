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
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"syscall"

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

	// Configure slog-http middleware
	config := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelError,
		WithRequestID:    false,
		Filters: []sloghttp.Filter{
			// Skip middleware logging for gateway proxy - it logs errors explicitly
			sloghttp.IgnorePathPrefix("/gateway/llm/"),
		},
	}

	// Wrap handler with slog middleware
	handlerWithLogging := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		// Add custom attributes for logging
		sloghttp.AddCustomAttributes(r, slog.String("method", r.Method))
		sloghttp.AddCustomAttributes(r, slog.String("path", r.URL.Path))
		if remoteIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			sloghttp.AddCustomAttributes(r, slog.String("remote_ip", remoteIP))
		}
	})
	slogMiddleware := sloghttp.NewWithConfig(s.log, config)(handlerWithLogging)

	s.server = &http.Server{
		Addr:    s.listenAddr,
		Handler: slogMiddleware,
	}

	s.log.InfoContext(ctx, "starting metadata service", "addr", s.listenAddr)

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.ErrorContext(ctx, "metadata service error", "err", err)
		}
	}()

	return nil
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

	s.log.DebugContext(r.Context(), "proxying gateway request", "original_path", originalPath, "new_path", newPath2, "box", boxName)

	// Create a reverse proxy for this request
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(s.exedTargetURL)
			pr.Out.URL.Path = pr.In.URL.Path
			pr.Out.Host = s.exedTargetURL.Host
			// Add header to identify the box making the request
			pr.Out.Header.Set("X-Exedev-Box", boxName)
		},
		ModifyResponse: func(resp *http.Response) error {
			s.gatewayRequests.WithLabelValues("success").Inc()
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, context.Canceled) {
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
