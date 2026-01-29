# VM Storage Replication

This document describes the design and operation of the exelet storage replication system.

## Overview

Storage replication provides continuous backup of VM volumes to a remote target. It uses ZFS snapshots and incremental sends to efficiently replicate data with minimal bandwidth usage.

## Architecture

```
┌─────────────────┐                    ┌─────────────────┐
│     Exelet      │                    │  Remote Target  │
│                 │                    │                 │
│  ┌───────────┐  │   zfs send/recv    │  ┌───────────┐  │
│  │ ZFS Pool  │──┼────────────────────┼─▶│ ZFS Pool  │  │
│  │ tank/vm-* │  │      (SSH)         │  │ tank/vm-* │  │
│  └───────────┘  │                    │  └───────────┘  │
│                 │                    │                 │
└─────────────────┘                    └─────────────────┘
```

### Components

- **Replication Service**: Orchestrates periodic replication cycles
- **Worker Pool**: Handles concurrent replication jobs
- **Target**: Abstraction for remote destinations (SSH or file-based)
- **Pruner**: Removes orphaned backups for deleted volumes
- **State**: Tracks replication history

## Configuration

| Flag | Default | Description |
|------|---------|-------------|
| `--storage-replication-enabled` | false | Enable replication |
| `--storage-replication-target` | - | Target URL (e.g., `ssh://user@host/pool` or `file:///path`) |
| `--storage-replication-interval` | 1h | Time between replication cycles |
| `--storage-replication-retention` | 24 | Number of snapshots to keep on remote |
| `--storage-replication-ssh-key` | - | SSH private key for authentication (falls back to SSH agent, then `~/.ssh/id_ed25519` and `~/.ssh/id_rsa`) |
| `--storage-replication-known-hosts` | ~/.ssh/known_hosts | Path to known_hosts file for SSH host key verification |
| `--storage-replication-bandwidth-limit` | - | Rate limit (e.g., "100M", "1G") |
| `--storage-replication-prune` | true | Enable pruning of orphaned backups |

## Operation

### Replication Cycle

1. **Instance Discovery**: Get all VM instances from compute service
2. **Filter**: Only replicate VMs in RUNNING state
3. **Queue**: Add volumes to worker pool for parallel processing
4. **Wait**: Block until all queued volumes finish processing
5. **Prune**: Remove backups for volumes that no longer exist locally

### Per-Volume Replication

For each volume, the worker performs:

1. **Create Snapshot**: Create a new ZFS snapshot with prefix `repl-` and UTC timestamp
   ```
   repl-20260128T143022Z
   ```

2. **Determine Send Type**:
   - Check remote for existing snapshots
   - Find most recent remote snapshot that exists locally
   - If found: incremental send from that base
   - If not found: full send

3. **Transfer**: Pipe `zfs send` to the target (SSH or file)

4. **Remote Retention**: Delete oldest remote replication snapshots to maintain retention count

5. **Local Cleanup**: Keep only the most recent local replication snapshot (for incremental base)

### Snapshot Naming

Replication snapshots use the format:
```
repl-YYYYMMDDTHHMMSSZ
```

Example: `repl-20260128T143022Z` (January 28, 2026 at 14:30:22 UTC)

## Restore

Volumes can be restored from any remote snapshot:

```bash
exelet-ctl storage replication restore <volume-id> <snapshot-name>
```

### Restore Flow

1. Stop VM if running
2. Check if snapshot exists locally → rollback if yes
3. If not local, verify snapshot exists on remote
4. Destroy local dataset
5. Receive snapshot from remote
6. Start VM

The restore automatically uses the remote when the snapshot doesn't exist locally, eliminating the need for a `--force` flag.

## CLI Commands

### Status
```bash
exelet-ctl storage replication status
```
Shows replication status, queue, and next scheduled run.

### Trigger
```bash
exelet-ctl storage replication trigger [volume-id]
```
Trigger immediate replication for all volumes or a specific volume.

### List Snapshots
```bash
exelet-ctl storage replication list <volume-id>
```
List remote snapshots for a volume. Use `--local` to also show local snapshots.

### History
```bash
exelet-ctl storage replication history [-n limit]
```
Show recent snapshots across all volumes on the remote target.

### Restore
```bash
exelet-ctl storage replication restore <volume-id> <snapshot-name>
```
Restore a volume from a remote snapshot.

## Target Types

### SSH Target

Replicates to a remote ZFS pool over SSH.

```
ssh://user@host[:port]/pool
```

- Uses `zfs send | ssh | zfs recv -F` pipeline
- Supports bandwidth limiting via `pv`
- Reuses SSH connections for efficiency
- SSH authentication: explicit key, SSH agent, or default keys (`~/.ssh/id_ed25519`, `~/.ssh/id_rsa`)
- Host key verification via `known_hosts` file

### File Target

Creates compressed `zfs send` streams locally.

```
file:///path/to/backups
```

- Uses `zfs send -c | gzip > file` for backups
- Uses `gunzip | zfs receive -F` for restore
- Supports incremental sends
- Primarily for testing or local backup

## Failure Handling

- **Transient Failures**: Retries up to 3 times with exponential backoff
- **Failed Snapshots**: Cleaned up on send failure
- **Connection Issues**: SSH connections are pooled and revalidated
- **Queue Blocking**: Volume queue blocks on context cancellation rather than silently dropping volumes

## Pruning

The pruner removes backups for volumes that no longer exist locally:

- Only prunes volumes whose IDs are valid UUIDs (instance IDs), to avoid deleting unrelated datasets on a shared pool
- Supports both volume-level deletion (if the target implements `VolumeDeleter`) and individual snapshot deletion as a fallback
- Runs at the end of each replication cycle after all volumes finish

## Metrics

The service exposes Prometheus metrics:

- `exelet_replication_success_total`: Successful replications
- `exelet_replication_failure_total`: Failed replications
- `exelet_replication_bytes_total`: Bytes transferred
- `exelet_replication_duration_seconds`: Replication duration
- `exelet_replication_queue_size`: Current queue size
- `exelet_replication_last_success_timestamp`: Last successful replication

## Design Decisions

### Only Replicate Running VMs

Stopped VMs are not replicated because:
- Their storage is not actively changing
- Reduces unnecessary snapshots and transfers
- Avoids issues with VMs in transitional states

Stopped VMs still retain their remote backups (not pruned).

### Single Local Snapshot Retention

Only the most recent replication snapshot is kept locally:
- Keeps local pool clean
- Sufficient for incremental sends (only need common snapshot with remote)
- Remote is the source of truth for backup history

### Remote as Source of Truth

The remote target is authoritative for backup history:
- `history` command queries remote snapshots
- `list` command shows remote by default
- Restore prefers remote when local snapshot missing

### Automatic VM Restart After Restore

VMs are automatically started after restore if the instance exists:
- Users expect the VM to be usable after restore
- Starting an already-running VM is harmless

### ZFS Rollback Limitation

If a volume is restored to an earlier snapshot via `zfs rollback`, all newer snapshots on that dataset are destroyed. The next replication cycle will perform a full send since the common incremental base no longer exists locally. This is a fundamental ZFS constraint.
