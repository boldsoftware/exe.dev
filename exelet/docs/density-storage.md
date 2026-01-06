# Storage Density Exploration

This document explores strategies to increase VM density from a storage perspective on exelet hosts, with particular focus on IOPS optimization.

## Current State

### Storage Backend
- **ZFS** with thin provisioning
- **EBS-backed** volumes (migrating to local NVMe)
- IOPS bottleneck during normal VM operation

### ZFS Configuration
Volumes are created with these properties:

| Property | Value | Purpose |
|----------|-------|---------|
| `compression` | `lz4` | Fast compression for space savings |
| `volblocksize` | `4K` | Optimized for random I/O |
| `primarycache` | `metadata` | Prevents double-caching with guest OS |
| `logbias` | `latency` | Optimized for random workloads |
| `sync` | `standard` | ZFS handles fsync durability |
| `refreservation` | `none` | Thin provisioning |

**File:** `exelet/docs/zfs.md` contains administration guide.

### Current Bottleneck
- EBS provides limited IOPS (baseline ~3,000 IOPS for gp3)
- High latency compared to local storage
- IOPS limits affect VM responsiveness during I/O bursts

---

## Investigation Areas

### 1. Local NVMe Migration

**Background:**
Moving from EBS to local NVMe SSDs dramatically improves IOPS and latency:

| Metric | EBS gp3 | Local NVMe |
|--------|---------|------------|
| IOPS (random read) | 3,000-16,000 | 500,000+ |
| IOPS (random write) | 3,000-16,000 | 200,000+ |
| Latency | 1-3ms | 0.05-0.1ms |
| Throughput | Up to 1,000 MB/s | 3,000+ MB/s |

**Questions to Answer:**
- What NVMe devices are available on current hosts?
- What RAID configuration provides best balance of performance and reliability?
- How do we handle data durability without EBS replication?

**Measurement Commands:**
```bash
# List NVMe devices
nvme list
lsblk -d | grep nvme

# NVMe device info and health
nvme smart-log /dev/nvme0
nvme id-ctrl /dev/nvme0

# Benchmark raw device IOPS
fio --name=randread --ioengine=libaio --iodepth=32 --rw=randread \
    --bs=4k --direct=1 --size=1G --numjobs=4 --runtime=30 \
    --filename=/dev/nvme0n1p1 --readonly

# Benchmark ZFS pool IOPS
fio --name=randread --ioengine=libaio --iodepth=32 --rw=randread \
    --bs=4k --direct=1 --size=1G --numjobs=4 --runtime=30 \
    --directory=/tank/testdir
```

**ZFS Pool Configuration Options:**

**Option A: Single NVMe (simplest)**
```bash
zpool create tank /dev/nvme0n1
```
- Pros: Maximum capacity, simple
- Cons: No redundancy (data loss on device failure)

**Option B: Mirror (recommended for data safety)**
```bash
zpool create tank mirror /dev/nvme0n1 /dev/nvme1n1
```
- Pros: Data protection, read performance boost
- Cons: 50% capacity reduction

**Option C: RAIDZ1 (3+ devices)**
```bash
zpool create tank raidz1 /dev/nvme0n1 /dev/nvme1n1 /dev/nvme2n1
```
- Pros: Better capacity efficiency than mirror
- Cons: Lower write IOPS, higher latency

**Option D: Stripe (maximum performance)**
```bash
zpool create tank /dev/nvme0n1 /dev/nvme1n1
```
- Pros: Maximum IOPS and throughput
- Cons: Any drive failure = complete data loss

**Recommendation:**
For VM workloads where data can be recreated (dev environments):
- Mirror for production, stripe for high-density dev

---

### 2. ZFS Tuning for IOPS

**Background:**
ZFS has many tunable parameters that affect IOPS performance. Current settings are balanced for VM workloads.

**Current Bottlenecks:**
- `sync=standard` ensures durability but costs IOPS
- ARC (RAM cache) may be undersized
- Write throttling may kick in during bursts

**Questions to Answer:**
- Is the ARC sized appropriately?
- Are write operations being throttled?
- What's the impact of sync vs async writes?

