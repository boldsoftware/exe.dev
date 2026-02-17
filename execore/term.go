package execore

import (
	"net/http"

	"exe.dev/exeweb"
)

// isTerminalRequest determines if a request is for a terminal subdomain
func (s *Server) isTerminalRequest(host string) bool {
	return exeweb.IsTerminalRequest(&s.env, host)
}

// handleTerminalRequest handles requests to terminal subdomains
func (s *Server) handleTerminalRequest(w http.ResponseWriter, r *http.Request) {
	s.proxyServer().HandleTerminalRequest(w, r)
}

// parseTerminalHostname extracts box name from terminal hostname
func (s *Server) parseTerminalHostname(hostname string) (string, error) {
	return exeweb.ParseTerminalHostname(&s.env, hostname)
}
