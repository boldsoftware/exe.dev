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
	// ApplyConnectionLimit applies a connection limit rule for the given IP
	ApplyConnectionLimit(ctx context.Context, ip string) error
	// ApplyBandwidthLimit applies bandwidth limiting to an existing TAP device
	ApplyBandwidthLimit(ctx context.Context, id string) error
	// ReconcileLeases releases any IPAM leases whose IPs are not in validIPs.
	// Returns the list of released IPs. It is safe to call concurrently with
	// DeleteInterface — releasing an already-released IP is a no-op.
	ReconcileLeases(ctx context.Context, validIPs map[string]struct{}) ([]string, error)
}
