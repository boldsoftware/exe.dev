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
	maxRetries = 2
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

func NewCloudHypervisorClient(apiSocketPath string, log *slog.Logger) (*CHClient, error) {
	hc := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				i := 0
				for range maxRetries {
					c, err := net.Dial("unix", apiSocketPath)
					if err != nil {
						log.Debug("error connecting to api socket; retrying", "path", apiSocketPath, "error", err, "retry", i)
						time.Sleep(time.Millisecond * 250)
						i += 1
						continue
					}
					return c, nil
				}
				return nil, fmt.Errorf("unable to connect to api socket: %w", ErrNotConnected)
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
