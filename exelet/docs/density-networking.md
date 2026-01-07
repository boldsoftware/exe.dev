# Networking Density Exploration

This document explores strategies to increase VM density from a networking perspective, with focus on per-VM bandwidth limiting and accounting.

## Current State

### Network Architecture
```
VM eth0 → TAP device → Bridge (br-exe-*) → iptables NAT → External network
```

- **TAP devices**: One per VM, named `tap-<instance-id>`
- **Bridges**: Linux bridges, 500 VMs per bridge max, with fq_codel for fair queuing
- **NAT**: iptables masquerade for outbound traffic
- **IPAM**: Simple IP reservation system (`pkg/ipam`) - not DHCP-based

### IP Address Management (IPAM)

VMs do **not** use DHCP to obtain their IP addresses. Instead:

1. **At VM creation**: The NAT manager reserves an IP from the `10.42.0.0/16` pool
2. **IP assignment**: Stored in a local datastore (`pkg/ipam/ds.go`) keyed by MAC address
3. **Boot-time config**: Network settings passed to VM via kernel `ip=` boot argument

**File:** `exelet/vmm/cloudhypervisor/config.go:134-175`
```go
// Format: ip=<client-ip>:<srv-ip>:<gw-ip>:<netmask>:<host>:<device>:<autoconf>:<dns0>:<dns1>:<ntp>
// Example: ip=10.42.1.5:10.42.0.1:10.42.0.1:255.255.0.0:vm-name:eth0:none:1.1.1.1:8.8.8.8:ntp.ubuntu.com
```

The `autoconf=none` means the guest kernel configures networking statically at boot - no DHCP client runs in the guest.

**File:** `exelet/network/nat/create_linux.go`
```go
ip, err := n.ipam.Reserve(macAddress)  // Reserve IP from pool
```

This design is simpler and faster than DHCP (no round-trip needed at boot).

### Current Limits

| Setting | Value | Location |
|---------|-------|----------|
| VMs per bridge | 500 | `DefaultMaxPortsPerBridge` |
| Connections per VM | 10,000 | `DefaultConnLimit` (iptables connlimit) |
| FDB hash table | 4,096 | `DefaultBridgeHashMax` |
| Upload bandwidth per VM | 100 Mbps | `DefaultBandwidthRate` (via IFB + HTB) |

**File:** `exelet/network/nat/nat.go`
```go
DefaultMaxPortsPerBridge = 500
DefaultBridgeHashMax     = 4096
DefaultConnLimit         = 10000
DefaultBandwidthRate     = "100mbit"
DefaultBandwidthBurst    = "256k"
```

### Per-VM Network Limits

Each VM has the following network constraints applied at creation time:

#### Upload Bandwidth Limiting

Upload bandwidth (traffic FROM the VM) is limited using an IFB (Intermediate Functional Block) device with HTB shaping:

```
TAP ingress → IFB device → HTB rate limit (100mbit) → fq_codel
```

**File:** `exelet/network/nat/configure_linux.go:applyBandwidthLimit()`

The implementation:
1. Creates an IFB device per TAP (`ifb-<tap-suffix>`)
2. Redirects TAP ingress to IFB egress via tc mirred
3. Applies HTB shaping on IFB with rate limit and burst
4. Adds fq_codel for fair queuing within the rate limit

This approach queues excess traffic (allowing TCP to adapt) rather than dropping it.

**Note:** Download bandwidth (traffic TO the VM) is currently unlimited.

#### Connection Limiting

Each VM is limited to 10,000 concurrent connections via iptables connlimit:

```bash
iptables -I FORWARD -s $VM_IP -m connlimit --connlimit-above 10000 --connlimit-mask 32 -j DROP
```

**File:** `exelet/network/nat/configure_linux.go:applyConnLimit()`

#### Bridge Fair Queuing

Each bridge has fq_codel applied for fair queuing between VMs:

```bash
tc qdisc replace dev br-exe-0 root fq_codel
```

**File:** `exelet/network/nat/configure_linux.go:applyBridgeFqCodel()`

### Why Multiple Bridges?

The multi-bridge architecture exists due to Linux bridge FDB (Forwarding Database) limitations:

1. **FDB hash table**: The kernel's default `hash_max=512` causes "exchange full" errors when too many MAC addresses hash to the same bucket
2. **Our mitigation**: We set `hash_max=4096` via `/sys/class/net/<bridge>/bridge/hash_max`
3. **Conservative limit**: 500 VMs/bridge stays well under hash capacity, accounting for:
   - Hash collisions (multiple MACs per bucket)
   - Dynamic MAC learning from guest traffic
   - Safety margin for bursts

