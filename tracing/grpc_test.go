package tracing

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestUnaryClientInterceptor(t *testing.T) {
	tests := []struct {
		name          string
		ctx           context.Context
		expectTraceID string
	}{
		{
			name:          "with trace_id in context",
			ctx:           context.WithValue(context.Background(), "trace_id", "test-trace-123"),
			expectTraceID: "test-trace-123",
		},
		{
			name:          "without trace_id in context",
			ctx:           context.Background(),
			expectTraceID: "",
		},
		{
			name:          "with empty trace_id",
			ctx:           context.WithValue(context.Background(), "trace_id", ""),
			expectTraceID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interceptor := UnaryClientInterceptor()

			// Mock invoker that captures the context
			var capturedCtx context.Context
			mockInvoker := func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
				capturedCtx = ctx
				return nil
			}

			// Call the interceptor
			err := interceptor(tt.ctx, "/test.Service/Method", nil, nil, nil, mockInvoker)
			if err != nil {
				t.Fatalf("interceptor returned error: %v", err)
			}

			// Check if trace_id was added to metadata
			md, ok := metadata.FromOutgoingContext(capturedCtx)
			if tt.expectTraceID != "" {
				if !ok {
					t.Fatal("expected metadata in context, got none")
				}
				values := md.Get(traceIDMetadataKey)
				if len(values) == 0 {
					t.Fatalf("expected trace_id in metadata, got none")
				}
				if values[0] != tt.expectTraceID {
					t.Fatalf("expected trace_id %q, got %q", tt.expectTraceID, values[0])
				}
			} else {
				// Should not have trace_id in metadata
				if ok {
					values := md.Get(traceIDMetadataKey)
					if len(values) > 0 && values[0] != "" {
						t.Fatalf("expected no trace_id in metadata, got %q", values[0])
					}
				}
			}
		})
	}
}

func TestUnaryServerInterceptor(t *testing.T) {
	tests := []struct {
		name          string
		incomingMD    metadata.MD
		expectTraceID string
	}{
		{
			name:          "with trace_id in metadata",
			incomingMD:    metadata.Pairs(traceIDMetadataKey, "server-trace-456"),
			expectTraceID: "server-trace-456",
		},
		{
			name:          "without trace_id in metadata",
			incomingMD:    metadata.Pairs("other-key", "value"),
			expectTraceID: "",
		},
		{
			name:          "with empty trace_id",
			incomingMD:    metadata.Pairs(traceIDMetadataKey, ""),
			expectTraceID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interceptor := UnaryServerInterceptor()

			// Create context with incoming metadata
			ctx := metadata.NewIncomingContext(context.Background(), tt.incomingMD)

			// Mock handler that captures the context
			var capturedCtx context.Context
			mockHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
				capturedCtx = ctx
				return nil, nil
			}

			// Call the interceptor
			_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, mockHandler)
			if err != nil {
				t.Fatalf("interceptor returned error: %v", err)
			}

			// Check if trace_id was added to context
			traceID := capturedCtx.Value("trace_id")
			if tt.expectTraceID != "" {
				if traceID == nil {
					t.Fatal("expected trace_id in context, got nil")
				}
				tid, ok := traceID.(string)
				if !ok {
					t.Fatalf("expected trace_id to be string, got %T", traceID)
				}
				if tid != tt.expectTraceID {
					t.Fatalf("expected trace_id %q, got %q", tt.expectTraceID, tid)
				}
			} else {
				// Should not have trace_id in context
				if traceID != nil {
					if tid, ok := traceID.(string); ok && tid != "" {
						t.Fatalf("expected no trace_id in context, got %q", tid)
					}
				}
			}
		})
	}
}

func TestStreamServerInterceptor_ContextPropagation(t *testing.T) {
	interceptor := StreamServerInterceptor()

	// Create context with incoming metadata containing trace_id
	md := metadata.Pairs(traceIDMetadataKey, "stream-trace-789")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	// Mock server stream
	mockStream := &mockServerStream{ctx: ctx}

	// Mock handler that captures the stream
	var capturedStream grpc.ServerStream
	mockHandler := func(srv interface{}, ss grpc.ServerStream) error {
		capturedStream = ss
		return nil
	}

	// Call the interceptor
	err := interceptor(nil, mockStream, &grpc.StreamServerInfo{}, mockHandler)
	if err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	// Check if trace_id was added to the stream's context
	streamCtx := capturedStream.Context()
	traceID := streamCtx.Value("trace_id")
	if traceID == nil {
		t.Fatal("expected trace_id in stream context, got nil")
	}
	tid, ok := traceID.(string)
	if !ok {
		t.Fatalf("expected trace_id to be string, got %T", traceID)
	}
	if tid != "stream-trace-789" {
		t.Fatalf("expected trace_id %q, got %q", "stream-trace-789", tid)
	}
}

// mockServerStream is a mock implementation of grpc.ServerStream for testing
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}
