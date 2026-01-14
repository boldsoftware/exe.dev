# VM Cold Migration

This document describes the VM cold migration feature for the exelet, which enables migrating stopped VMs between exelets.

## Overview

VM cold migration transfers a stopped VM's disk (including its ZFS snapshot tree) and configuration from one exelet to another. The feature uses ZFS send/recv for efficient disk transfer and gRPC bidirectional streaming for the API.

## API

Two new gRPC RPCs in `ComputeService`:

- `SendVM(stream SendVMRequest) returns (stream SendVMResponse)` - Streams a stopped VM's disk and config
- `ReceiveVM(stream ReceiveVMRequest) returns (stream ReceiveVMResponse)` - Receives a VM from another exelet

### SendVM Flow

1. Client sends `SendVMStartRequest` with instance ID and whether target has base image
2. Server locks instance for migration (prevents Start/Stop/Delete/Update)
3. Server validates VM is stopped
4. Server sends `SendVMMetadata` (instance config, base image ID, encryption key)
5. Server creates migration snapshot (`{dataset}@migration`)
6. Server streams ZFS send data in 64KB chunks (`SendVMDataChunk`)
7. Server sends `SendVMComplete` with SHA256 checksum
8. Server cleans up migration snapshot and unlocks instance

### ReceiveVM Flow

1. Client sends `ReceiveVMStartRequest` with instance ID, source config, encryption key
2. Server checks instance doesn't already exist
3. Server checks if base image exists locally
4. Server sends `ReceiveVMReady` with `has_base_image` flag
5. Server stores encryption key if provided
6. Server starts `zfs recv` process
7. Client streams data chunks, server pipes to zfs recv
8. Server verifies checksum
9. Server creates instance directory and kernel
10. Server saves instance config (state=STOPPED)
11. Server sends `ReceiveVMResult` with created instance

## Transfer Mode

Migration always uses a full (non-incremental) ZFS stream:

```
zfs send {instance}@migration
```

This creates an independent dataset on the target (not a clone of the base image). We use full streams because ZFS incremental streams contain the GUID of the origin snapshot. Even if the target has the same base image, its snapshots will have different GUIDs, causing `zfs recv` to fail with "local origin for clone does not exist".

Full streams are larger than incremental streams but work reliably regardless of the target's existing snapshots.

## Migration Locking

During migration, the source instance is locked to prevent concurrent operations:

- `StartInstance` - blocked (would conflict with disk being sent)
- `StopInstance` - blocked (already stopped, but block for consistency)
- `DeleteInstance` - blocked (would destroy data being sent)
- `UpdateInstance` - blocked (would modify config during send)

The lock is held from the start of SendVM until completion or failure.

## Encryption

Encrypted VMs are supported. The encryption key is:

1. Read from `/var/lib/exelet/storage/volumes/{id}/encryption.key` on source
2. Sent in `SendVMMetadata.encryption_key`
3. Written to the same path on target before `zfs recv`

ZFS automatically uses the key for decryption based on the dataset's `keylocation` property.

## Rollback

If ReceiveVM fails after partially creating resources, rollback cleans up:

1. ZFS dataset (via `StorageManager.Delete`)
2. Instance directory
3. Encryption key (stored in volumes dir, cleaned with dataset)

Rollback uses `context.WithoutCancel` to ensure cleanup completes even if the client disconnects.

## Files

### New Files

| File | Description |
|------|-------------|
| `exelet/storage/zfs/migration.go` | ZFS send/recv helpers |
| `exelet/services/compute/send_vm.go` | SendVM implementation |
| `exelet/services/compute/receive_vm.go` | ReceiveVM implementation |

### Modified Files

| File | Change |
|------|--------|
| `api/exe/compute/v1/compute.proto` | SendVM/ReceiveVM RPCs and messages |
| `exelet/storage/storage.go` | Extended interface with migration methods |
| `pkg/api/exe/compute/v1/errors.go` | Added `ErrMigrating` |
| `exelet/services/compute/compute.go` | Migration locking |
| `exelet/services/compute/start_instance.go` | Migration lock check |
| `exelet/services/compute/stop_instance.go` | Migration lock check |
| `exelet/services/compute/delete_instance.go` | Migration lock check |
| `exelet/services/compute/update_instance.go` | Migration lock check |

## StorageManager Interface

New methods added for migration:

