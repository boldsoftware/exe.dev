# VM Migration

This document describes the VM migration system, which enables migrating VMs between exelets.

## Overview

VM migration transfers a VM's disk (ZFS dataset), configuration, and optionally its running process state from one exelet to another. The orchestrator (`exed`) mediates a bidirectional gRPC stream between the source exelet's `SendVM` and the target exelet's `ReceiveVM`, then updates the database with the new location.

Three migration modes are supported:

| Mode | VM State | Downtime | Description |
|------|----------|----------|-------------|
| Cold | Stopped | N/A | Full ZFS stream, target VM remains stopped |
| Two-Phase | Running | Brief (phase 2 delta) | Phase 1: snapshot live VM, send full stream. Phase 2: stop VM, send incremental delta |
| Live | Running | Near-zero | Two-phase ZFS + CH snapshot/restore with in-flight IP reconfiguration |

## API

Two gRPC RPCs in `ComputeService`:

- `SendVM(stream SendVMRequest) returns (stream SendVMResponse)` — Streams a VM's disk, config, and optionally process state
- `ReceiveVM(stream ReceiveVMRequest) returns (stream ReceiveVMResponse)` — Receives a VM from another exelet

### Message Types

**SendVM responses** (source → orchestrator → target):
- `SendVMMetadata` — Instance config, base image ID
- `SendVMDataChunk` — 4MB ZFS send chunks
- `SendVMPhaseComplete` — Marks end of a transfer phase
- `SendVMAwaitControl` — Requests orchestrator action (e.g., IP reconfig)
- `SendVMSnapshotChunk` — CH snapshot file chunks (live only, zstd-compressed)
- `SendVMComplete` — SHA-256 checksum of all data

**SendVM requests** (orchestrator → source):
- `SendVMStartRequest` — Instance ID, mode flags (`TwoPhase`, `Live`), `TargetHasBaseImage`
- `SendVMControl` — Control signals (e.g., `PROCEED_WITH_PAUSE`)

**ReceiveVM requests** (orchestrator → target):
- `ReceiveVMStartRequest` — Instance ID, source config, mode flags
- `ReceiveVMDataChunk` — Echoed data chunks
- `ReceiveVMPhaseComplete` — Echoed phase markers
- `ReceiveVMSnapshotChunk` — Echoed CH snapshot chunks
- `ReceiveVMComplete` — Echoed checksum

**ReceiveVM responses** (target → orchestrator):
- `ReceiveVMReady` — `HasBaseImage` flag, allocated `TargetNetwork` (live only)
- `ReceiveVMResult` — Created instance, `ColdBooted` flag

## Cold Migration

The simplest mode. VM must be stopped.

### SendVM Flow

1. Client sends `SendVMStartRequest` with instance ID
2. Server locks instance for migration (prevents Start/Stop/Delete/Update)
3. Server suspends replication and waits for in-flight replication jobs
4. Server validates VM is stopped
5. Server sends `SendVMMetadata` (instance config, base image ID)
6. Server creates migration snapshot (`{dataset}@migration`)
7. Server streams ZFS send data in ~4MB chunks (`SendVMDataChunk`)
8. Server sends `SendVMComplete` with SHA-256 checksum
9. Server cleans up migration snapshot and unlocks instance

### ReceiveVM Flow

1. Client sends `ReceiveVMStartRequest` with instance ID, source config
2. Server locks instance for migration and suspends replication
3. Server checks instance doesn't already exist
4. Server checks if base image exists locally
5. Server sends `ReceiveVMReady` with `has_base_image` flag
6. Server starts `zfs recv` process and pipes incoming data chunks
7. Server verifies SHA-256 checksum
8. Server copies embedded kernel to instance directory
9. Server saves instance config (state=STOPPED)
10. Server sends `ReceiveVMResult` with created instance

## Two-Phase Migration

Allows migrating a running VM with only a brief pause during the final delta transfer.

### Flow

1. **Phase 1** (VM running): Create `@migration-pre` snapshot, send full ZFS stream. VM continues running throughout — writes accumulate as delta.
2. **Phase complete marker**: Source sends `SendVMPhaseComplete`, target completes `zfs recv` for phase 1.
3. **Phase 2** (VM stopped): Stop the VM, create `@migration` snapshot, send incremental diff from `@migration-pre` to `@migration`. This delta is typically small (only writes since phase 1 snapshot).
4. **Complete**: Verify checksum, save instance as STOPPED on target.

