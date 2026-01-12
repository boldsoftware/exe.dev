# VM Storage Manual Resize

This runbook describes how to manually resize a VM's persistent disk.

## Prerequisites

- SSH access to the exelet host
- The instance ID of the VM to resize
- The target size for the volume

## Procedure

### 1. Stop the Instance

Stop the VM to ensure the disk is not in use:

```bash
exelet-ctl --addr <exelet-addr> instances stop <instance-id>
```

Verify the instance is stopped:

```bash
exelet-ctl --addr <exelet-addr> instances get <instance-id>
```

### 2. Create a Backup Snapshot

Before modifying the volume, create a temporary backup snapshot:

```bash
zfs snapshot tank/<instance-id>@resize-backup
```

This allows rollback if something goes wrong:

```bash
# To rollback (destroys all changes since snapshot):
zfs rollback tank/<instance-id>@resize-backup
```

### 3. Resize the ZFS Volume

Increase the volume size:

```bash
zfs set volsize=<new-size> tank/<instance-id>
```

For example, to resize to 50GB:

```bash
zfs set volsize=50G tank/<instance-id>
```

Verify the new size:

```bash
zfs get volsize tank/<instance-id>
```

### 4. Check and Resize the Filesystem

Run fsck to ensure filesystem integrity before resizing:

```bash
e2fsck -f /dev/zvol/tank/<instance-id>
```

Resize the ext4 filesystem to fill the volume:

```bash
resize2fs /dev/zvol/tank/<instance-id>
```

Run fsck again to verify the resized filesystem:

```bash
e2fsck -f /dev/zvol/tank/<instance-id>
```

### 5. Start the Instance

Start the VM:

```bash
exelet-ctl --addr <exelet-addr> instances start <instance-id>
```

### 6. Verify the Instance Boots

Check the instance logs to ensure it boots successfully:

```bash
exelet-ctl --addr <exelet-addr> instances logs <instance-id>
```

Look for normal boot messages and verify there are no filesystem errors.

You can also verify the new size from inside the VM:

```bash
df -h /
```

### 7. Remove the Backup Snapshot

Once verified, remove the temporary backup snapshot to reclaim space:

```bash
zfs destroy tank/<instance-id>@resize-backup
```

## Rollback

If the VM fails to boot after resizing, rollback to the backup snapshot:

```bash
# Stop the instance if it's in a failed state
exelet-ctl --addr <exelet-addr> instances stop <instance-id>

# Rollback to the snapshot
zfs rollback tank/<instance-id>@resize-backup

# Start the instance
exelet-ctl --addr <exelet-addr> instances start <instance-id>

# Verify it boots
exelet-ctl --addr <exelet-addr> instances logs <instance-id>

# If successful, remove the snapshot
zfs destroy tank/<instance-id>@resize-backup
```

## Notes

- This procedure only supports increasing disk size, not shrinking
- The VM must be stopped during the entire resize operation
- Always create a backup snapshot before modifying volumes
- The `resize2fs` command without a size argument automatically expands to fill the available space
