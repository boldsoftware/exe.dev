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
	link, err := n.createTapInterface(tapName)
	if err != nil {
		return nil, err
	}
	macAddress, err := randomMAC()
	if err != nil {
		return nil, err
	}

	ip, err := n.dhcpServer.Reserve(macAddress)
	if err != nil {
		return nil, err
	}
	gwIP, err := n.dhcpServer.ServerIP()
	if err != nil {
		return nil, err
	}
	_, ipnet, err := net.ParseCIDR(n.network)
	if err != nil {
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
