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

	// IP release is handled by periodic cleanup goroutine (after 10-minute grace period)
	// This prevents rapid IP reuse which can cause ARP cache and connection state issues
	n.log.DebugContext(ctx, "tap interface deleted, IP will be released after grace period", "tap", tapName, "ip", ip)

	return nil
}
