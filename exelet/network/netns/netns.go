//go:build linux

// Package netns implements a network manager that uses per-VM network
// namespaces instead of IPAM. Each VM gets its own network namespace with
// a static IP (10.42.0.42/16), eliminating the need for IP address
// management entirely.
//
// # Architecture
//
// Cloud-hypervisor requires the TAP device to be in the root network
// namespace (it opens the TAP by name). So the topology per VM is:
//
//	Root namespace:
//	  tap-{vmid}    on br-{vmid}  (per-VM L2 bridge, no IP)
//	  vb-{vmid}     on br-{vmid}  (inner veth, root-ns side)
//	  vx-{vmid}     on br-exe     (outer veth, root-ns side, shared outbound bridge)
//
//	Per-VM netns (exe-{vmid}):
//	  vg-{vmid}     10.42.0.1/16  (inner veth peer, acts as VM gateway)
//	  ve-{vmid}     10.99.x.x/16  (outer veth peer, outbound connectivity)
//	  iptables: SNAT, forwarding, metadata DNAT, conn limit
//
// {vmid} is the numeric VM identifier prefix (e.g. "vm000003") extracted
// from the instance ID ("vm000003-orbit-falcon").
//
// The VM always sees IP 10.42.0.42/16 with gateway 10.42.0.1. Since each
// VM is in its own namespace, there are no IP conflicts. The /16 subnet
// ensures VMs migrated from the old NAT network manager (which assigns
// IPs across the full 10.42.0.0/16 range) can still reach the gateway.
//
// The metadata service identifies VMs by looking up which instance owns
// the source ext-IP (10.99.x.x) on the shared bridge.
package netns

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
)

const (
	VMSubnet    = "10.42.0.0/16"
	VMIP        = "10.42.0.42"
	VMGateway   = "10.42.0.1"
	VMCIDR      = VMIP + "/16"
	GatewayCIDR = VMGateway + "/16"

	// SharedBridgeBaseName is the base name for the outbound bridge(s) in the root namespace.
	// The primary bridge is named br-exe-0, secondary bridges br-exe-1, br-exe-2, etc.
	SharedBridgeBaseName = "br-exe"

	// SharedBridge is the primary shared bridge name (br-exe-0).
	SharedBridge        = SharedBridgeBaseName + "-0"
	SharedBridgeNetwork = "10.99.0.0/16"
	SharedBridgeGateway = "10.99.0.1"
	SharedBridgeCIDR    = SharedBridgeGateway + "/16"

	DefaultNameserver        = "1.1.1.1"
	DefaultNTPServer         = "ntp.ubuntu.com"
	MetadataIP               = "169.254.169.254"
	DeviceName               = "eth0"
	CarrierNATCIDR           = "100.64.0.0/10"
	DefaultConnLimit         = 10000
	DefaultBandwidthRate     = "125mbit"
	DefaultBandwidthCeil     = "250mbit"
	DefaultBandwidthBurst    = "1mb"
	DefaultMaxPortsPerBridge = 500
	DefaultBridgeHashMax     = 4096 // FDB hash table size; default 512 causes "exchange full" at scale
)

// bridgeInfo tracks a shared bridge and its current port count.
type bridgeInfo struct {
	name      string
	portCount int
}

// Manager implements network.NetworkManager using per-VM network namespaces.
type Manager struct {
	nameservers      []string
	ntpServer        string
	router           string
	log              *slog.Logger
	connLimit        int
	bandwidthRate    string
	bandwidthCeil    string
	bandwidthBurst   string
	disableBandwidth bool

	mu sync.Mutex
	// extIPs maps instance ID -> allocated ext IP on the shared bridge.
	extIPs     map[string]string
	nextOctet3 byte
	nextOctet4 byte

	// bridges tracks the shared outbound bridges and their port counts.
	// The primary bridge (index 0) has the gateway IP and iptables rules;
	// secondary bridges are connected to it via veth pairs.
	bridges           []bridgeInfo
	maxPortsPerBridge int
	bridgeCreateMu    sync.Mutex // serializes bridge creation

	// vmBridge maps instance ID -> shared bridge name the VM's outer veth is on.
	vmBridge map[string]string
}

