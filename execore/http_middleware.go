package execore

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	sloghttp "github.com/samber/slog-http"

	"exe.dev/tracing"
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

// LoggerMiddleware adds trace_id generation and request logging.
// The middleware chain (in order of execution) is:
//  1. tracing.HTTPMiddleware - generates trace_id and adds to context
//  2. requestInfoMiddleware - sets up RequestLogInfo context and local_addr attribute
//  3. sloghttp middleware - captures request/response and logs
//  4. customAttrsMiddleware - adds custom attributes after handler runs
func LoggerMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	slogConfig := sloghttp.Config{
		DefaultLevel:     slog.LevelInfo,
		ClientErrorLevel: slog.LevelInfo,
		ServerErrorLevel: slog.LevelInfo,
		WithRequestID:    false,
	}

	return func(next http.Handler) http.Handler {
		// Build chain from inside out: 4 -> 3 -> 2 -> 1
		h := customAttrsMiddleware(next)
		h = sloghttp.NewWithConfig(logger, slogConfig)(h)
		h = requestInfoMiddleware(h)
		h = tracing.HTTPMiddleware(h)
		return h
	}
}

// requestInfoMiddleware sets up RequestLogInfo context and adds local_addr attribute.
func requestInfoMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := WithNewRequestLogInfo(r.Context())
		r = r.WithContext(ctx)

		if conn, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && conn != nil {
			sloghttp.AddCustomAttributes(r, slog.String("local_addr", conn.String()))
		}

		next.ServeHTTP(w, r)
	})
}

// customAttrsMiddleware adds custom log attributes after the handler runs.
// TODO: only add these attributes on server errors?
// (We could use our own http.ResponseWriter wrapper to capture status code.)
func customAttrsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		sloghttp.AddCustomAttributes(r, slog.String("method", r.Method))
		if host := r.Host; host != "" {
			sloghttp.AddCustomAttributes(r, slog.String("host", host))
		}
		if uri := r.URL.RequestURI(); uri != "" {
			sloghttp.AddCustomAttributes(r, slog.String("uri", uri))
		}

		if info := GetRequestLogInfo(r.Context()); info != nil {
			if info.IsProxy {
				sloghttp.AddCustomAttributes(r, slog.Bool("proxy", true))
			}
			if info.IsTerminal {
				sloghttp.AddCustomAttributes(r, slog.Bool("terminal", true))
			}
		}
	})
}
