//go:build linux

package nat

import (
	"context"
	"fmt"
	"time"
)

func (n *NAT) Start(ctx context.Context) error {
	primaryBridge := n.primaryBridgeName()

	n.log.DebugContext(ctx, "configuring bridge", "device", primaryBridge)
	if err := n.configureBridge(ctx); err != nil {
		return fmt.Errorf("error configuring bridge %s: %w", primaryBridge, err)
	}

	// Create cancellable context for DHCP server
	dhcpCtx, dhcpCancel := context.WithCancel(context.Background())
	n.dhcpCancel = dhcpCancel

	// start dhcp server
	go func() {
		n.log.DebugContext(ctx, "starting dhcp server", "device", primaryBridge)

		if err := n.dhcpServer.Serve(dhcpCtx); err != nil && err != context.Canceled {
			n.log.ErrorContext(ctx, "error starting dhcp server", "err", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second*20)
	defer cancel()

	// configure forwarding
	n.log.DebugContext(ctx, "configuring forwarding", "device", primaryBridge)
	if err := n.applyIPTablesForwarding(ctx, primaryBridge); err != nil {
		return err
	}

	// configure NAT masquerade
	n.log.DebugContext(ctx, "configuring masquerade", "device", primaryBridge)
	if err := n.applyIPTablesMasquerade(ctx, primaryBridge, n.network); err != nil {
		return err
	}

	return nil
}
