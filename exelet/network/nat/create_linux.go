//go:build linux

package nat

import (
	"context"
	"fmt"
	"net"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func (n *NAT) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	tapName := getTapID(id)

	// Select a bridge with available capacity (atomically increments port count)
	bridgeName, needsNewBridge := n.selectBridgeAndIncrement()
	if needsNewBridge {
		// All bridges are full, need to create a new one
		// Use bridgeCreateMu to serialize bridge creation
		n.bridgeCreateMu.Lock()

		// Re-check after acquiring lock - another goroutine may have created a bridge
		bridgeName, needsNewBridge = n.selectBridgeAndIncrement()
		if needsNewBridge {
			// Still need to create - get the next bridge name
			newBridgeName := n.reserveNextBridge()
			if err := n.createSecondaryBridge(ctx, newBridgeName); err != nil {
				n.bridgeCreateMu.Unlock()
				return nil, fmt.Errorf("failed to create secondary bridge: %w", err)
			}
			bridgeName = n.addBridgeAndSelect(newBridgeName)
		}
		n.bridgeCreateMu.Unlock()
	}

	// Track cleanup actions for rollback on error
	var cleanupTap, cleanupIP, cleanupConnLimit bool
	var ipStr string

	cleanup := func() {
		if cleanupConnLimit && ipStr != "" {
			_ = n.removeConnLimit(ctx, ipStr)
		}
		if cleanupIP && ipStr != "" {
			_ = n.dhcpServer.Release(ipStr)
		}
		if cleanupTap {
			_ = n.removeBandwidthLimit(ctx, tapName)
			_ = n.deleteTapInterface(tapName)
			n.decrementBridgePort(bridgeName)
		}
	}

	link, err := n.createTapInterface(tapName, bridgeName)
	if err != nil {
		// Decrement port count since we failed to create the TAP
		n.decrementBridgePort(bridgeName)
		return nil, err
	}
	cleanupTap = true

	macAddress, err := randomMAC()
	if err != nil {
		cleanup()
		return nil, err
	}

	ip, err := n.dhcpServer.Reserve(macAddress)
	if err != nil {
		cleanup()
		return nil, err
	}
	ipStr = ip.String()
	cleanupIP = true

	// Apply connection limit for this VM
	if err := n.applyConnLimit(ctx, ipStr); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to apply connection limit: %w", err)
	}
	cleanupConnLimit = true

	// Apply bandwidth limit to the TAP device
	if err := n.applyBandwidthLimit(ctx, tapName); err != nil {
		cleanup()
		return nil, fmt.Errorf("failed to apply bandwidth limit: %w", err)
	}

	gwIP, err := n.dhcpServer.ServerIP()
	if err != nil {
		cleanup()
		return nil, err
	}
	_, ipnet, err := net.ParseCIDR(n.network)
	if err != nil {
		cleanup()
		return nil, err
	}
	sz, _ := ipnet.Mask.Size()

	iface := &api.NetworkInterface{
		Name:       link.Attrs().Name,
		DeviceName: DeviceName,
		Type:       api.NetworkInterface_TYPE_TAP,
		MACAddress: macAddress,
		IP: &api.IPAddress{
			IPV4:      fmt.Sprintf("%s/%d", ip, sz),
			GatewayV4: gwIP.String(),
		},
		Nameservers: n.nameservers,
		Network:     n.network,
		NTPServer:   n.ntpServer,
	}

	if v := n.router; v != "" {
		iface.Router = v
	}

	return iface, nil
}

// ApplyConnectionLimit applies a connection limit rule for the given IP.
// This is used to apply limits to existing VMs at startup.
func (n *NAT) ApplyConnectionLimit(ctx context.Context, ip string) error {
	return n.applyConnLimit(ctx, ip)
}

// ApplyBandwidthLimit applies bandwidth limiting to an existing TAP device.
// This is used to apply limits to existing VMs at startup.
func (n *NAT) ApplyBandwidthLimit(ctx context.Context, id string) error {
	tapName := getTapID(id)
	return n.applyBandwidthLimit(ctx, tapName)
}
