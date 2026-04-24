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

// getNextIPLocked returns the next available IP using a monotonic cursor
// (db.NextIP) that advances past each allocation and wraps at the subnet's
// last host address. A recently-released IP therefore sits unused until the
// cursor cycles all the way back around, which defuses races in which a
// lease is released (correctly or wrongly) and immediately re-handed to a
// different VM. The caller must hold m.ds.mu.
func (m *Manager) getNextIPLocked() (net.IP, error) {
	_, network, err := iplib.ParseCIDR(m.config.Network)
	if err != nil {
		return nil, err
	}
	firstU := iplib.IP4ToUint32(network.FirstAddress())
	lastU := iplib.IP4ToUint32(network.LastAddress())
	if lastU < firstU {
		return nil, fmt.Errorf("ipam: invalid subnet bounds for %s", m.config.Network)
	}
	span := lastU - firstU + 1
	serverU := iplib.IP4ToUint32(m.serverIP)

	cursorU := m.cursorStartLocked(firstU, lastU, network)

	for i := uint32(0); i < span; i++ {
		candidateU := firstU + ((cursorU - firstU + i) % span)
		if candidateU == serverU {
			continue
		}
		candidate := iplib.Uint32ToIP4(candidateU)
		if _, ok := m.ds.db.IPs[candidate.String()]; ok {
			continue
		}
		nextU := firstU + ((candidateU-firstU)+1)%span
		m.ds.db.NextIP = iplib.Uint32ToIP4(nextU).String()
		return candidate, nil
	}

	return nil, fmt.Errorf("no IPs available in %s", m.config.Network)
}

// cursorStartLocked returns the index to start scanning from. Falls back to
// the first host address if the persisted cursor is missing or outside the
// configured subnet (e.g., operator changed the CIDR between runs).
func (m *Manager) cursorStartLocked(firstU, lastU uint32, network iplib.Net) uint32 {
	s := m.ds.db.NextIP
	if s == "" {
		return firstU
	}
	ip := net.ParseIP(s)
	if ip == nil {
		m.log.Warn("ipam cursor unparseable, resetting", "cursor", s)
		return firstU
	}
	if !network.Contains(ip) {
		m.log.Warn("ipam cursor outside configured subnet, resetting",
			"cursor", s, "network", m.config.Network)
		return firstU
	}
	u := iplib.IP4ToUint32(ip)
	if u < firstU || u > lastU {
		return firstU
	}
	return u
}
