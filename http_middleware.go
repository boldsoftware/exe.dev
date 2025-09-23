package exe

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// requestLogInfoKey is used to pass request classification info via context.
type requestLogInfoKey struct{}

// RequestLogInfo holds extra info that handlers can fill in for logging.
type RequestLogInfo struct {
	IsProxy    bool
	IsTerminal bool
}

// WithNewRequestLogInfo attaches a fresh RequestLogInfo to the context.
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

// LoggerMiddleware adds request logging. It logs one line per HTTP request.
func LoggerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Attach RequestLogInfo so downstream handlers can enrich the final log line.
			ctx, _ := WithNewRequestLogInfo(r.Context())
			r = r.WithContext(ctx)

			start := time.Now()
			next.ServeHTTP(w, r)
			duration := time.Since(start)

			// host and local_addr for richer context
			host := r.Host
			localAddr := ""
			if conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && conn != nil {
				localAddr = conn.String()
			}

			// Optional classification if downstream populated it
			if info := GetRequestLogInfo(r.Context()); info != nil && (info.IsProxy || info.IsTerminal) {
				logger.Info("HTTP request",
					"method", r.Method,
					"path", r.URL.Path,
					"host", host,
					"local_addr", localAddr,
					"proxy", info.IsProxy,
					"terminal", info.IsTerminal,
					"duration", duration,
				)
				return
			}

			logger.Info("HTTP request",
				"method", r.Method,
				"path", r.URL.Path,
				"host", host,
				"local_addr", localAddr,
				"duration", duration,
			)
		})
	}
}
