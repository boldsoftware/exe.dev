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
	DefaultBridgeName    = "br-exe"
	DefaultNetwork       = "10.42.0.0/16"
	DefaultNameserver    = "1.1.1.1"
	DefaultNTPServer     = "ntp.ubuntu.com"
	MetadataIP           = "169.254.169.254"
	DefaultMaxPortsPerBridge = 500

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

// bridgeInfo tracks a bridge and its current port count
type bridgeInfo struct {
	name      string
	portCount int
}

type NAT struct {
	bridgeBaseName    string
	network           string
	dhcpServer        *dhcpd.DHCPServer
	nameservers       []string
	ntpServer         string
	router            string
	availableIPs      map[string]net.IP
	allocatedIPs      []net.IP
	dhcpCancel        func() // cancel function for DHCP server context
	log               *slog.Logger
	bridges           []bridgeInfo
	maxPortsPerBridge int
	mu                sync.Mutex
	bridgeCreateMu    sync.Mutex // serializes bridge creation
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

	bridgeBaseName := DefaultBridgeName
	network := DefaultNetwork
	nameservers := []string{DefaultNameserver}
	ntpServer := DefaultNTPServer
	router := ""
	maxPortsPerBridge := DefaultMaxPortsPerBridge
	// configure bridge
	if v := u.Query().Get("bridge"); v != "" {
		bridgeBaseName = v
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

	// Primary bridge name is the base name with -0 suffix
	primaryBridgeName := fmt.Sprintf("%s-0", bridgeBaseName)

	// configure DHCP server
	dhcpSrv, err := dhcpd.NewDHCPServer(&dhcpd.Config{
		Interface:  primaryBridgeName,
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
		bridgeBaseName:    bridgeBaseName,
		network:           network,
		dhcpServer:        dhcpSrv,
		nameservers:       nameservers,
		ntpServer:         ntpServer,
		router:            router,
		log:               log,
		bridges:           []bridgeInfo{{name: primaryBridgeName, portCount: 0}},
		maxPortsPerBridge: maxPortsPerBridge,
	}

	return n, nil
}

// primaryBridgeName returns the name of the primary bridge
func (n *NAT) primaryBridgeName() string {
	if len(n.bridges) > 0 {
		return n.bridges[0].name
	}
	return fmt.Sprintf("%s-0", n.bridgeBaseName)
}

// selectBridge finds a bridge with available capacity or returns empty string if all full
func (n *NAT) selectBridge() string {
	n.mu.Lock()
	defer n.mu.Unlock()

	for i := range n.bridges {
		if n.bridges[i].portCount < n.maxPortsPerBridge {
			return n.bridges[i].name
		}
	}
	return ""
}

// selectBridgeAndIncrement atomically selects a bridge with capacity and increments its port count.
// Returns the bridge name and whether a new bridge needs to be created.
// If needsNewBridge is true, the caller should create the bridge and call addBridgeAndSelect.
func (n *NAT) selectBridgeAndIncrement() (bridgeName string, needsNewBridge bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	for i := range n.bridges {
		if n.bridges[i].portCount < n.maxPortsPerBridge {
			n.bridges[i].portCount++
			return n.bridges[i].name, false
		}
	}
	return "", true
}

// addBridgeAndSelect adds a new bridge to the list, increments its port count, and returns the name.
// This is called after creating the bridge when selectBridgeAndIncrement returned needsNewBridge=true.
func (n *NAT) addBridgeAndSelect(name string) string {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Check if another goroutine already added this bridge
	for i := range n.bridges {
		if n.bridges[i].name == name {
			n.bridges[i].portCount++
			return name
		}
	}

	n.bridges = append(n.bridges, bridgeInfo{name: name, portCount: 1})
	return name
}

// nextBridgeNameLocked returns the name for the next bridge to create.
// Must be called with n.mu held.
func (n *NAT) nextBridgeNameLocked() string {
	return fmt.Sprintf("%s-%d", n.bridgeBaseName, len(n.bridges))
}

// reserveNextBridge reserves the next bridge name and returns it.
// The caller must create the bridge and call addBridgeAndSelect.
func (n *NAT) reserveNextBridge() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return fmt.Sprintf("%s-%d", n.bridgeBaseName, len(n.bridges))
}

// incrementBridgePort increments the port count for the specified bridge
func (n *NAT) incrementBridgePort(bridgeName string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	for i := range n.bridges {
		if n.bridges[i].name == bridgeName {
			n.bridges[i].portCount++
			return
		}
	}
}

// decrementBridgePort decrements the port count for the specified bridge
func (n *NAT) decrementBridgePort(bridgeName string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	for i := range n.bridges {
		if n.bridges[i].name == bridgeName {
			if n.bridges[i].portCount > 0 {
				n.bridges[i].portCount--
			}
			return
		}
	}
}

// nextBridgeName returns the name for the next bridge to create
func (n *NAT) nextBridgeName() string {
	return fmt.Sprintf("%s-%d", n.bridgeBaseName, len(n.bridges))
}

// addBridge adds a new bridge to the tracking list
func (n *NAT) addBridge(name string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.bridges = append(n.bridges, bridgeInfo{name: name, portCount: 0})
}

// Close stops the NAT manager and cleans up resources
func (n *NAT) Close() error {
	// Stop DHCP server to close socket
	if n.dhcpServer != nil {
		if err := n.dhcpServer.Stop(); err != nil {
			n.log.Warn("failed to stop DHCP server", "error", err)
		}
	}

	return nil
}