```go
// GetDatasetName returns the full dataset name for an ID (e.g., "tank/instance-id")
GetDatasetName(id string) string

// GetOrigin returns the origin (parent snapshot) of a dataset, or empty string if none
GetOrigin(id string) string

// CreateMigrationSnapshot creates a snapshot for migration and returns its name and a cleanup function
CreateMigrationSnapshot(ctx context.Context, id string) (snapName string, cleanup func(), err error)

// SendSnapshot streams ZFS snapshot data. If incremental is true, sends only delta from baseSnap.
SendSnapshot(ctx context.Context, snapName string, incremental bool, baseSnap string) (io.ReadCloser, error)

// ReceiveSnapshot receives a ZFS stream and creates/updates a dataset
ReceiveSnapshot(ctx context.Context, id string, reader io.Reader) error

// GetEncryptionKey returns the encryption key for an encrypted dataset, or nil if not encrypted
GetEncryptionKey(id string) ([]byte, error)

// SetEncryptionKey stores an encryption key for a dataset
SetEncryptionKey(id string, key []byte) error
```

## Usage Example

Migration between two exelets (pseudo-code):

```go
// On orchestrator (e.g., exed)
sourceClient := exelet.NewClient(sourceAddr)
targetClient := exelet.NewClient(targetAddr)

// Start send on source
// We use TargetHasBaseImage=true to get a full stream, which works reliably
// regardless of what snapshots exist on the target.
sendStream, _ := sourceClient.SendVM(ctx)
sendStream.Send(&SendVMRequest{
    Type: &SendVMRequest_Start{
        Start: &SendVMStartRequest{
            InstanceID: instanceID,
            TargetHasBaseImage: true,
        },
    },
})

// Receive metadata from source
resp, _ := sendStream.Recv()
metadata := resp.GetMetadata()

// Start receive on target
recvStream, _ := targetClient.ReceiveVM(ctx)

// Send start to target
recvStream.Send(&ReceiveVMRequest{
    Type: &ReceiveVMRequest_Start{
        Start: &ReceiveVMStartRequest{
            InstanceID:     instanceID,
            SourceInstance: metadata.Instance,
            BaseImageID:    metadata.BaseImageID,
            Encrypted:      metadata.Encrypted,
            EncryptionKey:  metadata.EncryptionKey,
        },
    },
})

// Receive ready from target (tells us if target has base image)
recvResp, _ := recvStream.Recv()
ready := recvResp.GetReady()
// ready.HasBaseImage indicates if target already has the base image

// Pipe data from source to target
for {
    resp, err := sendStream.Recv()
    if err == io.EOF {
        break
    }
    if data := resp.GetData(); data != nil {
        recvStream.Send(&ReceiveVMRequest{
            Type: &ReceiveVMRequest_Data{
                Data: &ReceiveVMDataChunk{
                    Data: data.Data,
                },
            },
        })
    }
    if complete := resp.GetComplete(); complete != nil {
        recvStream.Send(&ReceiveVMRequest{
            Type: &ReceiveVMRequest_Complete{
                Complete: &ReceiveVMComplete{
                    Checksum: complete.Checksum,
                },
            },
        })
        break
    }
}

// Get result from target
recvResp, _ = recvStream.Recv()
result := recvResp.GetResult()
// result.Instance is the migrated VM on target

// Optionally delete from source
sourceClient.DeleteInstance(ctx, &DeleteInstanceRequest{ID: instanceID})
```

## CLI Usage

The `exelet-ctl` tool provides a `migrate` command for VM migration:

```bash
# Migrate a stopped VM to another exelet
exelet-ctl --addr tcp://source-host:9080 compute instances migrate <instance-id> tcp://target-host:9080

# Migrate and delete from source after success
exelet-ctl --addr tcp://source-host:9080 compute instances migrate --delete <instance-id> tcp://target-host:9080
```

The command:
1. Connects to both source and target exelets
2. Starts SendVM stream on source, ReceiveVM stream on target
3. Pipes the VM data between them with progress display
4. Optionally deletes the VM from source after successful migration

## Known Limitations

- **No flow control**: The migration protocol relies on gRPC's internal buffering for flow control. This should work for typical workloads. If flow control becomes necessary in the future, the protocol would need to be extended with ack messages and clients updated to drain them concurrently while sending data.

- **No space savings from shared base images**: Migrations always send a full stream, creating an independent dataset on the target (not a clone of any base image). This uses more disk space than if we could share base images across migrated VMs. We cannot use incremental streams because ZFS requires matching snapshot GUIDs, which we can't guarantee across different exelets.

## Future Work

- **exed orchestration**: Add API for exed to initiate migrations and update database locality ✓ (implemented in debug UI)
- **Live migration**: Support migrating running VMs (requires memory state transfer)
- **Flow control**: Add ack messages and implement proper flow control if needed for large transfers
- **Resumable transfers**: Support resuming failed transfers
