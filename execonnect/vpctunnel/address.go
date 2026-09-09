package vpctunnel

import "net/netip"

var (
	tunnelAddressPrefix = netip.MustParsePrefix("fd65:7865::/48")
	serverTunnelAddress = netip.MustParseAddr("fd65:7865::1")
)

const (
	tunnelMTU = 1280
	flowPort  = 51821
)

// TunnelAddressPrefix returns the private IPv6 range used by the WireGuard data plane.
func TunnelAddressPrefix() netip.Prefix {
	return tunnelAddressPrefix
}

// ServerTunnelAddress returns the shared address of the WireGuard tunnel service.
func ServerTunnelAddress() netip.Addr {
	return serverTunnelAddress
}

// TunnelMTU returns the MTU used by the WireGuard data plane.
func TunnelMTU() int {
	return tunnelMTU
}

// FlowPort returns the inner TCP port used for External Connection flows.
func FlowPort() uint16 {
	return flowPort
}
