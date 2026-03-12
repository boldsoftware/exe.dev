package ipam

import (
	"errors"
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

	return &Manager{
		config:   cfg,
		ds:       ds,
		serverIP: serverIP,
		log:      log,
	}, nil
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

// Release releases the specified IP address
func (m *Manager) Release(ip string) error {
	err := m.ds.Release(ip)
	if err != nil {
		m.log.Warn("IP lease release failed", "ip", ip, "error", err)
	} else {
		m.log.Info("IP lease released", "ip", ip)
	}
	return err
}

// ListLeases returns all active IP leases
func (m *Manager) ListLeases() ([]*Lease, error) {
	return m.ds.List()
}
