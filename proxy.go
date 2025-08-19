package exe

import (
	"fmt"
	"net/http"
	"strings"
)

// handleProxyRequest handles requests that should be proxied to containers
// This handler is called when the Host header matches machine.team.exe.dev or machine.team.localhost
func (s *Server) handleProxyRequest(w http.ResponseWriter, r *http.Request) {
	// Extract machine and team from Host header
	hostname := r.Host
	// Remove port if present
	if idx := strings.LastIndex(hostname, ":"); idx > 0 {
		hostname = hostname[:idx]
	}

	// For now, just return some static text to confirm the routing works
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "Proxy handler called for host: %s\n", hostname)
	fmt.Fprintf(w, "Request method: %s\n", r.Method)
	fmt.Fprintf(w, "Request path: %s\n", r.URL.Path)
	fmt.Fprintf(w, "Request headers:\n")
	for name, values := range r.Header {
		for _, value := range values {
			fmt.Fprintf(w, "  %s: %s\n", name, value)
		}
	}
}

// isProxyRequest determines if a request should be handled by the proxy
func (s *Server) isProxyRequest(host string) bool {
	// Remove port if present
	hostname := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		hostname = host[:idx]
	}

	// Check for production pattern: *.exe.dev (but not just exe.dev)
	if strings.HasSuffix(hostname, ".exe.dev") && hostname != "exe.dev" {
		return true
	}

	// Check for dev pattern: *.localhost (but not just localhost)
	if strings.HasSuffix(hostname, ".localhost") && hostname != "localhost" {
		return true
	}

	return false
}
