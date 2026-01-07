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

// Reserve will reserve an IP for the specified mac address
func (m *Manager) Reserve(macAddress string) (net.IP, error) {
	existing, err := m.ds.Get(&Query{MACAddress: macAddress})
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	// already reserved
	if existing != nil {
		m.log.Debug("IP lease already exists", "mac", macAddress, "ip", existing.IP)
		return net.ParseIP(existing.IP), nil
	}

	// Retry loop to handle race conditions where another goroutine
	// reserves the same IP between getNextIP() and Reserve()
	for {
		ip, err := m.getNextIP()
		if err != nil {
			return nil, err
		}
		err = m.ds.Reserve(macAddress, ip.String())
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
