# Per-VM Network Limits

This document describes the per-VM network limiting implementation and troubleshooting commands.

## Overview

Each VM has two types of network limits:

| Limit | Default | Description |
|-------|---------|-------------|
| Upload bandwidth | 100 Mbps | Max sustained upload rate per VM |
| Burst | 256 KB | HTB burst allowance |
| Concurrent connections | 10,000 | Max simultaneous connections (iptables connlimit) |

Defaults are defined in `exelet/network/nat/nat.go`.

## Architecture

Upload bandwidth limiting uses IFB (Intermediate Functional Block) devices:

```
VM eth0 → TAP device → [ingress qdisc] → redirect → IFB device → [HTB + fq_codel] → bridge
                       (captures upload)            (rate limiting happens here)
```

**Why IFB?** Linux tc can only shape egress traffic. To limit uploads (TAP ingress from the host's perspective), we redirect ingress to an IFB device where it becomes egress and can be shaped.

## Why IFB + HTB Instead of Ingress Policing

There are two approaches to limit ingress bandwidth:

### Option 1: Ingress Policing (we don't use this)

```bash
tc qdisc add dev tap-xxx handle ffff: ingress
tc filter add dev tap-xxx parent ffff: protocol ip u32 match u32 0 0 \
    police rate 100mbit burst 32k drop
```

Policing **drops packets immediately** when the rate is exceeded. This causes problems:

1. **TCP sees packet loss** → triggers retransmission timeout or fast retransmit
2. **Sender retransmits** → wastes bandwidth sending the same data again
3. **Congestion window shrinks** → TCP backs off aggressively
4. **Sawtooth pattern** → throughput oscillates as TCP probes for bandwidth, hits drops, backs off, repeats

The result is that actual throughput is often well below the configured limit, and the VM experiences unpredictable latency spikes during retransmissions.

### Option 2: IFB + HTB Shaping (what we use)

```bash
# Redirect to IFB
tc qdisc add dev tap-xxx handle ffff: ingress
tc filter add dev tap-xxx parent ffff: protocol all u32 match u32 0 0 \
    action mirred egress redirect dev ifb-xxx

# Shape on IFB with HTB + fq_codel
tc qdisc add dev ifb-xxx root handle 1: htb default 10
tc class add dev ifb-xxx parent 1: classid 1:10 htb rate 100mbit burst 256k
tc qdisc add dev ifb-xxx parent 1:10 handle 10: fq_codel
```

Shaping **queues excess packets** instead of dropping them:

1. **Packets are buffered** in the HTB queue when rate is exceeded
2. **Released at the configured rate** → no packet loss
3. **TCP sees increased RTT** → naturally slows down via congestion control
4. **Smooth throughput** → steady rate at or near the limit
5. **fq_codel prevents bufferbloat** → drops packets intelligently when queue grows too large, using ECN or strategic drops to signal congestion without causing retransmit storms

### The Tradeoff

| Aspect | Ingress Policing | IFB + HTB Shaping |
|--------|------------------|-------------------|
| Packet loss | Immediate drops | Queued, minimal drops |
| TCP behavior | Retransmits, backoff | Smooth adaptation |
| Actual throughput | Often below limit | Close to limit |
| Latency under load | Spiky (retransmit timeouts) | Increased but stable |
| Memory usage | None | Queue buffers |
| Complexity | Simple | More moving parts |

We chose IFB + HTB because it provides **predictable throughput at the configured limit** without triggering unnecessary TCP retransmissions. The fq_codel leaf qdisc ensures that if queuing does cause latency to grow too high, it will signal congestion in a way that TCP handles gracefully.

### Device Naming

- TAP devices: `tap-<instance-id>` (e.g., `tap-abc123`)
- IFB devices: `ifb-<instance-id>` (e.g., `ifb-abc123`)

The IFB name replaces the `tap-` prefix with `ifb-`.

### Traffic Control Setup (per VM)

1. Create IFB device for the TAP
2. Add ingress qdisc to TAP with handle `ffff:`
3. Add mirred filter to redirect all TAP ingress to IFB egress
4. Apply HTB qdisc to IFB with class `1:10` for rate limiting
5. Add fq_codel under the HTB class for fair queuing

Implementation: `exelet/network/nat/configure_linux.go:760-864`

## Troubleshooting Commands

### List all qdiscs (quick overview)

```bash
tc qdisc show
```

### Check a specific VM's bandwidth limit

```bash
# Replace <instance-id> with actual ID (e.g., abc123)
TAP=tap-<instance-id>
IFB=ifb-<instance-id>

# Show TAP ingress qdisc and filter (redirects to IFB)
tc qdisc show dev $TAP
tc filter show dev $TAP parent ffff:

# Show IFB HTB config (where rate limiting happens)
tc qdisc show dev $IFB
tc class show dev $IFB
```

### Show all IFB rate limits

```bash
# List all IFB devices with their HTB class settings
for ifb in /sys/class/net/ifb-*/; do
  name=$(basename $ifb)
  echo "=== $name ==="
  tc class show dev $name 2>/dev/null
done
```

### Show tc statistics (packets/bytes/drops)

```bash
# Detailed stats for a specific IFB
tc -s qdisc show dev ifb-<instance-id>
tc -s class show dev ifb-<instance-id>

# All IFBs with stats
tc -s qdisc show | grep -A5 "ifb-"
```

### Check connection limits (iptables)

```bash
# Show all connlimit rules
iptables -L FORWARD -v -n | grep connlimit

# Check specific VM IP
iptables -L FORWARD -v -n | grep <vm-ip>
```

### Real-time bandwidth per TAP

```bash
# Snapshot of bytes transferred
for tap in /sys/class/net/tap-*/; do
  name=$(basename $tap)
  rx=$(cat $tap/statistics/rx_bytes)
  tx=$(cat $tap/statistics/tx_bytes)
  echo "$name: rx=$(numfmt --to=iec $rx) tx=$(numfmt --to=iec $tx)"
done

# Watch in real-time (1 second intervals)
watch -n 1 'for tap in /sys/class/net/tap-*/statistics; do
  name=$(dirname $tap | xargs basename)
  echo "$name rx=$(cat $tap/rx_bytes) tx=$(cat $tap/tx_bytes)"
done'
```

### Check for dropped packets

```bash
# Drops on IFB (indicates bandwidth limit being hit)
tc -s qdisc show dev ifb-<instance-id> | grep -E "dropped|overlimits"

# Drops on TAP
cat /sys/class/net/tap-<instance-id>/statistics/rx_dropped
cat /sys/class/net/tap-<instance-id>/statistics/tx_dropped
```

### Bridge fair queuing

Bridges also have fq_codel applied for fair sharing between VMs:

```bash
# Check bridge qdisc
tc qdisc show dev br-exe-0

# Stats
tc -s qdisc show dev br-exe-0
```

## Common Issues

### VM upload is slow

1. Check if IFB exists and has correct rate:
   ```bash
   tc class show dev ifb-<instance-id>
   ```
   Look for `rate 100Mbit` (or configured rate).

2. Check for drops (indicates hitting the limit):
   ```bash
   tc -s qdisc show dev ifb-<instance-id> | grep dropped
   ```

### IFB device missing

If the IFB doesn't exist, bandwidth limiting wasn't applied. Check exelet logs for errors during VM creation.

```bash
ip link show type ifb
```

### Connection limit reached

VMs are limited to 10,000 concurrent connections. Check current count:

```bash
conntrack -L 2>/dev/null | grep <vm-ip> | wc -l
```
