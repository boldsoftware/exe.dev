# CPU Density Exploration

This document explores strategies to increase VM density from a CPU perspective on exelet hosts.

## Current State

### Host Configuration
- Typical host: 96 cores, 384GB RAM
- VM sizing: 2 vCPUs per VM
- Theoretical max (no oversubscription): 48 VMs per host

### Cloud Hypervisor Configuration
- `BootVcpus` and `MaxVcpus` both set to requested CPU count
- No CPU pinning configured
- Each VM gets dedicated vCPU threads

**File:** `exelet/vmm/cloudhypervisor/config.go:77-81`
```go
Cpus: &client.CpusConfig{
    BootVcpus: int(cfg.CPUs),
    MaxVcpus:  int(cfg.CPUs),
},
```

### Resource Manager (Implemented)
- Polls VM activity every 30 seconds
- Detects idle VMs (CPU < 3%, no network activity for 1 minute)
- Adjusts cgroup `cpu.weight` for idle VMs

**File:** `exelet/services/resourcemanager/priority.go:14-27`
```go
cpuWeightNormal = 100  // Active VMs
cpuWeightLow = 50      // Idle VMs
```

---

## Investigation Areas

### 1. CPU Oversubscription Ratios

**Background:**
CPU oversubscription means allocating more vCPUs than physical cores. This works because VMs are rarely using 100% CPU simultaneously. With mostly-idle workloads, aggressive oversubscription (8:1 or higher) is often viable.

**Questions to Answer:**
- What's the average CPU utilization per VM during typical usage?
- What's the P95/P99 CPU utilization during burst periods?
- How many VMs have concurrent high-CPU activity?

**Measurement Commands:**
```bash
# Per-VM CPU usage from cgroups (requires resource manager cgroups)
for cg in /sys/fs/cgroup/exelet.slice/vm-*/cpu.stat; do
  echo "=== $cg ==="
  cat "$cg"
done

# CPU usage percentage per poll interval (from resource manager logs)
journalctl -u exelet | grep "activity check"

# System-wide CPU pressure (PSI)
cat /proc/pressure/cpu

# All cloud-hypervisor CPU threads and their processor affinity
ps -eLo pid,tid,psr,pcpu,comm | grep cloud-hyper

# Historical CPU usage with sar (if sysstat installed)
sar -u 1 60
```

**Data to Collect:**
- Record per-VM CPU usage over 24-48 hours
- Identify peak usage patterns (time of day, day of week)
- Calculate correlation of CPU bursts across VMs

**Potential Solutions:**
- Set system-wide oversubscription ratio based on data (e.g., allow 8 vCPUs per physical core)
- Implement admission control based on current host CPU allocation
- Use `cpu.max` quotas for hard limits during contention

---

### 2. cgroup CPU Controls

**Background:**
Linux cgroup v2 provides two main CPU controls:
- `cpu.weight`: Relative priority during contention (1-10000, default 100)
- `cpu.max`: Hard cap on CPU time (quota/period microseconds)

Current implementation uses `cpu.weight` only.

**Current Implementation:**
```
/sys/fs/cgroup/exelet.slice/vm-<id>.scope/
├── cpu.weight    # 100 (normal) or 50 (idle)
└── cpu.max       # Not currently set
```

**Questions to Answer:**
- Should we use `cpu.max` to enforce hard limits?
- What quota values prevent noisy neighbor issues?
- How does `cpu.weight` interact with oversubscription?

**Measurement Commands:**
```bash
# Check current cgroup CPU configuration
for cg in /sys/fs/cgroup/exelet.slice/vm-*/; do
  echo "=== $(basename $cg) ==="
  cat "$cg/cpu.weight" 2>/dev/null || echo "no cpu.weight"
  cat "$cg/cpu.max" 2>/dev/null || echo "no cpu.max"
  cat "$cg/cpu.stat"
done

# CPU throttling events (if cpu.max is set)
cat /sys/fs/cgroup/exelet.slice/vm-*/cpu.stat | grep throttled
```

**Potential Solutions:**

**Option A: Weight-only (current)**
- Pros: Allows burst usage when system is idle
- Cons: No protection against sustained CPU hogs

**Option B: Weight + Max quota**
```bash
# Example: Limit VM to 200% CPU (2 cores worth)
echo "200000 100000" > cpu.max
```
- Pros: Hard guarantee, prevents runaway processes
- Cons: Can't burst even when system is idle

**Option C: Weight + Adaptive max**
- Adjust `cpu.max` based on system load
- When load is low: high quota (allow bursting)
- When load is high: reduce to fair share

---

### 3. CPU Pinning vs Sharing

**Background:**
CPU pinning assigns specific physical cores to VMs. Sharing allows the scheduler to distribute vCPUs across all cores.

**Current State:**
- No CPU pinning - vCPUs float across all cores
- Cloud Hypervisor supports `CpusConfig.affinity` for pinning

**NUMA Considerations:**
96-core servers typically have 2 NUMA nodes (48 cores each). Memory access is faster within the same NUMA node.

**Questions to Answer:**
- Do we have NUMA-aware memory allocation?
- What's the performance impact of cross-NUMA scheduling?
- Should we pin VMs to NUMA nodes?

