# Memory Density Exploration

This document explores strategies to increase VM density from a memory perspective on exelet hosts.

## Current State

### Host Configuration
- Typical host: 96 cores, 384GB RAM
- VM sizing: 8GB RAM per VM
- Theoretical max (no overcommit): 48 VMs per host (384GB / 8GB)
- **Memory is the primary density bottleneck**

### Cloud Hypervisor Configuration
- Memory allocated as shared memory (`Shared: true`)
- Optional hugepage support via config flag
- Memory aligned to page boundaries (4KB default, hugepage size if enabled)

**File:** `exelet/vmm/cloudhypervisor/config.go:82-86`
```go
Memory: &client.MemoryConfig{
    Size:      int64(vmMemory),
    Shared:    &sharedMemory,
    Hugepages: &v.enableHugepages,
},
```

### Resource Manager Memory Controls (Implemented)
- `memory.swap.max = "max"` - All VMs can swap
- `memory.high` - Controls reclaim priority
  - Normal VMs: `max` (no limit)
  - Idle VMs: 80% of allocated (triggers earlier reclaim)

**File:** `exelet/services/resourcemanager/priority.go:26-27`
```go
memoryHighRatio = 0.8  // Idle VMs get memory.high = 80% of allocated
```

---

## Investigation Areas

### 1. Memory Overcommit

**Background:**
Memory overcommit allows allocating more virtual memory than physical RAM. Linux supports this via `vm.overcommit_memory`:
- `0` (heuristic): Kernel uses heuristics to allow/deny allocations
- `1` (always): Always allow allocations (dangerous without swap)
- `2` (never): Only allow `swap + RAM * overcommit_ratio`

**Current State:**
- VMs get dedicated memory (no overcommit)
- Unknown current `vm.overcommit_memory` setting

**Questions to Answer:**
- What is the actual memory usage per VM vs allocated?
- How much memory is typically unused (reclaimable)?
- What's the risk profile with swap backing?

**Measurement Commands:**
```bash
# Current overcommit settings
cat /proc/sys/vm/overcommit_memory
cat /proc/sys/vm/overcommit_ratio

# Committed vs available memory
cat /proc/meminfo | grep -E "Committed_AS|CommitLimit|MemTotal|MemAvailable"

# Per-VM memory usage (cgroup)
for cg in /sys/fs/cgroup/exelet.slice/vm-*/memory.current; do
  echo "$(dirname $cg | xargs basename): $(cat $cg | numfmt --to=iec)"
done

# Per-VM memory vs allocated
for cg in /sys/fs/cgroup/exelet.slice/vm-*/; do
  current=$(cat "$cg/memory.current" 2>/dev/null)
  high=$(cat "$cg/memory.high" 2>/dev/null)
  echo "$(basename $cg): current=$(echo $current | numfmt --to=iec 2>/dev/null) high=$high"
done

# Cloud Hypervisor process RSS
ps -eo pid,rss,comm | grep cloud-hyper | awk '{sum += $2} END {print "Total RSS:", sum/1024, "MB"}'
```

**Potential Solutions:**

**Option A: Conservative overcommit (1.5x)**
- Allow 576GB allocated on 384GB host (72 VMs)
- Requires adequate swap (1-2x RAM)
- Set: `vm.overcommit_memory=2`, `vm.overcommit_ratio=150`

**Option B: Aggressive overcommit with monitoring (2-3x)**
- Allow 768GB-1152GB allocated
- Requires robust OOM handling
- Need memory pressure monitoring and proactive migration

**Risk Mitigation:**
- Monitor `/proc/pressure/memory` for early warning
- Implement VM migration when memory pressure increases
- Configure OOM killer priorities per VM

---

### 2. Kernel Same-page Merging (KSM)

**Background:**
KSM scans memory pages and merges identical pages across processes. This is highly effective for VMs running similar workloads (same OS, same base image).

