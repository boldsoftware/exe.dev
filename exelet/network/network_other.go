//go:build !linux

package network

import (
	"fmt"
	"log/slog"
	"net/url"
)

// NewNetworkManager returns a new Network Manager
func NewNetworkManager(addr string, log *slog.Logger) (NetworkManager, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	return nil, fmt.Errorf("unsupported network manager %q", u.Scheme)
}
