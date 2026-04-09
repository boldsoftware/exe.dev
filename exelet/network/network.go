package network

import (
	"context"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// ExtIPLookup is an optional interface implemented by network managers
// that support looking up instances by their external IP address.
// The netns manager implements this: each VM has a unique ext IP on
// the shared bridge, used by the metadata service to identify VMs.
type ExtIPLookup interface {
	GetInstanceByExtIP(ip string) (instanceID string, ok bool)
}

// ExtIPRecoverer is an optional interface implemented by network managers
// that maintain an in-memory ext IP mapping which must be rebuilt on
// restart. The netns manager implements this.
type ExtIPRecoverer interface {
	RecoverExtIPs(ctx context.Context, instanceIDs []string) error
}

type NetworkManager interface {
	// Start starts the network manager
	Start(ctx context.Context) error
	// Stop stops the network manager
	Stop(ctx context.Context) error
	// Config returns network specific configuration
	Config(ctx context.Context) any
	// CreateInterface creates a new network interface
	CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error)
	// DeleteInterface deletes the specified network interface and releases
	// its IP resources. id is the instance ID. ip is the VM's IP address,
	// used by NAT to release the IPAM lease and remove connlimit rules;
	// the netns manager ignores ip because it tracks the external IP by
	// instance ID internally (see releaseExtIP).
	DeleteInterface(ctx context.Context, id, ip string) error
	// ApplyConnectionLimit applies a connection limit rule for a VM.
	// Each implementation extracts the identifier it needs from the
	// instance: NAT uses the IP address; netns uses the instance ID.
	ApplyConnectionLimit(ctx context.Context, inst *api.Instance) error
	// ApplyBandwidthLimit applies bandwidth limiting to an existing TAP device
	ApplyBandwidthLimit(ctx context.Context, id string) error
	// ReconcileLeases releases orphaned network resources that don't belong
	// to any of the given instances. Each implementation extracts the
	// identifiers it needs: NAT uses IPs from network configs; netns uses
	// instance IDs. Returns a list of released resources (IPs or namespace
	// names).
	ReconcileLeases(ctx context.Context, instances []*api.Instance) ([]string, error)
}
