# Networking Density Exploration

This document explores strategies to increase VM density from a networking perspective, with focus on per-VM bandwidth limiting and accounting.

## Current State

### Network Architecture
```
VM eth0 → TAP device → Bridge (br-exe-*) → iptables NAT → External network
```

- **TAP devices**: One per VM, named `tap-<instance-id>`
- **Bridges**: Linux bridges, 500 VMs per bridge max
- **NAT**: iptables masquerade for outbound traffic
- **IPAM**: Simple IP reservation system (not DHCP-based)

### IP Address Management (IPAM)

VMs do **not** use DHCP to obtain their IP addresses. Instead:

1. **At VM creation**: The NAT manager reserves an IP from the `10.42.0.0/16` pool
2. **IP assignment**: Stored in a local datastore (`pkg/dhcpd/ds.go`) keyed by MAC address
3. **Boot-time config**: Network settings passed to VM via kernel `ip=` boot argument

**File:** `exelet/vmm/cloudhypervisor/config.go:134-175`
```go
// Format: ip=<client-ip>:<srv-ip>:<gw-ip>:<netmask>:<host>:<device>:<autoconf>:<dns0>:<dns1>:<ntp>
// Example: ip=10.42.1.5:10.42.0.1:10.42.0.1:255.255.0.0:vm-name:eth0:none:1.1.1.1:8.8.8.8:ntp.ubuntu.com
```

The `autoconf=none` means the guest kernel configures networking statically at boot - no DHCP client runs in the guest.

**File:** `exelet/network/nat/create_linux.go:49-52`
```go
ip, err := n.dhcpServer.Reserve(macAddress)  // Reserve IP from pool
```

This design is simpler and faster than DHCP (no round-trip needed at boot).

### Current Limits

| Setting | Value | Location |
|---------|-------|----------|
| VMs per bridge | 500 | `DefaultMaxPortsPerBridge` |
| Connections per VM | 10,000 | `DefaultConnLimit` (iptables connlimit) |
| FDB hash table | 4,096 | `DefaultBridgeHashMax` |
| Bandwidth limit | None | Not implemented |

**File:** `exelet/network/nat/nat.go:20-23`
```go
DefaultMaxPortsPerBridge = 500
DefaultBridgeHashMax     = 4096
DefaultConnLimit         = 10000
```

### Why Multiple Bridges?

The multi-bridge architecture exists due to Linux bridge FDB (Forwarding Database) limitations:

1. **FDB hash table**: The kernel's default `hash_max=512` causes "exchange full" errors when too many MAC addresses hash to the same bucket
2. **Our mitigation**: We set `hash_max=4096` via `/sys/class/net/<bridge>/bridge/hash_max`
3. **Conservative limit**: 500 VMs/bridge stays well under hash capacity, accounting for:
   - Hash collisions (multiple MACs per bucket)
   - Dynamic MAC learning from guest traffic
   - Safety margin for bursts

**File:** `exelet/network/nat/configure_linux.go:674-680`
```go
// setBridgeHashMax sets the FDB hash_max for a bridge to allow more MAC addresses.
// The default is 512 which can cause "exchange full" errors at scale.
func setBridgeHashMax(bridgeName string, hashMax int) error {
    path := fmt.Sprintf("/sys/class/net/%s/bridge/hash_max", bridgeName)
    ...
}
```

**Note:** The 500 limit is not a hard kernel limit - it's a conservative choice. Testing with 1000+ VMs per bridge may be viable with `hash_max=4096`, but requires monitoring for "exchange full" errors in `dmesg`.

### Network IP Range
- Default: `10.42.0.0/16` (~65,000 IPs)
- Gateway: `10.42.0.1`
- IPAM assigns sequentially from this range

---

## Investigation Areas

### 1. Per-VM Bandwidth Limiting

**Background:**
Without bandwidth limits, a single VM can saturate the host's network link, affecting all other VMs. This is critical for:
- Preventing noisy neighbor issues
- Ensuring fair bandwidth distribution
- Controlling egress costs

**Current State:**
- No bandwidth limiting implemented
- All VMs share full network bandwidth
- Connection limit (10,000) is the only constraint

**Questions to Answer:**
- What bandwidth should each VM be entitled to?
- Should limits be symmetric (same ingress/egress)?
- How do we handle burst traffic vs sustained?

