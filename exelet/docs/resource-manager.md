# Resource Manager Design

## Overview

The Resource Manager is a new exelet service that provides:

1. **Node Capacity Tracking** - Detect and report total node resources
2. **Per-VM Usage Reporting** - Track actual resource consumption per VM
3. **Priority Management** - Dynamically adjust VM CPU priority based on activity

This is separate from the existing ResourceMonitor service, which focuses on Prometheus metrics for observability.

## Components

```
exelet/services/resourcemanager/
├── resourcemanager.go    # Main service, orchestrates components
├── capacity.go           # Node capacity detection & tracking
├── usage.go              # Per-VM usage collection & activity tracking
├── priority.go           # cgroup v2 priority adjustment
└── resourcemanager_test.go
```

## Node Capacity Detection

### CPU
- Use `runtime.NumCPU()` to detect available CPU cores

### Memory
- Parse `/proc/meminfo` for `MemTotal`

### Disk
- Query ZFS pool size via `zpool get -Hp size <pool>`
- Pool name derived from StorageManagerAddress config

## Per-VM Usage Tracking

Track actual resource consumption for each running VM:

| Metric | Source |
|--------|--------|
| CPU seconds | `/proc/<pid>/stat` (utime + stime) |
| Memory bytes | Cloud Hypervisor API or `/proc/<pid>/status` |
| Disk bytes | `zfs get -Hp used <dataset>/<vm-id>` |
| Network RX/TX | `/sys/class/net/<tap>/statistics/` |

### Activity Detection

Track `last_activity` timestamp per VM. Activity is detected using **rate-based CPU percentage**:

| Metric | Threshold | Calculation |
|--------|-----------|-------------|
| CPU | >1% | `(cpu_delta_seconds / elapsed_seconds) * 100` |
| Network | >10KB | Sum of RX and TX byte deltas |

**Why rate-based?** The cloud-hypervisor VMM process uses some CPU even when the guest is idle (timer interrupts, virtio polling, etc.). Using absolute CPU seconds would cause VMs to never be marked as idle. By calculating CPU as a percentage of wall-clock time, we can distinguish between:
- Idle VM: ~0.5% CPU (VMM overhead only)
- Active VM: >1% CPU (guest workload)

**Example calculation:**
- Poll interval: 30 seconds
- CPU delta: 0.15 seconds
- CPU percentage: (0.15 / 30) * 100 = **0.5%** → idle
- CPU delta: 0.6 seconds
- CPU percentage: (0.6 / 30) * 100 = **2%** → active

A VM is considered **idle** when `now - last_activity > idle_threshold`.

Default idle threshold: 1 minute (configurable).

## Priority Management

Use cgroup v2 to adjust CPU scheduling weight, IO weight, and memory controls for idle VMs.

### Cgroup Structure

```
/sys/fs/cgroup/
└── exelet.slice/
    ├── vm-<id-1>.scope/
    │   ├── cgroup.procs      # cloud-hypervisor PID
    │   ├── cpu.weight        # 100 (normal) or 10 (idle)
    │   ├── io.weight         # default 100 or default 10
    │   ├── memory.swap.max   # max (swap allowed)
    │   └── memory.high       # max (normal) or 80% of allocated (idle)
    ├── vm-<id-2>.scope/
    └── ...
```

### CPU Weight

| Priority | cpu.weight | Description |
|----------|------------|-------------|
| Normal   | 100        | Default, active VM |
| Low      | 10         | Idle VM, reduced scheduling priority |

### IO Weight

| Priority | io.weight | Description |
|----------|-----------|-------------|
| Normal   | 100       | Default, active VM |
| Low      | 10        | Idle VM, reduced IO priority |

### Memory Management

Memory priority uses `memory.high` to control which VMs get swapped first under memory pressure:

| Priority | memory.swap.max | memory.high | Effect |
|----------|-----------------|-------------|--------|
| Normal   | max             | max         | Swap allowed, no throttling (swapped last) |
| Low      | max             | 80% of allocated | Swap allowed, throttled (swapped first) |

