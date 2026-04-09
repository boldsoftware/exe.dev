//go:build linux

package nat

import (
	"context"
	"net"

	api "exe.dev/pkg/api/exe/compute/v1"

	"github.com/vishvananda/netlink"
)

func (n *NAT) DeleteInterface(ctx context.Context, id, ip string) error {
	tapName := getTapID(id)

	// Find which bridge this TAP belongs to before deleting it
	bridgeName := n.getTapBridge(tapName)

	// Remove bandwidth limit before deleting TAP (cleanup, ignore errors)
	if err := n.removeBandwidthLimit(ctx, tapName); err != nil {
		n.log.WarnContext(ctx, "failed to remove bandwidth limit", "tap", tapName, "error", err)
	}

	if err := n.deleteTapInterface(tapName); err != nil {
		return err
	}

	// Decrement port count for the bridge
	if bridgeName != "" {
		n.decrementBridgePort(bridgeName)
	}

	// Remove connection limit rule for this IP
	if ip != "" {
		if err := n.removeConnLimit(ctx, ip); err != nil {
			n.log.WarnContext(ctx, "failed to remove connection limit", "ip", ip, "error", err)
		}
	}

	// Release the IP lease
	if ip != "" {
		if err := n.ipam.Release(ip); err != nil {
			n.log.WarnContext(ctx, "failed to release IP lease", "ip", ip, "error", err)
		} else {
			n.log.DebugContext(ctx, "released IP lease", "tap", tapName, "ip", ip)
		}
	}

	return nil
}

// ReconcileLeases releases any IPAM leases whose IPs are not associated with
// the given instances. This cleans up orphaned leases from failed migrations
// or incomplete deletions.
func (n *NAT) ReconcileLeases(ctx context.Context, instances []*api.Instance) ([]string, error) {
	validIPs := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		if inst.VMConfig != nil && inst.VMConfig.NetworkInterface != nil && inst.VMConfig.NetworkInterface.IP != nil {
			ip, _, err := net.ParseCIDR(inst.VMConfig.NetworkInterface.IP.IPV4)
			if err != nil {
				n.log.WarnContext(ctx, "instance has unparseable IP, its lease will be released",
					"instance", inst.ID,
					"ip", inst.VMConfig.NetworkInterface.IP.IPV4,
					"error", err,
				)
				continue
			}
			validIPs[ip.String()] = struct{}{}
		}
	}

	leases, err := n.ipam.ListLeases()
	if err != nil {
		return nil, err
	}

	var released []string
	for _, lease := range leases {
		if _, ok := validIPs[lease.IP]; !ok {
			n.log.WarnContext(ctx, "releasing orphaned IP lease", "ip", lease.IP, "mac", lease.MACAddress)
			if err := n.ipam.Release(lease.IP); err != nil {
				n.log.WarnContext(ctx, "failed to release orphaned IP lease", "ip", lease.IP, "error", err)
				continue
			}
			released = append(released, lease.IP)
		}
	}

	return released, nil
}

// getTapBridge returns the bridge name that a TAP interface belongs to
func (n *NAT) getTapBridge(tapName string) string {
	link, err := netlink.LinkByName(tapName)
	if err != nil {
		return ""
	}

	masterIndex := link.Attrs().MasterIndex
	if masterIndex == 0 {
		return ""
	}

	master, err := netlink.LinkByIndex(masterIndex)
	if err != nil {
		return ""
	}

	return master.Attrs().Name
}