**How it Works:**
1. KSM daemon scans pages marked as `MADV_MERGEABLE`
2. Identical pages are merged (copy-on-write)
3. When a merged page is written, it's copied out

**Expected Savings:**
- Similar VMs (same base image): 30-50% memory reduction
- Diverse workloads: 10-20% reduction

**Questions to Answer:**
- How similar are VM memory contents?
- What's the CPU overhead of KSM scanning?
- Is KSM compatible with hugepages? (No - mutually exclusive)

**Measurement Commands:**
```bash
# Check KSM status
cat /sys/kernel/mm/ksm/run           # 0=off, 1=on
cat /sys/kernel/mm/ksm/pages_sharing # Number of pages being shared
cat /sys/kernel/mm/ksm/pages_shared  # Number of unique pages being shared
cat /sys/kernel/mm/ksm/pages_unshared # Pages not merged (unique)
cat /sys/kernel/mm/ksm/full_scans    # Number of full scans completed

# KSM configuration
cat /sys/kernel/mm/ksm/sleep_millisecs   # Sleep between scans
cat /sys/kernel/mm/ksm/pages_to_scan     # Pages scanned per cycle

# Calculate memory saved
shared=$(cat /sys/kernel/mm/ksm/pages_sharing)
page_size=4096
echo "Memory saved: $((shared * page_size / 1024 / 1024)) MB"

# CPU overhead from KSM
top -p $(pgrep -d',' ksmd) -b -n 1
```

**Potential Solutions:**

**Enable KSM:**
```bash
# Enable KSM
echo 1 > /sys/kernel/mm/ksm/run

# Tune scanning (trade-off: more aggressive = more CPU)
echo 1000 > /sys/kernel/mm/ksm/pages_to_scan   # Pages per cycle
echo 20 > /sys/kernel/mm/ksm/sleep_millisecs   # Sleep between cycles
```

**Systemd persistence:**
```ini
# /etc/sysctl.d/90-ksm.conf
# Note: KSM is enabled via /sys, not sysctl
```

**Trade-offs:**
- CPU overhead: 1-5% depending on scan rate
- Memory latency: Slightly increased for merged pages
- Incompatible with hugepages (must disable hugepages to use KSM)

---

### 3. Balloon Drivers

**Background:**
Memory ballooning allows the hypervisor to reclaim unused guest memory dynamically. The guest "inflates" a balloon to return memory, and "deflates" when it needs memory back.

**How it Works:**
1. Hypervisor sends balloon inflation request
2. Guest driver allocates pages (balloon inflates)
3. These pages are returned to host
4. When guest needs memory, balloon deflates

**Cloud Hypervisor Support:**
- Supports virtio-balloon device
- Not currently configured in exelet

**Questions to Answer:**
- Does Cloud Hypervisor balloon work with our guest kernel?
- What's the latency to inflate/deflate?
- How do we determine target balloon size?

**Measurement Commands:**
```bash
# Check if balloon device exists in guest
lspci | grep -i balloon
ls /sys/devices/pci*/*/virtio*/balloon/

# Guest balloon size (if available)
cat /sys/devices/*/virtio*/balloon/virt_page_info 2>/dev/null

# Cloud Hypervisor API - resize memory
curl --unix-socket /path/to/ch.sock http://localhost/api/v1/vm.info
```

**Potential Implementation:**
```go
// Add balloon config to Cloud Hypervisor VM config
Balloon: &client.BalloonConfig{
    Size: int64(initialBalloonSize),
    DeflateOnOom: true,
},
```

**Trade-offs:**
- Complexity: Need to manage balloon sizing per VM
- Latency: Balloon operations take time
- Guest support: Requires virtio-balloon in guest kernel

---

### 4. Memory Pressure Monitoring

**Background:**
Linux Pressure Stall Information (PSI) provides metrics on resource contention. Memory PSI indicates when processes are waiting for memory.