**Measurement Commands:**
```bash
# View NUMA topology
numactl --hardware
lscpu | grep NUMA

# Check NUMA memory statistics
numastat -c

# View which CPUs cloud-hypervisor threads are using
for pid in $(pgrep cloud-hyper); do
  echo "PID $pid:"
  taskset -cp $pid 2>/dev/null || cat /proc/$pid/status | grep Cpus_allowed
done

# Monitor cross-NUMA memory access
numastat -p $(pgrep cloud-hyper | head -1)
```

**Potential Solutions:**

**Option A: No pinning (current)**
- Pros: Simple, scheduler handles balancing
- Cons: Potential cross-NUMA overhead

**Option B: NUMA node pinning**
- Pin VM to specific NUMA node (both CPU and memory)
- Requires coordination with memory allocation
- Cloud Hypervisor: `CpusConfig.affinity` field

**Option C: CPU pools**
- Reserve some cores for host overhead
- Dedicate remaining cores to VM pool
- Use cgroup `cpuset` for enforcement

---

### 4. VM Priority Tuning

**Background:**
VM priority is controlled explicitly via the `SetVMPriority` RPC. VMs default to PRIORITY_NORMAL; low-priority VMs get reduced cpu.weight, io.weight, and memory.high.

**Measurement Commands:**
```bash
# Check priority changes
journalctl -u exelet | grep "priority changed"

# View per-VM activity metrics (Prometheus)
curl -s localhost:9090/metrics | grep exelet_vm_cpu_percent
```

**Potential Solutions:**
- More aggressive weight reduction (10 instead of 50 for low priority)

---

### 5. Hot-add vCPUs

**Background:**
Cloud Hypervisor supports adding vCPUs to running VMs. VMs could start with minimal vCPUs and scale up on demand.

**Current State:**
- `BootVcpus` = `MaxVcpus` (no hot-add headroom)
- VM starts with full CPU allocation

**Cloud Hypervisor API:**
```
PUT /api/v1/vm.resize
{
  "desired_vcpus": N
}
```

**Questions to Answer:**
- Does the guest kernel support CPU hot-add?
- What's the latency to add a vCPU?
- How do we detect when a VM needs more CPU?

**Measurement Commands:**
```bash
# Check if guest supports CPU hot-plug
# (run inside guest)
cat /sys/devices/system/cpu/hotplug/states

# Test hot-add via Cloud Hypervisor API
curl -X PUT --unix-socket /path/to/ch.sock \
  -H "Content-Type: application/json" \
  -d '{"desired_vcpus": 4}' \
  http://localhost/api/v1/vm.resize
```

**Potential Solutions:**

**Strategy: Start small, grow on demand**
1. Boot VMs with 1 vCPU, `MaxVcpus` = requested (e.g., 2)
2. Monitor CPU utilization
3. When sustained high CPU, add vCPU via API
4. When idle, optionally remove vCPU

**Challenges:**
- Requires guest kernel support
- Need CPU pressure detection logic
- May add latency to CPU-intensive workloads

---

## Measurement Commands Summary

```bash
# === System Overview ===
lscpu                                    # CPU info
numactl --hardware                       # NUMA topology
cat /proc/pressure/cpu                   # CPU pressure

# === Per-VM Usage ===
# cgroup stats
ls /sys/fs/cgroup/exelet.slice/vm-*/
cat /sys/fs/cgroup/exelet.slice/vm-*/cpu.stat

# Process stats
ps -eLo pid,tid,psr,pcpu,comm | grep cloud-hyper

# === Resource Manager Logs ===
journalctl -u exelet | grep "activity check"
journalctl -u exelet | grep "priority changed"

# === Cloud Hypervisor ===
# List VMs and their vCPU configuration
curl --unix-socket /path/to/ch.sock http://localhost/api/v1/vm.info
```

---

## Expected Impact

| Strategy | Density Improvement | Complexity | Risk |
|----------|-------------------|------------|------|
| Oversubscription (4:1) | 2x | Low | Low |
| Oversubscription (8:1) | 4x | Low | Medium |
| Aggressive idle weights | 20-50% | Low | Low |
| cpu.max quotas | N/A (fairness) | Medium | Low |
| NUMA pinning | 10-20% performance | Medium | Low |
| Hot-add vCPUs | 2-4x | High | Medium |

**Recommended Starting Point:**
1. Measure actual CPU usage across fleet
2. Enable 4:1 oversubscription with monitoring
3. Tune idle detection thresholds
4. Consider cpu.max quotas if noisy neighbor issues arise

---

## Quick Wins

These changes can be implemented quickly with high confidence:

### 1. Enable CPU Oversubscription (4:1)
**Impact:** 2x density improvement
**Risk:** Low (for mostly-idle workloads)
**Effort:** Configuration change

With mostly-idle VMs, 4:1 oversubscription is safe. Current setup: 96 cores / 2 vCPUs = 48 VMs max. With 4:1: 96 * 4 / 2 = 192 VMs.

```bash
# No code change needed - just allow scheduling more VMs
# Monitor CPU pressure to validate:
watch -n 5 'cat /proc/pressure/cpu'
```

### 2. Lower Low-Priority VM CPU Weight
**Impact:** Better responsiveness for active VMs
**Risk:** Low
**Effort:** One-line change

```go
// In priority.go, change:
cpuWeightLow = 10  // was 50
```

This gives active VMs 10x priority over idle VMs during contention.
