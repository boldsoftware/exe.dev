#!/bin/bash
set -euo pipefail

if [ $# -ne 2 ]; then
    echo "Usage: $0 <hostname> <grow-by-gb>"
    exit 1
fi

HOSTNAME="$1"
GROW_BY="$2"

echo "Finding data volume for $HOSTNAME..."
# Get the non-root volume (data disk, not /dev/sda1 or /dev/xvda)
VOL_ID=$(aws ec2 describe-instances \
    --filters "Name=tag:Name,Values=$HOSTNAME" \
    --query 'Reservations[].Instances[].BlockDeviceMappings[?DeviceName!=`/dev/sda1` && DeviceName!=`/dev/xvda`].Ebs.VolumeId' \
    --output text)

if [ -z "$VOL_ID" ] || [ "$VOL_ID" = "None" ]; then
    echo "Error: Could not find data volume for $HOSTNAME"
    exit 1
fi

# Handle case where there might still be multiple volumes
VOL_COUNT=$(echo "$VOL_ID" | wc -w | tr -d ' ')
if [ "$VOL_COUNT" -gt 1 ]; then
    echo "Error: Found multiple data volumes: $VOL_ID"
    echo "Please specify which volume to grow."
    exit 1
fi

echo "Found data volume: $VOL_ID"

CURRENT=$(aws ec2 describe-volumes \
    --volume-ids "$VOL_ID" \
    --query 'Volumes[0].Size' \
    --output text)

NEW_SIZE=$((CURRENT + GROW_BY))
echo "Current size: ${CURRENT}GB, growing to: ${NEW_SIZE}GB"

echo ""
echo "The following commands will be executed:"
echo "  1. aws ec2 modify-volume --volume-id $VOL_ID --size $NEW_SIZE"
echo "  2. ssh -lubuntu $HOSTNAME 'sudo zpool online -e tank <disk>'"
echo ""
read -rp "Proceed? [y/N] " CONFIRM
if [[ "$CONFIRM" != [yY] ]]; then
    echo "Aborted."
    exit 0
fi

aws ec2 modify-volume --volume-id "$VOL_ID" --size "$NEW_SIZE"

echo "Waiting for volume modification to complete..."
while true; do
    STATE=$(aws ec2 describe-volumes-modifications \
        --volume-ids "$VOL_ID" \
        --query 'VolumesModifications[0].ModificationState' \
        --output text)
    echo "  State: $STATE"
    if [ "$STATE" = "optimizing" ] || [ "$STATE" = "completed" ]; then
        break
    fi
    sleep 5
done

echo "Expanding ZFS pool on $HOSTNAME..."
PART=$(ssh -lubuntu "$HOSTNAME" "zpool list -vHPp tank | awk '/^\t/{print \$1}'")
# Strip partition suffix (e.g., /dev/nvme5n1p1 -> /dev/nvme5n1,
#   or /dev/disk/by-id/nvme-...-part1 -> /dev/disk/by-id/nvme-...)
DISK=$(ssh -lubuntu "$HOSTNAME" "echo $PART | sed -E 's/(-part|p)[0-9]+\$//'")
echo "Resolved partition: $PART -> disk: $DISK"
echo "  Running: sudo zpool online -e tank $DISK"

ssh -lubuntu "$HOSTNAME" "sudo zpool online -e tank $DISK"

echo "Done! New pool status:"
ssh -lubuntu "$HOSTNAME" "zpool list tank"
