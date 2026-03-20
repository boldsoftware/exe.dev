# Tiered Storage

This document describes the design, operation, and disaster recovery procedures for the exelet tiered storage system.

## Overview

Tiered storage allows an exelet to manage multiple ZFS pools as named "storage tiers." VMs can be migrated between pools on the same host — for example, moving a VM from fast NVMe storage to slower block storage, or recovering from a failed pool by serving VMs from a backup pool.

This reuses the existing migration machinery (ZFS send/recv, cloud-hypervisor snapshot/restore, migration locking) but operates entirely locally — no network or IPAM changes are needed.

## Architecture

```
┌──────────────────────────────────────────────┐
│                   Exelet                      │
│                                               │
│  ┌─────────────────────────────────────────┐  │
│  │        TieredStorageManager             │  │
│  │                                         │  │
│  │  ┌─────────┐  ┌─────────┐  ┌────────┐  │  │
│  │  │  tank   │  │  nvme   │  │ backup │  │  │
│  │  │ (primary)│  │ (tier)  │  │ (tier) │  │  │
│  │  └─────────┘  └─────────┘  └────────┘  │  │
│  │       │              │           │      │  │
│  │       ▼              ▼           ▼      │  │
│  │   tank/vm-1     nvme/vm-2   backup/vm-3 │  │
│  └─────────────────────────────────────────┘  │
│                                               │
│  ┌──────────┐  ┌──────────┐  ┌────────────┐  │
│  │ Compute  │  │ Replicat.│  │ Resource   │  │
│  │ Service  │  │ Service  │  │ Manager    │  │
│  └──────────┘  └──────────┘  └────────────┘  │
└──────────────────────────────────────────────┘
```

### Key Components

- **TieredStorageManager**: Wraps multiple `StorageManager` instances. Implements the `StorageManager` interface by delegating to the primary pool, making it a drop-in replacement for single-pool setups.
- **Pool Resolution**: `PoolForInstance(ctx, id)` scans all pools to find which one holds a given VM's dataset. Used by `startInstance`, `deleteInstance`, `GrowDisk`, `SendVM`, and `CloneInstance`.
- **Tier Migration**: `MigrateStorageTier` RPC performs async local ZFS send/recv between pools, with optional live migration via CH snapshot/restore.
- **Worker Pool**: A configurable semaphore limits concurrent tier migrations to avoid overloading the host.

## Configuration

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--storage-manager-address` | `zfs:///var/tmp/exelet/storage?dataset=tank` | Primary storage pool (always required) |
| `--storage-tier` | (none) | Additional storage tier address (repeatable) |
| `--storage-tier-migration-workers` | `1` | Maximum concurrent tier migrations |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `EXELET_STORAGE_MANAGER_ADDRESS` | Primary storage pool address |
| `EXELET_STORAGE_TIERS` | Comma-separated additional tier addresses |
| `EXELET_STORAGE_TIER_MIGRATION_WORKERS` | Max concurrent tier migrations |

### Example

```bash
exelet \
  --stage prod \
  --storage-manager-address "zfs:///data/exelet/storage?dataset=tank" \
  --storage-tier "zfs:///data/exelet/storage?dataset=nvme" \
  --storage-tier "zfs:///data/exelet/storage?dataset=backup" \
  --storage-tier-migration-workers 2
```

This configures three pools: `tank` (primary), `nvme`, and `backup`. Up to 2 tier migrations can run concurrently.

### Constraints

- Each tier must have a unique pool/dataset name. You cannot add the primary pool as a tier.
- All tiers use the same URL format as `--storage-manager-address` (e.g., `zfs:///path?dataset=name`).
- The `dataDir` path in the URL is where per-pool metadata (encryption keys) is stored.

## Pool Resolution

When the exelet needs to operate on an existing VM's storage (start, stop, delete, grow disk, send, clone), it must find which pool holds that VM's ZFS dataset. This is handled by `TieredStorageManager.PoolForInstance()`, which calls `Get()` on each pool in order (primary first) until one succeeds.

