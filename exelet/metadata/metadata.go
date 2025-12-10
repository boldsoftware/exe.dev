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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"

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
	listenAddr     string // actual address to bind to (may differ from MetadataIP for isolation)
}

// NewService creates a new metadata service
// listenAddr is the IP:port to bind to (e.g., "192.168.1.1:80")
func NewService(log *slog.Logger, computeSvc InstanceLookup, listenAddr string) (*Service, error) {
	if listenAddr == "" {
		return nil, fmt.Errorf("listenAddr is required")
	}

	s := &Service{
		log:            log,
		instanceLookup: computeSvc,
		listenAddr:     listenAddr,
	}

	return s, nil
}

// Start starts the metadata HTTP server
func (s *Service) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)

	// Configure slog-http middleware
	config := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelError,
		WithRequestID:    false,
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
