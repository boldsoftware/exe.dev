package exeprox

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"

	sloghttp "github.com/samber/slog-http"

	"exe.dev/tcprtt"
	"exe.dev/tracing"
)

// LoggerMiddleware adds trace_id generation and request logging.
// The middleware chain (in order of execution) is:
//  1. tracing.HTTPMiddleware - generates trace_id and adds to context
//  2. localAddrMiddleware - adds local_addr attribute
//  3. sloghttp middleware - captures request/response and logs
//  4. customAttrsMiddleware - adds custom attributes after handler runs
func LoggerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	slogConfig := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
		WithUserAgent:    true,
		WithRequestID:    false,
		Filters: []sloghttp.Filter{
			skipMetricsLogs,
		},
	}

	return func(next http.Handler) http.Handler {
		// Build chain from inside out: 4 -> 3 -> 2 -> 1
		h := customAttrsMiddleware(next)
		h = sloghttp.NewWithConfig(logger, slogConfig)(h)
		h = localAddrMiddleware(h)
		h = tracing.HTTPMiddleware(h)
		return h
	}
}

// RecoverHTTPMiddleware logs panics at error level and responds with 500.
// http.ErrAbortHandler is re-panicked to preserve net/http semantics.
func RecoverHTTPMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					traceID := tracing.TraceIDFromContext(r.Context())
					logger.Error("http panic",
						"panic", rec,
						"trace_id", traceID,
						"stack", string(debug.Stack()),
					)
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// localAddrMiddleware adds local_addr attribute.
func localAddrMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && conn != nil {
			sloghttp.AddCustomAttributes(r, slog.String("local_addr", conn.String()))
		}
		next.ServeHTTP(w, r)
	})
}

// customAttrsMiddleware adds custom log attributes after the handler runs.
func customAttrsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		sloghttp.AddCustomAttributes(r, slog.String("log_type", "http_request"))
		sloghttp.AddCustomAttributes(r, slog.String("method", r.Method))
		if host := r.Host; host != "" {
			sloghttp.AddCustomAttributes(r, slog.String("host", host))
		}
		if uri := r.URL.RequestURI(); uri != "" {
			sloghttp.AddCustomAttributes(r, slog.String("uri", uri))
		}

		// Log TCP socket RTT for the client connection.
		if conn := tcprtt.ConnFromContext(r.Context()); conn != nil {
			if rtt, err := tcprtt.Get(conn); err == nil && rtt > 0 {
				sloghttp.AddCustomAttributes(r, slog.String("socket_rtt_us", fmt.Sprintf("%d", rtt.Microseconds())))
			}
		}
	})
}

func skipMetricsLogs(w sloghttp.WrapResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet && r.URL.Path == "/metrics" && w.Status() == http.StatusOK {
		return false
	}

	return true
}