**Measurement Commands:**
```bash
# Current TAP device statistics
for tap in /sys/class/net/tap-*/; do
  name=$(basename $tap)
  rx=$(cat $tap/statistics/rx_bytes)
  tx=$(cat $tap/statistics/tx_bytes)
  echo "$name: rx=$(numfmt --to=iec $rx) tx=$(numfmt --to=iec $tx)"
done

# Real-time bandwidth per TAP
watch -n 1 'for tap in /sys/class/net/tap-*/statistics; do
  echo "$(dirname $tap | xargs basename): $(cat $tap/rx_bytes) $(cat $tap/tx_bytes)"
done'

# Check existing tc qdiscs
tc -s qdisc show

# Check iptables byte counters
iptables -L -v -n -x | head -50
```

**Implementation with tc (Traffic Control):**

**Option A: Simple rate limit (TBF - Token Bucket Filter)**
```bash
# Limit TAP to 100 Mbps egress
tc qdisc add dev tap-abc123 root tbf rate 100mbit burst 32kbit latency 400ms

# Verify
tc -s qdisc show dev tap-abc123
```
- Pros: Simple, low overhead
- Cons: No burst allowance, strict rate

**Option B: HTB (Hierarchical Token Bucket) - Recommended**
```bash
# Create HTB qdisc
tc qdisc add dev tap-abc123 root handle 1: htb default 10

# Create class with rate and burst ceiling
tc class add dev tap-abc123 parent 1: classid 1:10 htb rate 100mbit ceil 200mbit burst 15k

# Add fair queuing for traffic within the class
tc qdisc add dev tap-abc123 parent 1:10 handle 10: fq_codel
```
- Pros: Allows bursting up to ceiling, fair queuing
- Cons: More complex setup

**Option C: CAKE qdisc (modern, feature-rich)**
```bash
# CAKE provides bandwidth shaping + fair queuing + latency management
tc qdisc add dev tap-abc123 root cake bandwidth 100mbit

# With RTT compensation for low latency
tc qdisc add dev tap-abc123 root cake bandwidth 100mbit rtt 50ms
```
- Pros: Modern, handles bufferbloat, per-flow fairness
- Cons: May not be available on older kernels

**Ingress Limiting:**
Ingress (incoming traffic) is harder to limit. Options:

```bash
# Option 1: Police at ingress (drop excess)
tc qdisc add dev tap-abc123 handle ffff: ingress
tc filter add dev tap-abc123 parent ffff: protocol ip prio 1 u32 match ip src 0.0.0.0/0 \
    police rate 100mbit burst 32k drop flowid :1

# Option 2: Use IFB (Intermediate Functional Block) device
ip link add ifb0 type ifb
ip link set ifb0 up
tc qdisc add dev tap-abc123 handle ffff: ingress
tc filter add dev tap-abc123 parent ffff: protocol ip u32 match u32 0 0 \
    action mirred egress redirect dev ifb0
tc qdisc add dev ifb0 root cake bandwidth 100mbit
```

**Recommended Implementation:**
1. Apply HTB + fq_codel to each TAP device at creation time
2. Set rate based on VM plan (e.g., 100 Mbps base, 500 Mbps ceiling)
3. Use ingress policing for incoming traffic

---

### 2. Noisy Neighbor Prevention

**Background:**
Beyond bandwidth, network contention can come from:
- Packet-per-second (PPS) floods
- Connection storms
- Small packet DoS

**Current State:**
- Connection limit of 10,000 per VM
- No PPS limiting
- No queue prioritization

**Questions to Answer:**
- What PPS rates cause host-level issues?
- Should interactive traffic get priority?
- How do we detect and mitigate network abuse?

**Measurement Commands:**
```bash
# Packet rate per TAP
for tap in /sys/class/net/tap-*/statistics; do
  name=$(dirname $tap | xargs basename)
  rx_pkts=$(cat $tap/rx_packets)
  tx_pkts=$(cat $tap/tx_packets)
  echo "$name: rx_pkts=$rx_pkts tx_pkts=$tx_pkts"
done

# Connections per VM (via conntrack)
for ip in 10.42.0.{1..255}; do
  count=$(conntrack -L 2>/dev/null | grep -c $ip)
  [ $count -gt 0 ] && echo "$ip: $count connections"
done

# Bridge queue depths
tc -s qdisc show dev br-exe-0

# Drop statistics
ip -s link show br-exe-0 | grep -E "dropped|errors"
```

**Fair Queuing on Bridge:**
```bash
# Replace default pfifo_fast with fq_codel on bridge
tc qdisc replace dev br-exe-0 root fq_codel

# Or use CAKE for more comprehensive fairness
tc qdisc replace dev br-exe-0 root cake bandwidth 10gbit
```