// NewManager creates a new netns network manager from a netns:// URL.
func NewManager(addr string, log *slog.Logger) (*Manager, error) {
	u, err := url.Parse(addr)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(u.Scheme, "netns") {
		return nil, fmt.Errorf("invalid scheme for netns network manager: %s", addr)
	}

	nameservers := []string{DefaultNameserver}
	ntpServer := DefaultNTPServer
	router := VMGateway
	disableBandwidth := u.Query().Get("disable_bandwidth") == "true"

	if v := u.Query().Get("dns"); v != "" {
		nameservers = strings.Split(v, ",")
	}
	if v := u.Query().Get("ntp"); v != "" {
		ntpServer = v
	}
	if v := u.Query().Get("router"); v != "" {
		router = v
	}

	return &Manager{
		nameservers:       nameservers,
		ntpServer:         ntpServer,
		router:            router,
		log:               log,
		connLimit:         DefaultConnLimit,
		bandwidthRate:     DefaultBandwidthRate,
		bandwidthCeil:     DefaultBandwidthCeil,
		bandwidthBurst:    DefaultBandwidthBurst,
		disableBandwidth:  disableBandwidth,
		extIPs:            make(map[string]string),
		nextOctet3:        0,
		nextOctet4:        2, // skip .0 and .1
		bridges:           []bridgeInfo{{name: SharedBridge, portCount: 0}},
		maxPortsPerBridge: DefaultMaxPortsPerBridge,
		vmBridge:          make(map[string]string),
	}, nil
}

// Config returns the netns network configuration.
type Config struct {
	Bridge  string
	Network string
	Router  string
}

func (m *Manager) Config(_ context.Context) any {
	// Router for Config is SharedBridgeGateway (the root-namespace IP on
	// the shared bridge) — not m.router (VMGateway, which lives inside
	// each per-VM netns). The metadata service binds to Config.Router,
	// and the per-netns DNAT rules forward 169.254.169.254 traffic here.
	return &Config{Bridge: m.primaryBridgeName(), Network: VMSubnet, Router: SharedBridgeGateway}
}

// Close is a no-op.
func (m *Manager) Close() error { return nil }

// primaryBridgeName returns the name of the primary shared bridge.
func (m *Manager) primaryBridgeName() string {
	if len(m.bridges) > 0 {
		return m.bridges[0].name
	}
	return SharedBridge
}

// selectBridgeAndIncrement atomically selects a bridge with capacity and increments its port count.
// Returns the bridge name and whether a new bridge needs to be created.
func (m *Manager) selectBridgeAndIncrement() (bridgeName string, needsNewBridge bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.bridges {
		if m.bridges[i].portCount < m.maxPortsPerBridge {
			m.bridges[i].portCount++
			return m.bridges[i].name, false
		}
	}
	return "", true
}

// addBridgeAndSelect adds a new bridge to the list, increments its port count, and returns the name.
func (m *Manager) addBridgeAndSelect(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if another goroutine already added this bridge.
	for i := range m.bridges {
		if m.bridges[i].name == name {
			m.bridges[i].portCount++
			return name
		}
	}

	m.bridges = append(m.bridges, bridgeInfo{name: name, portCount: 1})
	return name
}

// reserveNextBridge returns the name that the next bridge should get.
func (m *Manager) reserveNextBridge() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("%s-%d", SharedBridgeBaseName, len(m.bridges))
}

// decrementBridgePort decrements the port count for the specified bridge.
func (m *Manager) decrementBridgePort(bridgeName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i := range m.bridges {
		if m.bridges[i].name == bridgeName {
			if m.bridges[i].portCount > 0 {
				m.bridges[i].portCount--
			}
			return
		}
	}
}

