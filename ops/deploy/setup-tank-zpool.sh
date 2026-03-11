#!/bin/bash
set -euo pipefail

# Create a ZFS pool named 'tank' with a tank/data dataset mounted at /data.
#
# Usage:
#   setup-tank-zpool.sh [--force] [--yes] [device ...]
#
# With no device arguments, auto-detects NVMe instance-store devices,
# partitions them (25% swap, 75% data), and uses the data partitions.
#
# With explicit device arguments, uses those devices directly (no partitioning).
#
# Flags:
#   --force   Destroy existing 'tank' pool before creating
#   --yes     Skip interactive confirmation prompt

FORCE=false
YES=false
DEVICES=()

while [[ $# -gt 0 ]]; do
    case "$1" in
    --force)
        FORCE=true
        shift
        ;;
    --yes)
        YES=true
        shift
        ;;
    -*)
        echo "Unknown flag: $1" >&2
        exit 1
        ;;
    *)
        DEVICES+=("$1")
        shift
        ;;
    esac
done

# Resolve a /dev/ path to its /dev/disk/by-id/nvme-* symlink for stable device naming
resolve_by_id() {
    local dev="$1"
    local real
    real=$(readlink -f "$dev")
    for link in /dev/disk/by-id/nvme-*; do
        [ -L "$link" ] || continue
        if [ "$(readlink -f "$link")" = "$real" ]; then
            echo "$link"
            return 0
        fi
    done
    echo "WARNING: no /dev/disk/by-id link found for $dev, using raw path" >&2
    echo "$dev"
}

AUTO_DETECT=false

# Destroy existing tank pool early so devices are released before partitioning
if zpool list tank &>/dev/null; then
    if [ "$FORCE" = true ]; then
        echo "=== Destroying existing tank pool ==="
        if [ "$YES" != true ]; then
            echo "WARNING: This will destroy all data on the existing tank pool."
            read -r -p "Continue? [y/N] " confirm
            if [[ ! "$confirm" =~ ^[Yy] ]]; then
                echo "Aborted."
                exit 1
            fi
        fi
        sudo zpool destroy tank
    else
        echo "ERROR: tank pool already exists. Use --force to destroy and re-create." >&2
        exit 1
    fi
fi

