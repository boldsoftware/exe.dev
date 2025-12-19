package server

import (
	"log/slog"
	"net/http"
	"strings"

	sloghttp "github.com/samber/slog-http"
)

// LoggerMiddleware adds request logging using slog-http
func LoggerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	config := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
		WithRequestID:    false,
	}
	return sloghttp.NewWithConfig(logger, config)
}

// CSRFMiddleware protects against CSRF attacks by requiring the X-Shelley-Request header
// on state-changing requests (POST, PUT, DELETE). This works because browsers will not
// add custom headers to simple cross-origin requests, and CORS preflight will block
// complex requests from other origins that don't have explicit permission.
func CSRFMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only check state-changing methods
			if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
				// Require X-Shelley-Request header (value doesn't matter, just presence)
				if r.Header.Get("X-Shelley-Request") == "" {
					http.Error(w, "CSRF protection: X-Shelley-Request header required", http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireHeaderMiddleware requires a specific header to be present on all API requests.
// This is used to ensure requests come through an authenticated proxy.
func RequireHeaderMiddleware(headerName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only check API routes
			if strings.HasPrefix(r.URL.Path, "/api/") {
				if r.Header.Get(headerName) == "" {
					http.Error(w, "missing required header: "+headerName, http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
