package ipam

import (
	"errors"
	"fmt"
	"net"

	iplib "github.com/c-robinson/iplib/v2"
)

func (m *Manager) getServerIP() (net.IP, error) {
	_, network, err := iplib.ParseCIDR(m.config.Network)
	if err != nil {
		return nil, err
	}

	return network.FirstAddress(), nil
}

func (m *Manager) getNextIP() (net.IP, error) {
	subnetIP, network, err := iplib.ParseCIDR(m.config.Network)
	if err != nil {
		return nil, err
	}

	ip := subnetIP
	for {
		next := iplib.NextIP(ip)

		// Skip the server IP (first address in network)
		if next.Equal(m.serverIP) {
			ip = next
			continue
		}

		if _, err := m.ds.Get(&Query{IP: next.String()}); err != nil {
			if !errors.Is(err, ErrNotFound) {
				return nil, err
			}

			return next, nil
		}
		if next.Equal(network.LastAddress()) {
			break
		}

		ip = next
	}

	return nil, fmt.Errorf("no IPs available in %s", m.config.Network)
}
