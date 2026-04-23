package execore

import (
	"net/http"
	"strings"
)

// routeAPIVM handles /api/vm/* routes. Returns true if the route was handled.
func (s *Server) routeAPIVM(w http.ResponseWriter, r *http.Request, path string) bool {
	// Match /api/vm/{name}/...
	rest, ok := strings.CutPrefix(path, "/api/vm/")
	if !ok {
		return false
	}

	name, suffix, _ := strings.Cut(rest, "/")
	if name == "" {
		return false
	}

	switch suffix {
	case "compute-usage/live":
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		userID, err := s.validateAuthCookie(r)
		if err != nil {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return true
		}
		s.handleAPIVMMetrics(w, r, userID, name)
		return true
	default:
		return false
	}
}
