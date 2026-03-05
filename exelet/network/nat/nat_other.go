//go:build !linux

package nat

import (
	"context"
	"errors"
	"log/slog"

	api "exe.dev/pkg/api/exe/compute/v1"
)

var errNotSupported = errors.New("NAT network manager is only supported on Linux")

// NAT is a stub for non-Linux platforms.
type NAT struct{}

// Config is the NAT specific configuration
type Config struct {
	Bridge  string
	Network string
	Router  string
}

func NewNATManager(addr string, log *slog.Logger) (*NAT, error) {
	return nil, errNotSupported
}

func (n *NAT) Start(ctx context.Context) error {
	return errNotSupported
}

func (n *NAT) Stop(ctx context.Context) error {
	return errNotSupported
}

func (n *NAT) Config(ctx context.Context) any {
	return nil
}

func (n *NAT) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	return nil, errNotSupported
}

func (n *NAT) DeleteInterface(ctx context.Context, id, ip string) error {
	return errNotSupported
}

func (n *NAT) ApplyConnectionLimit(ctx context.Context, ip string) error {
	return errNotSupported
}

func (n *NAT) ApplyBandwidthLimit(ctx context.Context, id string) error {
	return errNotSupported
}

func (n *NAT) ReconcileLeases(ctx context.Context, validIPs map[string]struct{}) ([]string, error) {
	return nil, errNotSupported
}

func (n *NAT) Close() error {
	return errNotSupported
}
