//go:build linux

package nat

import (
	"context"
	"strings"
)

func (n *NAT) DeleteInterface(ctx context.Context, id, ip string) error {
	tapName := getTapID(id)
	if err := n.deleteTapInterface(tapName); err != nil {
		return err
	}

	// release DHCP lease if IP is provided
	if ip != "" {
		// strip CIDR suffix if present (e.g., "192.168.70.2/24" -> "192.168.70.2")
		if idx := strings.Index(ip, "/"); idx > 0 {
			ip = ip[:idx]
		}
		if err := n.dhcpServer.Release(ip); err != nil {
			n.log.WarnContext(ctx, "failed to release DHCP lease", "ip", ip, "error", err)
		}
	}

	return nil
}
