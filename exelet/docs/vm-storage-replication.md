# VM Storage Replication

This document describes the design and operation of the exelet storage replication system.

## Overview

Storage replication provides continuous backup of VM volumes to a remote target. It uses ZFS snapshots and incremental sends to efficiently replicate data with minimal bandwidth usage.

## Architecture

```
┌─────────────────┐                    ┌─────────────────┐
│     Exelet       │                    │  Remote Target   │
│                  │                    │                  │
│  ┌────────────┐  │   zfs send/recv   │  ┌────────────┐  │
│  │ ZFS Pool   │──┼───────────────────┼─▶│ ZFS Pool   │  │
│  │ tank/vm-*  │  │     (SSH)         │  │ tank/vm-*  │  │
│  └────────────┘  │                   │  └────────────┘  │
│                  │                    │                  │
└─────────────────┘                    └─────────────────┘
```

### Components

- **Replication Service**: Orchestrates periodic replication cycles
- **Worker Pool**: Handles concurrent replication jobs
- **Target**: Abstraction for remote destinations (SSH, system SSH, or file-based)
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
| `--storage-replication-ssh-command` | - | System SSH binary (e.g., `ssh`). When set, uses the system SSH binary instead of Go's built-in SSH client, allowing Tailscale SSH, ProxyCommand, and other system SSH config to work. |
| `--storage-replication-known-hosts` | ~/.ssh/known_hosts | Path to known_hosts file for SSH host key verification |
| `--storage-replication-bandwidth-limit` | - | Rate limit (e.g., "100M", "1G") |
| `--storage-replication-prune` | true | Enable pruning of orphaned backups |

## Operation

### Replication Cycle

1. **Dataset Discovery**: List all datasets from the storage manager
2. **Filter**: Skip temporary image extraction datasets (`tmp-sha256:` prefix)
3. **Volume ID Mapping**: Map local dataset IDs to remote-safe IDs (see below)
4. **Queue**: Add volumes to worker pool for parallel processing
5. **Wait**: Block until all queued volumes finish processing
6. **Prune**: Remove backups for volumes that no longer exist locally
7. **Base Image Pruning**: Remove orphaned base images (`sha256:` datasets with no dependent clones)

### Volume ID Mapping

VM instance IDs (matching `vm\d{6}-*`, e.g., `vm000123-blue-falcon`) are globally unique and used as-is on the remote. Non-VM datasets get a `-<nodeName>` suffix appended to avoid collisions when multiple exelets replicate to the same target pool.

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

3. **Transfer**: Pipe `zfs send` to the target (SSH or file), with up to 3 retries and backoff (0s, 5s, 30s). If incremental send fails after all retries, destroys the remote dataset and retries as a full send.

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
exelet-ctl storage replication restore <volume-id> <snapshot-name> --force
```

### Restore Flow

1. Mark volume as restoring (prevents concurrent snapshotting)
2. Check if snapshot exists locally:
   - If yes: require `--force` (rollback destroys newer snapshots), stop VM if running, rollback
3. If not local:
   - Require `--force` if dataset already exists
   - Verify snapshot exists on remote
   - Stop VM if running
   - Destroy local dataset if it exists
   - Receive snapshot from remote
4. Start VM if it was running before restore

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
exelet-ctl storage replication restore <volume-id> <snapshot-name> --force
```
Restore a volume from a remote snapshot or rollback to a local snapshot.

## Target Types

### SSH Target (Go built-in)

Default when `--storage-replication-ssh-command` is not set. Replicates to a remote ZFS pool over SSH using Go's built-in SSH client.

```
ssh://user@host[:port]/pool
```

- Uses `zfs send | ssh | zfs recv -F` pipeline
- Supports bandwidth limiting via `pv`
- Reuses SSH connections for efficiency (connection pooling with keepalive)
- SSH authentication: explicit key, SSH agent, or default keys (`~/.ssh/id_ed25519`, `~/.ssh/id_rsa`)
- Host key verification via `known_hosts` file

### System SSH Target

Used when `--storage-replication-ssh-command` is set (e.g., `--storage-replication-ssh-command=ssh`). Replicates using the system SSH binary instead of Go's built-in client.

```
ssh://user@host[:port]/pool
```

- Uses `zfs send | ssh zfs recv -F` pipeline (system processes)
- Supports Tailscale SSH, ProxyCommand, and other system SSH config
- Supports bandwidth limiting via `pv`
- SSH options: BatchMode=yes, `-T` (no pseudo-terminal)
- When `--storage-replication-known-hosts` is not set, disables strict host key checking

### File Target

Creates compressed `zfs send` streams locally.

```
file:///path/to/backups
```

- Uses `zfs send -c | gzip > file` for backups (with `-c` for compressed block passthrough)
- Uses `gunzip | zfs receive -F` for restore
- Supports incremental sends
- Backup filenames: `<volumeID>-<timestamp>.tar.gz` (e.g., `vm000123-20260128T143022Z.tar.gz`)
- Primarily for testing or local backup

## Failure Handling

- **Transient Failures**: Retries up to 3 times with backoff (0s, 5s, 30s)
- **Incremental Fallback**: If incremental send fails after retries, destroys remote dataset and retries as full send
- **Failed Snapshots**: Cleaned up on send failure
- **Connection Issues**: Go SSH connections are pooled and revalidated via keepalive
- **Queue Blocking**: Volume queue blocks on context cancellation rather than silently dropping volumes

## Pruning

The pruner removes backups for volumes that no longer exist locally:

- Only prunes volumes in this node's namespace:
  - VM instance IDs (`vm\d{6}-*`): globally unique, always considered
  - Non-VM datasets ending with `-<nodeName>`: belong to this node
- Skips base image datasets (`sha256:`, `tmp-sha256:`) and other nodes' datasets
- Supports both volume-level deletion (if the target implements `VolumeDeleter`) and individual snapshot deletion as a fallback
- Runs at the end of each replication cycle after all volumes finish
- Orphaned base images (`sha256:` datasets with no dependent clones) are also pruned separately

## Metrics

The service exposes Prometheus metrics:

- `exelet_replication_operations_total{status,target_type}`: Replication operations by status (`success` or `failed`)
- `exelet_replication_bytes_total{volume_id,target_type}`: Bytes transferred
- `exelet_replication_duration_seconds{volume_id,target_type}`: Replication duration (histogram)
- `exelet_replication_queue_size`: Current queue size
- `exelet_replication_last_success_timestamp`: Last successful replication cycle (Unix timestamp)

## Design Decisions

### Replicate All Datasets

All datasets from the storage manager are replicated, regardless of VM state. Temporary image extraction datasets (`tmp-sha256:` prefix) are excluded.

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

VMs are automatically started after restore if they were running before:
- Users expect the VM to be usable after restore
- Only restarts if the VM was previously running

### ZFS Rollback Limitation

If a volume is restored to an earlier snapshot via `zfs rollback`, all newer snapshots on that dataset are destroyed. The next replication cycle will perform a full send since the common incremental base no longer exists locally. This is a fundamental ZFS constraint.
