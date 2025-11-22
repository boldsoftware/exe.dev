package tracing

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const traceIDMetadataKey = "trace_id"

// UnaryClientInterceptor returns a gRPC unary client interceptor that propagates trace_id
// from the context to gRPC metadata.
func UnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Extract trace_id from context
		if traceID := ctx.Value("trace_id"); traceID != nil {
			if tid, ok := traceID.(string); ok && tid != "" {
				// Add trace_id to outgoing metadata
				ctx = metadata.AppendToOutgoingContext(ctx, traceIDMetadataKey, tid)
			}
		} else {
			slog.Debug("gRPC client: NO trace_id in context", "method", method)
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// StreamClientInterceptor returns a gRPC stream client interceptor that propagates trace_id
// from the context to gRPC metadata.
func StreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		// Extract trace_id from context
		if traceID := ctx.Value("trace_id"); traceID != nil {
			if tid, ok := traceID.(string); ok && tid != "" {
				// Add trace_id to outgoing metadata
				ctx = metadata.AppendToOutgoingContext(ctx, traceIDMetadataKey, tid)
			}
		} else {
			slog.Debug("gRPC client stream: NO trace_id in context", "method", method)
		}
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// UnaryServerInterceptor returns a gRPC unary server interceptor that extracts trace_id
// from incoming gRPC metadata and adds it to the context.
func UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Extract trace_id from incoming metadata
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if values := md.Get(traceIDMetadataKey); len(values) > 0 && values[0] != "" {
				// Add trace_id to context for use in request handling
				ctx = context.WithValue(ctx, "trace_id", values[0])
				// slog.WarnContext(ctx, "gRPC server: trace_id extracted", "trace_id", values[0], "method", info.FullMethod)
			} else {
				slog.Debug("gRPC server: NO trace_id in metadata", "method", info.FullMethod)
			}
		}
		return handler(ctx, req)
	}
}

// StreamServerInterceptor returns a gRPC stream server interceptor that extracts trace_id
// from incoming gRPC metadata and adds it to the context.
func StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Extract trace_id from incoming metadata
		ctx := ss.Context()
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if values := md.Get(traceIDMetadataKey); len(values) > 0 && values[0] != "" {
				// Create wrapped stream with trace_id in context
				ctx = context.WithValue(ctx, "trace_id", values[0])
				ss = &wrappedServerStream{ServerStream: ss, ctx: ctx}
			} else {
				slog.Debug("gRPC server stream: NO trace_id in metadata", "method", info.FullMethod)
			}
		}
		return handler(srv, ss)
	}
}

// wrappedServerStream wraps a grpc.ServerStream to override the context.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the wrapped context with trace_id.
func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}
