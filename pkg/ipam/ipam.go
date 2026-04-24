package ipam

import (
	"errors"
	"fmt"
	"log/slog"
	"net"

	"github.com/c-robinson/iplib/v2"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("resource already exists")
)

type Manager struct {
	config   *Config
	ds       *Datastore
	serverIP net.IP
	log      *slog.Logger
}

// NewManager returns a new IP address manager
func NewManager(cfg *Config, log *slog.Logger) (*Manager, error) {
	ds, err := NewDatastore(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	// reserve first IP for server
	subnetIP, _, err := iplib.ParseCIDR(cfg.Network)
	if err != nil {
		return nil, err
	}
	serverIP := iplib.NextIP(subnetIP)

	m := &Manager{
		config:   cfg,
		ds:       ds,
		serverIP: serverIP,
		log:      log,
	}

	if err := m.seedCursorIfNeeded(); err != nil {
		return nil, fmt.Errorf("ipam: seed allocation cursor: %w", err)
	}

	return m, nil
}

// seedCursorIfNeeded initializes db.NextIP on upgraded hosts that already
// have leases but no persisted cursor. Without this, the first post-upgrade
// allocation would walk from the subnet base and hand out whatever sits in
// the lowest-numbered gap — which is precisely the "just-released IP
// immediately reused" pattern the cursor is meant to prevent. We seed the
// cursor one past the highest-indexed existing lease so the first
// allocation falls in untouched space. On a fresh host (no leases) the
// cursor stays empty and defaults to the subnet's first host address at
// allocation time.
func (m *Manager) seedCursorIfNeeded() error {
	m.ds.mu.Lock()
	defer m.ds.mu.Unlock()

	if m.ds.db.NextIP != "" {
		return nil
	}
	if len(m.ds.db.IPs) == 0 {
		return nil
	}

	_, network, err := iplib.ParseCIDR(m.config.Network)
	if err != nil {
		return err
	}
	firstU := iplib.IP4ToUint32(network.FirstAddress())
	lastU := iplib.IP4ToUint32(network.LastAddress())
	if lastU < firstU {
		return fmt.Errorf("invalid subnet bounds for %s", m.config.Network)
	}
	span := lastU - firstU + 1

	var (
		maxU  uint32
		found bool
	)
	for s := range m.ds.db.IPs {
		ip := net.ParseIP(s)
		if ip == nil {
			m.log.Warn("ipam seed: unparseable lease IP, skipping", "ip", s)
			continue
		}
		if !network.Contains(ip) {
			continue
		}
		u := iplib.IP4ToUint32(ip)
		if !found || u > maxU {
			maxU = u
			found = true
		}
	}
	if !found {
		return nil
	}

	nextU := firstU + ((maxU-firstU)+1)%span
	m.ds.db.NextIP = iplib.Uint32ToIP4(nextU).String()
	m.log.Info("ipam cursor seeded past existing leases",
		"cursor", m.ds.db.NextIP, "max_existing", iplib.Uint32ToIP4(maxU).String())
	return m.ds.saveDB()
}

// ServerIP returns the pre-reserved server IP
func (m *Manager) ServerIP() (net.IP, error) {
	return m.getServerIP()
}

// Reserve will reserve an IP for the specified mac address.
// The operation is atomic: the datastore lock is held for the entire
// check-existing + allocate-new sequence, preventing a concurrent Release
// from freeing an IP between the existence check and the caller using it.
func (m *Manager) Reserve(macAddress string) (net.IP, error) {
	m.ds.mu.Lock()
	defer m.ds.mu.Unlock()

	// Check if MAC already has a lease (lock held)
	if existing, ok := m.ds.db.Hosts[macAddress]; ok {
		m.log.Debug("IP lease already exists", "mac", macAddress, "ip", existing.IP)
		return net.ParseIP(existing.IP), nil
	}

	// Find and reserve an IP atomically (lock already held)
	for {
		ip, err := m.getNextIPLocked()
		if err != nil {
			return nil, err
		}
		err = m.ds.reserveLocked(macAddress, ip.String())
		if err == nil {
			m.log.Info("IP lease allocated", "mac", macAddress, "ip", ip.String())
			return ip, nil
		}
		if !errors.Is(err, ErrExists) {
			return nil, err
		}
		// IP was taken by another reservation, retry with next available
		m.log.Debug("IP lease collision, retrying", "mac", macAddress, "ip", ip.String())
	}
}

// Release releases the lease identified by (mac, ip). The MAC scopes the
// release to a specific allocation so that releasing a stale IP whose
// ownership has since transferred to another MAC is a safe no-op.
func (m *Manager) Release(mac, ip string) error {
	err := m.ds.Release(mac, ip)
	if err != nil {
		m.log.Warn("IP lease release failed", "mac", mac, "ip", ip, "error", err)
	} else {
		m.log.Info("IP lease released", "mac", mac, "ip", ip)
	}
	return err
}

// ListLeases returns all active IP leases
func (m *Manager) ListLeases() ([]*Lease, error) {
	return m.ds.List()
}