**PPS Limiting (iptables):**
```bash
# Limit packets per second from each VM
iptables -I FORWARD -s 10.42.0.0/16 -m limit --limit 50000/s --limit-burst 100000 -j ACCEPT
iptables -I FORWARD -s 10.42.0.0/16 -j DROP

# Or per-VM (requires per-VM rules)
iptables -I FORWARD -s $VM_IP -m hashlimit \
    --hashlimit-above 50000/s --hashlimit-mode srcip \
    --hashlimit-name vm_pps -j DROP
```

**Priority for Interactive Traffic:**
```bash
# Mark SSH traffic as high priority
iptables -t mangle -A FORWARD -p tcp --dport 22 -j MARK --set-mark 1

# Apply DSCP marking
iptables -t mangle -A FORWARD -p tcp --dport 22 -j DSCP --set-dscp-class EF

# Configure tc to prioritize marked packets
tc filter add dev br-exe-0 parent 1: protocol ip prio 1 handle 1 fw flowid 1:1
```

---

### 3. Bridge Scaling

**Background:**
Current configuration uses multiple bridges (br-exe-0, br-exe-1, etc.) with 500 VMs per bridge to avoid scalability issues.

**Current Limits:**
- 500 VMs per bridge (`DefaultMaxPortsPerBridge`)
- FDB hash table: 4,096 entries (`DefaultBridgeHashMax`)

**Questions to Answer:**
- Can we increase VMs per bridge?
- What are the actual scalability limits?
- Are there better alternatives to bridges?

**Measurement Commands:**
```bash
# Current bridge usage
for br in /sys/class/net/br-exe-*/; do
  name=$(basename $br)
  ports=$(ls $br/brif/ 2>/dev/null | wc -l)
  echo "$name: $ports ports"
done

# FDB table size
bridge fdb show | wc -l

# Bridge queue statistics
tc -s qdisc show dev br-exe-0

# Kernel bridge limits
cat /sys/class/net/br-exe-0/bridge/hash_max
cat /sys/class/net/br-exe-0/bridge/multicast_hash_max
```

**Increasing Bridge Capacity:**
```bash
# Increase FDB hash table (already set to 4096)
echo 8192 > /sys/class/net/br-exe-0/bridge/hash_max

# Increase multicast hash
echo 4096 > /sys/class/net/br-exe-0/bridge/multicast_hash_max

# Note: 500 VMs/bridge is conservative; 1000+ may be fine
```

**Alternatives to Linux Bridge:**

**Option A: macvlan (bypass bridge)**
```bash
# Create macvlan interface directly on physical NIC
ip link add link eth0 name macvlan0 type macvlan mode bridge
ip link set macvlan0 up
```
- Pros: Lower overhead, direct path to physical NIC
- Cons: All VMs share MAC address space, may hit switch limits
- **Not supported on AWS EC2** (requires promiscuous mode)

**Option B: ipvlan (L3 mode)**
```bash
ip link add link eth0 name ipvlan0 type ipvlan mode l3
```
- Pros: Very low overhead, L3 routing, works on AWS EC2
- Cons: Different networking model, may break some protocols (no ARP/broadcast)

**Option C: eBPF-based (experimental)**
- Use XDP for packet steering
- Lower latency than bridge + iptables
- Higher complexity
- Works on AWS EC2 (ENA driver v2.2+) with some XDP_TX caveats

**Recommendation:**
- Test increasing `maxPortsPerBridge` to 1000
- Monitor for "exchange full" errors or performance degradation
- Consider ipvlan for highest density scenarios on AWS EC2
- Consider macvlan only on bare metal deployments

---

### 4. Per-VM Accounting

**Background:**
Tracking per-VM bandwidth usage is essential for:
- Billing/metering
- Capacity planning
- Identifying heavy users

**Current State:**
- TAP device statistics available via sysfs
- No aggregation or historical tracking
- Resource manager doesn't track network usage long-term

**Questions to Answer:**
- What granularity is needed (per-second? per-minute? per-hour?)
- Should we track by VM or by user/project?
- Integration with existing metrics (Prometheus)?