**Measurement Commands:**
```bash
# ARC (cache) statistics
arc_summary
# or
cat /proc/spl/kstat/zfs/arcstats | grep -E "^(hits|misses|size|c_max)"

# Pool I/O statistics (1 second intervals)
zpool iostat -v 1

# Per-dataset I/O (requires debug)
cat /proc/spl/kstat/zfs/*/objset-*/writes
cat /proc/spl/kstat/zfs/*/objset-*/reads

# Check ZFS module parameters
cat /sys/module/zfs/parameters/zfs_arc_max
cat /sys/module/zfs/parameters/zfs_txg_timeout

# Monitor write throttling
dtrace -n 'fbt::dsl_pool_undirty_space:entry { @[execname] = count(); }'
# or watch for "delay" in zpool iostat
```

**Tuning Options:**

**ARC Size (RAM cache):**
```bash
# Check current ARC max
cat /sys/module/zfs/parameters/zfs_arc_max

# Set ARC to 32GB (must have enough RAM)
echo 34359738368 > /sys/module/zfs/parameters/zfs_arc_max

# Persistent via /etc/modprobe.d/zfs.conf:
# options zfs zfs_arc_max=34359738368
```

**Transaction Group Timeout:**
```bash
# Default is 5 seconds - shorter = more responsive, more IOPS overhead
# Longer = better batching, higher latency
cat /sys/module/zfs/parameters/zfs_txg_timeout

# For latency-sensitive workloads, consider shorter timeout
echo 3 > /sys/module/zfs/parameters/zfs_txg_timeout
```

**Record Size (for new volumes):**
```bash
# Current: 4K (good for random I/O)
# Consider 8K or 16K if workloads have larger I/O patterns
zfs set recordsize=8K tank
```

**Sync Mode (DANGER - trade-off):**
```bash
# NEVER disable sync in production - data loss on crash
# But for ephemeral dev environments:
# zfs set sync=disabled tank/dev-instances

# Better option: Add SLOG for sync writes
zpool add tank log mirror /dev/nvme2n1 /dev/nvme3n1
```

---

### 3. Per-VM IOPS Limits

**Background:**
Without IOPS limits, a single VM can consume all available storage bandwidth, affecting other VMs (noisy neighbor problem).

**Current State:**
- No IOPS limits implemented
- Resource manager doesn't manage storage I/O

**Questions to Answer:**
- What IOPS should each VM be entitled to?
- How do we enforce limits without hurting burst performance?
- Should limits be per-plan or per-VM?

**Measurement Commands:**
```bash
# Per-VM I/O from cgroups (if cgroup v2 IO controller enabled)
for cg in /sys/fs/cgroup/exelet.slice/vm-*/io.stat; do
  echo "=== $(dirname $cg | xargs basename) ==="
  cat "$cg" 2>/dev/null || echo "io controller not enabled"
done

# System-wide I/O pressure
cat /proc/pressure/io

# Per-device I/O stats
iostat -x 1

# ZFS per-dataset I/O (approximate via arc hits/misses)
zfs list -o name,written,read
```

**Implementation Options:**

**Option A: cgroup v2 io.max**
```bash
# Set IOPS limit for a VM
# Format: "MAJ:MIN rbps=X wbps=Y riops=Z wiops=W"
device_major_minor=$(stat -c '%t:%T' /dev/zvol/tank/vm-123)
echo "$device_major_minor riops=1000 wiops=500" > /sys/fs/cgroup/exelet.slice/vm-123.scope/io.max
```

**Option B: cgroup v2 io.weight (proportional)**
```bash
# Relative priority (similar to cpu.weight)
echo "default 100" > /sys/fs/cgroup/exelet.slice/vm-123.scope/io.weight
echo "default 50" > /sys/fs/cgroup/exelet.slice/vm-idle.scope/io.weight  # idle VM
```

**Option C: ZFS per-dataset limits (experimental)**
```bash
# ZFS has no native IOPS limits, but can limit bandwidth
# Not recommended - cgroup is better
```

**Recommended Approach:**
1. Use `io.weight` for proportional fairness (already partially implemented)
2. Add `io.max` for hard limits on problematic VMs
3. Calculate fair share: pool_iops / num_vms with burst headroom

---

### 4. Thin Provisioning Optimization

**Background:**
Thin provisioning (`refreservation=none`) allows overcommitting storage. VMs don't reserve space upfront, so actual usage can exceed physical capacity.

