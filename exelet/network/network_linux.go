//go:build linux

package network

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"exe.dev/exelet/network/nat"
)

// NewNetworkManager returns a new Network Manager
func NewNetworkManager(addr string, log *slog.Logger) (NetworkManager, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(u.Scheme) {
	case "nat":
		return nat.NewNATManager(addr, log)
	}

	return nil, fmt.Errorf("unsupported network manager %q", u.Scheme)
}
