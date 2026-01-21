package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

const (
	// Initial backoff for exponential retry
	initialBackoff = 100 * time.Millisecond
	// Maximum backoff cap
	maxBackoff = 1 * time.Second
)

// ErrNotConnected is returned when not connected
var ErrNotConnected = errors.New("not connected")

// CHClient is a composite type that wraps a CloudHypervisor OpenAPI client
// along with a custom http.Client to dial the CloudHypervisor Unix socket.
// This is needed to add a Close func otherwise it will exhaust file descriptors.
type CHClient struct {
	*ClientWithResponses
	httpClient *http.Client
}

func (c *CHClient) Close() {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
}

// NewCloudHypervisorClient creates a client for the cloud-hypervisor API socket.
// If retry is true, uses exponential backoff (100ms, 200ms, 400ms, 800ms, 1s, 1s, ...)
// until context timeout. If retry is false, fails immediately on first connection error.
func NewCloudHypervisorClient(ctx context.Context, apiSocketPath string, retry bool, log *slog.Logger) (*CHClient, error) {
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
				return dialWithBackoff(dialCtx, apiSocketPath, retry, log)
			},
		},
	}
	c, err := NewClientWithResponses("http://localhost/api/v1", WithHTTPClient(hc))
	if err != nil {
		return nil, err
	}
	return &CHClient{
		c,
		hc,
	}, nil
}

// dialWithBackoff attempts to connect to the unix socket with optional exponential backoff.
func dialWithBackoff(ctx context.Context, socketPath string, retry bool, log *slog.Logger) (net.Conn, error) {
	backoff := initialBackoff
	attempt := 0
	var lastErr error

	for {
		// Check context before attempting
		select {
		case <-ctx.Done():
			return nil, wrapDialError(lastErr)
		default:
		}

		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		// If not retrying, fail immediately
		if !retry {
			return nil, wrapDialError(err)
		}

		log.DebugContext(ctx, "error connecting to api socket; retrying", "path", socketPath, "error", err, "attempt", attempt, "backoff", backoff)
		attempt++

		// Wait with backoff, respecting context
		select {
		case <-ctx.Done():
			return nil, wrapDialError(lastErr)
		case <-time.After(backoff):
		}

		// Exponential backoff with cap
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// wrapDialError wraps a dial error to include both ErrNotConnected (for API compatibility)
// and the underlying error (for precise error classification via errors.Is).
func wrapDialError(err error) error {
	if err == nil {
		return fmt.Errorf("unable to connect to api socket: %w", ErrNotConnected)
	}
	// Create an error chain: message -> ErrNotConnected -> underlying error
	// This allows errors.Is to find both ErrNotConnected and the root cause
	return fmt.Errorf("unable to connect to api socket: %w", &chainedError{sentinel: ErrNotConnected, cause: err})
}

// chainedError wraps two errors so errors.Is can find both.
type chainedError struct {
	sentinel error
	cause    error
}

func (e *chainedError) Error() string {
	return fmt.Sprintf("%v: %v", e.sentinel, e.cause)
}

func (e *chainedError) Is(target error) bool {
	return errors.Is(e.sentinel, target) || errors.Is(e.cause, target)
}

func (e *chainedError) Unwrap() []error {
	return []error{e.sentinel, e.cause}
}
