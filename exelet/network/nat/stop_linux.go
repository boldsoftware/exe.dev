//go:build linux

package nat

import "context"

func (n *NAT) Stop(ctx context.Context) error {
	// Cancel cleanup goroutine to prevent goroutine leak
	if n.cleanupCancel != nil {
		n.cleanupCancel()
	}

	// Cancel DHCP server context to stop the Serve goroutine
	if n.dhcpCancel != nil {
		n.dhcpCancel()
	}

	// Stop DHCP server to close the socket
	if n.dhcpServer != nil {
		if err := n.dhcpServer.Stop(); err != nil {
			n.log.WarnContext(ctx, "failed to stop DHCP server", "error", err)
		}
	}

	return nil
}
