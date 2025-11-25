package network

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

type NetworkManager interface {
	// Start starts the network manager
	Start(ctx context.Context) error
	// Stop stops the network manager
	Stop(ctx context.Context) error
	// Config returns network specific configuration
	Config(ctx context.Context) any
	// CreateInterface creates a new network interface
	CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error)
	// DeleteInterface deletes the specified network interface and releases its IP
	DeleteInterface(ctx context.Context, id, ip string) error
}
