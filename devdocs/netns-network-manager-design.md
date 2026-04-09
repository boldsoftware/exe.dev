# Implementation Design Document: Netns Network Manager

## 1. Overview

### What is the netns network manager?

The netns (network namespace) network manager is an alternative to the existing NAT-based network manager for isolating VM network traffic on exelet hosts. Instead of assigning each VM a unique IP from a shared IPAM pool (the NAT approach), the netns manager places each VM in its own Linux network namespace where every VM sees the **same static IP** (`10.42.0.42/16`) with gateway `10.42.0.1`.

### Why does it exist?

The NAT manager assigns unique IPs to each VM from a shared `10.42.0.0/16` pool. This creates several operational challenges:

1. **IP address management complexity**: IPAM leases must be tracked, reconciled, and recovered on restart.
2. **Migration friction**: When a VM migrates between hosts, it gets a new IP. The orchestrator must SSH into the running VM to reconfigure its network interface, update `/etc/hosts`, etc. This is fragile and slow.
3. **Scalability limits**: A single `/16` subnet limits the number of VMs per host, and shared bridge FDB tables become bottlenecks.

The netns manager eliminates these problems:

- **No IPAM**: Every VM is `10.42.0.42`. No address conflicts because each VM is in its own namespace.
- **Simpler migration**: When migrating between two netns hosts, the VM IP doesn't change, so in-guest IP reconfiguration can be skipped entirely (`skip_ip_reconfig`).
- **Better isolation**: Each VM has its own iptables, routing table, and connection tracking — no shared firewall state.
- **Scalable bridging**: A bridge-splitting mechanism creates secondary shared bridges when port count exceeds limits.

### How is it selected?

The network manager is selected by URL scheme in the `--network-manager-address` flag:
- `nat:///path?network=10.42.0.0/16` → NAT manager (existing)
- `netns:///` → Netns manager (new)

---

## 2. Architecture

### Network Manager Abstraction

The existing `NetworkManager` interface in `exelet/network/network.go` defines:

```go
type NetworkManager interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Config(ctx context.Context) any
    Close() error
    CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error)
    DeleteInterface(ctx context.Context, id, ip string) error
    ApplyConnectionLimit(ctx context.Context, ip string) error
    ApplyBandwidthLimit(ctx context.Context, id string) error
    ReconcileLeases(ctx context.Context, validIPs map[string]struct{}) ([]string, error)
}
```

The netns manager implements this interface. It also implements a new `ExtIPLookup` interface for metadata service integration.

### Component Interaction

```
exelet main
  ├── parses --network-manager-address URL
  ├── if scheme == "netns":
  │     ├── creates netns.Manager
  │     ├── sets ProxyBindDevFunc = netns.BridgeName (for socat SO_BINDTODEVICE)
  │     └── sets ProxyNetnsFunc = netns.NsName (for exepipe netns-aware dialing)
  ├── compute service
  │     ├── SSH proxy manager (socat or exepipe)
  │     │     ├── socat: uses SO_BINDTODEVICE on per-VM bridge
  │     │     └── exepipe: passes netns name, exepipe dials from within netns
  │     ├── receive_vm: sets skip_ip_reconfig in ReceiveVMReady
  │     └── GetInstanceByIP: falls back to ExtIPLookup for metadata
  └── metadata service
        └── listens on SharedBridgeGateway (10.99.0.1:80)
```

---

## 3. Network Topology

### Per-VM Network Stack

For each VM (e.g., instance ID `vm000003-orbit-falcon`):

```
┌─── Root Network Namespace ───────────────────────────────────────┐
│                                                                   │
│  ┌─ Per-VM Bridge: br-vm000003 ─┐     ┌─ Shared Bridge: br-exe-0 ┐
│  │  IP: 10.42.0.1/16            │     │  IP: 10.99.0.1/16        │
│  │                               │     │                           │
│  │  tap-vm000003  (TAP device)  │     │  vx-vm000003              │
│  │  vb-vm000003   (veth root)   │     │   (veth root, outer)      │
│  └───────────────────────────────┘     └───────────────────────────┘
│         │                                       │
│         │ veth pair                              │ veth pair
│         ▼                                       ▼
│ ┌─── Per-VM Netns: exe-vm000003 ──────────────────────────────────┐
│ │                                                                   │
│ │  vg-vm000003  10.42.0.1/16  (inner veth, gateway for VM)        │
│ │  ve-vm000003  10.99.0.2/16  (outer veth, unique ext IP)         │
│ │                                                                   │
│ │  Routes:                                                          │
│ │    default via 10.99.0.1 dev ve-vm000003                         │
│ │                                                                   │
│ │  iptables:                                                        │
│ │    SNAT -s 10.42.0.0/16 -o ve-vm000003 --to-source 10.99.0.2   │
│ │    FORWARD -i vg-vm000003 -o ve-vm000003 ACCEPT                 │
│ │    FORWARD -i ve-vm000003 -o vg-vm000003 RELATED,ESTABLISHED    │
│ │    FORWARD -i vg-vm000003 -d 100.64.0.0/10 DROP                │
│ │    DNAT 169.254.169.254:80  → 10.99.0.1:80  (metadata)         │
│ │    DNAT 169.254.169.254:443 → 10.99.0.1:2443 (metadata TLS)    │
│ │    INPUT -i vg-vm000003 -d 10.42.0.1 --syn DROP                │
│ │    connlimit per VM IP                                           │
│ │    tc HTB bandwidth shaping on ve-vm000003 egress               │
│ └───────────────────────────────────────────────────────────────────┘
│                                                                   │
│  Cloud-Hypervisor opens tap-vm000003 in root ns                   │
│  VM guest sees: eth0 = 10.42.0.42/16, gw 10.42.0.1              │
└───────────────────────────────────────────────────────────────────┘
```

### Naming Convention

All interface names must fit in IFNAMSIZ (15 chars). The `vmID` is extracted from instance IDs like `vm000003-orbit-falcon` → `vm000003` (8 chars).

| Device | Name Pattern | Location | Purpose |
|--------|-------------|----------|---------|
| TAP | `tap-vm000003` (12) | root ns, on per-VM bridge | Cloud-hypervisor opens this |
| Per-VM bridge | `br-vm000003` (11) | root ns | L2 bridge connecting TAP and inner veth |
| Inner veth (root) | `vb-vm000003` (11) | root ns, on per-VM bridge | Root-side of inner veth pair |
| Inner veth (ns) | `vg-vm000003` (11) | per-VM netns | Gateway veth inside namespace |
| Outer veth (root) | `vx-vm000003` (11) | root ns, on shared bridge | Root-side of outer veth pair |
| Outer veth (ns) | `ve-vm000003` (11) | per-VM netns | Outbound veth inside namespace |
| Namespace | `exe-vm000003` (12) | — | Network namespace name |

### Shared Bridge Architecture

The primary shared bridge `br-exe-0` has:
- Gateway IP `10.99.0.1/16`
- Host iptables: MASQUERADE for `10.99.0.0/16`, FORWARD rules
- When port count exceeds `DefaultMaxPortsPerBridge` (500), secondary bridges (`br-exe-1`, `br-exe-2`, ...) are created and connected to the primary via veth pairs.

### Ext IP Allocation

Each VM gets a unique "ext IP" from `10.99.0.0/16` (e.g., `10.99.0.2`). This is assigned to the outer veth (`ve-vm000003`) inside the namespace. The metadata service uses this ext IP to identify which VM is making a request, since all VMs share the same internal IP `10.42.0.42`.

### SSH Proxy Routing

In netns mode, the SSH proxy must route traffic to the VM's `10.42.0.42` through the correct per-VM bridge. Two mechanisms:

1. **Socat mode**: Uses `so-bindtodevice=br-vm000003` to bind the connect-side socket to the per-VM bridge, which has `10.42.0.1/16` and can reach the VM.
2. **Exepipe mode**: Passes the namespace name `exe-vm000003` to exepipe, which enters the namespace before dialing `10.42.0.42:22`.

---

## 4. New Files to Create

### 4.1 `exelet/network/netns/netns.go`

This is the main package file (Linux build). Contains the `Manager` struct, constructor, IP allocation, naming helpers, and constants.

```go
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
	DefaultBandwidthBurst    = "200mb"
	DefaultMaxPortsPerBridge = 500
	DefaultBridgeHashMax     = 4096 // FDB hash table size; default 512 causes "exchange full" at scale
)

// bridgeInfo tracks a shared bridge and its current port count.
type bridgeInfo struct {
	name      string
	portCount int
}

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

type Config struct {
	Network string
	Router  string
}

func (m *Manager) Config(_ context.Context) any {
	return &Config{Network: VMSubnet, Router: m.router}
}

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
			m.nextOctet3++
			m.nextOctet4 = 1
			if m.nextOctet3 == 0 {
				m.nextOctet3 = 0
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

// vmID extracts the VM identifier prefix from an instance ID.
// Given "vm000003-orbit-falcon", returns "vm000003".
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
```

### 4.2 `exelet/network/netns/netns_other.go`

Non-Linux stub so the package compiles cross-platform:

```go
//go:build !linux

package netns

import (
	"context"
	"errors"
	"log/slog"

	api "exe.dev/pkg/api/exe/compute/v1"
)

var errNotSupported = errors.New("netns network manager is only supported on Linux")

type Manager struct{}

type Config struct {
	Network string
	Router  string
}

func NewManager(addr string, log *slog.Logger) (*Manager, error) {
	return nil, errNotSupported
}

func (m *Manager) Start(ctx context.Context) error { return errNotSupported }
func (m *Manager) Stop(ctx context.Context) error  { return errNotSupported }
func (m *Manager) Config(_ context.Context) any    { return nil }
func (m *Manager) Close() error                    { return errNotSupported }

func (m *Manager) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	return nil, errNotSupported
}

func (m *Manager) DeleteInterface(ctx context.Context, id, ip string) error {
	return errNotSupported
}

func (m *Manager) ApplyConnectionLimit(ctx context.Context, ip string) error {
	return errNotSupported
}

func (m *Manager) ApplyBandwidthLimit(ctx context.Context, id string) error {
	return errNotSupported
}

func (m *Manager) ReconcileLeases(ctx context.Context, validIPs map[string]struct{}) ([]string, error) {
	return nil, errNotSupported
}

func (m *Manager) GetInstanceByExtIP(ip string) (string, bool) {
	return "", false
}
```

### 4.3 `exelet/network/netns/create_linux.go`

Contains `CreateInterface`, `configureNS`, `ApplyConnectionLimit`, `ApplyBandwidthLimit`, and netlink helpers. **Use the complete content from the "NEW FILES" section above** (the file labeled `netns_create.go`). Place it at path `exelet/network/netns/create_linux.go`.

### 4.4 `exelet/network/netns/delete_linux.go`

Contains `DeleteInterface` and `ReconcileLeases` (no-op). **Use the complete content from** the file labeled `netns_delete.go` above. Place at `exelet/network/netns/delete_linux.go`.

### 4.5 `exelet/network/netns/start_linux.go`

Contains `Start`, `Stop`, `recoverSecondaryBridges`, `createSecondaryBridge`, `setBridgeHashMax`. **Use the complete content from** the file labeled `netns_start.go` above. Place at `exelet/network/netns/start_linux.go`.

### 4.6 `exelet/network/netns/recover_linux.go`

Contains `RecoverExtIPs` and helpers for reading addresses from namespaces. **Use the complete content from** the file labeled `netns_recover.go` above. Place at `exelet/network/netns/recover_linux.go`.

### 4.7 `exelet/network/netns/utils_linux.go`

Contains `writeSysctl` and `ensureIptablesRule`. **Use the complete content from** the file labeled `netns_utils.go` above. Place at `exelet/network/netns/utils_linux.go`.

### 4.8 `exelet/network/netns/debug_linux.go`

Diagnostic tooling for inspecting netns state. **Use the complete content from** the file labeled `netns_debug.go` above. Place at `exelet/network/netns/debug_linux.go`.

### 4.9 `exelet/network/netns/debug_other.go`

Non-Linux stub for diagnostics. **Use the complete content from** the file labeled `netns_debug_other.go` above. Place at `exelet/network/netns/debug_other.go`.

### 4.10 `exelet/network/netns/live_linux.go`

Live conntrack streaming for debugging. **Use the complete content from** the file labeled `netns_live.go` above. Place at `exelet/network/netns/live_linux.go`.

### 4.11 `exelet/network/netns/live_other.go`

Non-Linux stub. **Use the complete content from** the file labeled `netns_live_other.go` above. Place at `exelet/network/netns/live_other.go`.

### 4.12 `exelet/network/netns/netns_linux_test.go`

Tests for the netns manager. **Use the complete content from** the file labeled `netns_test.go` above. Place at `exelet/network/netns/netns_linux_test.go`.

### 4.13 `cmd/exelet-netns/main.go`

Diagnostic CLI tool. **Use the complete content from** the file labeled `exelet_netns_main.go` above. Place at `cmd/exelet-netns/main.go`.

### 4.14 `exepipe/dial_netns_linux.go`

Netns-aware dialing for exepipe on Linux. **Use the complete content from** the file labeled `dial_netns_linux.go` above. Place at `exepipe/dial_netns_linux.go`.

### 4.15 `exepipe/dial_netns_other.go`

Non-Linux stub. **Use the complete content from** the file labeled `dial_netns_other.go` above. Place at `exepipe/dial_netns_other.go`.

### 4.16 `exelet/services/compute/receive_vm_test.go`

Tests for `sameVMIP` and `editSnapshotConfig` tap name update. **Use the complete content from** the file labeled `receive_vm_test.go` above. Place at `exelet/services/compute/receive_vm_test.go`.

---

## 5. Existing Files to Modify

### 5.1 `exelet/network/network.go`

**Add the `ExtIPLookup` interface** before the existing `NetworkManager` interface:

```go
// ExtIPLookup is an optional interface implemented by network managers
// that support looking up instances by their external IP address.
// The netns manager implements this: each VM has a unique ext IP on
// the shared bridge, used by the metadata service to identify VMs.
type ExtIPLookup interface {
	GetInstanceByExtIP(ip string) (instanceID string, ok bool)
}
```

### 5.2 `exelet/network/network_linux.go`

**Add the netns import and case to `NewNetworkManager`**:

Add import:
```go
"exe.dev/exelet/network/netns"
```

In the `switch strings.ToLower(u.Scheme)` block, add after the `"nat"` case:
```go
case "netns":
    return netns.NewManager(addr, log)
```

### 5.3 `exelet/config/config.go`

**Add two new fields to `ExeletConfig`** after the existing `ProxyBindIP` field:

```go
// ProxyBindDevFunc optionally returns the device name for SO_BINDTODEVICE
// on the connect side of SSH proxies. Used in netns mode where each VM
// has its own per-VM bridge.
ProxyBindDevFunc func(instanceID string) string
// ProxyNetnsFunc optionally returns the network namespace name for an
// instance. Used by exepipe to dial from within the VM's netns.
ProxyNetnsFunc func(instanceID string) string
```

### 5.4 `cmd/exelet/main.go`

**Add imports**:
```go
"exe.dev/exelet/network/netns"
```
(Also ensure `"net/url"` and `"strings"` are imported — they likely already are.)

**After the `ExeletConfig` struct is populated** (after the block that sets `PktFlowMaxFlows`), add:

```go
// In netns mode, the SSH proxy needs SO_BINDTODEVICE to route
// through the per-VM bridge.
if nmURL, err := url.Parse(networkManagerAddress); err == nil && strings.EqualFold(nmURL.Scheme, "netns") {
    cfg.ProxyBindDevFunc = netns.BridgeName
    cfg.ProxyNetnsFunc = netns.NsName
}
```

**Replace the metadata service listen address block**. Find the section that currently reads the NAT config for the metadata service listen address. Replace it with a type switch:

