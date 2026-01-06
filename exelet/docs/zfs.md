# ZFS Administration Guide

This document covers ZFS usage in the exelet and common administration commands.

## Overview

The exelet uses ZFS as its primary storage backend for VM persistent disks. Each instance gets a ZFS volume (zvol) that appears as a block device at `/dev/zvol/<pool>/<instance-id>`.

Key characteristics:
- **Thin provisioning**: Volumes don't reserve space upfront
- **LZ4 compression**: Enabled by default for space efficiency
- **Copy-on-write cloning**: Instance creation from images is nearly instant
- **4K block size**: Optimized for VM workloads

### Storage Layout

```
ZFS Pool (tank)
├── sha256:abc123...       # base image (pulled from registry)
├── sha256:def456...       # another base image
├── <instance-id>          # instance volume (cloned from image)
└── ...

Filesystem:
/var/lib/exelet/storage/
└── mounts/<instance-id>/  # temporary mount during instance create/update
```

## Base Images

Base images are container images pulled from registries (e.g., Docker Hub) and stored as ZFS volumes. The volume name is the image digest, e.g., `tank/sha256:abc123...`.

### How Images Are Loaded

1. User requests an instance with an image (e.g., `ubuntu:latest`)
2. Exelet resolves the tag to a digest (e.g., `sha256:a1b2c3...`)
3. If the image volume doesn't exist:
   - Create a temporary 10GB sparse volume (`tmp-sha256:...`)
   - Mount and extract the container image contents
   - Run fsck to validate the filesystem
   - Rename to final name (`sha256:...`)
4. Clone the image volume to create the instance volume

### Listing Images

```bash
# List all base images (volumes starting with sha256:)
zfs list -t volume | grep sha256:

# Example output:
# tank/sha256:a1b2c3d4e5f6...   10G   847M   847M  -
# tank/sha256:f6e5d4c3b2a1...   10G   1.2G   1.2G  -

# Show image sizes and compression
zfs list -t volume -o name,volsize,used,refer,compressratio | grep sha256:
```

### Image Snapshots

When an instance is created, a snapshot is taken of the base image:

```bash
# List snapshots (shows which instances cloned from which images)
zfs list -t snapshot

# Example output:
# tank/sha256:a1b2c3...@inst_abc123   0B  -  847M  -
# tank/sha256:a1b2c3...@inst_def456   0B  -  847M  -

# The snapshot name is the instance ID that was cloned from this image
```

### Cleaning Up Unused Images

Images accumulate over time. To find images with no dependent clones:

```bash
# List images and their snapshot count (0 = no instances using it)
for img in $(zfs list -H -t volume -o name | grep sha256:); do
  snaps=$(zfs list -H -t snapshot -o name -r "$img" 2>/dev/null | wc -l)
  echo "$img: $snaps snapshots"
done

# Check if an image has dependents before destroying
zfs list -t snapshot -r tank/sha256:abc123...

# Destroy an unused image (fails if snapshots/clones exist)
zfs destroy tank/sha256:abc123...

# Destroy image and its snapshots (fails if clones exist)
zfs destroy -r tank/sha256:abc123...

# DANGER: Force destroy image, snapshots, AND all dependent clones.
# This will DELETE all instance volumes that were created from this image!
# Only use this if you're sure no running VMs depend on this image.
zfs destroy -R tank/sha256:abc123...
```

### Temporary Image Volumes

During image loading, temporary volumes are created with a `tmp-` prefix. These should be cleaned up automatically, but if loading fails mid-way:

```bash
# Find orphaned temp volumes
zfs list -t volume | grep "tmp-sha256:"

# Clean up manually if needed
zfs destroy tank/tmp-sha256:abc123...
```

## Basic Commands

### Pool Status

