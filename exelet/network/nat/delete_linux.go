//go:build linux

package nat

import (
	"context"

	"github.com/vishvananda/netlink"
)

func (n *NAT) DeleteInterface(ctx context.Context, id, ip string) error {
	tapName := getTapID(id)

	// Find which bridge this TAP belongs to before deleting it
	bridgeName := n.getTapBridge(tapName)

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

	// Release the DHCP lease for this IP
	if ip != "" {
		if err := n.dhcpServer.Release(ip); err != nil {
			n.log.WarnContext(ctx, "failed to release DHCP lease", "ip", ip, "error", err)
		} else {
			n.log.DebugContext(ctx, "released DHCP lease", "tap", tapName, "ip", ip)
		}
	}

	return nil
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