**Current State:**
- Thin provisioning enabled
- No monitoring of actual vs allocated space

**Questions to Answer:**
- What's the typical actual-to-allocated ratio?
- How close are we to physical capacity?
- At what threshold should we stop scheduling new VMs?

**Measurement Commands:**
```bash
# Pool capacity (actual usage vs total)
zpool list -o name,size,alloc,free,cap,health

# Per-volume allocated vs used
zfs list -t volume -o name,volsize,used,refer

# Calculate overcommit ratio
allocated=$(zfs list -t volume -Hp -o volsize | awk '{sum+=$1} END {print sum}')
pool_size=$(zpool list -Hp -o size tank)
echo "Overcommit ratio: $(echo "scale=2; $allocated / $pool_size" | bc)"

# Actual compression savings
zfs get compressratio tank
zfs list -o name,used,logicalused,compressratio

# Space used by snapshots (often forgotten)
zfs list -t snapshot -o name,used -s used | tail -20
```

**Potential Solutions:**

**Monitoring:**
1. Track allocated vs actual usage over time
2. Alert when pool usage exceeds threshold (e.g., 80%)
3. Implement admission control when pool is full

**Optimization:**
1. Clean up orphaned snapshots regularly
2. Remove unused base images
3. Consider ZFS deduplication (high memory cost)

---

### 5. Image Deduplication

**Background:**
Many VMs use similar base images. Deduplication can significantly reduce storage usage.

**Current Implementation:**
- Copy-on-write cloning (instant image creation)
- No block-level deduplication

**ZFS Deduplication:**
ZFS supports block-level deduplication, but it has significant drawbacks.

| Aspect | Value |
|--------|-------|
| Dedup ratio (typical) | 1.5-3x |
| Memory cost | 5GB RAM per 1TB deduplicated data |
| Performance impact | 20-50% write slowdown |
| Recommendation | Avoid for VM workloads |

**Questions to Answer:**
- How much data is duplicated across VMs?
- Is COW cloning sufficient?
- What's the memory budget for dedup?

**Measurement Commands:**
```bash
# Check current dedup ratio (if enabled)
zpool list -o name,dedup,size,alloc

# Simulate dedup (dry run - very slow, run overnight)
zdb -S tank

# Check dedup table memory usage (if enabled)
echo "::arc" | mdb -k 2>/dev/null || cat /proc/spl/kstat/zfs/arcstats | grep ddt

# Count unique blocks across base images
# This estimates potential dedup savings
zfs list -t volume -o name,guid | grep sha256: | wc -l
```

**Potential Solutions:**

**Rely on COW cloning (recommended):**
- All instances clone from base images
- Only differences are stored
- No additional memory overhead

**ZFS deduplication (not recommended):**
- High memory cost
- Performance penalty
- Only useful if VMs aren't cloned from images

**Application-level dedup:**
- Use overlayfs or similar in guest
- Single base layer, per-VM overlay
- Complexity in guest management

---

### 6. Compression Analysis

**Background:**
LZ4 compression is enabled by default. Understanding compression ratios helps estimate actual capacity.

**Current State:**
- LZ4 compression enabled
- Typical VM compression: 1.2-2x

**Questions to Answer:**
- What compression ratios are we achieving?
- Would zstd provide better ratios?
- Is compression CPU-bound?

**Measurement Commands:**
```bash
# Pool-wide compression ratio
zfs get compressratio tank

# Per-volume compression
zfs list -o name,used,logicalused,compressratio -r tank

# Compression statistics
zfs get all tank | grep compress

# CPU usage from compression (approximate)
perf top -p $(pgrep -d',' z_compress)
```

**Compression Options:**

| Algorithm | Compression | CPU Cost | Use Case |
|-----------|-------------|----------|----------|
| lz4 (current) | 1.5-2x | Very low | Default, good balance |
| zstd | 2-3x | Medium | Higher density, more CPU |
| zstd-fast | 1.8-2.5x | Low-medium | Good compromise |
| gzip | 2-3x | High | Not recommended for VM IOPS |

