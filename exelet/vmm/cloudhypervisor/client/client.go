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

	for {
		// Check context before attempting
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("unable to connect to api socket: %w", ErrNotConnected)
		default:
		}

		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			return conn, nil
		}

		// If not retrying, fail immediately
		if !retry {
			return nil, fmt.Errorf("unable to connect to api socket: %w", ErrNotConnected)
		}

		log.Debug("error connecting to api socket; retrying", "path", socketPath, "error", err, "attempt", attempt, "backoff", backoff)
		attempt++

		// Wait with backoff, respecting context
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("unable to connect to api socket: %w", ErrNotConnected)
		case <-time.After(backoff):
		}

		// Exponential backoff with cap
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}