```go
var metadataListenAddr string
networkConfig := nm.Config(ctx)
switch cfg := networkConfig.(type) {
case *nat.Config:
    if cfg == nil || cfg.Router == "" {
        return fmt.Errorf("failed to get NAT configuration for metadata service")
    }
    metadataListenAddr = cfg.Router + ":80"
case *netns.Config:
    // In netns mode, the metadata service listens on the shared bridge
    // gateway. VMs reach it via DNAT inside their namespace, and the
    // traffic arrives SNATed with the VM's ext IP as source.
    metadataListenAddr = netns.SharedBridgeGateway + ":80"
default:
    return fmt.Errorf("unsupported network config type for metadata service: %T", networkConfig)
}
log.InfoContext(ctx, "metadata service listen address", "addr", metadataListenAddr)
```

### 5.5 `exelet/services/compute/compute.go`

**Add a `newProxyManager` helper function** before or after the `New` function:

```go
func newProxyManager(ctx context.Context, cfg *config.ExeletConfig, log *slog.Logger) sshproxy.Manager {
	var opts sshproxy.ProxyOpts
	if cfg.ProxyBindDevFunc != nil {
		opts.BindDev = sshproxy.BindDevFunc(cfg.ProxyBindDevFunc)
	}
	if cfg.ProxyNetnsFunc != nil {
		opts.NetnsFunc = sshproxy.NetnsFunc(cfg.ProxyNetnsFunc)
	}
	return sshproxy.NewManager(ctx, cfg.DataDir, cfg.ProxyBindIP, cfg.ExepipeAddress, log, opts)
}
```

**Change the `proxyManager` initialization in `New`** from:
```go
proxyManager: sshproxy.NewManager(ctx, cfg.DataDir, cfg.ProxyBindIP, cfg.ExepipeAddress, log),
```
to:
```go
proxyManager: newProxyManager(ctx, cfg, log),
```

### 5.6 `exelet/services/compute/utils.go`

**Add import**:
```go
"exe.dev/exelet/network"
```

**In `GetInstanceByIP`**, after the existing loop that searches by VM IP, add a fallback for netns ext IP lookup before the final error return:

```go
// In netns mode, all VMs share the same internal IP (10.42.0.42).
// The metadata service sees the VM's unique ext IP (10.99.x.x) as
// the source. Fall back to the network manager's ext IP lookup.
if lookup, ok := s.context.NetworkManager.(network.ExtIPLookup); ok {
    if instanceID, found := lookup.GetInstanceByExtIP(ip); found {
        for _, instance := range instances {
            if instance.ID == instanceID {
                return instance.ID, instance.Name, nil
            }
        }
    }
}
```

### 5.7 `exelet/services/compute/receive_vm.go`

**Add import**:
```go
"exe.dev/exelet/network"
```

**In `receiveVM`, after the network interface is allocated and before sending the ready response**, add the `skipIPReconfig` logic:

```go
// Skip SSH IP reconfiguration only when the target uses IP isolation
// (e.g. netns) AND the source VM already has the same IP as the target.
_, hasIPIsolation := s.context.NetworkManager.(network.ExtIPLookup)
skipIPReconfig := hasIPIsolation && sameVMIP(startReq.SourceInstance, targetNetwork)
```

**Modify the `ReceiveVMReady` message** to include `SkipIpReconfig`:
```go
Ready: &api.ReceiveVMReady{
    HasBaseImage:   hasBaseImage,
    TargetNetwork:  targetNetwork,
    SkipIpReconfig: skipIPReconfig,
},
```

**Add the `sameVMIP` helper function** (after `editSnapshotConfig` or at the end of the file):

```go
// sameVMIP reports whether the source instance and target network have the
// same VM IP (ignoring the CIDR prefix length). When they match, no in-guest
// IP reconfiguration is needed during live migration.
func sameVMIP(source *api.Instance, target *api.NetworkInterface) bool {
	if source == nil || source.VMConfig == nil || source.VMConfig.NetworkInterface == nil ||
		source.VMConfig.NetworkInterface.IP == nil || target == nil || target.IP == nil {
		return false
	}
	srcIP, _, err1 := net.ParseCIDR(source.VMConfig.NetworkInterface.IP.IPV4)
	dstIP, _, err2 := net.ParseCIDR(target.IP.IPV4)
	if err1 != nil || err2 != nil {
		return false
	}
	return srcIP.Equal(dstIP)
}
```

**In `editSnapshotConfig`**, add TAP name patching after the existing disk/kernel/cmdline patching:

```go
// Update network tap name: the source exelet may use a different tap naming
// scheme (e.g. NAT uses random IDs like "tap-5c4c99", netns uses deterministic
// names like "tap-vm000001"). CHV restores will create a new tap with the old
// name if we don't patch it, leaving the VM disconnected from the target bridge.
if targetNetwork != nil && targetNetwork.Name != "" {
    if nets, ok := config["net"].([]any); ok && len(nets) > 0 {
        if netCfg, ok := nets[0].(map[string]any); ok {
            netCfg["tap"] = targetNetwork.Name
        }
    }
}
```

### 5.8 `exelet/sshproxy/manager.go`

**Add types and modify `NewManager`**:

Add before `socatManager`:
```go
// BindDevFunc returns the device name for SO_BINDTODEVICE on the connect
// side of the SSH proxy (e.g., the per-VM bridge name). Return empty
// string for default routing.
type BindDevFunc func(instanceID string) string

// ProxyOpts holds optional configuration for an SSH proxy manager.
type ProxyOpts struct {
	BindDev   BindDevFunc // optional: SO_BINDTODEVICE for socat connect side
	NetnsFunc NetnsFunc   // optional: netns name for exepipe dial
}
```

Add `bindDev` field to `socatManager`:
```go
type socatManager struct {
	mu      sync.Mutex
	proxies map[string]*socatSSHProxy
	ports   map[string]int
	dataDir string
	bindIP  string
	bindDev BindDevFunc  // ADD THIS
	log     *slog.Logger
}
```

**Change `NewManager` signature** to accept optional opts:
```go
func NewManager(ctx context.Context, dataDir, bindIP, exepipeAddress string, log *slog.Logger, opts ...ProxyOpts) Manager {
	var o ProxyOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	if exepipeAddress != "" {
		var nf []NetnsFunc
		if o.NetnsFunc != nil {
			nf = append(nf, o.NetnsFunc)
		}
		return NewExepipeManager(ctx, exepipeAddress, bindIP, log, nf...)
	} else {
		return &socatManager{
			proxies: make(map[string]*socatSSHProxy),
			ports:   make(map[string]int),
			dataDir: dataDir,
			bindIP:  bindIP,
			bindDev: o.BindDev,
			log:     log,
		}
	}
}
```

**In `socatManager.CreateProxy`**, pass the bind device:
```go
var ns string
if m.bindDev != nil {
    ns = m.bindDev(instanceID)
}
proxy := newSocatSSHProxy(instanceID, port, targetIP, instanceDir, m.bindIP, ns, m.log)
```

**In `socatManager.RecoverProxies`**, similarly pass bind device when creating proxies.

### 5.9 `exelet/sshproxy/sshproxy.go`

**Add `bindDev` field** to `socatSSHProxy`:
```go
bindDev string // optional: SO_BINDTODEVICE for connect-side (per-VM bridge name)
```

**Update `newSocatSSHProxy` signature** to accept `bindDev`:
```go
func newSocatSSHProxy(instanceID string, port int, targetIP, instanceDir, bindIP, bindDev string, log *slog.Logger) *socatSSHProxy {
```

Set it in the struct literal:
```go
bindDev: bindDev,
```

**In the `start()` method**, modify the `targetAddr` construction:
```go
var targetAddr string
if p.bindDev != "" {
    targetAddr = fmt.Sprintf("TCP:%s:%d,connect-timeout=3,so-bindtodevice=%s",
        p.targetIP, p.targetPort, p.bindDev)
} else {
    targetAddr = fmt.Sprintf("TCP:%s:%d,connect-timeout=3", p.targetIP, p.targetPort)
}
```

### 5.10 `exelet/sshproxy/exepipe.go`

**Add `NetnsFunc` type and field**:
```go
// NetnsFunc returns the network namespace name for an instance.
type NetnsFunc func(instanceID string) string
```

Add to `exepipeManager`:
```go
netnsFunc NetnsFunc
```

**Update `NewExepipeManager`** to accept optional netnsFunc:
```go
func NewExepipeManager(ctx context.Context, exepipeAddress, bindIP string, lg *slog.Logger, netnsFunc ...NetnsFunc) Manager {
	epm := &exepipeManager{
		exepipeAddress: exepipeAddress,
		bindIP:         bindIP,
		lg:             lg,
		ports:          make(map[string]int),
	}
	if len(netnsFunc) > 0 {
		epm.netnsFunc = netnsFunc[0]
	}
	epm.getClient(ctx)
	return epm
}
```

**Add `resetClient` method**:
```go
func (epm *exepipeManager) resetClient() {
	epm.cliMu.Lock()
	defer epm.cliMu.Unlock()
	if epm.cli != nil {
		epm.cli.Close()
		epm.cli = nil
	}
}
```

**Update `CreateProxy`** to include retry logic and pass netns name:
- Add retry loop (2 attempts) with `resetClient()` on first failure
- In `startProxy`, resolve the netns name and pass it to `cli.Listen`

**Update `startProxy`** to pass netns:
```go
var nsName string
if epm.netnsFunc != nil {
    nsName = epm.netnsFunc(instanceID)
}
if err := cli.Listen(ctx, instanceID, ln, targetIP, 22, "ssh", nsName); err != nil {
```

**Update `StopProxy`** with similar retry logic.

**Update `RecoverProxies`** with retry logic on `Listeners()` failure.

### 5.11 `exepipe/internal/cmds/cmds.go`

**Add `Netns` field to `cmd` struct**:
```go
Netns  string `json:"netns,omitempty"` // network namespace for outbound dial
```

**Change `Action` type signature** to include netns parameter:
```go
type Action func(ctx context.Context, key string, fds []int, host string, port int, typ string, netns string) error
```

**Update `ListenCmd`** to accept and set netns:
```go
func ListenCmd(key string, listener net.Listener, host string, port int, typ string, netns string) (data, oob []byte, err error) {
```
Set `Netns: netns` in the cmd struct.

**Update `Dispatch`** to pass `c.Netns`:
```go
return action(ctx, c.Key, fds, c.Host, c.Port, c.Type, c.Netns)
```

**Add `Netns` to `Listener` struct**:
```go
type Listener struct {
	Key   string `json:"key"`
	Host  string `json:"host"`
	Port  int    `json:"port"`
	Type  string `json:"type"`
	Netns string `json:"netns,omitempty"`
}
```

### 5.12 `exepipe/client/client.go`

**Update `Listen` method** to accept optional netns:
```go
func (c *Client) Listen(ctx context.Context, key string, listener net.Listener, host string, port int, typ string, netns ...string) error {
	// ...
	var ns string
	if len(netns) > 0 {
		ns = netns[0]
	}
	data, oob, err := cmds.ListenCmd(key, listener, host, port, typ, ns)
```

**Add `Netns` to `Listener` struct** and populate it in `Listeners`.

### 5.13 `exepipe/exepipe.go`

**Add `DialFunc` to `PipeConfig`**:
```go
DialFunc DialFunc // optional: custom dialer (e.g. netns-aware); nil for default
```

### 5.14 `exepipe/piping.go`

**Add `DialFunc` type**:
```go
// DialFunc dials a TCP connection, optionally from a network namespace.
type DialFunc func(ctx context.Context, host string, port int, netns string, timeout time.Duration) (net.Conn, error)
```

**Add `dialFunc` field to `piping`** and set it in `setupPiping`.

**Add dial timeout sequence**:
```go
var dialTimeouts = [...]time.Duration{
	500 * time.Millisecond,
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
}
```

**Update all method signatures** that pass through to include `netns string`:
- `Listen(ctx, key, fd, host, port, typ, netns)`
- `doListen(ctx, key, ln, host, port, typ, netns)`
- `connect(ctx, conn, key, host, port, typ, netns)`
- `addListener(ctx, key, ln, host, port, typ, netns)`

**Update `connect`** to use retry with dial timeouts and the custom `dialFunc`:
```go
func (p *piping) connect(ctx context.Context, conn1 net.Conn, key, host string, port int, typ string, netns string) {
	var conn2 net.Conn
	var err error

	for _, timeout := range dialTimeouts {
		if p.dialFunc != nil {
			conn2, err = p.dialFunc(ctx, host, port, netns, timeout)
		} else {
			d := net.Dialer{Timeout: timeout}
			conn2, err = d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		}
		if err == nil {
			break
		}
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) {
			break
		}
	}
	// ... rest of error handling and copy
```

**Update `addListener`** to store `Netns` in the listener info.

### 5.15 `exepipe/cmdloop.go`

**Update all action method signatures** to accept the netns parameter:
```go
func (ca *cmdActor) copyAction(ctx context.Context, key string, fds []int, host string, port int, typ string, _ string) error {
func (ca *cmdActor) listenAction(ctx context.Context, key string, fds []int, host string, port int, typ string, netns string) error {
func (ca *cmdActor) unlistenAction(ctx context.Context, key string, fds []int, host string, port int, typ string, _ string) error {
func (ca *cmdActor) listenersAction(ctx context.Context, key string, fds []int, host string, port int, typ string, _ string) error {
```

In `listenAction`, pass `netns` through:
```go
ca.pipeInstance.piping.Listen(ctx, key, fds[0], host, port, typ, netns)
```

### 5.16 `cmd/exepipe/exepipe.go`

**Add `--netns` flag**:
```go
netnsMode := flag.Bool("netns", false, "enable network namespace-aware dialing")
```

**Set `DialFunc` on config when enabled**:
```go
if *netnsMode {
    cfg.DialFunc = exepipe.NetnsDialFunc()
}
```

---

## 6. Proto Changes

### `api/exe/compute/v1/compute.proto`

**IMPORTANT**: On main, `ReceiveVMReady` currently has:
- field 1: `has_base_image` (bool)
- field 2: `target_network` (NetworkInterface)
- field 3: `sideband_addr` (string) — **this exists on main**

So our new field must be **field 4**:

```protobuf
message ReceiveVMReady {
  bool has_base_image = 1;              // Target already has the base image
  NetworkInterface target_network = 2;  // Allocated network for live migration
  string sideband_addr = 3;            // [existing on main]
  bool skip_ip_reconfig = 4;           // Target handles IP isolation (e.g. netns); orchestrator should skip SSH IP reconfiguration
}
```

After modifying the proto, regenerate the Go code with `protoc` / `buf generate`.

---

## 7. Migration Integration (skipIPReconfig Logic)

### How it works

During migration, the target exelet sends a `ReceiveVMReady` message back to the orchestrator. This message now includes `skip_ip_reconfig`:

1. **Target's `receive_vm.go`**: Checks if the network manager implements `ExtIPLookup` (meaning it's netns mode) AND if the source VM's IP matches the target's allocated IP. If both conditions hold, sets `skip_ip_reconfig = true`.

2. **Orchestrator's `debugsrv.go`**: When it receives `skip_ip_reconfig = true`:
   - **Live migration**: Skips the `reconfigureVMIP` SSH call during the `AwaitControl` phase. Instead of SSHing into the VM to change its IP, it just sends the proceed signal.
   - **Cold migration**: Skips the post-migration `/etc/hosts` update via SSH.
   - **Both**: Skips the post-migration `/etc/hosts` rewrite for the hostname→IP mapping.

### Important: NAT→netns migration

When migrating from a NAT host (source IP = `10.42.0.5`) to a netns host (target IP = `10.42.0.42`), the IPs differ, so `sameVMIP` returns false and `skip_ip_reconfig` is false. The orchestrator correctly performs IP reconfiguration in this case.

### Changes needed in `execore/debugsrv.go`

**IMPORTANT NOTE ABOUT MAIN BRANCH**: On main, `migrateVM` takes `(source *exeletclient.Client, instanceID, targetAddr string, twoPhase, directOnly bool, progress)` and returns `error`. It does direct exelet-to-exelet transfer without the execore acting as intermediary. `migrateVMLive` takes `migrateVMLiveParams{source, targetAddr, instanceID, box, progress, directOnly}` and returns `(int64, bool, error)`.

The migration functions on main do NOT have the execore acting as a pass-through proxy for data. Instead, they coordinate direct exelet-to-exelet transfers. The `skipIPReconfig` concept still applies but the integration point is different.

