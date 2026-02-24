# Recovering Data from an EBS Snapshot

## 1. Find the snapshot

List snapshots for a specific volume:

```bash
aws ec2 describe-snapshots \
  --filters "Name=volume-id,Values=vol-XXXX" \
  --query 'Snapshots[].[SnapshotId,VolumeSize,StartTime,Description]' \
  --output text
```

Or search by description/tag if you don't know the volume ID.

## 2. Find a recovery instance in the right AZ

Snapshots are region-wide, but volumes are AZ-specific. Find which AZ
your target instance is in:

```bash
aws ec2 describe-instances \
  --instance-ids i-XXXX \
  --query 'Reservations[].Instances[].Placement.AvailabilityZone' \
  --output text
```

If you don't have an instance in that AZ, you can create the volume in
whatever AZ your recovery instance is in.

## 3. Create a volume from the snapshot

```bash
aws ec2 create-volume \
  --snapshot-id snap-XXXX \
  --availability-zone us-west-2b \
  --volume-type gp3
```

Wait for it:

```bash
aws ec2 wait volume-available --volume-ids vol-NEW
```

## 4. Attach the volume

Pick a device name that doesn't conflict with existing attachments. Check
what's already attached:

```bash
aws ec2 describe-instances \
  --instance-ids i-XXXX \
  --query 'Reservations[].Instances[].BlockDeviceMappings[].[DeviceName,Ebs.VolumeId]' \
  --output text
```

Important: if the instance already has a ZFS pool on `/dev/xvdf`, do NOT
attach the new volume as `/dev/xvdf` -- the device names will collide.
Use a different name like `/dev/xvdg`:

```bash
aws ec2 attach-volume \
  --volume-id vol-NEW \
  --instance-id i-XXXX \
  --device /dev/xvdg
```

Note: AWS may remap device names (e.g. xvdg may show up as xvdf if that
slot is free). Always verify with `lsblk` on the instance.

## 5. Verify the volume is visible

```bash
lsblk
```

Look for the new device (e.g. `xvdg` with the expected size and a
partition layout like `xvdg1` + `xvdg9`).

## 6. Import the ZFS pool

First, list importable pools on the new device:

```bash
sudo zpool import -d /dev/xvdg1
```

If the pool has the same name as an existing pool (e.g. both are called
"tank"), import it under a different name. Use `-f` because the pool was
last accessed by another system, and `-N` to skip mounting filesystems:

```bash
sudo zpool import -d /dev/xvdg1 -f -N tank oldtank
```

Verify:

```bash
sudo zpool list
sudo zfs list -r oldtank
```

## 7. Mount a zvol

Our VM disks are ZFS zvols containing ext4 filesystems. To mount one:

```bash
# Check the zvol device exists
ls /dev/zvol/oldtank/vm014471-lagoon-king

# Mount it read-only
sudo mkdir -p /mnt/recovered
sudo mount -o ro /dev/zvol/oldtank/vm014471-lagoon-king /mnt/recovered
```

To also mount the parent image (the origin snapshot the zvol was cloned from):

```bash
sudo zfs get origin oldtank/vm014471-lagoon-king
# Use the origin dataset name (without the @snapshot part) to find its zvol
sudo mkdir -p /mnt/parent
sudo mount -o ro /dev/zvol/oldtank/sha256:XXXX /mnt/parent
```

## 8. Clean up

When done, unmount and export the pool, then detach and delete the volume:

```bash
sudo umount /mnt/recovered /mnt/parent
sudo zpool export oldtank

aws ec2 detach-volume --volume-id vol-NEW
aws ec2 wait volume-available --volume-ids vol-NEW
aws ec2 delete-volume --volume-id vol-NEW
```

## Gotchas

- **AZ mismatch**: volumes must be in the same AZ as the instance. If
  not, create the volume in the correct AZ (snapshots are not AZ-bound).
- **Device name collisions**: if the instance already uses `/dev/xvdf`
  for its own ZFS pool, attaching a second volume as xvdf will cause ZFS
  to get confused. Always use a distinct device name.
- **Pool name/GUID conflicts**: `zpool import` silently hides pools
  whose GUID matches an already-imported pool. Use `zdb -l /dev/xvdg1`
  to read the label directly and `zpool import -f -N tank othername` to
  import under a different name.
- **Force detach is dangerous**: `aws ec2 detach-volume --force` while
  ZFS has the device open will SUSPEND the pool. If this happens, you
  may need to reboot the instance to recover.
- **Suspended pools block shutdown**: a SUSPENDED zpool can make instance
  stop/reboot take a very long time. Be patient or use the AWS console
  to force-stop.