**To test zstd:**
```bash
# Create test volume with zstd
zfs create -o compression=zstd tank/test-zstd

# Compare compression of similar data
dd if=/dev/urandom of=/tank/test-lz4/testfile bs=1M count=1000
dd if=/dev/urandom of=/tank/test-zstd/testfile bs=1M count=1000
zfs list -o name,used,logicalused,compressratio tank/test-*
```

---

## Measurement Commands Summary

```bash
# === Pool Overview ===
zpool list
zpool iostat -v 1
zpool status

# === Space Usage ===
zfs list -t volume -o name,volsize,used,compressratio
zfs list -t snapshot -o name,used -s used | tail -10

# === Performance ===
iostat -x 1
cat /proc/pressure/io

# === Per-VM I/O (cgroups) ===
cat /sys/fs/cgroup/exelet.slice/vm-*/io.stat

# === ARC Cache ===
arc_summary
cat /proc/spl/kstat/zfs/arcstats | grep -E "hits|misses|size"

# === NVMe Health ===
nvme smart-log /dev/nvme0
```

---

## Expected Impact

| Strategy | IOPS Improvement | Density Impact | Complexity | Risk |
|----------|-----------------|----------------|------------|------|
| NVMe migration | 50-100x | Indirect (remove bottleneck) | Medium | Low |
| ARC tuning | 20-50% | None | Low | Low |
| io.weight (fairness) | N/A | Prevents noisy neighbor | Low | Low |
| io.max (hard limits) | N/A | Prevents noisy neighbor | Medium | Low |
| Thin provisioning monitoring | N/A | +20-50% capacity | Low | Low |
| zstd compression | N/A | +20-30% capacity | Low | Low |

**Recommended Starting Point:**
1. **Migrate to local NVMe** - biggest impact on IOPS
2. **Tune ARC size** - ensure adequate caching
3. **Implement io.weight** - fairness during contention
4. **Monitor thin provisioning** - prevent unexpected full
5. **Evaluate zstd** - for additional capacity if CPU allows

---

## Quick Wins

These changes can be implemented quickly with high confidence:

### 1. Migrate to Local NVMe
**Impact:** 50-100x IOPS improvement
**Risk:** Medium (data durability changes)
**Effort:** Pool recreation

This is the single biggest improvement for the storage IOPS bottleneck.

```bash
# Create new ZFS pool on local NVMe (mirror for safety)
zpool create tank mirror /dev/nvme0n1 /dev/nvme1n1

# Apply same properties as current setup
zfs set compression=lz4 tank
zfs set primarycache=metadata tank
zfs set logbias=latency tank
```

**Expected improvement:** EBS gp3 ~3,000 IOPS → NVMe ~500,000+ IOPS

### 2. Increase ARC Size
**Impact:** 20-50% fewer disk reads
**Risk:** Low
**Effort:** Module parameter

The ARC (Adaptive Replacement Cache) caches frequently accessed data in RAM.

```bash
# Check current ARC size
cat /proc/spl/kstat/zfs/arcstats | grep c_max

# Set ARC to 32GB (if you have RAM to spare)
echo 34359738368 > /sys/module/zfs/parameters/zfs_arc_max

# Make persistent in /etc/modprobe.d/zfs.conf
echo 'options zfs zfs_arc_max=34359738368' >> /etc/modprobe.d/zfs.conf
```

### 3. Enable IO Weight for Idle VMs
**Impact:** Prevents idle VMs from blocking active ones
**Risk:** Low
**Effort:** Already partially implemented

The resource manager already sets `io.weight` for idle VMs. Verify it's working:

```bash
# Check IO weights are being applied
for cg in /sys/fs/cgroup/exelet.slice/vm-*/io.weight; do
  echo "$(dirname $cg | xargs basename): $(cat $cg 2>/dev/null)"
done
```

### 4. Monitor Thin Provisioning Headroom
**Impact:** Prevents surprise disk full
**Risk:** Low
**Effort:** Add alerting

```bash
# Add to monitoring/alerting
allocated=$(zfs list -t volume -Hp -o volsize | awk '{sum+=$1} END {print sum}')
actual=$(zpool list -Hp -o alloc tank)
pool_size=$(zpool list -Hp -o size tank)

# Alert if actual usage > 80%
usage_pct=$((actual * 100 / pool_size))
[ $usage_pct -gt 80 ] && echo "ALERT: Pool usage at ${usage_pct}%"
```