if [ ${#DEVICES[@]} -eq 0 ]; then
    AUTO_DETECT=true

    # Detect NVMe instance-store devices by model string
    echo "=== Detecting NVMe instance-store devices ==="
    INSTANCE_STORE_DEVICES=()

    for dev in /dev/nvme*n1; do
        [ -b "$dev" ] || continue
        devname=$(basename "$dev")
        model=$(cat "/sys/block/${devname}/device/model" 2>/dev/null | xargs)
        size_gb=$(lsblk -b -n -d -o SIZE "$dev" 2>/dev/null | awk '{printf "%.0f", $1/1073741824}')

        # Safety: never touch a device that has mounted filesystems
        if lsblk -n -o MOUNTPOINT "$dev" 2>/dev/null | grep -q '/'; then
            echo "Mounted: $dev (${size_gb}GB) - skipping"
            continue
        fi

        if [ "$model" = "Amazon EC2 NVMe Instance Storage" ]; then
            echo "Instance-store: $dev (${size_gb}GB)"
            INSTANCE_STORE_DEVICES+=("$dev")
        fi
    done

    if [ ${#INSTANCE_STORE_DEVICES[@]} -eq 0 ]; then
        echo "ERROR: No instance-store NVMe devices found"
        lsblk
        exit 1
    fi

    echo "Found ${#INSTANCE_STORE_DEVICES[@]} instance-store device(s)"

    # Disable any existing swap on these devices before repartitioning
    for dev in "${INSTANCE_STORE_DEVICES[@]}"; do
        if [ -b "${dev}p1" ]; then
            sudo swapoff "${dev}p1" 2>/dev/null || true
        fi
    done
    # Clean stale swap entries from fstab
    sudo sed -i '\|^/dev/nvme.*swap|d' /etc/fstab 2>/dev/null || true

    # Partition each instance-store drive: 25% swap, 75% data
    echo ""
    echo "=== Partitioning instance-store NVMe devices (25% swap, 75% data) ==="
    SWAP_PARTS=()

    for dev in "${INSTANCE_STORE_DEVICES[@]}"; do
        size_bytes=$(lsblk -b -n -d -o SIZE "$dev")
        swap_gib=$((size_bytes / 4 / 1024 / 1024 / 1024))

        echo "Partitioning ${dev}: ${swap_gib}GiB swap, remainder for ZFS..."
        sudo wipefs -a "$dev" >/dev/null 2>&1 || true
        sudo parted -s "$dev" mklabel gpt
        sudo parted -s "$dev" mkpart primary linux-swap 1MiB "${swap_gib}GiB"
        sudo parted -s "$dev" mkpart primary "${swap_gib}GiB" 100%
        sudo udevadm settle || sleep 1

        sudo mkswap "${dev}p1"
        SWAP_PARTS+=("${dev}p1")
        DEVICES+=("${dev}p2")
    done

    # Resolve to /dev/disk/by-id paths
    echo ""
    echo "=== Resolving device paths to /dev/disk/by-id ==="
    RESOLVED=()
    for part in "${DEVICES[@]}"; do
        resolved=$(resolve_by_id "$part")
        echo "  $part -> $resolved"
        RESOLVED+=("$resolved")
    done
    DEVICES=("${RESOLVED[@]}")

    # Enable swap
    echo ""
    echo "=== Enabling swap ==="
    for part in "${SWAP_PARTS[@]}"; do
        sudo swapon -p 1 "$part"
        echo "$part none swap sw,pri=1 0 0" | sudo tee -a /etc/fstab >/dev/null
    done
    echo "Swap enabled on ${#SWAP_PARTS[@]} partition(s)"
fi

# Validate devices
for dev in "${DEVICES[@]}"; do
    if [ ! -b "$dev" ]; then
        echo "ERROR: $dev is not a block device" >&2
        exit 1
    fi
done

NDISKS=${#DEVICES[@]}

# Determine vdev topology
if [ "$NDISKS" -eq 1 ]; then
    TOPOLOGY_DESC="single vdev (no redundancy)"
    ZPOOL_VDEVS=("${DEVICES[0]}")
elif [ "$NDISKS" -eq 2 ]; then
    TOPOLOGY_DESC="mirror"
    ZPOOL_VDEVS=("mirror" "${DEVICES[@]}")
elif [ "$NDISKS" -le 6 ]; then
    TOPOLOGY_DESC="raidz1 ($NDISKS drives)"
    ZPOOL_VDEVS=("raidz1" "${DEVICES[@]}")
else
    if [ $((NDISKS % 2)) -ne 0 ]; then
        echo "ERROR: odd number of drives ($NDISKS), cannot create mirrored vdevs" >&2
        exit 1
    fi
    TOPOLOGY_DESC="mirrored pairs ($((NDISKS / 2)) mirrors)"
    ZPOOL_VDEVS=()
    for ((i = 0; i < NDISKS; i += 2)); do
        ZPOOL_VDEVS+=("mirror" "${DEVICES[$i]}" "${DEVICES[$((i + 1))]}")
    done
fi

# Confirmation prompt
echo ""
echo "=== Tank zpool creation plan ==="
echo "Topology: $TOPOLOGY_DESC"
echo "Devices:"
for dev in "${DEVICES[@]}"; do
    echo "  $dev"
done

if [ "$YES" != true ]; then
    echo ""
    read -r -p "Create tank pool? [y/N] " confirm
    if [[ ! "$confirm" =~ ^[Yy] ]]; then
        echo "Aborted."
        exit 1
    fi
fi

# Create the pool
echo ""
echo "=== Creating ZFS pool 'tank' ==="
sudo zpool create -o ashift=12 -m none tank "${ZPOOL_VDEVS[@]}"

# Configure ZFS properties
sudo zfs set compression=lz4 tank
sudo zfs set atime=off tank
sudo zfs set xattr=sa tank

# Create /data dataset
sudo zfs create -o mountpoint=/data tank/data
sudo mkdir -p /data/exelet

echo "ZFS pool 'tank' ready:"
zpool status tank
