//go:build linux

package nat

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"

	"exe.dev/pkg/ipam"
)

const (
	DefaultBridgeName        = "br-exe"
	DefaultNetwork           = "10.42.0.0/16"
	DefaultNameserver        = "1.1.1.1"
	DefaultNTPServer         = "ntp.ubuntu.com"
	MetadataIP               = "169.254.169.254"
	DefaultMaxPortsPerBridge = 500
	DefaultBridgeHashMax     = 4096 // FDB hash table size; default 512 causes "exchange full" at scale
	CarrierNATCIDR           = "100.64.0.0/10"
	DefaultConnLimit         = 10000 // Max concurrent connections per VM

	// Bandwidth limiting defaults (per VM upload limit)
	DefaultBandwidthRate  = "100mbit" // Max upload bandwidth per VM
	DefaultBandwidthBurst = "256k"    // HTB burst size

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
	ipam              *ipam.Manager
	nameservers       []string
	ntpServer         string
	router            string
	log               *slog.Logger
	bridges           []bridgeInfo
	maxPortsPerBridge int
	connLimit         int    // max concurrent connections per VM
	bandwidthRate     string // max upload bandwidth per VM (e.g., "100mbit")
	bandwidthBurst    string // police burst size (e.g., "15k")
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

	// configure IPAM
	ipamMgr, err := ipam.NewManager(&ipam.Config{
		DataDir: u.Path,
		Network: network,
	}, log)
	if err != nil {
		return nil, err
	}

	// If router not explicitly set, use the bridge IP (first address in network)
	if router == "" {
		serverIP, err := ipamMgr.ServerIP()
		if err != nil {
			return nil, fmt.Errorf("failed to get server IP: %w", err)
		}
		router = serverIP.String()
	}

	n := &NAT{
		bridgeBaseName:    bridgeBaseName,
		network:           network,
		ipam:              ipamMgr,
		nameservers:       nameservers,
		ntpServer:         ntpServer,
		router:            router,
		log:               log,
		bridges:           []bridgeInfo{{name: primaryBridgeName, portCount: 0}},
		maxPortsPerBridge: maxPortsPerBridge,
		connLimit:         DefaultConnLimit,
		bandwidthRate:     DefaultBandwidthRate,
		bandwidthBurst:    DefaultBandwidthBurst,
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

// reserveNextBridge reserves the next bridge name and returns it.
// The caller must create the bridge and call addBridgeAndSelect.
func (n *NAT) reserveNextBridge() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return fmt.Sprintf("%s-%d", n.bridgeBaseName, len(n.bridges))
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

// Close stops the NAT manager and cleans up resources
func (n *NAT) Close() error {
	return nil
}