```bash
# List all pools with size/usage
zpool list

# Example output:
# NAME   SIZE  ALLOC   FREE  CKPOINT  EXPANDSZ   FRAG    CAP  DEDUP    HEALTH  ALTROOT
# tank   928G   153G   775G        -         -     7%    16%  1.00x    ONLINE  -

# Detailed pool status (health, errors, scrub status)
zpool status

# Pool I/O statistics (refresh every 5 seconds)
zpool iostat 5
```

### Dataset/Volume Listing

```bash
# List all datasets and volumes
zfs list

# List only volumes (zvols)
zfs list -t volume

# List with specific properties
zfs list -o name,used,avail,refer,mountpoint

# List volumes in a specific pool
zfs list -r tank
```

### Volume Properties

```bash
# Get all properties for a volume
zfs get all tank/<instance-id>

# Get specific properties
zfs get volsize,used,compressratio tank/<instance-id>

# Common properties to check:
#   volsize       - Logical size of the volume
#   used          - Actual space consumed (after compression)
#   referenced    - Data referenced by this volume
#   compressratio - Compression ratio achieved
```

### Snapshots

```bash
# List snapshots
zfs list -t snapshot

# Create a snapshot
zfs snapshot tank/<instance-id>@<snapshot-name>

# Delete a snapshot
zfs destroy tank/<instance-id>@<snapshot-name>
```

## Common Operations

### Check Volume Health

```bash
# Run a scrub to verify data integrity
zpool scrub tank

# Check scrub progress
zpool status tank

# View error counts
zpool status -v tank
```

### Space Usage

```bash
# Pool-level usage
zpool list -o name,size,alloc,free,cap,health

# Per-volume usage (sorted by size)
zfs list -t volume -o name,volsize,used,refer -s used

# Check compression savings
zfs get compressratio tank
```

### Manual Volume Operations

These are typically handled by the exelet, but useful for debugging.

**WARNING**: Only perform these operations when the VM is stopped. The VM uses the
zvol as a raw block device. Mounting or running fsck while the VM is running will
cause filesystem corruption.

```bash
# Check zvol device exists
ls -la /dev/zvol/tank/<instance-id>

# Check filesystem on zvol (VM must be stopped)
e2fsck -n /dev/zvol/tank/<instance-id>

# Mount a volume manually for inspection (VM must be stopped)
mount /dev/zvol/tank/<instance-id> /mnt/inspect

# Unmount
umount /mnt/inspect
```

## Troubleshooting

### Volume Won't Delete

If a volume is busy:

```bash
# Check what's using it
lsof /dev/zvol/tank/<instance-id>
fuser -m /dev/zvol/tank/<instance-id>

# Force unmount if needed
umount -f /var/lib/exelet/storage/mounts/<instance-id>

# Then destroy
zfs destroy tank/<instance-id>
```

### Pool Running Low on Space

```bash
# Find largest volumes
zfs list -t volume -o name,used -s used | tail -20

# Check for orphaned snapshots
zfs list -t snapshot -o name,used -s used

# Check actual vs logical usage (compression)
zfs list -o name,used,logicalused,compressratio
```

### Slow I/O

```bash
# Check pool I/O stats
zpool iostat -v 5

# Check for degraded devices
zpool status

# Check ARC (cache) stats
arc_summary  # or cat /proc/spl/kstat/zfs/arcstats
```

### Recovery

```bash
# Import pool after system recovery
zpool import tank

# If pool won't import normally
zpool import -f tank

# Clear transient errors after fixing issues
zpool clear tank
```

## Volume Properties Reference

Properties set by exelet when creating volumes:

| Property | Value | Purpose |
|----------|-------|---------|
| `compression` | `lz4` | Fast compression for space savings |
| `volblocksize` | `4K` | Optimized for random I/O |
| `primarycache` | `metadata` | Prevents double-caching with guest OS |
| `logbias` | `latency` | Optimized for random workloads |
| `sync` | `standard` | ZFS handles fsync durability |
| `refreservation` | `none` | Thin provisioning (no space pre-allocation) |