**PSI Metrics:**
- `some`: % of time at least one task was stalled
- `full`: % of time all tasks were stalled

**Current State:**
- No proactive memory pressure monitoring
- Rely on OOM killer as last resort

**Questions to Answer:**
- What memory pressure levels are acceptable?
- At what pressure should we stop scheduling VMs?
- At what pressure should we migrate VMs away?

**Measurement Commands:**
```bash
# System-wide memory pressure
cat /proc/pressure/memory
# Example output:
# some avg10=0.00 avg60=0.00 avg300=0.00 total=123456
# full avg10=0.00 avg60=0.00 avg300=0.00 total=78901

# Per-cgroup memory pressure
for cg in /sys/fs/cgroup/exelet.slice/vm-*/memory.pressure; do
  echo "=== $(dirname $cg | xargs basename) ==="
  cat "$cg"
done

# Memory stats
cat /sys/fs/cgroup/exelet.slice/vm-*/memory.stat | head -50

# Watch memory pressure in real-time
watch -n 1 'cat /proc/pressure/memory'
```

**Potential Solutions:**

**Alert Thresholds:**
| Metric | Warning | Critical | Action |
|--------|---------|----------|--------|
| some avg10 | > 10% | > 25% | Stop scheduling new VMs |
| full avg10 | > 5% | > 15% | Migrate idle VMs away |

**Implementation:**
1. Monitor `/proc/pressure/memory` in resource manager
2. Expose metrics via Prometheus
3. Implement admission control when pressure is high
4. Trigger VM migration when pressure exceeds threshold

---

### 5. Hugepages Trade-offs

**Background:**
Hugepages reduce TLB misses by using larger page sizes (2MB or 1GB vs 4KB). This improves memory-intensive workload performance but has trade-offs.

**Current State:**
- Hugepages optional via `enableHugepages` config
- Memory aligned to hugepage size when enabled

**Trade-offs:**

| Feature | Without Hugepages | With Hugepages |
|---------|------------------|----------------|
| Page size | 4KB | 2MB or 1GB |
| TLB efficiency | Lower | Higher |
| KSM | Compatible | Incompatible |
| Memory granularity | Fine (4KB) | Coarse (2MB+) |
| Internal fragmentation | Low | Higher |
| Swap efficiency | Better | Worse |

**Questions to Answer:**
- What's the performance benefit of hugepages for our workloads?
- Is the KSM memory savings greater than hugepage performance gains?
- What's the fragmentation overhead?

**Measurement Commands:**
```bash
# Hugepage configuration
cat /proc/meminfo | grep -i huge
cat /sys/kernel/mm/hugepages/hugepages-*/nr_hugepages

# TLB miss rates (requires perf)
perf stat -e dTLB-load-misses,dTLB-loads -p $(pgrep cloud-hyper | head -1) sleep 10

# Transparent Huge Pages status
cat /sys/kernel/mm/transparent_hugepage/enabled
```

**Recommendation:**
For density-focused deployments with similar VM images:
- **Disable hugepages** to enable KSM
- The memory savings from KSM (30-50%) likely exceeds hugepage performance gains

For performance-focused deployments with diverse workloads:
- **Enable hugepages** for better memory performance
- KSM won't provide significant savings anyway

---

### 6. Memory Tiering (Future)

**Background:**
Modern servers support heterogeneous memory (DRAM + PMEM, or CXL-attached memory). Slower memory tiers can extend capacity for cold data.

**Technologies:**
- Intel Optane PMEM (deprecated but still deployed)
- CXL memory expanders
- NUMA-based tiering

**Questions to Answer:**
- Do current hosts have multiple memory tiers?
- What's the latency difference between tiers?
- Can idle VM memory be demoted to slower tiers?

**Measurement Commands:**
```bash
# Check for PMEM
ndctl list -Ni

# NUMA topology (slower memory may be remote NUMA)
numactl --hardware

# Memory tiering support (kernel 5.15+)
cat /sys/kernel/mm/numa/demotion_enabled 2>/dev/null
```