If the VM happens to stop on its own before phase 2, it reloads state and skips the stop.

Falls back to cold migration if the VM is already stopped when two-phase is requested.

## Live Migration

The most complex mode — preserves the VM's running process state across exelets. Combines two-phase ZFS transfer with CloudHypervisor (CH) snapshot/restore and in-flight IP reconfiguration.

### Flow

1. **Phase 1** (VM running): Same as two-phase — snapshot + full ZFS stream while VM runs.

2. **IP reconfiguration** (VM still running): After phase 1, the source sends `AwaitControl` with `NEED_IP_RECONFIG` and the source network config. The orchestrator then:
   - SSHes into the running VM
   - Detects the guest network interface: `ip -o addr show to {source_ip}`
   - Enables `promote_secondaries`: `echo 1 > /proc/sys/net/ipv4/conf/{dev}/promote_secondaries`
   - Adds target IP as secondary address: `ip addr add {target_ip}/{mask} dev {dev}`
   - The old IP is **not** removed yet — SSH stays alive

3. **Pause** (downtime begins): Orchestrator sends `PROCEED_WITH_PAUSE`. Source deflates the memory balloon (forces all pages back from host, preventing "Bad address" on restore), then pauses the VM via CH API.

4. **Phase 2** (VM paused): Incremental ZFS diff from `@migration-pre` to `@migration`.

5. **Phase 3 — CH snapshot**: Source creates a CH snapshot (JSON config + memory state files), streams each file as `SendVMSnapshotChunk` messages. Each chunk is independently zstd-compressed (skipped if incompressible).

6. **Restore on target**: Target edits the CH `config.json` — updates disk path, kernel path, and replaces the `ip=` kernel boot argument with the target IP. Then calls `RestoreFromSnapshot` to resume the VM.

7. **Old IP cleanup** (background): Orchestrator SSHes in with `nohup` to delete the old IP (`ip addr del`) and fix the default route (`ip route replace`). The SSH connection dies when the old IP is removed, but `promote_secondaries` ensures the new IP is promoted to primary.

8. **Complete**: Verify checksum. VM is running on target with new IP.

### IP Reconfiguration Timeline

```
T0  VM running on source with source_ip, source_gw
T1  Target allocates target_ip (in ReceiveVMReady)
T2  Orchestrator SSHes into VM, adds target_ip as secondary
T3  Orchestrator sends PROCEED_WITH_PAUSE
T4  Source deflates balloon, pauses VM (downtime starts)
T5  Source streams ZFS delta + CH snapshot
T6  Target edits CH config, restores VM (downtime ends)
T7  Orchestrator SSH removes old IP in background (promote_secondaries auto-promotes new IP)
T8  VM fully running on target with target_ip
```

### Live Migration Fallback

If CH snapshot restore fails (e.g., memory region issues):
1. Stop the failed CH process
2. Delete the network interface allocated for live migration
3. Clear network config from instance (prevents duplicate IPs)
4. Save instance as STOPPED
5. Cold boot the VM via `startInstance` (allocates fresh IP)
6. Trigger async IPAM reconciliation to clean up any orphaned leases
7. Return `ColdBooted=true` so the orchestrator knows (can send maintenance email)

### Memory Pre-flight Check

Before accepting a live migration, the target checks `/proc/meminfo` for available memory. If insufficient, it attempts up to 3 rounds of `memory.reclaim` (cgroup v2) to push idle VMs to swap. If memory is still insufficient after all attempts, the RPC fails with `ResourceExhausted`.

## Transfer Mode

Migration always uses a full (non-incremental) ZFS stream for the initial transfer:

```
zfs send {instance}@migration
```

ZFS incremental streams embed the GUID of the origin snapshot. Even if the target has the same base image, its snapshots have different GUIDs, so `zfs recv` would fail with "local origin for clone does not exist".

**Exception**: If `TargetHasBaseImage=false` and the source dataset is a clone, the source sends the base image as a full stream first, then the instance as an incremental from that origin (since the target just received the origin, GUIDs match).

