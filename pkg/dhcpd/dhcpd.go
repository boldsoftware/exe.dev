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

	ip, err := s.getNextIP()
	if err != nil {
		return nil, err
	}
	if err := s.ds.Reserve(macAddress, ip.String()); err != nil {
		return nil, err
	}
	return ip, nil
}

// Release releases the specified IP address
func (s *DHCPServer) Release(ip string) error {
	return s.ds.Release(ip)
}
