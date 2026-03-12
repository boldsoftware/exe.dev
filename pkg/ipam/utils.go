package ipam

import (
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
	m.ds.mu.Lock()
	defer m.ds.mu.Unlock()
	return m.getNextIPLocked()
}

// getNextIPLocked finds the next available IP. The caller must hold m.ds.mu.
func (m *Manager) getNextIPLocked() (net.IP, error) {
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

		// Check directly against the in-memory db (lock already held)
		if _, ok := m.ds.db.IPs[next.String()]; !ok {
			return next, nil
		}
		if next.Equal(network.LastAddress()) {
			break
		}

		ip = next
	}

	return nil, fmt.Errorf("no IPs available in %s", m.config.Network)
}
