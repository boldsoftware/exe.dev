package server

import (
	"log/slog"
	"net/http"
	"time"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Flush delegates to the underlying ResponseWriter if it implements http.Flusher.
// This is required for SSE handlers wrapped by the request logger.
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying ResponseWriter so http.ResponseController
// and interface assertions can discover it.
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// RequestLogger returns middleware that logs each request.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sr, r)
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sr.status,
				"duration", time.Since(start),
			)
		})
	}
}
