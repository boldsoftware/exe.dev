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