Data is streamed in chunks of `4*1024*1024 - 1024` bytes (just under 4MB to leave room for protobuf framing within gRPC's 4MB message limit).

## Locking

### Migration Lock

The `migratingInstances` sync.Map prevents:
- Concurrent lifecycle operations (Start, Stop, Delete, Update) on a VM being migrated
- Concurrent migrations of the same instance (e.g., orchestrator retry + original still in-flight)

`lockForMigration` briefly acquires the per-instance lock to atomically set the migration flag, then releases it so lifecycle ops fail fast with `ErrMigrating` rather than blocking for the entire migration.

### Per-Instance Operation Lock

The `instanceOpLocks` map provides refcounted per-instance mutexes that serialize lifecycle operations. This prevents:
- Double IP allocation from concurrent Start calls
- IP leaks from concurrent Stop + Delete
- Config corruption from concurrent state changes

Lifecycle ops acquire the instance lock and check `checkNotMigrating` under that lock to prevent TOCTOU races.

### Replication Suspension

Before any migration data transfer, both `SendVM` and `ReceiveVM` suspend ZFS replication for the volume and wait for any in-flight replication job to complete (`WaitVolumeIdle`). This prevents replication snapshots from conflicting with migration snapshots. Replication resumes after migration completes (via defer).

## Rollback

If `ReceiveVM` fails after partially creating resources, `receiveVMRollback` cleans up:

| Resource | Condition | Cleanup |
|----------|-----------|---------|
| ZFS dataset | `zfsDatasetCreated` | `StorageManager.Delete` |
| Instance directory | `instanceDirCreated` or `snapshotDirCreated` | `os.RemoveAll` |
| Network interface | `targetNetwork != nil` (live) | `NetworkManager.DeleteInterface` |
| CH process | `stopVM` set after successful restore | `vmmgr.Stop` |
| Base image | created during transfer | **Not deleted** (shared resource, expensive to re-transfer) |

Rollback uses `context.WithoutCancel` to ensure cleanup completes even if the client disconnects.

## Orchestrator Coordination

The orchestrator (`exed`) coordinates the full migration lifecycle:

1. Look up box in database (current exelet location)
2. Connect to source and target exelet gRPC clients
3. Mediate the bidirectional stream (`migrateVM` or `migrateVMLive`)
4. For live migrations: SSH into VM to reconfigure IP
5. Update database with new `ctrhost`, `ssh_port`, `region`
6. Delete instance from source (best-effort)

Orchestration is currently exposed via debug endpoints (`/debug/vms/migrate`, `/debug/user/migrate-vms`).

## Files

| File | Description |
|------|-------------|
| `exelet/services/compute/send_vm.go` | SendVM implementation (cold, two-phase, live) |
| `exelet/services/compute/receive_vm.go` | ReceiveVM implementation + rollback |
| `exelet/services/compute/compute.go` | Migration locking, per-instance locks, IPAM reconciliation |
| `exelet/storage/zfs/migration.go` | ZFS send/recv helpers |
| `api/exe/compute/v1/compute.proto` | SendVM/ReceiveVM RPCs and messages |
| `pkg/api/exe/compute/v1/errors.go` | `ErrMigrating` error |
| `execore/debugsrv.go` | Orchestrator: `migrateVM`, `migrateVMLive`, `reconfigureVMIP` |
| `cmd/exelet-ctl/compute/instances/migrate.go` | `exelet-ctl migrate` CLI command |

## CLI Usage

```bash
# Migrate a stopped VM to another exelet
exelet-ctl --addr tcp://source-host:9080 compute instances migrate <instance-id> tcp://target-host:9080

# Migrate and delete from source after success
exelet-ctl --addr tcp://source-host:9080 compute instances migrate --delete <instance-id> tcp://target-host:9080
```

## Known Limitations

- **No flow control**: The protocol relies on gRPC's internal buffering. The proto defines `ReceiveVMAck` for future flow control, but clients don't drain acks concurrently while sending — sending acks without readers could fill the gRPC send buffer and deadlock.

- **No space savings from shared base images**: Full ZFS streams create independent datasets on the target (not clones). This uses more disk than sharing base images, but works reliably across exelets with different snapshot GUIDs.

- **Live migration requires SSH access**: IP reconfiguration is done by SSHing into the guest, which requires the orchestrator to have SSH access to the VM.

## Future Work

- **Flow control**: Add ack-based flow control if needed for large transfers (requires clients to drain acks concurrently)
- **Resumable transfers**: Support resuming failed transfers
