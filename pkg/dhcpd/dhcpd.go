package dhcpd

import (
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/c-robinson/iplib/v2"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
)

const (
	DefaultListenPort = 67
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("resource already exists")

	DefaultDNSServers = []string{"1.1.1.1"}

	leaseTTL = time.Second * 600
)

type DHCPServer struct {
	config   *Config
	srv      *server4.Server
	ds       *Datastore
	serverIP net.IP
	log      *slog.Logger
}

// NewDHCPServer returns a new DHCP server
func NewDHCPServer(cfg *Config, log *slog.Logger) (*DHCPServer, error) {
	if cfg.Port == 0 {
		cfg.Port = DefaultListenPort
	}

	if len(cfg.DNSServers) == 0 {
		cfg.DNSServers = DefaultDNSServers
	}

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

	return &DHCPServer{
		config:   cfg,
		ds:       ds,
		serverIP: serverIP,
		log:      log,
	}, nil
}

// ServerIP returns the pre-reserved server IP
func (s *DHCPServer) ServerIP() (net.IP, error) {
	return s.getServerIP()
}

// Reserve will reserve an IP for the specified mac address
func (s *DHCPServer) Reserve(macAddress string) (net.IP, error) {
	existing, err := s.ds.Get(&Query{MACAddress: macAddress})
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	// already reserved
	if existing != nil {
		return net.ParseIP(existing.IP), nil
	}

	// Retry loop to handle race conditions where another goroutine
	// reserves the same IP between getNextIP() and Reserve()
	for {
		ip, err := s.getNextIP()
		if err != nil {
			return nil, err
		}
		err = s.ds.Reserve(macAddress, ip.String())
		if err == nil {
			return ip, nil
		}
		if !errors.Is(err, ErrExists) {
			return nil, err
		}
		// IP was taken by another reservation, retry with next available
	}
}

// Release releases the specified IP address
func (s *DHCPServer) Release(ip string) error {
	return s.ds.Release(ip)
}

// ReleaseBatch releases multiple IP addresses in a single transaction
func (s *DHCPServer) ReleaseBatch(ips []string) error {
	return s.ds.ReleaseBatch(ips)
}

// ListLeases returns all active DHCP leases
func (s *DHCPServer) ListLeases() ([]*Lease, error) {
	return s.ds.List()
}
