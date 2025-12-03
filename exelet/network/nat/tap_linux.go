//go:build linux

package nat

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// listTapInterfaces returns all tap interfaces with their MAC addresses
func (n *NAT) listTapInterfaces() ([]TapInterface, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list network interfaces: %w", err)
	}

	var taps []TapInterface
	for _, link := range links {
		attrs := link.Attrs()
		// Check if this is a tap interface (starts with "tap-")
		if len(attrs.Name) >= 4 && attrs.Name[:4] == "tap-" {
			taps = append(taps, TapInterface{
				Name:       attrs.Name,
				MACAddress: net.HardwareAddr(attrs.HardwareAddr).String(),
			})
		}
	}

	return taps, nil
}