The pool name is naturally embedded in the ZFS dataset path (e.g., `tank/vm-xxxx` vs `nvme/vm-xxxx`), and the `RootDiskPath` in the instance config (e.g., `/dev/zvol/tank/vm-xxxx`) reflects the current pool. On every VM start, `startInstance` refreshes `RootDiskPath` from the storage manager's `Load()` result, so the VMM always gets the correct device path even after a tier migration or DR recovery.

## Tier Migration

### Modes

| Mode | VM State | Downtime | Description |
|------|----------|----------|-------------|
| Stopped | Stopped | N/A | Full ZFS send/recv, update config |
| Live | Running | Sub-second | Two-phase ZFS + CH snapshot/restore |

### Stopped VM Migration

1. Lock instance for migration
2. Suspend replication for the volume
3. `sync` to flush in-flight writes
4. Create migration snapshot on source pool
5. Full ZFS send from source, pipe directly to ZFS recv on target (local, no gRPC)
6. Copy encryption key if present
7. Update instance config `RootDiskPath` to target pool's zvol path
8. Delete source dataset
9. Cleanup snapshot, resume replication, unlock

### Live VM Migration

1. Steps 1-2 same as stopped
2. **Phase 1 (VM running):** `sync`, create pre-copy snapshot, full ZFS send/recv
3. **Pause VM** (no balloon deflation needed — memory stays on same host)
4. **Phase 2 (VM paused):** `sync`, create migration snapshot, incremental ZFS send/recv
5. Copy encryption key, create CH snapshot (process state)
6. Edit snapshot config: update disk path only (no IP changes — `targetNetwork` is nil)
7. Stop old CH process
8. Restore from snapshot with new disk path
9. Update instance config, delete source dataset, cleanup

**Rollback:** If restore fails after pause, the VM is resumed on the source disk. The source dataset is never deleted until restore is confirmed.

## Operator Commands

### List Tiers

```bash
exelet-ctl storage tiers list
```

```
NAME     SIZE      USED      AVAIL     INSTANCES  PRIMARY
tank     1.8 TB    1.2 TB    600 GB    45         *
nvme     3.6 TB    800 GB    2.8 TB    12
backup   7.2 TB    2.0 TB    5.2 TB    0
```

### Migrate Instances

```bash
# Migrate a stopped VM
exelet-ctl storage tiers migrate nvme <instance-id>

# Migrate a running VM with near-zero downtime
exelet-ctl storage tiers migrate nvme <instance-id> --live

# Batch migrate multiple VMs
exelet-ctl storage tiers migrate nvme <id1> <id2> <id3>
```

Migrations are async — the command returns immediately with operation IDs. Excess migrations queue behind the worker limit.

### Check Migration Status

```bash
exelet-ctl storage tiers status
```

```
OP ID         INSTANCE      FROM    TO      STATE        PROGRESS  STARTED
019578a1b2c3  vm000001-spi  tank    nvme    migrating    45%       2m3s ago
019578a1d4e5  vm000002-blu  tank    nvme    pending      0%        1m12s ago
```

### Clear Completed/Failed Operations

```bash
exelet-ctl storage tiers status --clear
```

## Replication Interaction

When storage tiers are configured, the replication service automatically:

1. **Iterates all tiers** when listing datasets for replication (not just the primary pool).
2. **Skips self-replication**: If the replication target pool (e.g., `backup`) is also configured as a storage tier, datasets on that pool are excluded from replication — they are already on the backup.
3. **Prunes orphaned base images** on all pools, not just the primary.

This means datasets on `tank` and `nvme` both replicate to `backup`, but datasets already on `backup` (e.g., after DR recovery) do not replicate to themselves.

## Disaster Recovery

The tiered storage system enables a straightforward DR workflow when the primary pool is lost.

### Scenario: Primary Pool Lost

If the `tank` pool fails but replicated datasets exist on the `backup` pool:

1. **Configure backup as a tier** (if not already):

   ```bash
   exelet --storage-tier "zfs:///data/exelet/storage?dataset=backup" ...
   ```

2. **Start the exelet.** On boot, `startInstance` calls `PoolForInstance()` which scans all pools. It finds the replicated datasets on `backup` and serves them.

3. **Start VMs normally:**

   ```bash
   exelet-ctl compute instances start <instance-id>
   ```

   The exelet resolves the instance to the `backup` pool, loads the zvol, and updates `RootDiskPath` automatically. No manual path editing needed.

4. **Once a replacement primary pool is available**, migrate VMs back:

   ```bash
   exelet-ctl storage tiers migrate tank <instance-id>
   ```

   Normal replication resumes for migrated datasets.

### Scenario: Rebalancing After DR

After recovering from DR, you may have VMs running from the backup pool. To rebalance:

```bash
# List current state
exelet-ctl storage tiers list

# Migrate VMs back to the primary (or another fast tier)
exelet-ctl storage tiers migrate tank $(exelet-ctl storage tiers list-instances backup)

# Or migrate live with near-zero downtime
exelet-ctl storage tiers migrate tank <id1> <id2> --live
```

### Key DR Invariants

- Instance configs are stored in the exelet's `dataDir` (not per-pool), so they survive pool loss.
- `PoolForInstance()` is a scan — it finds datasets regardless of which pool they're on.
- `startInstance` always refreshes `RootDiskPath` from the storage manager, so stale paths in configs are corrected automatically.
- The VMM config `RootDiskPath` is also refreshed on every start, preventing "Cannot open disk path" errors after pool changes.

## Crash Safety

All critical file writes during tier migration use atomic write (write to `.tmp` sibling, then `os.Rename`):

- **Instance config** (`saveInstanceConfig`): Previously did `RemoveAll` + `WriteFile` which could lose data on crash between the two calls.
- **Encryption keys** (`SetEncryptionKey`): Atomic write prevents partial key files.
- **CH snapshot config** (`editSnapshotConfig`): Atomic write during live migration config editing.

A `sync` is issued before every ZFS migration snapshot to flush in-flight writes to the dataset, preventing data loss from buffered I/O.

## Incoming VM Migration

Incoming VM migrations (via `ReceiveVM` from another exelet) always land on the **primary pool**. This is by design — the primary pool is the default target for new workloads, and the receiving exelet doesn't know the operator's tier placement preferences.

After an incoming migration completes, the operator can move the VM to the appropriate tier:

```bash
exelet-ctl storage tiers migrate nvme <instance-id> --live
```

## Base Images and Pool Affinity

Base image existence checks during instance creation and incoming migration only check the **primary pool**. This is intentional — ZFS `clone` requires the origin snapshot (base image) to be in the same pool as the new dataset. Since new instances and incoming migrations always target the primary pool, the base image must also exist there.

If a base image exists only on a non-primary tier (e.g., after DR recovery or replication), it will be re-pulled from the registry into the primary pool. This is correct behavior: a local cross-pool copy would be possible as an optimization, but ZFS does not support cross-pool clones, so the base image must be present on the target pool regardless.

This also means that each pool may end up with its own copy of frequently-used base images. Base images are immutable and deduplicated within a pool via ZFS clones, so the storage overhead is limited to one copy per pool that has VMs derived from that image.

## I/O Throttling and Limits

I/O throttle settings and device-level limits are **not preserved** across tier migrations. Different storage tiers may have fundamentally different performance characteristics (NVMe vs spinning disk vs network-attached block storage), so I/O limits that make sense on one tier may be inappropriate on another. After a tier migration, operators should re-evaluate and reapply any I/O throttling policies appropriate for the target pool.

## Single-Pool Compatibility

When no `--storage-tier` flags are provided, the `TieredStorageManager` wraps the single primary pool. All operations delegate directly to it with no overhead. The system behaves identically to the pre-tiered-storage implementation.