**Measurement Commands:**
```bash
# Per-TAP cumulative statistics
for tap in /sys/class/net/tap-*/statistics; do
  name=$(dirname $tap | xargs basename)
  echo "=== $name ==="
  cat $tap/rx_bytes $tap/tx_bytes $tap/rx_packets $tap/tx_packets
done

# iptables byte/packet counters
iptables -L FORWARD -v -n -x | grep 10.42

# Per-VM with instance ID mapping
# (requires mapping TAP name to instance ID)

# Prometheus metrics (if exported)
curl -s localhost:9090/metrics | grep -E "exelet.*net"
```

**Implementation Options:**

**Option A: Extend Resource Manager**
Add network stats collection to existing polling loop:
```go
// In resourcemanager/usage.go
func (m *ResourceManager) collectNetworkUsage(tapName string) (rx, tx uint64, err error) {
    // Read from /sys/class/net/<tap>/statistics/
}
```

**Option B: Prometheus node_exporter**
```bash
# Enable netdev collector (may already be enabled)
# Metrics: node_network_receive_bytes_total, node_network_transmit_bytes_total
```

**Option C: eBPF for detailed flow tracking**
```bash
# Use bpftrace or custom eBPF program for per-flow accounting
bpftrace -e 'tracepoint:net:net_dev_xmit /args->name == "tap-abc123"/ { @bytes = sum(args->len); }'
```

**Recommended Approach:**
1. Add network stats to resource manager (already partially done)
2. Export to Prometheus
3. Aggregate by user/project in external system

---

### 5. Network Overhead Measurement

**Background:**
Understanding the network stack overhead helps optimize for density.

**Overhead Sources:**
- Bridge forwarding
- iptables rule traversal
- NAT connection tracking
- TAP device overhead

**Measurement Commands:**
```bash
# iptables rule count (affects traversal time)
iptables -L -n | wc -l
iptables -t nat -L -n | wc -l

# Connection tracking table size
cat /proc/sys/net/netfilter/nf_conntrack_count
cat /proc/sys/net/netfilter/nf_conntrack_max

# softirq CPU usage (network processing)
cat /proc/softirqs | grep -E "NET_RX|NET_TX"

# Network stack latency
# Use ping from VM to external host, compare to ping from host

# Per-CPU network distribution
cat /proc/net/softnet_stat
```

**Optimization:**
```bash
# Increase conntrack table if near max
echo 1048576 > /proc/sys/net/netfilter/nf_conntrack_max

# Enable conntrack hash table tuning
echo 262144 > /proc/sys/net/netfilter/nf_conntrack_buckets

# Enable RPS (Receive Packet Steering) for multi-core distribution
echo "ff" > /sys/class/net/br-exe-0/queues/rx-0/rps_cpus
```

---

### 6. Alternative Network Backends

**Background:**
For highest density scenarios, alternative networking may provide better performance than bridge + NAT.

**Options:**

**SR-IOV (Single Root I/O Virtualization)**
- Hardware-based network virtualization
- Direct NIC assignment to VMs
- Near-native performance
- Requires SR-IOV capable NIC
- Limited by number of VFs (virtual functions)
- **AWS EC2 note**: AWS uses SR-IOV internally via ENA (Elastic Network Adapter), but you cannot create your own VFs - AWS manages this. The `sriov_numvfs` sysfs interface is not available. For bare-metal EC2 instances (`.metal`), you get direct hardware access but still through ENA.

**macvlan**
- Software-based, lower overhead than bridge
- Each container/VM gets its own MAC address
- **Not supported on AWS EC2** - requires promiscuous mode which ENA driver doesn't support, and AWS VPC blocks traffic not addressed to the instance's MAC
- Works on bare metal and some other cloud providers

**ipvlan (L3 mode)**
- Similar to macvlan but shares host's MAC address
- Multiple IPs on single MAC - works on AWS
- Lower overhead than bridge
- Requires L3 routing (no ARP/broadcast)

**eBPF/XDP**
- Programmable packet processing
- Can replace iptables rules
- Lower latency than traditional stack
- Higher complexity
- **AWS EC2 note**: Supported on ENA driver v2.2+ (check with `modinfo ena`). Native XDP works but XDP_TX has performance limitations due to small TX ring size (1024). Generic/SKB mode may actually outperform native mode for TX-heavy workloads on smaller instances.

**Questions to Answer:**
- Do current NICs support SR-IOV?
- Is direct IP assignment feasible?
- What's the complexity vs performance trade-off?

