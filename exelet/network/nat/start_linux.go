//go:build linux

package nat

import (
	"context"
	"fmt"
	"time"
)

func (n *NAT) Start(ctx context.Context) error {
	n.log.DebugContext(ctx, "configuring bridge", "device", n.bridgeName)
	if err := n.configureBridge(ctx); err != nil {
		return fmt.Errorf("error configuring bridge %s: %w", n.bridgeName, err)
	}

	// start dhcp server
	go func() {
		n.log.DebugContext(ctx, "starting dhcp server", "device", n.bridgeName)

		if err := n.dhcpServer.Serve(context.Background()); err != nil {
			n.log.ErrorContext(ctx, "error starting dhcp server", "err", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*20)
	defer cancel()

	// configure forwarding
	n.log.DebugContext(ctx, "configuring forwarding", "device", n.bridgeName)
	if err := n.applyIPTablesForwarding(ctx, n.bridgeName); err != nil {
		return err
	}

	// configure NAT masquerade
	n.log.DebugContext(ctx, "configuring masquerade", "device", n.bridgeName)
	if err := n.applyIPTablesMasquerade(ctx, n.bridgeName, n.network); err != nil {
		return err
	}

	return nil
}
