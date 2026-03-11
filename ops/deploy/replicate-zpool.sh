#!/bin/bash
set -euo pipefail

# Replicate all ZFS datasets from a source pool to a target pool using
# zfs send/recv. Handles datasets, volumes (zvols), and nested hierarchies.
#
# Usage:
#   replicate-zpool.sh [--yes] [--destroy-stale] <source-pool> <target-pool>
#
# Example:
#   replicate-zpool.sh backup tank
#   replicate-zpool.sh --yes --destroy-stale backup tank
#
# This creates a snapshot @replicate on each source dataset, sends a full
# stream to the target pool, then cleans up the snapshots. Target datasets
# are created under the same relative path (e.g. backup/data -> tank/data).
#
# Flags:
#   --yes            Skip interactive confirmation
#   --destroy-stale  Destroy target datasets that don't exist in the source

YES=false
DESTROY_STALE=false

while [[ $# -gt 0 ]]; do
    case "$1" in
    --yes)
        YES=true
        shift
        ;;
    --destroy-stale)
        DESTROY_STALE=true
        shift
        ;;
    -*)
        echo "Unknown flag: $1" >&2
        exit 1
        ;;
    *) break ;;
    esac
done

if [ $# -ne 2 ]; then
    echo "Usage: $0 [--yes] [--destroy-stale] <source-pool> <target-pool>" >&2
    exit 1
fi

SRC_POOL="$1"
DST_POOL="$2"

# Validate pools exist
if ! zpool list "$SRC_POOL" &>/dev/null; then
    echo "ERROR: source pool '$SRC_POOL' does not exist" >&2
    exit 1
fi
if ! zpool list "$DST_POOL" &>/dev/null; then
    echo "ERROR: target pool '$DST_POOL' does not exist" >&2
    exit 1
fi

SNAP_TAG="replicate-$(date +%s)"

# List all datasets and volumes under the source pool (excluding the pool root)
SRC_DATASETS=()
while IFS= read -r ds; do
    SRC_DATASETS+=("$ds")
done < <(zfs list -H -o name -t filesystem,volume -r "$SRC_POOL" | grep -v "^${SRC_POOL}$")

if [ ${#SRC_DATASETS[@]} -eq 0 ]; then
    echo "No datasets found under $SRC_POOL"
    exit 0
fi

# Build relative paths
SRC_RELPATHS=()
for ds in "${SRC_DATASETS[@]}"; do
    SRC_RELPATHS+=("${ds#${SRC_POOL}/}")
done

# Check for stale target datasets
STALE_DATASETS=()
if [ "$DESTROY_STALE" = true ]; then
    while IFS= read -r ds; do
        relpath="${ds#${DST_POOL}/}"
        found=false
        for src_rel in "${SRC_RELPATHS[@]}"; do
            if [ "$src_rel" = "$relpath" ]; then
                found=true
                break
            fi
        done
        if [ "$found" = false ]; then
            STALE_DATASETS+=("$ds")
        fi
    done < <(zfs list -H -o name -t filesystem,volume -r "$DST_POOL" | grep -v "^${DST_POOL}$")
fi

# Show plan
echo "=== ZFS replication plan ==="
echo "Source: $SRC_POOL"
echo "Target: $DST_POOL"
echo "Snapshot tag: @${SNAP_TAG}"
echo ""
echo "Datasets to replicate:"
for i in "${!SRC_DATASETS[@]}"; do
    ds="${SRC_DATASETS[$i]}"
    rel="${SRC_RELPATHS[$i]}"
    ds_type=$(zfs get -H -o value type "$ds")
    if zfs list "${DST_POOL}/${rel}" &>/dev/null; then
        echo "  $ds -> ${DST_POOL}/${rel} (${ds_type}, exists - will overwrite)"
    else
        echo "  $ds -> ${DST_POOL}/${rel} (${ds_type})"
    fi
done

if [ ${#STALE_DATASETS[@]} -gt 0 ]; then
    echo ""
    echo "Stale target datasets to destroy:"
    for ds in "${STALE_DATASETS[@]}"; do
        echo "  $ds"
    done
fi

if [ "$YES" != true ]; then
    echo ""
    read -r -p "Proceed with replication? [y/N] " confirm
    if [[ ! "$confirm" =~ ^[Yy] ]]; then
        echo "Aborted."
        exit 1
    fi
fi

# Clean up snapshots on exit
cleanup() {
    echo ""
    echo "=== Cleaning up replication snapshots ==="
    for ds in "${SRC_DATASETS[@]}"; do
        zfs destroy "${ds}@${SNAP_TAG}" 2>/dev/null || true
    done
}
trap cleanup EXIT

# Destroy stale target datasets (in reverse order for nested datasets)
if [ ${#STALE_DATASETS[@]} -gt 0 ]; then
    echo ""
    echo "=== Destroying stale target datasets ==="
    for ((i = ${#STALE_DATASETS[@]} - 1; i >= 0; i--)); do
        ds="${STALE_DATASETS[$i]}"
        echo "  Destroying $ds..."
        sudo zfs destroy -r "$ds"
    done
fi

# Replicate each dataset
echo ""
echo "=== Replicating datasets ==="
ERRORS=0
for i in "${!SRC_DATASETS[@]}"; do
    ds="${SRC_DATASETS[$i]}"
    rel="${SRC_RELPATHS[$i]}"
    target="${DST_POOL}/${rel}"

    echo ""
    echo "--- ${ds} -> ${target} ---"

    # Create snapshot
    echo "  Creating snapshot ${ds}@${SNAP_TAG}..."
    if ! zfs snapshot "${ds}@${SNAP_TAG}"; then
        echo "  ERROR: failed to create snapshot on $ds, skipping" >&2
        ERRORS=$((ERRORS + 1))
        continue
    fi

    # Destroy target if it exists (full send requires no existing target,
    # unless we receive with -F)
    if zfs list "$target" &>/dev/null; then
        echo "  Destroying existing target $target..."
        sudo zfs destroy -r "$target"
    fi

    # Ensure parent dataset exists on target
    parent_rel="${rel%/*}"
    if [ "$parent_rel" != "$rel" ]; then
        parent_target="${DST_POOL}/${parent_rel}"
        if ! zfs list "$parent_target" &>/dev/null; then
            echo "  Creating parent dataset $parent_target..."
            sudo zfs create -p "$parent_target"
        fi
    fi

    # Send/recv
    echo "  Sending..."
    if zfs send "${ds}@${SNAP_TAG}" | sudo zfs recv -F "$target"; then
        # Clean up the replicated snapshot on the target
        sudo zfs destroy "${target}@${SNAP_TAG}" 2>/dev/null || true
        size=$(zfs get -H -o value used "$target")
        echo "  Done ($size)"
    else
        echo "  ERROR: replication failed for $ds" >&2
        ERRORS=$((ERRORS + 1))
    fi
done

echo ""
if [ "$ERRORS" -gt 0 ]; then
    echo "=== Replication completed with $ERRORS error(s) ==="
    exit 1
else
    echo "=== Replication complete ==="
    echo ""
    zfs list -r "$DST_POOL"
fi
