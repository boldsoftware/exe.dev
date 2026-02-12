package server

import (
	"compress/gzip"
	"log/slog"
	"net/http"
	"strings"
	"sync"

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

// gzipResponseWriter wraps http.ResponseWriter to compress responses
type gzipResponseWriter struct {
	http.ResponseWriter
	gw *gzip.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.gw.Write(b)
}

var gzipWriterPool = sync.Pool{
	New: func() interface{} {
		gw, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed)
		return gw
	},
}

// gzipHandler wraps a handler to compress responses when the client accepts gzip.
// Use this to wrap specific handlers that benefit from compression.
// Do NOT use for SSE or streaming responses.
func gzipHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		gw := gzipWriterPool.Get().(*gzip.Writer)
		gw.Reset(w)
		defer func() {
			gw.Close()
			gzipWriterPool.Put(gw)
		}()

		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length") // Compression changes size

		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gw: gw}, r)
	})
}
