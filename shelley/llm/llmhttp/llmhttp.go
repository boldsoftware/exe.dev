// Package llmhttp provides HTTP utilities for LLM requests including
// custom headers and database recording.
package llmhttp

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"time"

	"shelley.exe.dev/version"
)

// contextKey is the type for context keys in this package.
type contextKey int

const (
	conversationIDKey contextKey = iota
	modelIDKey
	providerKey
)

// WithConversationID returns a context with the conversation ID attached.
func WithConversationID(ctx context.Context, conversationID string) context.Context {
	return context.WithValue(ctx, conversationIDKey, conversationID)
}

// ConversationIDFromContext returns the conversation ID from the context, if any.
func ConversationIDFromContext(ctx context.Context) string {
	if v := ctx.Value(conversationIDKey); v != nil {
		return v.(string)
	}
	return ""
}

// WithModelID returns a context with the model ID attached.
func WithModelID(ctx context.Context, modelID string) context.Context {
	return context.WithValue(ctx, modelIDKey, modelID)
}

// ModelIDFromContext returns the model ID from the context, if any.
func ModelIDFromContext(ctx context.Context) string {
	if v := ctx.Value(modelIDKey); v != nil {
		return v.(string)
	}
	return ""
}

// WithProvider returns a context with the provider name attached.
func WithProvider(ctx context.Context, provider string) context.Context {
	return context.WithValue(ctx, providerKey, provider)
}

// ProviderFromContext returns the provider name from the context, if any.
func ProviderFromContext(ctx context.Context) string {
	if v := ctx.Value(providerKey); v != nil {
		return v.(string)
	}
	return ""
}

// Recorder is called after each LLM HTTP request with the request/response details.
type Recorder func(ctx context.Context, url string, requestBody, responseBody []byte, statusCode int, err error, duration time.Duration)

// Transport wraps an http.RoundTripper to add Shelley-specific headers
// and optionally record requests to a database.
type Transport struct {
	Base     http.RoundTripper
	Recorder Recorder
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	// Clone the request to avoid modifying the original
	req = req.Clone(req.Context())

	// Add User-Agent with Shelley version
	info := version.GetInfo()
	userAgent := "Shelley"
	if info.Commit != "" {
		userAgent += "/" + info.Commit[:min(8, len(info.Commit))]
	}
	req.Header.Set("User-Agent", userAgent)

	// Add conversation ID header if present
	if conversationID := ConversationIDFromContext(req.Context()); conversationID != "" {
		req.Header.Set("Shelley-Conversation-Id", conversationID)

		// Add x-session-affinity header for Fireworks to enable prompt caching
		if ProviderFromContext(req.Context()) == "fireworks" {
			req.Header.Set("x-session-affinity", conversationID)
		}
	}

	// Read and store the request body for recording
	var requestBody []byte
	if t.Recorder != nil && req.Body != nil {
		var err error
		requestBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(requestBody))
	}

	// Perform the actual request
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)

	// Record the request if we have a recorder
	if t.Recorder != nil {
		var responseBody []byte
		var statusCode int

		if resp != nil {
			statusCode = resp.StatusCode
			// Read and restore the response body
			responseBody, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(responseBody))
		}

		t.Recorder(req.Context(), req.URL.String(), requestBody, responseBody, statusCode, err, time.Since(start))
	}

	return resp, err
}

// NewClient creates an http.Client with Shelley headers and optional recording.
func NewClient(base *http.Client, recorder Recorder) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}

	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}

	return &http.Client{
		Transport: &Transport{
			Base:     transport,
			Recorder: recorder,
		},
		Timeout: base.Timeout,
	}
}