**You need to find where on main the orchestrator:**
1. Receives the `ReceiveVMReady` from the target (look for `ready.HasBaseImage` or `TargetNetwork`)
2. Decides whether to SSH into the VM for IP reconfiguration
3. Decides whether to update `/etc/hosts` after migration

At each of these points, add the `skipIPReconfig` check. The pattern is:
- Extract `skipIPReconfig` from the ready message: `skipIPReconfig := ready.SkipIpReconfig`
- Guard SSH reconfiguration with `if !skipIPReconfig { ... }`
- Guard `/etc/hosts` updates with `if !skipIPReconfig { ... }`

If main's `migrateVM` doesn't directly receive the `ReceiveVMReady` (because it's exelet-to-exelet), then the skip_ip_reconfig field travels through the target exelet's response and surfaces in whatever result the orchestrator receives. Check the `ReceiveVMResult` or the migration completion path.

---

## 8. Exepipe Changes

### Overview

Exepipe is the connection proxy daemon that handles SSH proxying. In netns mode, exepipe needs to dial the VM from within the VM's network namespace (because `10.42.0.42` is only reachable from within that namespace).

### Flow

1. **Exelet** creates an SSH proxy via the exepipe client
2. **Client** sends a `listen` command with `netns: "exe-vm000003"`
3. **Exepipe** stores the netns name alongside the listener
4. When a connection arrives on the listener, exepipe calls `connect()`
5. `connect()` uses `dialFunc` which enters the namespace before dialing
6. The TCP connection is established within the namespace; once connected, the socket works from any thread

### `--netns` flag

The `exepipe` binary gets a new `--netns` flag. When set, it configures a `NetnsDialFunc()` as the pipe's `DialFunc`. This function:
- If `nsName` is empty: dials normally
- If `nsName` is set: `LockOSThread()`, switches to the target netns, dials, switches back

### Retry with backoff

The `connect` function in `piping.go` gains dial retry with exponential backoff timeouts: `500ms, 1s, 2s, 4s`. This helps when a VM is still booting after migration.

---

## 9. SSH Proxy Changes

### Socat mode (SO_BINDTODEVICE)

When using socat (no exepipe), the SSH proxy needs `SO_BINDTODEVICE` to route through the per-VM bridge:

```
socat TCP-LISTEN:$port,fork TCP:10.42.0.42:22,connect-timeout=3,so-bindtodevice=br-vm000003
```

The per-VM bridge `br-vm000003` has IP `10.42.0.1/16` which provides a route to the VM's `10.42.0.42`.

### Exepipe mode (netns dial)

When using exepipe, the SSH proxy passes the namespace name. Exepipe enters `exe-vm000003` before dialing `10.42.0.42:22`.

### Configuration flow

1. `cmd/exelet/main.go` detects netns scheme, sets `ProxyBindDevFunc` and `ProxyNetnsFunc` on config
2. `compute.go` passes these through `ProxyOpts` to `sshproxy.NewManager`
3. `sshproxy.NewManager` passes them to either `socatManager` or `exepipeManager`
4. Each manager uses them when creating/recovering proxies

---

## 10. Virt-Cluster Changes

### `ops/virt-cluster.sh`

**New environment variable**:
```bash
EXELET_NETWORK_MANAGER="${EXELET_NETWORK_MANAGER:-nat}"  # comma-separated modes to cycle
```

**Mode cycling function**:
```bash
network_mode_for_exelet() {
    local idx="$1"
    IFS=',' read -ra _modes <<< "${EXELET_NETWORK_MANAGER}"
    local n=${#_modes[@]}
    local i=$(( (idx - 1) % n ))
    echo "${_modes[$i]}"
}
```

With `EXELET_NETWORK_MANAGER=nat,netns`, exelet 1 gets nat, exelet 2 gets netns, exelet 3 gets nat, etc.

**Additional packages**: Add `conntrack` to cloud-init packages.

**Build targets**: Add `exepipe` and `exelet-netns` to `build_binaries()`.

**Provisioning changes**:
- `provision_exelet` takes a 4th argument `net_mode`
- Generates appropriate `--network-manager-address` and exepipe flags
- Creates `exepipe.service` systemd unit
- Makes exelet depend on exepipe

**Deploy changes**:
- Copies exepipe and exelet-netns binaries
- Ensures exepipe.service exists
- Patches exelet.service with correct network manager address
- Keeps exepipe `--netns` flag in sync with network mode

**Status changes**: Shows both exelet and exepipe service status.

---

## 11. Testing

### Unit Tests

1. **`exelet/network/netns/netns_linux_test.go`** (requires root):
   - `TestNewManager` — constructor with valid netns:// URL
   - `TestNewManagerBadScheme` — rejects non-netns schemes
   - `TestNaming` — all interface names fit in IFNAMSIZ
   - `TestAllocateExtIP` — sequential allocation, idempotent re-alloc, release
   - `TestGetInstanceByExtIP` — reverse lookup
   - `TestCreateDeleteInterface` — full integration: creates ns, tap, bridge, veths; verifies iptables, IPs; deletes everything
   - `TestTwoVMs` — two VMs get same internal IP, different ext IPs, isolated namespaces
   - `TestRecoverExtIPs` — simulates restart, verifies recovery from kernel state
   - `TestBridgeSplitting` — bridge port count tracking and secondary bridge creation
   - `TestVMBridgeTracking` — VM-to-bridge mapping

2. **`exelet/services/compute/receive_vm_test.go`**:
   - `TestSameVMIP` — various cases: same IP, different IP, nil source/target
   - `TestEditSnapshotConfigUpdatesTapName` — NAT→netns, netns→NAT, same mode, nil target

3. **`execore/debugsrv_migrate_test.go`** — existing tests updated to handle extra return value from `migrateVMLive` and `migrateVM`

4. **`exelet/services/compute/service_test.go`**:
   - `TestCreateSSHProxyExepipeReconnect` — verifies exepipe client reconnect after restart

### Integration Tests

- Virt-cluster with `EXELET_NETWORK_MANAGER=nat,netns`:
  - Create VM on nat exelet, verify SSH works
  - Create VM on netns exelet, verify SSH works
  - Live migrate from nat→netns, verify IP reconfiguration happens
  - Live migrate from netns→netns, verify IP reconfiguration is skipped
  - Cold migrate from netns→nat, verify new IP is assigned
  - Verify metadata service works from both VM types

### Manual Testing

- `exelet-netns --all` — verify all namespaces are listed with correct state
- `exelet-netns vm000003` — verify single VM diagnostics show all devices
- `exelet-netns --live vm000003` — verify conntrack streaming works
---

## Appendix: Complete New File Contents

Every new file that needs to be created is included below in full. Copy these directly into the target paths.


### `exelet/network/netns/netns.go`

```go
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
	DefaultBandwidthBurst    = "200mb"
	DefaultMaxPortsPerBridge = 500
	DefaultBridgeHashMax     = 4096 // FDB hash table size; default 512 causes "exchange full" at scale
)

// bridgeInfo tracks a shared bridge and its current port count.
type bridgeInfo struct {
	name      string
	portCount int
}

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

type Config struct {
	Network string
	Router  string
}

func (m *Manager) Config(_ context.Context) any {
	return &Config{Network: VMSubnet, Router: m.router}
}

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
			m.nextOctet3++
			m.nextOctet4 = 1
			if m.nextOctet3 == 0 {
				m.nextOctet3 = 0
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
	// Expected format: vm\d{6}-...
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

func tapName(id string) string { return "tap-" + vmID(id) } // 12 chars
func nsName(id string) string  { return NsName(id) }        // 12 chars

// NsName returns the network namespace name for a given instance ID.
func NsName(id string) string { return "exe-" + vmID(id) }
func brName(id string) string { return BridgeName(id) } // 11 chars

// BridgeName returns the per-VM bridge name for a given instance ID.
func BridgeName(id string) string { return "br-" + vmID(id) }
func vethBrName(id string) string { return "vb-" + vmID(id) } // 11 chars
func vethGwName(id string) string { return "vg-" + vmID(id) } // 11 chars
func vethXHost(id string) string  { return "vx-" + vmID(id) } // 11 chars
func vethXNS(id string) string    { return "ve-" + vmID(id) } // 11 chars

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
```

### `exelet/network/netns/netns_other.go`

```go
//go:build !linux

package netns

import (
	"context"
	"errors"
	"log/slog"

	api "exe.dev/pkg/api/exe/compute/v1"
)

var errNotSupported = errors.New("netns network manager is only supported on Linux")

type Manager struct{}

type Config struct {
	Network string
	Router  string
}

func NewManager(addr string, log *slog.Logger) (*Manager, error) {
	return nil, errNotSupported
}

func (m *Manager) Start(ctx context.Context) error { return errNotSupported }
func (m *Manager) Stop(ctx context.Context) error  { return errNotSupported }
func (m *Manager) Config(_ context.Context) any    { return nil }
func (m *Manager) Close() error                    { return errNotSupported }

func (m *Manager) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	return nil, errNotSupported
}

func (m *Manager) DeleteInterface(ctx context.Context, id, ip string) error {
	return errNotSupported
}

func (m *Manager) ApplyConnectionLimit(ctx context.Context, ip string) error {
	return errNotSupported
}

func (m *Manager) ApplyBandwidthLimit(ctx context.Context, id string) error {
	return errNotSupported
}

func (m *Manager) ReconcileLeases(ctx context.Context, validIPs map[string]struct{}) ([]string, error) {
	return nil, errNotSupported
}

func (m *Manager) GetInstanceByExtIP(ip string) (string, bool) {
	return "", false
}
```

### `exelet/network/netns/create_linux.go`

```go
//go:build linux

package netns

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/vishvananda/netlink"
	uns "github.com/vishvananda/netns"

	api "exe.dev/pkg/api/exe/compute/v1"
)

// IFA_F_NOPREFIXROUTE prevents automatic route creation when adding an address.
const ifaFNoPrefixRoute = 0x200

func (m *Manager) CreateInterface(ctx context.Context, id string) (*api.NetworkInterface, error) {
	tap := tapName(id)
	ns := nsName(id)
	br := brName(id)
	vBr := vethBrName(id) // root ns, on per-VM bridge
	vGw := vethGwName(id) // netns, VM gateway (10.42.0.1)
	vXH := vethXHost(id)  // root ns, on shared bridge
	vXN := vethXNS(id)    // netns, outbound

	extIP, err := m.allocateExtIP(id)
	if err != nil {
		return nil, fmt.Errorf("allocate ext IP: %w", err)
	}

	// Track what was created for rollback.
	type state struct {
		ns, br, tap, innerVeth, outerVeth, nsCfg bool
	}
	var s state

	cleanup := func() {
		if s.outerVeth {
			delLinkByName(vXH)
		}
		if s.innerVeth {
			delLinkByName(vBr)
		}
		if s.tap {
			delLinkByName(tap)
		}
		if s.br {
			delLinkByName(br)
		}
		if s.ns {
			_ = exec.CommandContext(ctx, "ip", "netns", "delete", ns).Run()
		}
		m.releaseExtIP(id)
	}

	// 1. Clean up any stale interfaces from a previous instance with the
	// same ID (e.g. failed delete, migrate-back). Delete root-ns veth peers
	// first — this cascade-deletes their netns-side peers — then remove
	// the stale namespace so we start completely clean.
	delLinkByName(vBr)
	delLinkByName(vXH)
	delLinkByName(tap)
	delLinkByName(br)
	if nsExists(ns) {
		_ = exec.CommandContext(ctx, "ip", "netns", "delete", ns).Run()
	}
	if err := exec.CommandContext(ctx, "ip", "netns", "add", ns).Run(); err != nil {
		cleanup()
		return nil, fmt.Errorf("create netns %s: %w", ns, err)
	}
	s.ns = true
	_ = exec.CommandContext(ctx, "ip", "netns", "exec", ns, "ip", "link", "set", "lo", "up").Run()

	nsHandle, err := uns.GetFromName(ns)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("get netns handle %s: %w", ns, err)
	}
	defer nsHandle.Close()

	// 2. Per-VM bridge with gateway IP (noprefixroute to avoid route table bloat).
	// The gateway IP lets the SSH proxy reach the VM via SO_BINDTODEVICE.
	brLink, err := ensureBridge(br)
	if err != nil {
		cleanup()
		return nil, err
	}
	s.br = true

	brAddr, err := netlink.ParseAddr(GatewayCIDR)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("parse bridge addr: %w", err)
	}
	brAddr.Flags |= ifaFNoPrefixRoute
	if err := netlink.AddrReplace(brLink, brAddr); err != nil {
		cleanup()
		return nil, fmt.Errorf("assign bridge addr: %w", err)
	}

	// 3. TAP device on per-VM bridge.
	if _, err := ensureTap(tap, brLink); err != nil {
		cleanup()
		return nil, err
	}
	s.tap = true

	// 4. Inner veth pair: vBr (root ns, on per-VM bridge) <-> vGw (netns, gateway).
	if err := ensureVethPair(vBr, vGw); err != nil {
		cleanup()
		return nil, fmt.Errorf("create inner veth: %w", err)
	}
	s.innerVeth = true

	if err := attachToBridge(vBr, brLink); err != nil {
		cleanup()
		return nil, err
	}
	if err := moveToNS(vGw, nsHandle); err != nil {
		cleanup()
		return nil, err
	}

	// 5. Outer veth pair: vXH (root ns, on shared bridge) <-> vXN (netns, outbound).
	if err := ensureVethPair(vXH, vXN); err != nil {
		cleanup()
		return nil, fmt.Errorf("create outer veth: %w", err)
	}
	s.outerVeth = true

	// Select a shared bridge with available capacity.
	sharedBridgeName, needsNewBridge := m.selectBridgeAndIncrement()
	if needsNewBridge {
		m.bridgeCreateMu.Lock()
		sharedBridgeName, needsNewBridge = m.selectBridgeAndIncrement()
		if needsNewBridge {
			newBridgeName := m.reserveNextBridge()
			if err := m.createSecondaryBridge(ctx, newBridgeName); err != nil {
				m.bridgeCreateMu.Unlock()
				cleanup()
				return nil, fmt.Errorf("create secondary shared bridge: %w", err)
			}
			sharedBridgeName = m.addBridgeAndSelect(newBridgeName)
		}
		m.bridgeCreateMu.Unlock()
	}

	sharedBr, err := netlink.LinkByName(sharedBridgeName)
	if err != nil {
		m.decrementBridgePort(sharedBridgeName)
		cleanup()
		return nil, fmt.Errorf("get shared bridge %s: %w", sharedBridgeName, err)
	}
	if err := attachToBridge(vXH, sharedBr); err != nil {
		m.decrementBridgePort(sharedBridgeName)
		cleanup()
		return nil, err
	}
	if err := moveToNS(vXN, nsHandle); err != nil {
		m.decrementBridgePort(sharedBridgeName)
		cleanup()
		return nil, err
	}
	m.setVMBridge(id, sharedBridgeName)

	// 6. Configure networking inside the namespace.
	if err := m.configureNS(ctx, ns, vGw, vXN, extIP); err != nil {
		cleanup()
		return nil, fmt.Errorf("configure netns: %w", err)
	}
	s.nsCfg = true

	// 7. Build response.
	mac, err := randomMAC()
	if err != nil {
		cleanup()
		return nil, err
	}

	iface := &api.NetworkInterface{
		Name:       tap,
		DeviceName: DeviceName,
		Type:       api.NetworkInterface_TYPE_TAP,
		MACAddress: mac,
		IP: &api.IPAddress{
			IPV4:      VMIP + "/16",
			GatewayV4: VMGateway,
		},
		Nameservers: m.nameservers,
		Network:     VMSubnet,
		NTPServer:   m.ntpServer,
		Router:      m.router,
	}

	m.log.InfoContext(ctx, "created netns network interface",
		"instance", id, "tap", tap, "netns", ns,
		"bridge", br, "shared_bridge", sharedBridgeName, "ext_ip", extIP,
	)

	return iface, nil
}

// configureNS sets up routing, NAT, and firewall rules inside a per-VM namespace.
func (m *Manager) configureNS(ctx context.Context, ns, vGw, vXN, extIP string) error {
	run := func(prog string, args ...string) error {
		all := append([]string{"netns", "exec", ns, prog}, args...)
		out, err := exec.CommandContext(ctx, "ip", all...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %v: %w (%s)", prog, args, err, out)
		}
		return nil
	}
	ipt := func(args ...string) error { return run("iptables", args...) }

	// Inner veth: VM gateway.
	if err := run("ip", "addr", "add", GatewayCIDR, "dev", vGw); err != nil {
		return err
	}
	if err := run("ip", "link", "set", vGw, "up"); err != nil {
		return err
	}

	// Outer veth: outbound.
	if err := run("ip", "addr", "add", extIP+"/16", "dev", vXN); err != nil {
		return err
	}
	if err := run("ip", "link", "set", vXN, "up"); err != nil {
		return err
	}

	// Default route.
	if err := run("ip", "route", "add", "default", "via", SharedBridgeGateway, "dev", vXN); err != nil {
		return err
	}

	// IP forwarding.
	if err := run("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}

	// SNAT outbound VM traffic to this namespace's ext IP. Using SNAT
	// instead of MASQUERADE avoids a per-packet interface address lookup
	// — the ext IP is static for the VM's lifetime. This is the inner
	// NAT hop (10.42.0.42 → 10.99.x.x); the outer hop on the shared
	// bridge (10.99.x.x → host IP) remains MASQUERADE since the host's
	// outbound IP may change.
	if err := ipt("-t", "nat", "-A", "POSTROUTING", "-s", VMSubnet, "-o", vXN, "-j", "SNAT", "--to-source", extIP); err != nil {
		return err
	}

	// Forwarding.
	if err := ipt("-A", "FORWARD", "-i", vGw, "-o", vXN, "-j", "ACCEPT"); err != nil {
		return err
	}
	if err := ipt("-A", "FORWARD", "-i", vXN, "-o", vGw, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		return err
	}

	// Block carrier-grade NAT.
	if err := ipt("-I", "FORWARD", "-i", vGw, "-d", CarrierNATCIDR, "-j", "DROP"); err != nil {
		return err
	}

	// Metadata DNAT: rewrite 169.254.169.254 to the shared bridge gateway
	// where the metadata service actually listens. The packet then gets
	// forwarded out through vXN (matching the FORWARD rules above) and
	// SNATed to the VM's ext IP, which is how the metadata service
	// identifies the requesting VM.
	if err := ipt("-t", "nat", "-A", "PREROUTING", "-i", vGw, "-d", MetadataIP, "-p", "tcp", "--dport", "80", "-j", "DNAT", "--to-destination", SharedBridgeGateway+":80"); err != nil {
		return err
	}
	if err := ipt("-t", "nat", "-A", "PREROUTING", "-i", vGw, "-d", MetadataIP, "-p", "tcp", "--dport", "443", "-j", "DNAT", "--to-destination", SharedBridgeGateway+":2443"); err != nil {
		return err
	}

	// Gateway firewall: block direct access to the gateway IP from VMs.
	if err := ipt("-A", "INPUT", "-i", vGw, "-d", VMGateway, "-p", "tcp", "--syn", "-j", "DROP"); err != nil {
		return err
	}

	// Connection limit.
	_ = ipt("-I", "FORWARD", "-s", VMIP, "-m", "connlimit",
		"--connlimit-above", fmt.Sprintf("%d", m.connLimit), "--connlimit-mask", "32", "-j", "DROP")

	// Bandwidth limit on the outbound veth egress.
	if !m.disableBandwidth {
		if err := m.applyBandwidthInNS(ctx, ns, vXN); err != nil {
			return err
		}
	}

	return nil
}

// ApplyConnectionLimit is a no-op — limits are set per-namespace at creation.
func (m *Manager) ApplyConnectionLimit(ctx context.Context, ip string) error {
	return nil
}

// ApplyBandwidthLimit applies bandwidth limiting to a VM's outbound veth.
func (m *Manager) ApplyBandwidthLimit(ctx context.Context, id string) error {
	if m.disableBandwidth {
		return nil
	}
	ns := nsName(id)
	vXN := vethXNS(id)
	return m.applyBandwidthInNS(ctx, ns, vXN)
}

// applyBandwidthInNS applies HTB bandwidth shaping on the outbound veth egress
// inside a per-VM network namespace. Upload traffic from the VM exits through
// ve-{vmid} egress, so we shape it there — no IFB device needed.
//
// The HTB class uses "rate" as the sustained maximum and "ceil" as the burst
// ceiling. HTB allows bursting up to ceil when tokens are available, then
// throttles back to rate.
func (m *Manager) applyBandwidthInNS(ctx context.Context, ns, dev string) error {
	run := func(args ...string) error {
		all := append([]string{"netns", "exec", ns}, args...)
		out, err := exec.CommandContext(ctx, "ip", all...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%v: %w (%s)", args, err, out)
		}
		return nil
	}

	// Replace any existing root qdisc.
	_ = run("tc", "qdisc", "del", "dev", dev, "root")

	// HTB root qdisc.
	if err := run("tc", "qdisc", "add", "dev", dev, "root", "handle", "1:", "htb", "default", "10"); err != nil {
		return fmt.Errorf("add htb qdisc: %w", err)
	}

	// HTB class: rate = sustained max, ceil = burst ceiling, burst = token bucket size.
	if err := run("tc", "class", "add", "dev", dev, "parent", "1:", "classid", "1:10", "htb",
		"rate", m.bandwidthRate,
		"ceil", m.bandwidthCeil,
		"burst", m.bandwidthBurst,
		"cburst", m.bandwidthBurst,
	); err != nil {
		return fmt.Errorf("add htb class: %w", err)
	}

	// fq_codel leaf for fair queuing and low latency within the shaped pipe.
	if err := run("tc", "qdisc", "add", "dev", dev, "parent", "1:10", "handle", "10:", "fq_codel"); err != nil {
		return fmt.Errorf("add fq_codel: %w", err)
	}

	return nil
}

// --- netlink helpers ---

func nsExists(name string) bool {
	h, err := uns.GetFromName(name)
	if err != nil {
		return false
	}
	h.Close()
	return true
}

func ensureBridge(name string) (netlink.Link, error) {
	if link, err := netlink.LinkByName(name); err == nil {
		return link, nil
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	br := &netlink.Bridge{LinkAttrs: attrs}
	if err := netlink.LinkAdd(br); err != nil {
		return nil, fmt.Errorf("create bridge %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(br); err != nil {
		return nil, fmt.Errorf("up bridge %s: %w", name, err)
	}
	return br, nil
}

func ensureTap(name string, master netlink.Link) (netlink.Link, error) {
	if link, err := netlink.LinkByName(name); err == nil {
		return link, nil
	}
	attrs := netlink.NewLinkAttrs()
	attrs.Name = name
	attrs.MasterIndex = master.Attrs().Index
	tap := &netlink.Tuntap{LinkAttrs: attrs, Mode: netlink.TUNTAP_MODE_TAP}
	if err := netlink.LinkAdd(tap); err != nil {
		return nil, fmt.Errorf("create tap %s: %w", name, err)
	}
	if err := netlink.LinkSetUp(tap); err != nil {
		return nil, fmt.Errorf("up tap %s: %w", name, err)
	}
	return tap, nil
}

func ensureVethPair(a, b string) error {
	if _, err := netlink.LinkByName(a); err == nil {
		return nil // already exists
	}
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: a},
		PeerName:  b,
	}
	return netlink.LinkAdd(veth)
}

func attachToBridge(vethName string, bridge netlink.Link) error {
	link, err := netlink.LinkByName(vethName)
	if err != nil {
		return fmt.Errorf("get %s: %w", vethName, err)
	}
	if err := netlink.LinkSetMaster(link, bridge); err != nil {
		return fmt.Errorf("attach %s to bridge: %w", vethName, err)
	}
	return netlink.LinkSetUp(link)
}

func moveToNS(name string, ns uns.NsHandle) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("get %s for ns move: %w", name, err)
	}
	return netlink.LinkSetNsFd(link, int(ns))
}

func delLinkByName(name string) {
	if link, err := netlink.LinkByName(name); err == nil {
		_ = netlink.LinkDel(link)
	}
}
```

### `exelet/network/netns/delete_linux.go`

```go
//go:build linux

package netns

import (
	"context"
	"os/exec"
)

func (m *Manager) DeleteInterface(ctx context.Context, id, ip string) error {
	ns := nsName(id)
	br := brName(id)
	tap := tapName(id)
	vBr := vethBrName(id)
	vXH := vethXHost(id)

	// Look up which shared bridge this VM's outer veth is on.
	sharedBridgeName := m.getVMBridge(id)

	m.log.InfoContext(ctx, "deleting netns network interface",
		"instance", id, "netns", ns, "bridge", br, "shared_bridge", sharedBridgeName,
	)

	// Delete network namespace. Note: `ip netns delete` only removes the
	// bind-mount; if any process still holds a reference to the namespace
	// (e.g. cloud-hypervisor via the TAP fd), the namespace and its
	// interfaces stay alive. So we must always explicitly delete the
	// root-ns veth peers below rather than relying on the netns teardown
	// to cascade-delete them.
	if err := exec.CommandContext(ctx, "ip", "netns", "delete", ns).Run(); err != nil {
		m.log.WarnContext(ctx, "failed to delete netns (may not exist)", "ns", ns, "error", err)
	}

	// Always delete root-ns veth peers explicitly. Deleting one side of a
	// veth pair destroys the other, so this also cleans up any interfaces
	// lingering inside a still-referenced namespace.
	delLinkByName(vBr)
	delLinkByName(vXH)

	// Delete the TAP (not inside the netns, so not auto-deleted).
	delLinkByName(tap)

	// Delete the per-VM bridge.
	delLinkByName(br)

	// Decrement port count on the shared bridge.
	if sharedBridgeName != "" {
		m.decrementBridgePort(sharedBridgeName)
	}
	m.removeVMBridge(id)

	m.releaseExtIP(id)
	return nil
}

// ReconcileLeases is a no-op — there are no IPAM leases.
func (m *Manager) ReconcileLeases(ctx context.Context, validIPs map[string]struct{}) ([]string, error) {
	return nil, nil
}
```

### `exelet/network/netns/start_linux.go`

```go
//go:build linux

package netns

import (
	"context"
	"fmt"
	"os"

	"github.com/vishvananda/netlink"
)

// Start creates and configures the shared outbound bridge (br-exe-0).
func (m *Manager) Start(ctx context.Context) error {
	primaryBridge := m.primaryBridgeName()
	m.log.InfoContext(ctx, "starting netns network manager", "shared_bridge", primaryBridge)

	// Create or get the shared bridge.
	br, err := ensureBridge(primaryBridge)
	if err != nil {
		return fmt.Errorf("shared bridge: %w", err)
	}

	// Increase FDB hash_max to prevent "exchange full" errors at scale.
	if err := setBridgeHashMax(primaryBridge, DefaultBridgeHashMax); err != nil {
		m.log.WarnContext(ctx, "failed to set bridge hash_max", "bridge", primaryBridge, "error", err)
	}

	// Assign gateway IP to the shared bridge.
	addr, err := netlink.ParseAddr(SharedBridgeCIDR)
	if err != nil {
		return fmt.Errorf("parse shared bridge addr: %w", err)
	}
	if err := netlink.AddrReplace(br, addr); err != nil {
		return fmt.Errorf("assign shared bridge addr: %w", err)
	}

	// Enable IP forwarding and masquerade on the host for the shared bridge subnet.
	if err := writeSysctl("net.ipv4.ip_forward", "1"); err != nil {
		return fmt.Errorf("enable ip forwarding: %w", err)
	}

	// Masquerade outbound traffic from the shared bridge subnet.
	if err := ensureIptablesRule(ctx,
		[]string{"-t", "nat", "-C", "POSTROUTING", "-s", SharedBridgeNetwork, "-j", "MASQUERADE"},
		[]string{"-t", "nat", "-A", "POSTROUTING", "-s", SharedBridgeNetwork, "-j", "MASQUERADE"},
	); err != nil {
		return fmt.Errorf("shared bridge masquerade: %w", err)
	}

	// Allow forwarding from/to the shared bridge.
	if err := ensureIptablesRule(ctx,
		[]string{"-C", "FORWARD", "-i", primaryBridge, "!", "-o", primaryBridge, "-j", "ACCEPT"},
		[]string{"-A", "FORWARD", "-i", primaryBridge, "!", "-o", primaryBridge, "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("shared bridge forward out: %w", err)
	}
	if err := ensureIptablesRule(ctx,
		[]string{"-C", "FORWARD", "-o", primaryBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		[]string{"-A", "FORWARD", "-o", primaryBridge, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("shared bridge forward in: %w", err)
	}

	// Re-create any secondary bridges that already exist on disk (exelet restart).
	if err := m.recoverSecondaryBridges(ctx); err != nil {
		return fmt.Errorf("recover secondary bridges: %w", err)
	}

	return nil
}

func (m *Manager) Stop(ctx context.Context) error {
	return nil
}

// recoverSecondaryBridges discovers any existing secondary bridges (br-exe-1, br-exe-2, ...)
// and adds them to the bridge list. Called on startup to handle exelet restart.
func (m *Manager) recoverSecondaryBridges(ctx context.Context) error {
	for i := 1; ; i++ {
		name := fmt.Sprintf("%s-%d", SharedBridgeBaseName, i)
		_, err := netlink.LinkByName(name)
		if err != nil {
			break // no more bridges
		}

		// Count attached ports.
		links, err := netlink.LinkList()
		portCount := 0
		if err == nil {
			br, _ := netlink.LinkByName(name)
			if br != nil {
				brIdx := br.Attrs().Index
				for _, l := range links {
					if l.Attrs().MasterIndex == brIdx {
						portCount++
					}
				}
			}
		}

		// Subtract 1 for the veth connecting to primary bridge.
		if portCount > 0 {
			portCount--
		}

		m.mu.Lock()
		m.bridges = append(m.bridges, bridgeInfo{name: name, portCount: portCount})
		m.mu.Unlock()

		m.log.InfoContext(ctx, "recovered secondary shared bridge",
			"name", name, "port_count", portCount)
	}

	// Also count ports on the primary bridge.
	links, err := netlink.LinkList()
	if err == nil {
		primary := m.primaryBridgeName()
		br, _ := netlink.LinkByName(primary)
		if br != nil {
			brIdx := br.Attrs().Index
			portCount := 0
			for _, l := range links {
				if l.Attrs().MasterIndex == brIdx {
					portCount++
				}
			}
			m.mu.Lock()
			if len(m.bridges) > 0 {
				m.bridges[0].portCount = portCount
			}
			m.mu.Unlock()
			m.log.InfoContext(ctx, "recovered primary shared bridge port count",
				"name", primary, "port_count", portCount)
		}
	}

	return nil
}

// createSecondaryBridge creates a new shared bridge and connects it to the primary
// bridge via a veth pair, mirroring the NAT network manager's approach.
func (m *Manager) createSecondaryBridge(ctx context.Context, bridgeName string) error {
	primaryBridge := m.primaryBridgeName()

	m.log.InfoContext(ctx, "creating secondary shared bridge", "name", bridgeName, "primary", primaryBridge)

	// Create the bridge.
	var br netlink.Link
	if existing, err := netlink.LinkByName(bridgeName); err == nil {
		br = existing
		m.log.DebugContext(ctx, "secondary bridge already exists", "name", bridgeName)
	} else {
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return fmt.Errorf("check bridge %s: %w", bridgeName, err)
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name = bridgeName
		newBr := &netlink.Bridge{LinkAttrs: attrs}
		if err := netlink.LinkAdd(newBr); err != nil {
			return fmt.Errorf("create bridge %s: %w", bridgeName, err)
		}
		br = newBr
	}

	if err := setBridgeHashMax(bridgeName, DefaultBridgeHashMax); err != nil {
		m.log.WarnContext(ctx, "failed to set bridge hash_max", "bridge", bridgeName, "error", err)
	}

	if err := netlink.LinkSetUp(br); err != nil {
		return fmt.Errorf("bring up bridge %s: %w", bridgeName, err)
	}

	// Create veth pair to connect secondary to primary.
	suffix := bridgeName[len(SharedBridgeBaseName)+1:]
	vethPrimary := fmt.Sprintf("veth-%s-p", suffix)
	vethSecondary := fmt.Sprintf("veth-%s-s", suffix)

	if _, err := netlink.LinkByName(vethPrimary); err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return fmt.Errorf("check veth %s: %w", vethPrimary, err)
		}
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: vethPrimary},
			PeerName:  vethSecondary,
		}
		if err := netlink.LinkAdd(veth); err != nil {
			return fmt.Errorf("create veth pair: %w", err)
		}
	} else {
		m.log.DebugContext(ctx, "veth pair already exists", "primary", vethPrimary, "secondary", vethSecondary)
	}

	// Attach vethPrimary to primary bridge.
	primaryBr, err := netlink.LinkByName(primaryBridge)
	if err != nil {
		return fmt.Errorf("get primary bridge %s: %w", primaryBridge, err)
	}
	vpLink, err := netlink.LinkByName(vethPrimary)
	if err != nil {
		return fmt.Errorf("get veth %s: %w", vethPrimary, err)
	}
	if err := netlink.LinkSetMaster(vpLink, primaryBr); err != nil {
		return fmt.Errorf("attach %s to %s: %w", vethPrimary, primaryBridge, err)
	}
	if err := netlink.LinkSetUp(vpLink); err != nil {
		return fmt.Errorf("bring up %s: %w", vethPrimary, err)
	}

	// Attach vethSecondary to secondary bridge.
	vsLink, err := netlink.LinkByName(vethSecondary)
	if err != nil {
		return fmt.Errorf("get veth %s: %w", vethSecondary, err)
	}
	if err := netlink.LinkSetMaster(vsLink, br); err != nil {
		return fmt.Errorf("attach %s to %s: %w", vethSecondary, bridgeName, err)
	}
	if err := netlink.LinkSetUp(vsLink); err != nil {
		return fmt.Errorf("bring up %s: %w", vethSecondary, err)
	}

	// Apply forwarding rules for the new bridge.
	if err := ensureIptablesRule(ctx,
		[]string{"-C", "FORWARD", "-i", bridgeName, "!", "-o", bridgeName, "-j", "ACCEPT"},
		[]string{"-A", "FORWARD", "-i", bridgeName, "!", "-o", bridgeName, "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("secondary bridge forward out: %w", err)
	}
	if err := ensureIptablesRule(ctx,
		[]string{"-C", "FORWARD", "-o", bridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		[]string{"-A", "FORWARD", "-o", bridgeName, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
	); err != nil {
		return fmt.Errorf("secondary bridge forward in: %w", err)
	}

	m.log.InfoContext(ctx, "created secondary shared bridge",
		"name", bridgeName, "veth_primary", vethPrimary, "veth_secondary", vethSecondary)

	return nil
}

// setBridgeHashMax sets the FDB hash_max for a bridge to allow more MAC addresses.
func setBridgeHashMax(bridgeName string, hashMax int) error {
	path := fmt.Sprintf("/sys/class/net/%s/bridge/hash_max", bridgeName)
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%d", hashMax)
	return err
}
```

### `exelet/network/netns/recover_linux.go`

```go
//go:build linux

package netns

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/vishvananda/netlink"
	uns "github.com/vishvananda/netns"
)

const recoverWorkers = 8

// recoveredInfo is a single result from a recovery worker.
type recoveredInfo struct {
	id     string
	ip     string
	bridge string // shared bridge that vx-{vmid} is attached to
}

// RecoverExtIPs rebuilds the in-memory extIPs map by reading the ext-veth
// IP from each instance's network namespace. All state comes from the
// kernel — there are no files or leases involved.
//
// Recovery is parallelized across workers since each netns probe involves
// syscalls (LockOSThread, netns switch, netlink). With hundreds of VMs
// this cuts startup time significantly.
//
// Call this during startup after loading the instance list.
func (m *Manager) RecoverExtIPs(ctx context.Context, instanceIDs []string) error {
	if len(instanceIDs) == 0 {
		m.log.InfoContext(ctx, "ext IP recovery complete", "recovered", 0, "total_instances", 0)
		return nil
	}

	// Fan out to workers.
	ids := make(chan string)
	var wg sync.WaitGroup

	nWorkers := recoverWorkers
	if len(instanceIDs) < nWorkers {
		nWorkers = len(instanceIDs)
	}

	results := make(chan recoveredInfo, len(instanceIDs))

	for range nWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range ids {
				ns := nsName(id)
				vExt := vethXNS(id)

				ip, err := readAddrInNS(ns, vExt)
				if err != nil {
					m.log.DebugContext(ctx, "no ext IP to recover (netns or veth missing)",
						"instance", id, "ns", ns, "error", err)
					continue
				}

				// Discover which shared bridge the outer veth is attached to.
				bridge := getVethMasterBridge(vethXHost(id))

				results <- recoveredInfo{id: id, ip: ip, bridge: bridge}
			}
		}()
	}

	for _, id := range instanceIDs {
		ids <- id
	}
	close(ids)

	wg.Wait()
	close(results)

	// Collect results under lock.
	m.mu.Lock()
	defer m.mu.Unlock()

	var recovered int
	for r := range results {
		m.extIPs[r.id] = r.ip
		if r.bridge != "" {
			m.vmBridge[r.id] = r.bridge
		}
		recovered++
		m.log.InfoContext(ctx, "recovered ext IP from netns",
			"instance", r.id, "ns", nsName(r.id), "ext_ip", r.ip, "shared_bridge", r.bridge)
	}

	if recovered > 0 {
		m.advanceAllocatorPastUsed()
	}

	m.log.InfoContext(ctx, "ext IP recovery complete",
		"recovered", recovered, "total_instances", len(instanceIDs),
		"workers", nWorkers)
	return nil
}

// advanceAllocatorPastUsed sets nextOctet3/4 past the highest used IP.
// Must be called with m.mu held.
func (m *Manager) advanceAllocatorPastUsed() {
	var maxO3, maxO4 byte
	for _, ip := range m.extIPs {
		var o3, o4 int
		if _, err := fmt.Sscanf(ip, "10.99.%d.%d", &o3, &o4); err != nil {
			continue
		}
		if byte(o3) > maxO3 || (byte(o3) == maxO3 && byte(o4) > maxO4) {
			maxO3 = byte(o3)
			maxO4 = byte(o4)
		}
	}
	// Start allocating after the highest used IP.
	next4 := maxO4 + 1
	next3 := maxO3
	if next4 == 0 {
		next3++
		next4 = 1
	}
	m.nextOctet3 = next3
	m.nextOctet4 = next4
}

// getVethMasterBridge returns the bridge name that a root-ns veth is attached to.
func getVethMasterBridge(vethName string) string {
	link, err := netlink.LinkByName(vethName)
	if err != nil {
		return ""
	}
	masterIndex := link.Attrs().MasterIndex
	if masterIndex == 0 {
		return ""
	}
	master, err := netlink.LinkByIndex(masterIndex)
	if err != nil {
		return ""
	}
	return master.Attrs().Name
}

// readAddrInNS reads the first IPv4 address from a named interface inside
// a network namespace. Returns the bare IP (no mask).
func readAddrInNS(nsName, ifName string) (string, error) {
	// We must lock the OS thread to safely switch network namespaces.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	origNS, err := uns.Get()
	if err != nil {
		return "", fmt.Errorf("get current netns: %w", err)
	}
	defer origNS.Close()

	targetNS, err := uns.GetFromName(nsName)
	if err != nil {
		return "", fmt.Errorf("get netns %s: %w", nsName, err)
	}
	defer targetNS.Close()

	if err := uns.Set(targetNS); err != nil {
		return "", fmt.Errorf("enter netns %s: %w", nsName, err)
	}
	defer uns.Set(origNS) //nolint:errcheck

	link, err := netlink.LinkByName(ifName)
	if err != nil {
		return "", fmt.Errorf("get %s in %s: %w", ifName, nsName, err)
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return "", fmt.Errorf("list addrs on %s in %s: %w", ifName, nsName, err)
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("no IPv4 addr on %s in %s", ifName, nsName)
	}

	return addrs[0].IP.String(), nil
}
```

### `exelet/network/netns/utils_linux.go`

```go
//go:build linux

package netns

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

func writeSysctl(name, value string) error {
	path := fmt.Sprintf("/proc/sys/%s", strings.ReplaceAll(name, ".", "/"))
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.WriteString(f, value)
	return err
}

// ensureIptablesRule checks if a rule exists (checkArgs) and adds it (addArgs) if not.
func ensureIptablesRule(ctx context.Context, checkArgs, addArgs []string) error {
	// -C returns 0 if rule exists, non-zero otherwise.
	if exec.CommandContext(ctx, "iptables", checkArgs...).Run() == nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "iptables", addArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %v: %w (%s)", addArgs, err, out)
	}
	return nil
}
```

### `exelet/network/netns/debug_linux.go`

```go
//go:build linux

package netns

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"

	"github.com/vishvananda/netlink"
	uns "github.com/vishvananda/netns"
)

// DeviceInfo holds diagnostic information about a single network device.
type DeviceInfo struct {
	Name      string
	Location  string // "root" or netns name
	State     string // "UP", "DOWN", etc.
	Master    string // bridge name if attached
	Type      string // "tuntap", "veth", "bridge", etc.
	Addrs     []string
	RxBytes   uint64
	TxBytes   uint64
	RxPackets uint64
	TxPackets uint64
	RxErrors  uint64
	TxErrors  uint64
	RxDropped uint64
	TxDropped uint64
	MTU       int
	MAC       string
	Error     string // non-empty if device couldn't be read
}

// DiagResult holds the full diagnostic output for one instance.
type DiagResult struct {
	InstanceID string
	NsName     string
	ExtIP      string
	Devices    []DeviceInfo
	NsIPTables string // iptables -L -n -v inside the netns
	NsRoutes   string // ip route inside the netns
}

// Diagnose collects full diagnostic info for a given instance ID.
func Diagnose(ctx context.Context, instanceID string) (*DiagResult, error) {
	ns := NsName(instanceID)
	vid := vmID(instanceID)

	result := &DiagResult{
		InstanceID: instanceID,
		NsName:     ns,
	}

	// Root-ns devices.
	for _, d := range []struct {
		name  string
		label string
	}{
		{"tap-" + vid, "tap"},
		{"br-" + vid, "per-vm-bridge"},
		{"vb-" + vid, "inner-veth-root"},
		{"vx-" + vid, "outer-veth-root"},
	} {
		info := readLinkInfo(d.name, "root")
		info.Type = d.label
		result.Devices = append(result.Devices, info)
	}

	// Netns devices + iptables + routes.
	nsDevices, iptOut, routeOut, extIP, err := probeNetns(ctx, ns, vid)
	if err != nil {
		// Netns might not exist; record what we can.
		result.Devices = append(result.Devices, DeviceInfo{
			Name:     ns,
			Location: "netns",
			Error:    err.Error(),
		})
	} else {
		result.Devices = append(result.Devices, nsDevices...)
		result.NsIPTables = iptOut
		result.NsRoutes = routeOut
		result.ExtIP = extIP
	}

	return result, nil
}

// FormatDiag writes a human-readable diagnostic report to w.
func FormatDiag(w io.Writer, d *DiagResult) {
	fmt.Fprintf(w, "Instance: %s\n", d.InstanceID)
	fmt.Fprintf(w, "Netns:    %s\n", d.NsName)
	fmt.Fprintf(w, "Ext IP:   %s\n", d.ExtIP)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "── Devices ──")
	for _, dev := range d.Devices {
		if dev.Error != "" {
			fmt.Fprintf(w, "  %-14s [%s] ERROR: %s\n", dev.Name, dev.Location, dev.Error)
			continue
		}
		fmt.Fprintf(w, "  %-14s [%s] %s  state=%s  mtu=%d  mac=%s\n",
			dev.Name, dev.Location, dev.Type, dev.State, dev.MTU, dev.MAC)
		if dev.Master != "" {
			fmt.Fprintf(w, "%19smaster=%s\n", "", dev.Master)
		}
		for _, a := range dev.Addrs {
			fmt.Fprintf(w, "%19saddr %s\n", "", a)
		}
		fmt.Fprintf(w, "%19srx: %d pkts  %d bytes  %d errors  %d dropped\n",
			"", dev.RxPackets, dev.RxBytes, dev.RxErrors, dev.RxDropped)
		fmt.Fprintf(w, "%19stx: %d pkts  %d bytes  %d errors  %d dropped\n",
			"", dev.TxPackets, dev.TxBytes, dev.TxErrors, dev.TxDropped)
	}

	if d.NsRoutes != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "── Routes (in %s) ──\n", d.NsName)
		for _, line := range strings.Split(strings.TrimSpace(d.NsRoutes), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}

	if d.NsIPTables != "" {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "── IPTables (in %s) ──\n", d.NsName)
		for _, line := range strings.Split(strings.TrimSpace(d.NsIPTables), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
}

// DiagnoseAll returns diagnostics for all netns instances found on the system.
func DiagnoseAll(ctx context.Context) ([]*DiagResult, error) {
	out, err := exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ip netns list: %w", err)
	}

	var results []*DiagResult
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.Fields(line)
		if len(name) == 0 {
			continue
		}
		ns := name[0]
		if !strings.HasPrefix(ns, "exe-") {
			continue
		}
		// Extract vmid from namespace name (exe-vm000003 → vm000003).
		vid := strings.TrimPrefix(ns, "exe-")
		diag := diagByVMID(ctx, vid)
		results = append(results, diag)
	}
	return results, nil
}

// DiagnoseByVMID collects diagnostics using just the vmid prefix (e.g. "vm000003").
func DiagnoseByVMID(ctx context.Context, vid string) *DiagResult {
	return diagByVMID(ctx, vid)
}

// diagByVMID builds a DiagResult using just the vmid prefix (when full instance ID is unknown).
func diagByVMID(ctx context.Context, vid string) *DiagResult {
	ns := "exe-" + vid
	result := &DiagResult{
		InstanceID: vid,
		NsName:     ns,
	}

	for _, d := range []struct {
		name  string
		label string
	}{
		{"tap-" + vid, "tap"},
		{"br-" + vid, "per-vm-bridge"},
		{"vb-" + vid, "inner-veth-root"},
		{"vx-" + vid, "outer-veth-root"},
	} {
		info := readLinkInfo(d.name, "root")
		info.Type = d.label
		result.Devices = append(result.Devices, info)
	}

	nsDevices, iptOut, routeOut, extIP, err := probeNetns(ctx, ns, vid)
	if err != nil {
		result.Devices = append(result.Devices, DeviceInfo{
			Name:     ns,
			Location: "netns",
			Error:    err.Error(),
		})
	} else {
		result.Devices = append(result.Devices, nsDevices...)
		result.NsIPTables = iptOut
		result.NsRoutes = routeOut
		result.ExtIP = extIP
	}

	return result
}

func readLinkInfo(name, location string) DeviceInfo {
	info := DeviceInfo{Name: name, Location: location}

	link, err := netlink.LinkByName(name)
	if err != nil {
		info.Error = err.Error()
		return info
	}

	attrs := link.Attrs()
	info.State = attrs.OperState.String()
	if info.State == "unknown" {
		// Fallback: check flags.
		if attrs.Flags&0x1 != 0 { // IFF_UP
			info.State = "UP"
		} else {
			info.State = "DOWN"
		}
	}
	info.MTU = attrs.MTU
	info.MAC = attrs.HardwareAddr.String()

	if attrs.MasterIndex > 0 {
		if master, err := netlink.LinkByIndex(attrs.MasterIndex); err == nil {
			info.Master = master.Attrs().Name
		}
	}

	if stats := attrs.Statistics; stats != nil {
		info.RxBytes = stats.RxBytes
		info.TxBytes = stats.TxBytes
		info.RxPackets = stats.RxPackets
		info.TxPackets = stats.TxPackets
		info.RxErrors = stats.RxErrors
		info.TxErrors = stats.TxErrors
		info.RxDropped = stats.RxDropped
		info.TxDropped = stats.TxDropped
	}

	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err == nil {
		for _, a := range addrs {
			info.Addrs = append(info.Addrs, a.IPNet.String())
		}
	}

	return info
}

// probeNetns enters a network namespace and reads device info, iptables, and routes.
func probeNetns(ctx context.Context, nsName, vid string) (devices []DeviceInfo, iptables, routes, extIP string, err error) {
	type nsResult struct {
		devices  []DeviceInfo
		iptables string
		routes   string
		extIP    string
		err      error
	}

	ch := make(chan nsResult, 1)
	go func() {
		runtime.LockOSThread()
		// Don't unlock — the OS thread's netns is tainted.

		origNS, err := uns.Get()
		if err != nil {
			ch <- nsResult{err: fmt.Errorf("get current netns: %w", err)}
			return
		}
		defer origNS.Close()

		targetNS, err := uns.GetFromName(nsName)
		if err != nil {
			ch <- nsResult{err: fmt.Errorf("open netns %s: %w", nsName, err)}
			return
		}
		defer targetNS.Close()

		if err := uns.Set(targetNS); err != nil {
			ch <- nsResult{err: fmt.Errorf("enter netns %s: %w", nsName, err)}
			return
		}
		defer uns.Set(origNS) //nolint:errcheck

		var r nsResult

		// Read devices inside the namespace.
		for _, d := range []struct {
			name  string
			label string
		}{
			{"vg-" + vid, "inner-veth-ns (gateway)"},
			{"ve-" + vid, "outer-veth-ns (outbound)"},
		} {
			info := readLinkInfo(d.name, nsName)
			info.Type = d.label
			r.devices = append(r.devices, info)

			// Extract ext IP from the outbound veth.
			if strings.HasPrefix(d.name, "ve-") && len(info.Addrs) > 0 {
				// Strip the /16 mask.
				parts := strings.SplitN(info.Addrs[0], "/", 2)
				r.extIP = parts[0]
			}
		}

		ch <- r
	}()

	r := <-ch
	if r.err != nil {
		return nil, "", "", "", r.err
	}

	// Run iptables and ip-route via `ip netns exec` (they need their own process).
	iptOut, _ := exec.CommandContext(ctx, "ip", "netns", "exec", nsName,
		"iptables", "-L", "-n", "-v", "--line-numbers").CombinedOutput()
	natOut, _ := exec.CommandContext(ctx, "ip", "netns", "exec", nsName,
		"iptables", "-t", "nat", "-L", "-n", "-v", "--line-numbers").CombinedOutput()
	routeOut, _ := exec.CommandContext(ctx, "ip", "netns", "exec", nsName,
		"ip", "route").CombinedOutput()

	var iptBuf strings.Builder
	iptBuf.WriteString("# filter\n")
	iptBuf.Write(iptOut)
	iptBuf.WriteString("\n# nat\n")
	iptBuf.Write(natOut)

	return r.devices, iptBuf.String(), string(routeOut), r.extIP, nil
}
```

### `exelet/network/netns/debug_other.go`

```go
//go:build !linux

package netns

import (
	"context"
	"fmt"
	"io"
)

// DeviceInfo holds diagnostic information about a single network device.
type DeviceInfo struct {
	Name      string
	Location  string
	State     string
	Master    string
	Type      string
	Addrs     []string
	RxBytes   uint64
	TxBytes   uint64
	RxPackets uint64
	TxPackets uint64
	RxErrors  uint64
	TxErrors  uint64
	RxDropped uint64
	TxDropped uint64
	MTU       int
	MAC       string
	Error     string
}

// DiagResult holds the full diagnostic output for one instance.
type DiagResult struct {
	InstanceID string
	NsName     string
	ExtIP      string
	Devices    []DeviceInfo
	NsIPTables string
	NsRoutes   string
}

func Diagnose(_ context.Context, _ string) (*DiagResult, error) {
	return nil, fmt.Errorf("netns diagnostics require linux")
}

func DiagnoseAll(_ context.Context) ([]*DiagResult, error) {
	return nil, fmt.Errorf("netns diagnostics require linux")
}

func DiagnoseByVMID(_ context.Context, _ string) *DiagResult {
	return &DiagResult{}
}

func FormatDiag(_ io.Writer, _ *DiagResult) {}
```

### `exelet/network/netns/live_linux.go`

```go
//go:build linux

package netns

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ConnEntry represents a single conntrack or ss connection.
type ConnEntry struct {
	Proto   string
	State   string
	Src     string
	Dst     string
	Sport   string
	Dport   string
	NATSrc  string // post-NAT source (if SNAT/masquerade)
	NATDst  string // post-NAT dest (if DNAT)
	NATSprt string
	NATDprt string
}

func (c ConnEntry) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-5s %-12s %s:%s → %s:%s", c.Proto, c.State, c.Src, c.Sport, c.Dst, c.Dport)
	if c.NATDst != "" || c.NATSrc != "" {
		natSrc := c.NATSrc
		natSprt := c.NATSprt
		if natSrc == "" {
			natSrc = c.Src
			natSprt = c.Sport
		}
		natDst := c.NATDst
		natDprt := c.NATDprt
		if natDst == "" {
			natDst = c.Dst
			natDprt = c.Dport
		}
		fmt.Fprintf(&b, "  NAT→ %s:%s → %s:%s", natSrc, natSprt, natDst, natDprt)
	}
	return b.String()
}

// LiveStream streams conntrack events from the instance's network namespace.
// It first prints a snapshot of existing connections, then streams new events.
// Blocks until ctx is cancelled.
func LiveStream(ctx context.Context, w io.Writer, instanceID string) error {
	ns := NsName(instanceID)
	vid := vmID(instanceID)

	// Verify the namespace exists.
	if _, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns, "true").CombinedOutput(); err != nil {
		return fmt.Errorf("netns %s not found: %w", ns, err)
	}

	fmt.Fprintf(w, "Live connections for %s (netns %s)\n", vid, ns)
	fmt.Fprintf(w, "VM: %s → gateway %s → ext %s → internet\n", VMIP, VMGateway, "10.99.x.x")
	fmt.Fprintf(w, "Press Ctrl-C to stop\n\n")

	// Try conntrack -E (real-time event stream). This is the best option
	// but requires conntrack to be installed.
	if err := streamConntrackEvents(ctx, w, ns); err != nil {
		// Fall back to polling conntrack -L.
		fmt.Fprintf(w, "conntrack -E unavailable (%v), falling back to polling\n\n", err)
		return pollConnections(ctx, w, ns)
	}
	return nil
}

// LiveStreamByVMID is like LiveStream but takes a vmid (e.g. "vm000003") instead of full instance ID.
func LiveStreamByVMID(ctx context.Context, w io.Writer, vid string) error {
	ns := "exe-" + vid

	if _, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns, "true").CombinedOutput(); err != nil {
		return fmt.Errorf("netns %s not found: %w", ns, err)
	}

	fmt.Fprintf(w, "Live connections for %s (netns %s)\n", vid, ns)
	fmt.Fprintf(w, "VM: %s → gateway %s → ext %s → internet\n", VMIP, VMGateway, "10.99.x.x")
	fmt.Fprintf(w, "Press Ctrl-C to stop\n\n")

	if err := streamConntrackEvents(ctx, w, ns); err != nil {
		fmt.Fprintf(w, "conntrack -E unavailable (%v), falling back to polling\n\n", err)
		return pollConnections(ctx, w, ns)
	}
	return nil
}

// streamConntrackEvents runs `conntrack -E` inside the namespace and streams
// parsed events to w. Returns an error immediately if conntrack is not available.
func streamConntrackEvents(ctx context.Context, w io.Writer, ns string) error {
	// First, print existing connections as a snapshot.
	snap, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns,
		"conntrack", "-L", "-o", "extended", "2>/dev/null").CombinedOutput()
	if err != nil {
		// conntrack not installed or no permissions.
		return fmt.Errorf("conntrack -L: %w", err)
	}

	existing := parseConntrackOutput(string(snap))
	if len(existing) > 0 {
		fmt.Fprintf(w, "── Existing connections (%d) ──\n", len(existing))
		for _, c := range existing {
			fmt.Fprintf(w, "  %s\n", c)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintln(w, "── Streaming events ──")

	// Start the event stream.
	cmd := exec.CommandContext(ctx, "ip", "netns", "exec", ns,
		"conntrack", "-E", "-o", "extended")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("conntrack -E: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		event, entry := parseConntrackEvent(line)
		if event == "" {
			// Unparseable, print raw.
			fmt.Fprintf(w, "  %s  %s\n", timestamp(), line)
			continue
		}
		fmt.Fprintf(w, "  %s  %-8s %s\n", timestamp(), event, entry)
	}

	// Wait for process to exit (cancelled context kills it).
	_ = cmd.Wait()
	return ctx.Err()
}

// pollConnections falls back to periodic `conntrack -L` + `ss` polling.
func pollConnections(ctx context.Context, w io.Writer, ns string) error {
	seen := make(map[string]struct{})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Try conntrack -L first, fall back to ss.
		var entries []ConnEntry
		snap, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns,
			"conntrack", "-L", "-o", "extended").CombinedOutput()
		if err == nil {
			entries = parseConntrackOutput(string(snap))
		} else {
			entries = pollSS(ctx, ns)
		}

		for _, c := range entries {
			key := c.Proto + ":" + c.Src + ":" + c.Sport + "→" + c.Dst + ":" + c.Dport
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			fmt.Fprintf(w, "  %s  NEW      %s\n", timestamp(), c)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// pollSS uses `ss` as a last resort when conntrack is unavailable.
func pollSS(ctx context.Context, ns string) []ConnEntry {
	out, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns,
		"ss", "-tunp", "-o", "state", "established").CombinedOutput()
	if err != nil {
		return nil
	}

	var entries []ConnEntry
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		proto := fields[0]
		if proto != "tcp" && proto != "udp" {
			continue
		}
		local := fields[3]
		peer := fields[4]
		lHost, lPort := splitHostPort(local)
		pHost, pPort := splitHostPort(peer)
		entries = append(entries, ConnEntry{
			Proto: proto,
			State: "ESTABLISHED",
			Src:   lHost,
			Sport: lPort,
			Dst:   pHost,
			Dport: pPort,
		})
	}
	return entries
}

// parseConntrackOutput parses `conntrack -L -o extended` output.
func parseConntrackOutput(output string) []ConnEntry {
	var entries []ConnEntry
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "conntrack") {
			continue
		}
		entry := parseConntrackLine(line)
		if entry.Src != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

// parseConntrackLine parses a single conntrack line like:
// ipv4     2 tcp      6 300 ESTABLISHED src=10.42.0.42 dst=1.2.3.4 sport=12345 dport=443 src=1.2.3.4 dst=10.99.0.2 sport=443 dport=12345 [ASSURED] mark=0 use=1
func parseConntrackLine(line string) ConnEntry {
	var entry ConnEntry
	fields := strings.Fields(line)

	// Find the protocol.
	for _, f := range fields {
		switch f {
		case "tcp", "udp", "icmp":
			entry.Proto = f
		}
	}

	// Find state (ESTABLISHED, SYN_SENT, etc.).
	for _, f := range fields {
		switch f {
		case "ESTABLISHED", "SYN_SENT", "SYN_RECV", "FIN_WAIT",
			"CLOSE_WAIT", "LAST_ACK", "TIME_WAIT", "CLOSE",
			"LISTEN", "UNREPLIED", "ASSURED":
			if entry.State == "" {
				entry.State = f
			}
		}
	}
	if entry.State == "" {
		entry.State = "-"
	}

	// Parse key=value pairs. Conntrack lines have two direction tuples;
	// the first is the original direction (src/dst from VM's perspective),
	// the second is the reply direction (which reveals NAT translations).
	kvPairs := make([]map[string]string, 0, 2)
	current := make(map[string]string)
	seenSrc := false
	for _, f := range fields {
		if !strings.Contains(f, "=") {
			continue
		}
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		if k == "src" {
			if seenSrc {
				// Second src= means we're in the reply tuple.
				kvPairs = append(kvPairs, current)
				current = make(map[string]string)
			}
			seenSrc = true
		}
		current[k] = v
	}
	kvPairs = append(kvPairs, current)

	if len(kvPairs) >= 1 {
		entry.Src = kvPairs[0]["src"]
		entry.Dst = kvPairs[0]["dst"]
		entry.Sport = kvPairs[0]["sport"]
		entry.Dport = kvPairs[0]["dport"]
	}
	if len(kvPairs) >= 2 {
		// In the reply tuple, src/dst are swapped. If the reply src
		// differs from the original dst, there's DNAT. If the reply dst
		// differs from the original src, there's SNAT.
		replySrc := kvPairs[1]["src"]
		replyDst := kvPairs[1]["dst"]
		replySport := kvPairs[1]["sport"]
		replyDport := kvPairs[1]["dport"]

		if replySrc != entry.Dst {
			entry.NATDst = replySrc
			entry.NATDprt = replySport
		}
		if replyDst != entry.Src {
			entry.NATSrc = replyDst
			entry.NATSprt = replyDport
		}
	}

	return entry
}

// parseConntrackEvent parses a `conntrack -E` event line like:
// [NEW] tcp      6 120 SYN_SENT src=10.42.0.42 dst=1.2.3.4 ...
func parseConntrackEvent(line string) (event string, entry ConnEntry) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", ConnEntry{}
	}

	// Extract event type from brackets.
	if line[0] == '[' {
		idx := strings.Index(line, "]")
		if idx > 0 {
			event = strings.TrimSpace(line[1:idx])
			line = strings.TrimSpace(line[idx+1:])
		}
	}

	entry = parseConntrackLine(line)
	return event, entry
}

func splitHostPort(s string) (host, port string) {
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return s, ""
	}
	return s[:idx], s[idx+1:]
}

func timestamp() string {
	return time.Now().Format("15:04:05.000")
}
```

### `exelet/network/netns/live_other.go`

```go
//go:build !linux

package netns

import (
	"context"
	"fmt"
	"io"
)

func LiveStream(_ context.Context, _ io.Writer, _ string) error {
	return fmt.Errorf("live streaming requires linux")
}

func LiveStreamByVMID(_ context.Context, _ io.Writer, _ string) error {
	return fmt.Errorf("live streaming requires linux")
}
```

### `exelet/network/netns/netns_linux_test.go`

```go
//go:build linux

package netns

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func skipUnlessRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
}

func TestNewManager(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	m, err := NewManager("netns:///tmp/netns-test", log)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected non-nil manager")
	}
}

func TestNewManagerBadScheme(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	_, err := NewManager("nat:///tmp", log)
	if err == nil {
		t.Fatal("expected error for wrong scheme")
	}
}

func TestNaming(t *testing.T) {
	id := "vm000003-orbit-falcon"
	// All names should be <= 15 chars (IFNAMSIZ).
	for _, name := range []string{
		tapName(id), nsName(id), brName(id),
		vethBrName(id), vethGwName(id), vethXHost(id), vethXNS(id),
	} {
		if len(name) > 15 {
			t.Errorf("%q is %d chars, exceeds IFNAMSIZ (15)", name, len(name))
		}
	}

	// Verify vmid extraction and naming.
	if got := tapName(id); got != "tap-vm000003" {
		t.Errorf("tapName(%q) = %q, want tap-vm000003", id, got)
	}
	if got := NsName(id); got != "exe-vm000003" {
		t.Errorf("NsName(%q) = %q, want exe-vm000003", id, got)
	}
	if got := BridgeName(id); got != "br-vm000003" {
		t.Errorf("BridgeName(%q) = %q, want br-vm000003", id, got)
	}
}

func TestAllocateExtIP(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, _ := NewManager("netns:///tmp/netns-test", log)

	ip1, err := m.allocateExtIP("vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if ip1 != "10.99.0.2" {
		t.Fatalf("expected 10.99.0.2, got %s", ip1)
	}

	ip2, err := m.allocateExtIP("vm-2")
	if err != nil {
		t.Fatal(err)
	}
	if ip2 != "10.99.0.3" {
		t.Fatalf("expected 10.99.0.3, got %s", ip2)
	}

	// Re-allocating same ID returns same IP.
	ip1again, err := m.allocateExtIP("vm-1")
	if err != nil {
		t.Fatal(err)
	}
	if ip1again != ip1 {
		t.Fatalf("re-alloc returned %s, want %s", ip1again, ip1)
	}

	// Release and re-allocate.
	m.releaseExtIP("vm-1")
	ip3, err := m.allocateExtIP("vm-3")
	if err != nil {
		t.Fatal(err)
	}
	if ip3 != "10.99.0.4" {
		t.Fatalf("expected 10.99.0.4, got %s", ip3)
	}
}

func TestGetInstanceByExtIP(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	m, _ := NewManager("netns:///tmp/netns-test", log)

	m.allocateExtIP("vm-abc")

	id, ok := m.GetInstanceByExtIP("10.99.0.2")
	if !ok || id != "vm-abc" {
		t.Fatalf("got (%q, %v), want (vm-abc, true)", id, ok)
	}

	_, ok = m.GetInstanceByExtIP("10.99.0.99")
	if ok {
		t.Fatal("expected not found")
	}
}

// TestCreateDeleteInterface is an integration test that actually creates
// network namespaces and interfaces. Requires root.
func TestCreateDeleteInterface(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}

	// Start sets up the shared bridge.
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() {
		// Clean up shared bridge.
		delLinkByName(SharedBridge)
	})

	id := "vm000001-test-instance"

	iface, err := m.CreateInterface(ctx, id)
	if err != nil {
		t.Fatal("CreateInterface:", err)
	}

	// Verify returned interface.
	if iface.IP.IPV4 != VMIP+"/16" {
		t.Errorf("IP = %q, want %q", iface.IP.IPV4, VMIP+"/16")
	}
	if iface.IP.GatewayV4 != VMGateway {
		t.Errorf("Gateway = %q, want %q", iface.IP.GatewayV4, VMGateway)
	}
	if iface.MACAddress == "" {
		t.Error("expected non-empty MAC")
	}

	// Verify TAP exists in root ns.
	tap := tapName(id)
	if _, err := netlink.LinkByName(tap); err != nil {
		t.Errorf("TAP %s not found: %v", tap, err)
	}

	// Verify per-VM bridge exists.
	br := brName(id)
	if _, err := netlink.LinkByName(br); err != nil {
		t.Errorf("bridge %s not found: %v", br, err)
	}

	// Verify netns exists.
	ns := nsName(id)
	out, err := exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
	if err != nil {
		t.Fatal("ip netns list:", err)
	}
	if !strings.Contains(string(out), ns) {
		t.Errorf("netns %s not in list: %s", ns, out)
	}

	// Verify connectivity inside netns: gateway veth has IP.
	out, err = exec.CommandContext(ctx, "ip", "netns", "exec", ns, "ip", "addr", "show").CombinedOutput()
	if err != nil {
		t.Fatal("ip addr in netns:", err)
	}
	if !strings.Contains(string(out), VMGateway) {
		t.Errorf("gateway IP %s not found in netns: %s", VMGateway, out)
	}

	// Verify iptables rules inside netns.
	out, err = exec.CommandContext(ctx, "ip", "netns", "exec", ns, "iptables", "-t", "nat", "-L", "-n").CombinedOutput()
	if err != nil {
		t.Fatal("iptables in netns:", err)
	}
	if !strings.Contains(string(out), "SNAT") {
		t.Error("SNAT rule not found in netns")
	}
	if !strings.Contains(string(out), MetadataIP) {
		t.Error("metadata DNAT rule not found in netns")
	}

	// Verify ext IP tracking.
	extIP, ok := m.getExtIP(id)
	if !ok {
		t.Fatal("ext IP not tracked")
	}
	lookedUp, ok := m.GetInstanceByExtIP(extIP)
	if !ok || lookedUp != id {
		t.Fatalf("GetInstanceByExtIP(%s) = (%q, %v), want (%q, true)", extIP, lookedUp, ok, id)
	}

	// Delete.
	if err := m.DeleteInterface(ctx, id, ""); err != nil {
		t.Fatal("DeleteInterface:", err)
	}

	// Verify cleanup.
	if _, err := netlink.LinkByName(tap); err == nil {
		t.Errorf("TAP %s still exists after delete", tap)
	}
	if _, err := netlink.LinkByName(br); err == nil {
		t.Errorf("bridge %s still exists after delete", br)
	}
	vBr := vethBrName(id)
	vXH := vethXHost(id)
	if _, err := netlink.LinkByName(vBr); err == nil {
		t.Errorf("veth %s still exists after delete", vBr)
	}
	if _, err := netlink.LinkByName(vXH); err == nil {
		t.Errorf("veth %s still exists after delete", vXH)
	}
	out, err = exec.CommandContext(ctx, "ip", "netns", "list").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ns) {
		t.Errorf("netns %s still exists after delete", ns)
	}
	_, ok = m.getExtIP(id)
	if ok {
		t.Error("ext IP still tracked after delete")
	}
}

// TestTwoVMs verifies that two VMs get the same internal IP but different
// ext IPs, and that their namespaces are isolated.
func TestTwoVMs(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() { delLinkByName(SharedBridge) })

	id1 := "vm000002-alpha"
	id2 := "vm000003-beta"

	iface1, err := m.CreateInterface(ctx, id1)
	if err != nil {
		t.Fatal("CreateInterface vm1:", err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id1, "") })

	iface2, err := m.CreateInterface(ctx, id2)
	if err != nil {
		t.Fatal("CreateInterface vm2:", err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id2, "") })

	// Both get same VM IP.
	if iface1.IP.IPV4 != iface2.IP.IPV4 {
		t.Errorf("VM IPs differ: %s vs %s", iface1.IP.IPV4, iface2.IP.IPV4)
	}
	if iface1.IP.IPV4 != VMIP+"/16" {
		t.Errorf("VM IP = %q, want %q", iface1.IP.IPV4, VMIP+"/16")
	}

	// Different ext IPs.
	ext1, _ := m.getExtIP(id1)
	ext2, _ := m.getExtIP(id2)
	if ext1 == ext2 {
		t.Errorf("ext IPs should differ: both %s", ext1)
	}

	// Both namespaces exist and are distinct.
	ns1 := nsName(id1)
	ns2 := nsName(id2)
	if ns1 == ns2 {
		t.Fatal("namespace names collide")
	}

	// Verify each namespace has the gateway IP.
	for _, ns := range []string{ns1, ns2} {
		out, err := exec.CommandContext(ctx, "ip", "netns", "exec", ns, "ip", "addr").CombinedOutput()
		if err != nil {
			t.Fatalf("ip addr in %s: %v", ns, err)
		}
		if !strings.Contains(string(out), VMGateway) {
			t.Errorf("ns %s missing gateway %s", ns, VMGateway)
		}
	}

	// Verify metadata lookup by ext IP.
	got1, ok := m.GetInstanceByExtIP(ext1)
	if !ok || got1 != id1 {
		t.Errorf("lookup ext1: got (%q, %v)", got1, ok)
	}
	got2, ok := m.GetInstanceByExtIP(ext2)
	if !ok || got2 != id2 {
		t.Errorf("lookup ext2: got (%q, %v)", got2, ok)
	}
}

// TestRecoverExtIPs verifies that after an exelet restart (simulated by
// clearing the in-memory map), RecoverExtIPs rebuilds the map from kernel state.
func TestRecoverExtIPs(t *testing.T) {
	skipUnlessRoot(t)

	ctx := t.Context()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal("Start:", err)
	}
	t.Cleanup(func() { delLinkByName(SharedBridge) })

	id1 := "vm000004-recover-one"
	id2 := "vm000005-recover-two"

	_, err = m.CreateInterface(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id1, "") })

	_, err = m.CreateInterface(ctx, id2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.DeleteInterface(context.Background(), id2, "") })

	// Remember the ext IPs before "restart".
	ext1Before, _ := m.getExtIP(id1)
	ext2Before, _ := m.getExtIP(id2)
	if ext1Before == "" || ext2Before == "" {
		t.Fatal("ext IPs not allocated")
	}

	// Simulate exelet restart: create a fresh manager with empty state.
	m2, err := NewManager("netns:///tmp/netns-test?disable_bandwidth=true", log)
	if err != nil {
		t.Fatal(err)
	}

	// Before recovery: map is empty.
	_, ok := m2.GetInstanceByExtIP(ext1Before)
	if ok {
		t.Fatal("fresh manager should have empty ext IP map")
	}

	// Recover.
	if err := m2.RecoverExtIPs(ctx, []string{id1, id2}); err != nil {
		t.Fatal("RecoverExtIPs:", err)
	}

	// After recovery: same IPs recovered.
	ext1After, ok := m2.getExtIP(id1)
	if !ok || ext1After != ext1Before {
		t.Errorf("id1 ext IP: got %q, want %q", ext1After, ext1Before)
	}
	ext2After, ok := m2.getExtIP(id2)
	if !ok || ext2After != ext2Before {
		t.Errorf("id2 ext IP: got %q, want %q", ext2After, ext2Before)
	}

	// Metadata lookup works.
	gotID, ok := m2.GetInstanceByExtIP(ext1Before)
	if !ok || gotID != id1 {
		t.Errorf("GetInstanceByExtIP(%s) = (%q, %v), want (%q, true)", ext1Before, gotID, ok, id1)
	}

	// New allocations don't collide with recovered IPs.
	newIP, err := m2.allocateExtIP("vm000006-brand-new")
	if err != nil {
		t.Fatal(err)
	}
	if newIP == ext1Before || newIP == ext2Before {
		t.Errorf("new allocation %s collides with recovered IP", newIP)
	}
}

// getExtIP is a test helper to check ext IP state.
func (m *Manager) getExtIP(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ip, ok := m.extIPs[id]
	return ip, ok
}

// TestBridgeSplitting verifies that the manager tracks bridge port counts
// and creates secondary bridges when the primary is full.
func TestBridgeSplitting(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	m, err := NewManager("netns:///tmp/netns-test", log)
	if err != nil {
		t.Fatal(err)
	}

	// Override max ports per bridge to a small value for testing.
	m.maxPortsPerBridge = 2

	// Verify initial state.
	if len(m.bridges) != 1 {
		t.Fatalf("expected 1 bridge, got %d", len(m.bridges))
	}
	if m.bridges[0].name != SharedBridge {
		t.Fatalf("expected primary bridge %s, got %s", SharedBridge, m.bridges[0].name)
	}

	// First two selections should go to the primary bridge.
	name1, needsNew := m.selectBridgeAndIncrement()
	if needsNew || name1 != SharedBridge {
		t.Fatalf("first select: got (%s, %v), want (%s, false)", name1, needsNew, SharedBridge)
	}
	name2, needsNew := m.selectBridgeAndIncrement()
	if needsNew || name2 != SharedBridge {
		t.Fatalf("second select: got (%s, %v), want (%s, false)", name2, needsNew, SharedBridge)
	}

	// Third selection should need a new bridge (primary is full at 2).
	_, needsNew = m.selectBridgeAndIncrement()
	if !needsNew {
		t.Fatal("third select: expected needsNewBridge=true")
	}

	// Reserve and add a secondary bridge.
	nextName := m.reserveNextBridge()
	if nextName != "br-exe-1" {
		t.Fatalf("expected next bridge br-exe-1, got %s", nextName)
	}
	selected := m.addBridgeAndSelect(nextName)
	if selected != "br-exe-1" {
		t.Fatalf("expected selected br-exe-1, got %s", selected)
	}

	// Next selection should go to the secondary bridge.
	name3, needsNew := m.selectBridgeAndIncrement()
	if needsNew || name3 != "br-exe-1" {
		t.Fatalf("fourth select: got (%s, %v), want (br-exe-1, false)", name3, needsNew)
	}

	// Decrement a port on primary, next selection should use primary again.
	m.decrementBridgePort(SharedBridge)
	name4, needsNew := m.selectBridgeAndIncrement()
	if needsNew || name4 != SharedBridge {
		t.Fatalf("fifth select: got (%s, %v), want (%s, false)", name4, needsNew, SharedBridge)
	}
}

// TestVMBridgeTracking verifies the VM-to-bridge mapping.
func TestVMBridgeTracking(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	m, err := NewManager("netns:///tmp/netns-test", log)
	if err != nil {
		t.Fatal(err)
	}

	m.setVMBridge("vm-1", SharedBridge)
	m.setVMBridge("vm-2", "br-exe-1")

	if got := m.getVMBridge("vm-1"); got != SharedBridge {
		t.Errorf("vm-1 bridge: got %s, want %s", got, SharedBridge)
	}
	if got := m.getVMBridge("vm-2"); got != "br-exe-1" {
		t.Errorf("vm-2 bridge: got %s, want br-exe-1", got)
	}

	m.removeVMBridge("vm-1")
	if got := m.getVMBridge("vm-1"); got != "" {
		t.Errorf("vm-1 bridge after remove: got %s, want empty", got)
	}
}
```

### `cmd/exelet-netns/main.go`

```go
//go:build linux

// exelet-netns prints diagnostic info for netns-managed VM network stacks.
//
// Usage:
//
//	exelet-netns <instance-id>          # diagnose one instance by full ID
//	exelet-netns <vmid>                  # diagnose by vmid (e.g. vm000003)
//	exelet-netns --all                   # diagnose all exe-* namespaces
//	exelet-netns --live <instance-id>    # stream live connections
//	exelet-netns --live <vmid>           # stream live connections by vmid
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"exe.dev/exelet/network/netns"
)

func main() {
	if err := run(); err != nil {
		if err != context.Canceled {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: exelet-netns <instance-id|vmid>\n")
	fmt.Fprintf(os.Stderr, "       exelet-netns --all\n")
	fmt.Fprintf(os.Stderr, "       exelet-netns --live <instance-id|vmid>\n")
	os.Exit(1)
}

// isVMID returns true if s looks like a bare vmid (e.g. "vm000003").
func isVMID(s string) bool {
	return strings.HasPrefix(s, "vm") && !strings.Contains(s, "-")
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if len(os.Args) < 2 {
		usage()
	}

	switch {
	case os.Args[1] == "--all":
		results, err := netns.DiagnoseAll(ctx)
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Println("no exe-* network namespaces found")
			return nil
		}
		for i, d := range results {
			if i > 0 {
				fmt.Println(strings.Repeat("\u2501", 60))
			}
			netns.FormatDiag(os.Stdout, d)
		}

	case os.Args[1] == "--live":
		if len(os.Args) < 3 {
			return fmt.Errorf("--live requires an instance ID or vmid")
		}
		arg := os.Args[2]
		if isVMID(arg) {
			return netns.LiveStreamByVMID(ctx, os.Stdout, arg)
		}
		return netns.LiveStream(ctx, os.Stdout, arg)

	default:
		arg := os.Args[1]
		if isVMID(arg) {
			d := netns.DiagnoseByVMID(ctx, arg)
			netns.FormatDiag(os.Stdout, d)
		} else {
			d, err := netns.Diagnose(ctx, arg)
			if err != nil {
				return err
			}
			netns.FormatDiag(os.Stdout, d)
		}
	}

	return nil
}
```

### `exepipe/dial_netns_linux.go`

```go
//go:build linux

package exepipe

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"time"

	"github.com/vishvananda/netns"
)

// NetnsDialFunc returns a DialFunc that enters the named network
// namespace before dialing. The TCP connection is established from
// within the netns; once connected, the socket works from any thread.
func NetnsDialFunc() DialFunc {
	return func(ctx context.Context, host string, port int, nsName string, timeout time.Duration) (net.Conn, error) {
		if nsName == "" {
			d := net.Dialer{Timeout: timeout}
			return d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		}

		// LockOSThread so the netns switch only affects this goroutine.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		origNS, err := netns.Get()
		if err != nil {
			return nil, fmt.Errorf("get current netns: %w", err)
		}
		defer origNS.Close()

		targetNS, err := netns.GetFromName(nsName)
		if err != nil {
			return nil, fmt.Errorf("get netns %s: %w", nsName, err)
		}
		defer targetNS.Close()

		if err := netns.Set(targetNS); err != nil {
			return nil, fmt.Errorf("enter netns %s: %w", nsName, err)
		}
		defer netns.Set(origNS) //nolint:errcheck

		d := net.Dialer{Timeout: timeout}
		return d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
}
```

### `exepipe/dial_netns_other.go`

```go
//go:build !linux

package exepipe

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"
)

// NetnsDialFunc returns a DialFunc that ignores the netns argument
// on non-Linux platforms (netns is not supported).
func NetnsDialFunc() DialFunc {
	return func(_ context.Context, host string, port int, nsName string, timeout time.Duration) (net.Conn, error) {
		if nsName != "" {
			return nil, fmt.Errorf("network namespaces not supported on this platform")
		}
		d := net.Dialer{Timeout: timeout}
		return d.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
}
```

### `exelet/services/compute/receive_vm_test.go`

```go
package compute

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	api "exe.dev/pkg/api/exe/compute/v1"
)

func TestSameVMIP(t *testing.T) {
	mkInstance := func(ipv4 string) *api.Instance {
		if ipv4 == "" {
			return &api.Instance{VMConfig: &api.VMConfig{}}
		}
		return &api.Instance{
			VMConfig: &api.VMConfig{
				NetworkInterface: &api.NetworkInterface{
					IP: &api.IPAddress{IPV4: ipv4},
				},
			},
		}
	}
	mkNet := func(ipv4 string) *api.NetworkInterface {
		return &api.NetworkInterface{IP: &api.IPAddress{IPV4: ipv4}}
	}

	tests := []struct {
		name   string
		source *api.Instance
		target *api.NetworkInterface
		want   bool
	}{
		{
			name:   "same IP different CIDR",
			source: mkInstance("10.42.0.42/24"),
			target: mkNet("10.42.0.42/16"),
			want:   true,
		},
		{
			name:   "same IP same CIDR",
			source: mkInstance("10.42.0.42/24"),
			target: mkNet("10.42.0.42/24"),
			want:   true,
		},
		{
			name:   "different IP nat to netns",
			source: mkInstance("10.42.0.5/16"),
			target: mkNet("10.42.0.42/24"),
			want:   false,
		},
		{
			name:   "nil source",
			source: nil,
			target: mkNet("10.42.0.42/24"),
			want:   false,
		},
		{
			name:   "nil target",
			source: mkInstance("10.42.0.42/24"),
			target: nil,
			want:   false,
		},
		{
			name:   "source missing network interface",
			source: mkInstance(""),
			target: mkNet("10.42.0.42/24"),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sameVMIP(tt.source, tt.target)
			if got != tt.want {
				t.Errorf("sameVMIP() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEditSnapshotConfigUpdatesTapName(t *testing.T) {
	mkSnapshotConfig := func(t *testing.T, tapName, cmdline string) string {
		t.Helper()
		dir := t.TempDir()
		chvConfig := map[string]any{
			"disks": []any{
				map[string]any{"path": "/old/disk"},
			},
			"payload": map[string]any{
				"kernel":  "/old/kernel",
				"cmdline": cmdline,
			},
			"net": []any{
				map[string]any{
					"tap": tapName,
					"mac": "02:73:6d:63:28:e6",
				},
			},
		}
		data, err := json.Marshal(chvConfig)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	readTap := func(t *testing.T, dir string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		nets := result["net"].([]any)
		netCfg := nets[0].(map[string]any)
		return netCfg["tap"].(string)
	}

	cmdline := "console=hvc0 root=/dev/vda ip=10.42.0.5::10.42.0.1:255.255.0.0:island-queen:eth0:none:1.1.1.1:8.8.8.8:ntp.ubuntu.com"
	srcVMConfig := &api.VMConfig{Name: "island-queen"}

	t.Run("nat to netns", func(t *testing.T) {
		dir := mkSnapshotConfig(t, "tap-5c4c99", cmdline)
		target := &api.NetworkInterface{
			Name:        "tap-vm000001",
			DeviceName:  "eth0",
			IP:          &api.IPAddress{IPV4: "10.42.0.42/24", GatewayV4: "10.42.0.1"},
			Nameservers: []string{"1.1.1.1"},
			NTPServer:   "ntp.ubuntu.com",
		}
		if err := editSnapshotConfig(dir, "/new/disk", "/new/kernel", srcVMConfig, target); err != nil {
			t.Fatal(err)
		}
		if got := readTap(t, dir); got != "tap-vm000001" {
			t.Errorf("tap = %q, want %q", got, "tap-vm000001")
		}
	})

	t.Run("netns to nat", func(t *testing.T) {
		dir := mkSnapshotConfig(t, "tap-vm000001", cmdline)
		target := &api.NetworkInterface{
			Name:        "tap-a1b2c3",
			DeviceName:  "eth0",
			IP:          &api.IPAddress{IPV4: "10.42.0.7/16", GatewayV4: "10.42.0.1"},
			Nameservers: []string{"1.1.1.1"},
			NTPServer:   "ntp.ubuntu.com",
		}
		if err := editSnapshotConfig(dir, "/new/disk", "/new/kernel", srcVMConfig, target); err != nil {
			t.Fatal(err)
		}
		if got := readTap(t, dir); got != "tap-a1b2c3" {
			t.Errorf("tap = %q, want %q", got, "tap-a1b2c3")
		}
	})

	t.Run("same mode preserves tap", func(t *testing.T) {
		dir := mkSnapshotConfig(t, "tap-vm000001", cmdline)
		target := &api.NetworkInterface{
			Name:        "tap-vm000002",
			DeviceName:  "eth0",
			IP:          &api.IPAddress{IPV4: "10.42.0.42/24", GatewayV4: "10.42.0.1"},
			Nameservers: []string{"1.1.1.1"},
			NTPServer:   "ntp.ubuntu.com",
		}
		if err := editSnapshotConfig(dir, "/new/disk", "/new/kernel", srcVMConfig, target); err != nil {
			t.Fatal(err)
		}
		if got := readTap(t, dir); got != "tap-vm000002" {
			t.Errorf("tap = %q, want %q", got, "tap-vm000002")
		}
	})

	t.Run("nil target skips tap update", func(t *testing.T) {
		dir := mkSnapshotConfig(t, "tap-orig", cmdline)
		if err := editSnapshotConfig(dir, "/new/disk", "/new/kernel", srcVMConfig, nil); err != nil {
			t.Fatal(err)
		}
		if got := readTap(t, dir); got != "tap-orig" {
			t.Errorf("tap = %q, want %q (should be unchanged)", got, "tap-orig")
		}
	})
}
```

### `exelet/services/compute/service_test.go`

```go
package compute

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"exe.dev/exelet/config"
	"exe.dev/exelet/services"
	api "exe.dev/pkg/api/exe/compute/v1"
)

// TestCreateSSHProxy tests that CreateProxy correctly creates a SSH proxy for an instance.
func TestCreateSSHProxy(t *testing.T) {
	testCreateSSHProxy(t, "")
}

func TestCreateSSHProxyExepipe(t *testing.T) {
	testCreateSSHProxy(t, exepipe.UnixAddr)
}

func testCreateSSHProxy(t *testing.T, exepipeAddress string) {
	// Skip test if socat is not installed
	if _, err := exec.LookPath("socat"); err != nil {
		t.Skip("socat not found in PATH, skipping test")
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	cfg := &config.ExeletConfig{
		Name:           "test",
		ListenAddress:  "127.0.0.1:0",
		DataDir:        dataDir,
		ProxyPortMin:   20000, // Use different range to avoid conflicts with dev
		ProxyPortMax:   30000,
		ExepipeAddress: exepipeAddress,
	}

	// Create a service instance
	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Start a mock TCP server to simulate the VM's SSH service
	vmIP := "127.0.0.1"
	listener, err := net.Listen("tcp", vmIP+":0")
	if err != nil {
		t.Fatalf("failed to start mock VM SSH server: %v", err)
	}
	defer listener.Close()

	// Accept and close connections (simple mock)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Create instance directory
	instanceID := "test-instance-123"
	// Allocate a dynamic port for the SSH proxy to avoid hardcoded port conflicts.
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate proxy port: %v", err)
	}
	sshPort := proxyLn.Addr().(*net.TCPAddr).Port
	proxyLn.Close()
	instanceDir := computeSvc.getInstanceDir(instanceID)
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		t.Fatalf("failed to create instance directory: %v", err)
	}

	// Create SSH proxy using CreateProxy
	if err := computeSvc.proxyManager.CreateProxy(t.Context(), instanceID, vmIP, sshPort, instanceDir); err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	// Verify that a SSH proxy was created
	proxyPort, exists := computeSvc.proxyManager.GetPort(t.Context(), instanceID)
	if !exists {
		t.Fatalf("SSH proxy should exist after CreateProxy")
	}

	if proxyPort != sshPort {
		t.Errorf("proxy port mismatch: expected %d, got %d", sshPort, proxyPort)
	}

	// Test that we can actually connect to the proxy
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", sshPort)
	var conn net.Conn
	var connErr error
	for range 10 {
		conn, connErr = net.DialTimeout("tcp", proxyAddr, 100*time.Millisecond)
		if connErr == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if connErr != nil {
		t.Errorf("failed to connect to SSH proxy at %s: %v", proxyAddr, connErr)
	}

	// Test that calling CreateProxy again is idempotent (stops old, creates new)
	if err := computeSvc.proxyManager.CreateProxy(t.Context(), instanceID, vmIP, sshPort, instanceDir); err != nil {
		t.Errorf("CreateProxy should be idempotent: %v", err)
	}

	// Verify proxy still exists after idempotent call
	if _, exists := computeSvc.proxyManager.GetPort(t.Context(), instanceID); !exists {
		t.Errorf("proxy should still exist after idempotent CreateProxy call")
	}

	// Cleanup
	if _, err := computeSvc.proxyManager.StopProxy(t.Context(), instanceID); err != nil {
		t.Errorf("failed to stop proxy: %v", err)
	}
}

// TestRecoverProxiesStopsProxyForStoppedInstance verifies that RecoverProxies
// stops proxies for instances that are in STOPPED state.
func TestRecoverProxiesStopsProxyForStoppedInstance(t *testing.T) {
	testRecoverProxiesStopsProxyForStoppedInstance(t, "")
}

func TestRecoverProxiesStopsProxyForStoppedInstanceExepipe(t *testing.T) {
	testRecoverProxiesStopsProxyForStoppedInstance(t, exepipe.UnixAddr)
}

func testRecoverProxiesStopsProxyForStoppedInstance(t *testing.T, exepipeAddress string) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dataDir := t.TempDir()
	cfg := &config.ExeletConfig{
		Name:           "test",
		ListenAddress:  "127.0.0.1:0",
		DataDir:        dataDir,
		ProxyPortMin:   20000, // Use different range to avoid conflicts with dev
		ProxyPortMax:   30000,
		ExepipeAddress: exepipeAddress,
	}

	// Create a service instance
	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Create a mock STOPPED instance (no network interface)
	instanceID := "test-instance-stopped"
	sshPort := int32(20023) // Use port in test range (20000-30000)
	instance := &api.Instance{
		ID:      instanceID,
		Name:    "test-instance-stopped",
		Image:   "test-image",
		State:   api.VMState_STOPPED,
		SSHPort: sshPort,
		VMConfig: &api.VMConfig{
			ID:     instanceID,
			Name:   "test-instance-stopped",
			CPUs:   1,
			Memory: 1024,
		},
	}

	// Verify that NO SSH proxy exists initially
	_, exists := computeSvc.proxyManager.GetPort(t.Context(), instanceID)
	if exists {
		t.Errorf("SSH proxy should NOT exist for STOPPED instance initially")
	}

	// Call RecoverProxies with a STOPPED instance - it should not create a proxy
	instances := []*api.Instance{instance}
	if err := computeSvc.proxyManager.RecoverProxies(t.Context(), instances); err != nil {
		t.Errorf("RecoverProxies failed: %v", err)
	}

	// Verify that still NO SSH proxy exists
	_, exists = computeSvc.proxyManager.GetPort(t.Context(), instanceID)
	if exists {
		t.Errorf("SSH proxy should NOT be created for STOPPED instance")
	}
}

// TestRegisterRequiresImageLoader verifies that Register fails with a clear error
// if ImageLoader is not set in ServiceContext.
func TestRegisterRequiresImageLoader(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	cfg := &config.ExeletConfig{
		Name:    "test",
		DataDir: t.TempDir(),
	}

	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Try to register with nil ImageLoader
	ctx := &services.ServiceContext{
		// ImageLoader is nil
	}

	err = computeSvc.Register(ctx, nil)
	if err == nil {
		t.Fatal("Register should fail when ImageLoader is nil")
	}

	expectedMsg := "compute service requires ImageLoader"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("error should mention ImageLoader requirement, got: %v", err)
	}
}

// TestCreateSSHProxyExepipeReconnect verifies that CreateProxy recovers
// after the exepipe unix socket connection breaks (e.g. exepipe restarts).
func TestCreateSSHProxyExepipeReconnect(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Start our own exepipe instance that we can restart.
	ep1, err := startExepipe(t.Context())
	if err != nil {
		t.Fatalf("failed to start first exepipe: %v", err)
	}

	dataDir := t.TempDir()
	cfg := &config.ExeletConfig{
		Name:           "test",
		ListenAddress:  "127.0.0.1:0",
		DataDir:        dataDir,
		ProxyPortMin:   20000,
		ProxyPortMax:   30000,
		ExepipeAddress: ep1.UnixAddr,
	}

	svc, err := New(t.Context(), cfg, log)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	computeSvc := svc.(*Service)

	// Start a mock TCP server.
	vmIP := "127.0.0.1"
	listener, err := net.Listen("tcp", vmIP+":0")
	if err != nil {
		t.Fatalf("failed to start mock VM SSH server: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	instanceID := "test-reconnect"
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate proxy port: %v", err)
	}
	sshPort := proxyLn.Addr().(*net.TCPAddr).Port
	proxyLn.Close()
	instanceDir := computeSvc.getInstanceDir(instanceID)
	if err := os.MkdirAll(instanceDir, 0o755); err != nil {
		t.Fatalf("failed to create instance directory: %v", err)
	}

	// First CreateProxy should succeed.
	if err := computeSvc.proxyManager.CreateProxy(t.Context(), instanceID, vmIP, sshPort, instanceDir); err != nil {
		t.Fatalf("first CreateProxy failed: %v", err)
	}

	// Stop the first exepipe to break the unix socket connection.
	ep1.Cmd.Process.Kill()
	ep1.Cmd.Wait()
	<-ep1.LoggerDone

	// Start a new exepipe on the SAME address.
	ep2, err := startExepipeAt(t.Context(), ep1.UnixAddr)
	if err != nil {
		t.Fatalf("failed to start second exepipe: %v", err)
	}
	defer func() {
		ep2.Cmd.Process.Kill()
		ep2.Cmd.Wait()
		<-ep2.LoggerDone
	}()

	// Allocate a new port for the second attempt.
	proxyLn2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to allocate second proxy port: %v", err)
	}
	sshPort2 := proxyLn2.Addr().(*net.TCPAddr).Port
	proxyLn2.Close()

	// This should fail on the stale client, reset, reconnect to the new
	// exepipe, and succeed.
	if err := computeSvc.proxyManager.CreateProxy(t.Context(), instanceID, vmIP, sshPort2, instanceDir); err != nil {
		t.Fatalf("CreateProxy after exepipe restart should succeed, got: %v", err)
	}

	// Verify connectivity.
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", sshPort2)
	var conn net.Conn
	var connErr error
	for range 10 {
		conn, connErr = net.DialTimeout("tcp", proxyAddr, 100*time.Millisecond)
		if connErr == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if connErr != nil {
		t.Errorf("failed to connect to SSH proxy at %s after reconnect: %v", proxyAddr, connErr)
	}
}
```

--- End of Appendix ---