**File:** `exelet/network/nat/configure_linux.go:setBridgeHashMax()`
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

**Status: Implemented (upload only)**

Upload bandwidth is limited to 100 Mbps per VM using IFB + HTB. See "Per-VM Network Limits" section above.

**Remaining Questions:**
- Should download bandwidth also be limited?
- Should limits be configurable per VM plan?
- Is 100 Mbps the right default?

**Measurement Commands:**
```bash
# Current TAP device statistics
for tap in /sys/class/net/tap-*/; do
  name=$(basename $tap)
  rx=$(cat $tap/statistics/rx_bytes)
  tx=$(cat $tap/statistics/tx_bytes)
  echo "$name: rx=$(numfmt --to=iec $rx) tx=$(numfmt --to=iec $tx)"
done

# Check tc qdiscs on TAP and IFB devices
tc -s qdisc show | grep -A5 -E "tap-|ifb-"

# Check HTB class stats
for ifb in /sys/class/net/ifb-*/; do
  name=$(basename $ifb)
  tc -s class show dev $name
done
```

**Download Limiting (Not Yet Implemented):**

To limit download bandwidth, apply HTB directly to TAP egress:

```bash
# Example: 100 Mbps download limit
tc qdisc add dev tap-abc123 root handle 1: htb default 10
tc class add dev tap-abc123 parent 1: classid 1:10 htb rate 100mbit burst 256k
tc qdisc add dev tap-abc123 parent 1:10 handle 10: fq_codel
```

---

### 2. Noisy Neighbor Prevention

**Status: Partially Implemented**

Current mitigations:
- ✅ Connection limit of 10,000 per VM
- ✅ Upload bandwidth limit of 100 Mbps
- ✅ fq_codel on bridges for fair queuing
- ❌ No PPS (packets per second) limiting
- ❌ No traffic prioritization

**Questions to Answer:**
- What PPS rates cause host-level issues?
- Should interactive traffic (SSH) get priority?
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
for ip in 10.42.0.{2..255}; do
  count=$(conntrack -L 2>/dev/null | grep -c $ip)
  [ $count -gt 0 ] && echo "$ip: $count connections"
done

# Bridge queue depths
tc -s qdisc show dev br-exe-0

# Drop statistics
ip -s link show br-exe-0 | grep -E "dropped|errors"
```

**PPS Limiting (Not Yet Implemented):**
```bash
# Limit packets per second from each VM using hashlimit
iptables -I FORWARD -s 10.42.0.0/16 -m hashlimit \
    --hashlimit-above 50000/s --hashlimit-mode srcip \
    --hashlimit-name vm_pps -j DROP
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
- Pros: Lower overhead, direct path to physical NIC
- Cons: All VMs share MAC address space, may hit switch limits
- **Not supported on AWS EC2** (requires promiscuous mode)

**Option B: ipvlan (L3 mode)**
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
```

---

### 5. Network Overhead Measurement

**Background:**
Understanding the network stack overhead helps optimize for density.

**Overhead Sources:**
- Bridge forwarding
- iptables rule traversal
- NAT connection tracking
- TAP device overhead
- IFB bandwidth limiting

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

# === Bandwidth Limit Status ===
for ifb in $(ip link show type ifb | grep ifb- | awk -F: '{print $2}'); do
  echo "=== $ifb ==="
  tc -s class show dev $ifb
done

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

## Implementation Status

| Feature | Status | Notes |
|---------|--------|-------|
| Per-VM upload bandwidth limit | ✅ Done | 100 Mbps via IFB + HTB |
| Per-VM download bandwidth limit | ❌ Not done | Could add HTB to TAP egress |
| Per-VM connection limit | ✅ Done | 10,000 via iptables connlimit |
| Bridge fair queuing | ✅ Done | fq_codel on all bridges |
| PPS limiting | ❌ Not done | Could use iptables hashlimit |
| Traffic prioritization | ❌ Not done | Could mark/prioritize SSH |
| Per-VM accounting | ❌ Not done | Stats available but not aggregated |

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

---

## Future Work

1. **Download bandwidth limiting** - Apply HTB to TAP egress
2. **Configurable limits per plan** - Different bandwidth/connection limits per VM tier
3. **PPS limiting** - Protect against packet flood attacks
4. **Network accounting** - Aggregate and export per-VM bandwidth metrics
5. **Test higher bridge density** - Try 1000 VMs per bridge with monitoring