**How it works:**
- All VMs can swap when the system is under memory pressure
- `memory.high` controls when the kernel starts aggressively reclaiming memory
- Idle VMs have a lower `memory.high` threshold (80% of allocated memory)
- When memory pressure occurs, the kernel targets VMs above their `memory.high` first
- This means idle VMs get swapped to disk before active VMs

### Lifecycle

1. **VM Start**: Create cgroup scope, move cloud-hypervisor process into it
2. **Poll**: Check activity, adjust cpu.weight/io.weight/memory.high if priority changed
3. **VM Stop**: Remove cgroup scope

## gRPC Service

New service in `api/exe/resource/v1/resource.proto`:

```protobuf
syntax = "proto3";

package exe.resource.v1;

option go_package = "exe.dev/api/resource/v1";

service ResourceManagerService {
  // GetNodeStatus returns current node capacity and allocation summary
  rpc GetNodeStatus(GetNodeStatusRequest) returns (GetNodeStatusResponse);

  // GetVMUsage returns usage information for a specific VM
  rpc GetVMUsage(GetVMUsageRequest) returns (GetVMUsageResponse);

  // SetVMPriority manually sets priority for a VM (overrides automatic)
  rpc SetVMPriority(SetVMPriorityRequest) returns (SetVMPriorityResponse);
}

message GetNodeStatusRequest {}

message GetNodeStatusResponse {
  NodeCapacity capacity = 1;
  NodeAllocation allocation = 2;
}

message GetVMUsageRequest {
  string vm_id = 1;
}

message GetVMUsageResponse {
  VMUsage usage = 1;
}

message NodeCapacity {
  uint64 cpus = 1;           // Total CPU cores
  uint64 memory_bytes = 2;   // Total RAM in bytes
  uint64 disk_bytes = 3;     // Total disk in bytes (ZFS pool)
}

message NodeAllocation {
  uint64 cpus = 1;           // Sum of allocated vCPUs
  uint64 memory_bytes = 2;   // Sum of allocated memory
  uint64 disk_bytes = 3;     // Sum of allocated disk quotas
}

message VMUsage {
  string id = 1;
  string name = 2;
  double cpu_seconds = 3;    // Total CPU time consumed
  uint64 memory_bytes = 4;   // Current memory usage
  uint64 disk_bytes = 5;     // Current disk usage
  uint64 net_rx_bytes = 6;   // Total network received
  uint64 net_tx_bytes = 7;   // Total network transmitted
  int64 last_activity = 8;   // Unix nano timestamp
  VMPriority priority = 9;
}

enum VMPriority {
  PRIORITY_NORMAL = 0;
  PRIORITY_LOW = 1;
}

message SetVMPriorityRequest {
  string vm_id = 1;
  VMPriority priority = 2;
}

message SetVMPriorityResponse {}
```

**Note:** Per-VM usage is retrieved via `GetVMUsage(vm_id)` rather than included in `GetNodeStatus` to avoid gRPC message size limits with many VMs.

## Configuration

Add to `ExeletConfig`:

```go
// ResourceManagerInterval controls polling frequency
ResourceManagerInterval time.Duration
// ResourceManagerEnabled enables the resource manager service
ResourceManagerEnabled bool
```

Defaults:
- `ResourceManagerInterval`: 30 seconds
- `ResourceManagerEnabled`: false (opt-in)

## Integration Points

### With ComputeService

- Query running instances via `InstanceLookup` interface
- Get VM configs for allocation calculation
- Get cloud-hypervisor PID for cgroup management

### With exed

- exed calls `GetNodeStatus` to:
  - Make scheduling decisions (place new VMs on nodes with capacity)
  - Monitor cluster-wide resource usage
  - Implement user quotas (aggregate usage across nodes)

## Future Considerations

- Memory ballooning for dynamic memory management
- Disk quota enforcement via ZFS
- Network bandwidth limits via tc
- Integration with billing/metering systems
