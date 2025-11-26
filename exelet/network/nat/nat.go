package nat

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync"

	"exe.dev/pkg/dhcpd"
)

const (
	DefaultBridgeName = "br-exe"
	DefaultNetwork    = "10.42.0.0/16"
	DefaultNameserver = "1.1.1.1"
	DefaultNTPServer  = "ntp.ubuntu.com"
	MetadataIP        = "169.254.169.254"

	DeviceName = "eth0"
)

// ErrNotImplemented is returned for functionality that is not implemented
var ErrNotImplemented = errors.New("not implemented")

// Config is the NAT specific configuration
type Config struct {
	Bridge  string
	Network string
	Router  string
}

type NAT struct {
	bridgeName   string
	network      string
	dhcpServer   *dhcpd.DHCPServer
	nameservers  []string
	ntpServer    string
	router       string
	mu           *sync.Mutex
	availableIPs map[string]net.IP
	allocatedIPs []net.IP
	log          *slog.Logger
}

func NewNATManager(addr string, log *slog.Logger) (*NAT, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(u.Scheme, "nat") {
		return nil, fmt.Errorf("invalid configuration specified for NAT manager: %s", addr)
	}

	if u.Path == "" {
		return nil, fmt.Errorf("path must be specified for nat network manager (e.g. nat:///tmp)")
	}

	bridgeName := DefaultBridgeName
	network := DefaultNetwork
	nameservers := []string{DefaultNameserver}
	ntpServer := DefaultNTPServer
	router := ""
	// configure bridge
	if v := u.Query().Get("bridge"); v != "" {
		bridgeName = v
	}
	if v := u.Query().Get("network"); v != "" {
		network = v
	}
	if v := u.Query().Get("dns"); v != "" {
		nameservers = strings.Split(v, ",")
	}
	if v := u.Query().Get("ntp"); v != "" {
		ntpServer = v
	}
	if v := u.Query().Get("router"); v != "" {
		router = v
	}

	// configure DHCP server
	dhcpSrv, err := dhcpd.NewDHCPServer(&dhcpd.Config{
		Interface:  bridgeName,
		DataDir:    u.Path,
		Network:    network,
		Port:       67,
		DNSServers: nameservers,
	}, log)
	if err != nil {
		return nil, err
	}

	// If router not explicitly set, use the bridge IP (first address in network)
	if router == "" {
		serverIP, err := dhcpSrv.ServerIP()
		if err != nil {
			return nil, fmt.Errorf("failed to get server IP: %w", err)
		}
		router = serverIP.String()
	}

	n := &NAT{
		bridgeName:  bridgeName,
		network:     network,
		dhcpServer:  dhcpSrv,
		nameservers: nameservers,
		ntpServer:   ntpServer,
		router:      router,
		mu:          &sync.Mutex{},
		log:         log,
	}

	return n, nil
}
