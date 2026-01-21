# VM Throttling

The `exelet-ctl compute instances throttle` command applies resource throttling to VMs via cgroup v2 controls.

## Usage

```bash
exelet-ctl compute instances throttle [flags] [ID...]
```

### Flags

| Flag | Description | Example |
|------|-------------|---------|
| `--cpu` | CPU limit as percentage (can exceed 100% for multi-core) | `--cpu=5%`, `--cpu=200%` |
| `--memory` | Memory high threshold as percentage of allocated (1-100%) | `--memory=50%` |
| `--clear` | Remove all throttling | `--clear` |

### Examples

```bash
# Throttle CPU to 5% of one core
exelet-ctl compute instances throttle --cpu=5% vm001

# Push memory above 50% of allocated to swap
exelet-ctl compute instances throttle --memory=50% vm001

# Combined throttle
exelet-ctl compute instances throttle --cpu=5% --memory=50% vm001

# Clear all throttles
exelet-ctl compute instances throttle --clear vm001

# Throttle multiple VMs
exelet-ctl compute instances throttle --cpu=10% vm001 vm002 vm003
```

## CPU Throttling (`--cpu`)

Sets the cgroup `cpu.max` value to limit CPU time.

**How it works:**
- The percentage represents a fraction of **one CPU core's** time
- `--cpu=5%` means the VM gets 5% of one core (5ms per 100ms period)
- `--cpu=100%` means the VM gets 100% of one core
- `--cpu=200%` means the VM gets the equivalent of 2 full cores

**Implementation:**
```
quota = (percent * period) / 100
period = 100000  (100ms in microseconds)
cpu.max = "quota period"
```

**Validation:**
- `cpu_percent` must be greater than 0
- Values above 100% are allowed for multi-core limits

**Examples:**
| Flag | cpu.max value | Meaning |
|------|---------------|---------|
| `--cpu=5%` | `5000 100000` | 5ms of CPU per 100ms |
| `--cpu=50%` | `50000 100000` | 50ms of CPU per 100ms |
| `--cpu=100%` | `100000 100000` | 1 full core |
| `--cpu=200%` | `200000 100000` | 2 full cores |
| `--clear` | `max 100000` | Unlimited |

**Note:** `--cpu=100%` is NOT the same as `--clear`. On a multi-core system, `--cpu=100%` limits the VM to one core's worth of CPU time, while `--clear` allows unlimited CPU usage across all cores.

### Using CPU throttling to limit disk I/O

CPU throttling can indirectly limit disk I/O because:
- A CPU-starved VM cannot generate as many I/O requests
- The VM spends more time waiting for CPU, reducing its ability to issue disk operations
- This is particularly effective for I/O-heavy workloads that also require CPU to process data

Example: A VM doing heavy disk I/O with `--cpu=10%` will see reduced I/O throughput because it can't run the code that issues I/O requests as frequently.

## Memory Throttling (`--memory`)

Sets the cgroup `memory.high` value to trigger aggressive memory reclaim.

**How it works:**
- The percentage is relative to the VM's **allocated** memory (from VM config)
- When usage exceeds `memory.high`, the kernel aggressively reclaims memory by pushing pages to swap
- This is a soft limit - the VM can still use its full allocated memory, but excess pages live in swap

**Example:** A VM with 4GB allocated memory:
| Flag | memory.high value | Behavior |
|------|-------------------|----------|
| `--memory=50%` | 2GB | Pages above 2GB pushed to swap |
| `--memory=80%` | 3.2GB | Pages above 3.2GB pushed to swap |
| `--clear` | `max` | No aggressive reclaim |

**Use case:** Force idle VMs to release physical RAM to swap, allowing active VMs to use the freed memory. The idle VM remains running but with reduced physical RAM footprint.

**Validation:**
- `memory_percent` must be between 1 and 100
- Requires the VM to have known allocated memory (tracked by resource manager)

**Note:** This does NOT hard-cap memory. For hard limits, use `memory.max` (not exposed via this command).

## Clearing Throttles

`--clear` removes all throttling by resetting cgroup values to unlimited:

| Control | Cleared value |
|---------|---------------|
| `cpu.max` | `max 100000` |
| `memory.high` | `max` |

**Validation:**
- `--clear` cannot be combined with `--cpu` or `--memory` options
- If clearing fails for any control, the command returns an error with details

## Cgroup Paths

Throttle settings are written to the VM's cgroup at:
```
/sys/fs/cgroup/exelet.slice/{group_id}.slice/vm-{vm_id}.scope/
├── cpu.max
└── memory.high
```

## Verification

After applying throttles, verify by reading the cgroup files:

```bash
# Check CPU throttle
cat /sys/fs/cgroup/exelet.slice/{group}.slice/vm-{id}.scope/cpu.max

# Check memory throttle
cat /sys/fs/cgroup/exelet.slice/{group}.slice/vm-{id}.scope/memory.high
```

## Disk I/O Throttling

Disk I/O throttling via cgroup `io.max` is **not supported** because it doesn't work reliably with ZFS:
- ZFS handles I/O through its own kernel threads, not through the VM's cgroup
- The ZFS ARC (cache) can serve reads without hitting the disk
- I/O from VM processes goes through ZFS's internal thread pool, bypassing cgroup accounting

**Alternatives for limiting disk I/O:**
1. Use `--cpu` throttling to indirectly limit I/O generation
2. Configure disk rate limits at VM creation time via cloud-hypervisor's `rate_limiter_config`
