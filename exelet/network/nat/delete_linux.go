//go:build linux

package nat

import (
	"context"
)

func (n *NAT) DeleteInterface(ctx context.Context, id, ip string) error {
	tapName := getTapID(id)
	if err := n.deleteTapInterface(tapName); err != nil {
		return err
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