**Measurement Commands:**
```bash
# Check SR-IOV support (bare metal only - not available on AWS EC2 VMs)
lspci | grep -i ethernet
ls /sys/class/net/*/device/sriov_numvfs 2>/dev/null

# Enable SR-IOV VFs (bare metal only - does NOT work on AWS EC2)
echo 32 > /sys/class/net/eth0/device/sriov_numvfs

# List VFs
ip link show eth0

# Check ENA driver version (AWS EC2)
modinfo ena | grep version

# Check XDP support on ENA
ethtool -i eth0 | grep driver
```

---

## Measurement Commands Summary

```bash
# === Per-VM Stats ===
for tap in /sys/class/net/tap-*/statistics; do
  echo "$(dirname $tap | xargs basename)"
  cat $tap/rx_bytes $tap/tx_bytes
done

# === tc/qdisc Status ===
tc -s qdisc show
tc -s class show dev br-exe-0

# === iptables Counters ===
iptables -L FORWARD -v -n -x

# === Bridge Status ===
bridge link show
bridge fdb show | wc -l

# === Connection Tracking ===
conntrack -C  # count
conntrack -L | head -20

# === System Pressure ===
cat /proc/softirqs | grep NET
cat /proc/net/softnet_stat
```

---

## Expected Impact

| Strategy | Density Impact | Performance Impact | Complexity | Risk | AWS EC2 |
|----------|---------------|-------------------|------------|------|---------|
| Per-VM bandwidth limits (tc) | Prevents abuse | Neutral (bounded) | Medium | Low | Yes |
| fq_codel on bridge | Fair sharing | Slight latency benefit | Low | Low | Yes |
| Increase VMs per bridge | +2x per bridge | Neutral if monitored | Low | Low | Yes |
| Per-VM accounting | None | Minimal overhead | Medium | Low | Yes |
| ipvlan (no bridge) | Better performance | Lower latency | Medium | Medium | Yes |
| macvlan (no bridge) | Better performance | Lower latency | Medium | Medium | **No** |
| SR-IOV (custom VFs) | Best performance | Near-native | High | Medium | **No** (managed by AWS) |
| eBPF/XDP | Lower latency | Variable | High | Medium | Yes (with caveats) |

**Recommended Starting Point:**
1. **Implement per-VM bandwidth limiting** with HTB + fq_codel
2. **Add fq_codel to bridges** for fairness
3. **Extend resource manager** for network accounting
4. **Test higher VMs per bridge** (1000 instead of 500)
5. **Evaluate ipvlan** for highest density scenarios (works on AWS EC2)

---

## Quick Wins

These changes can be implemented quickly with high confidence:

### 1. Add fq_codel to Bridges
**Impact:** Fair bandwidth sharing, reduced bufferbloat
**Risk:** Low
**Effort:** One tc command per bridge

Replace the default qdisc with fq_codel for fair queuing:

```bash
# Apply to all bridges
for br in $(ip link show type bridge | grep br-exe | awk -F: '{print $2}'); do
  tc qdisc replace dev $br root fq_codel
done

# Add to bridge creation code in exelet/network/nat/
```

### 2. Per-VM Bandwidth Limits
**Impact:** Prevents noisy neighbors, controls costs
**Risk:** Low
**Effort:** Add to TAP creation

Apply bandwidth limits when creating TAP devices:

```bash
# Example: 100 Mbps base rate, 200 Mbps burst ceiling
tap_name="tap-abc123"
tc qdisc add dev $tap_name root handle 1: htb default 10
tc class add dev $tap_name parent 1: classid 1:10 htb rate 100mbit ceil 200mbit burst 15k
tc qdisc add dev $tap_name parent 1:10 handle 10: fq_codel
```

Add to `exelet/network/nat/create_linux.go` after TAP creation.

### 3. Increase VMs per Bridge
**Impact:** Simpler network topology
**Risk:** Low (with monitoring)
**Effort:** Constant change

The 500 VM limit is conservative. Test increasing to 1000:

```go
// In exelet/network/nat/nat.go, change:
DefaultMaxPortsPerBridge = 1000  // was 500
```

Monitor for "exchange full" errors in dmesg after change.

### 4. Network Accounting in Resource Manager
**Impact:** Visibility into per-VM bandwidth
**Risk:** Low
**Effort:** Extend existing code

Network stats are already collected but not exposed. Add to Prometheus metrics:

```go
// Already in usage.go - ensure it's exported to Prometheus
netRxBytes, netTxBytes := m.collectNetworkUsage(tapName)
```

Check if metrics are available:
```bash
curl -s localhost:9090/metrics | grep -E "exelet.*net"
```