// setVMBridge records which shared bridge a VM's outer veth is attached to.
func (m *Manager) setVMBridge(id, bridgeName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.vmBridge[id] = bridgeName
}

// getVMBridge returns the shared bridge for a VM's outer veth.
func (m *Manager) getVMBridge(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.vmBridge[id]
}

// removeVMBridge removes the VM-to-bridge mapping.
func (m *Manager) removeVMBridge(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.vmBridge, id)
}

// allocateExtIP assigns a unique IP from 10.99.0.0/16 for this instance.
func (m *Manager) allocateExtIP(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ip, ok := m.extIPs[id]; ok {
		return ip, nil
	}

	used := make(map[string]struct{}, len(m.extIPs))
	for _, ip := range m.extIPs {
		used[ip] = struct{}{}
	}

	for range 65534 {
		ip := fmt.Sprintf("10.99.%d.%d", m.nextOctet3, m.nextOctet4)
		m.nextOctet4++
		if m.nextOctet4 == 0 {
			// octet4 wrapped past 255, advance to the next /24 block.
			// Skip .0 in every block (it's the network address for /24
			// interpretations and adds no value).
			m.nextOctet3++
			m.nextOctet4 = 1
			if m.nextOctet3 == 0 {
				// Full wrap around the /16. Skip 10.99.0.0 (network
				// address) and 10.99.0.1 (gateway).
				m.nextOctet4 = 2
			}
		}
		if _, ok := used[ip]; ok {
			continue
		}
		m.extIPs[id] = ip
		return ip, nil
	}
	return "", fmt.Errorf("exhausted shared bridge IP space")
}

func (m *Manager) releaseExtIP(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.extIPs, id)
}

// GetInstanceByExtIP maps a shared-bridge source IP back to an instance ID.
// Used by the metadata service.
func (m *Manager) GetInstanceByExtIP(ip string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, extIP := range m.extIPs {
		if extIP == ip {
			return id, true
		}
	}
	return "", false
}

// Naming helpers. All names must fit in IFNAMSIZ (15 chars).
//
// Instance IDs are formatted as "vm000003-orbit-falcon" by exed.
// We extract the "vm000003" prefix (the numeric VM identifier) for
// device names. This makes devices immediately identifiable during
// debugging — no hash lookup needed.

// vmID extracts the VM identifier prefix from an instance ID.
// Given "vm000003-orbit-falcon", returns "vm000003".
// If the ID doesn't match the expected format, it panics — the exed
// always generates IDs in this format.
func vmID(id string) string {
	idx := strings.Index(id, "-")
	if idx < 0 {
		panic(fmt.Sprintf("netns: instance ID %q has no dash; expected format vm000003-name", id))
	}
	prefix := id[:idx]
	if !strings.HasPrefix(prefix, "vm") {
		panic(fmt.Sprintf("netns: instance ID %q doesn't start with vm; expected format vm000003-name", id))
	}
	return prefix
}

func tapName(id string) string { return "tap-" + vmID(id) }
func nsName(id string) string  { return NsName(id) }

// NsName returns the network namespace name for a given instance ID.
func NsName(id string) string { return "exe-" + vmID(id) }
func brName(id string) string { return BridgeName(id) }

// BridgeName returns the per-VM bridge name for a given instance ID.
func BridgeName(id string) string { return "br-" + vmID(id) }
func vethBrName(id string) string { return "vb-" + vmID(id) }
func vethGwName(id string) string { return "vg-" + vmID(id) }
func vethXHost(id string) string  { return "vx-" + vmID(id) }
func vethXNS(id string) string    { return "ve-" + vmID(id) }

func randomMAC() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	buf[0] = 0b10 // unicast, locally administered
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x",
		buf[0], buf[1], buf[2], buf[3], buf[4], buf[5],
	), nil
}
