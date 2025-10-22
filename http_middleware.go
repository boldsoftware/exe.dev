package exe

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	sloghttp "github.com/samber/slog-http"
)

// requestLogInfoKey is used to pass request classification info via context.
type requestLogInfoKey struct{}

// RequestLogInfo holds extra info that handlers can fill in for logging.
type RequestLogInfo struct {
	IsProxy    bool
	IsTerminal bool
}

// WithNewRequestLogInfo attaches a fresh RequestLogInfo to the context.
// The info can be populated by handlers and will be added as custom attributes in logs.
func WithNewRequestLogInfo(ctx context.Context) (context.Context, *RequestLogInfo) {
	info := &RequestLogInfo{}
	return context.WithValue(ctx, requestLogInfoKey{}, info), info
}

// GetRequestLogInfo retrieves RequestLogInfo from context, if present.
func GetRequestLogInfo(ctx context.Context) *RequestLogInfo {
	if v := ctx.Value(requestLogInfoKey{}); v != nil {
		if info, ok := v.(*RequestLogInfo); ok {
			return info
		}
	}
	return nil
}

// LoggerMiddleware adds request logging using slog-http. It logs one line per HTTP request.
func LoggerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	config := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
		WithRequestID:    false,
	}

	// Wrap slog-http middleware to inject RequestLogInfo and custom attributes
	return func(next http.Handler) http.Handler {
		// Wrap the actual handler to capture RequestLogInfo after it runs
		wrappedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)

			// After the handler runs, add custom attributes based on RequestLogInfo
			if info := GetRequestLogInfo(r.Context()); info != nil {
				if info.IsProxy {
					sloghttp.AddCustomAttributes(r, slog.Bool("proxy", true))
				}
				if info.IsTerminal {
					sloghttp.AddCustomAttributes(r, slog.Bool("terminal", true))
				}
			}
		})

		// Apply slog-http middleware on top
		slogMiddleware := sloghttp.NewWithConfig(logger, config)(wrappedHandler)

		// Outermost wrapper adds RequestLogInfo context and local_addr
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Attach RequestLogInfo so downstream handlers can enrich the final log line.
			ctx, _ := WithNewRequestLogInfo(r.Context())
			r = r.WithContext(ctx)

			// Add local_addr as custom attribute
			if conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && conn != nil {
				sloghttp.AddCustomAttributes(r, slog.String("local_addr", conn.String()))
			}

			// Serve the request through slog-http middleware
			slogMiddleware.ServeHTTP(w, r)
		})
	}
}
