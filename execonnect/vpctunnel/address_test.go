package vpctunnel

import (
	"net/netip"
	"testing"
)

func TestServerTunnelAddress(t *testing.T) {
	address := ServerTunnelAddress()
	if want := netip.MustParseAddr("fd65:7865::1"); address != want {
		t.Fatalf("server tunnel address = %v, want %v", address, want)
	}
	if !TunnelAddressPrefix().Contains(address) {
		t.Fatalf("server tunnel address %v is outside %v", address, TunnelAddressPrefix())
	}
}

func TestTunnelMTU(t *testing.T) {
	if mtu := TunnelMTU(); mtu != 1280 {
		t.Fatalf("tunnel MTU = %d, want 1280", mtu)
	}
}

func TestFlowPort(t *testing.T) {
	if port := FlowPort(); port != 51821 {
		t.Fatalf("flow port = %d, want 51821", port)
	}
}