**Potential Solutions:**
- Enable NUMA balancing for automatic migration
- Use `memory.reclaim` cgroup v2 for proactive demotion
- Configure tiered memory pools per VM priority

---

## Measurement Commands Summary

```bash
# === System Overview ===
free -h                                  # Memory summary
cat /proc/meminfo                        # Detailed memory info
cat /proc/pressure/memory                # Memory pressure

# === KSM Status ===
cat /sys/kernel/mm/ksm/run
cat /sys/kernel/mm/ksm/pages_sharing

# === Per-VM Memory ===
for cg in /sys/fs/cgroup/exelet.slice/vm-*/; do
  echo "=== $(basename $cg) ==="
  cat "$cg/memory.current" | numfmt --to=iec
  cat "$cg/memory.stat" | head -5
done

# === Process Memory ===
ps -eo pid,rss,vsz,comm | grep cloud-hyper

# === Swap ===
free -h
swapon --show
cat /proc/swaps
```

---

## Expected Impact

| Strategy | Density Improvement | Complexity | Risk |
|----------|-------------------|------------|------|
| KSM (same base images) | 30-50% | Low | Low |
| Overcommit 1.5x | 50% | Low | Low |
| Overcommit 2x | 100% | Medium | Medium |
| Balloon drivers | 20-40% | High | Medium |
| Disable hugepages for KSM | +30-50% (enables KSM) | Low | Low |
| Memory pressure admission | N/A (safety) | Medium | Low |

**Recommended Starting Point:**
1. Enable KSM and measure savings (disable hugepages if needed)
2. Measure actual memory usage vs allocated
3. Enable conservative overcommit (1.5x) with swap
4. Implement memory pressure monitoring
5. Consider balloon drivers for dynamic memory management

---

## Quick Wins

These changes can be implemented quickly with high confidence:

### 1. Enable KSM (Kernel Same-page Merging)
**Impact:** 30-50% memory reduction (with similar base images)
**Risk:** Low (1-5% CPU overhead)
**Effort:** Sysfs writes + systemd service

VMs from similar base images share many identical memory pages. KSM deduplicates them.

```bash
# Enable KSM immediately
echo 1 > /sys/kernel/mm/ksm/run

# Tune for faster scanning (more aggressive)
echo 2000 > /sys/kernel/mm/ksm/pages_to_scan
echo 10 > /sys/kernel/mm/ksm/sleep_millisecs

# Make persistent via systemd unit or init script
# Create /etc/systemd/system/ksm.service
```

**Note:** If hugepages are enabled, disable them first (KSM and hugepages are incompatible).

### 2. Configure Swap as Safety Net
**Impact:** Enables safe overcommit
**Risk:** Low
**Effort:** Partition/file setup

Swap allows the kernel to page out idle VM memory, enabling overcommit.

```bash
# Create swap file (adjust size as needed)
fallocate -l 64G /swapfile
chmod 600 /swapfile
mkswap /swapfile
swapon /swapfile

# Add to /etc/fstab for persistence
echo '/swapfile none swap sw 0 0' >> /etc/fstab

# Tune swappiness (lower = prefer keeping in RAM)
echo 10 > /proc/sys/vm/swappiness
```

### 3. Enable Conservative Overcommit (1.5x)
**Impact:** 50% more VMs
**Risk:** Low (with swap configured)
**Effort:** Sysctl change

```bash
# Allow 150% of RAM to be committed
echo 2 > /proc/sys/vm/overcommit_memory
echo 150 > /proc/sys/vm/overcommit_ratio

# Make persistent
echo 'vm.overcommit_memory=2' >> /etc/sysctl.d/90-density.conf
echo 'vm.overcommit_ratio=150' >> /etc/sysctl.d/90-density.conf
```

With 384GB RAM: can now allocate 576GB (72 VMs at 8GB each, up from 48).
