#!/bin/bash
set -euo pipefail

if [ $# -ne 1 ]; then
    echo "Usage: $0 <pool-name>"
    exit 1
fi

POOL="$1"

if ! zpool list "$POOL" &>/dev/null; then
    echo "Error: pool '$POOL' not found"
    zpool list
    exit 1
fi

DATASET="$POOL/bench"
MOUNTPOINT="/tmp/zfs-bench-$POOL"

cleanup() {
    echo "Cleaning up benchmark dataset..."
    zfs destroy -f "$DATASET" 2>/dev/null || true
    rmdir "$MOUNTPOINT" 2>/dev/null || true
}
trap cleanup EXIT

echo "Creating temporary dataset $DATASET..."
zfs create -o mountpoint="$MOUNTPOINT" "$DATASET"

if ! command -v fio &>/dev/null; then
    echo "Installing fio..."
    apt-get install -y fio
fi

FIO_COMMON=(
    --directory="$MOUNTPOINT"
    --direct=1
    --ioengine=libaio
    --size=4G
    --runtime=30
    --time_based
    --output-format=json
    --group_reporting
)

run_bench() {
    local name="$1"
    shift
    echo ""
    echo "=== $name ==="
    local output
    output=$(fio "${FIO_COMMON[@]}" --name="$name" "$@")

    local rw_key bw iops
    # Determine which JSON key to read based on the workload
    case "$1" in
        --rw=write|--rw=randwrite) rw_key="write" ;;
        *) rw_key="read" ;;
    esac

    bw=$(echo "$output" | jq -r ".jobs[0].${rw_key}.bw_bytes")
    iops=$(echo "$output" | jq -r ".jobs[0].${rw_key}.iops")

    # Convert to human-readable
    local bw_mb
    bw_mb=$(awk "BEGIN {printf \"%.1f\", $bw / 1048576}")
    iops=$(awk "BEGIN {printf \"%.0f\", $iops}")

    echo "$name  ${bw_mb} MB/s  ${iops} IOPS"
    RESULTS+=("$name|${bw_mb} MB/s|${iops}")
}

RESULTS=()

run_bench "seq-write"  --rw=write     --bs=1M --numjobs=4
run_bench "seq-read"   --rw=read      --bs=1M --numjobs=4
run_bench "rand-read"  --rw=randread  --bs=4K --numjobs=16 --iodepth=32
run_bench "rand-write" --rw=randwrite --bs=4K --numjobs=16 --iodepth=32

echo ""
echo "==============================="
echo "  Benchmark Summary: $POOL"
echo "==============================="
printf "%-12s %12s %10s\n" "TEST" "BANDWIDTH" "IOPS"
printf "%-12s %12s %10s\n" "----" "---------" "----"
for row in "${RESULTS[@]}"; do
    IFS='|' read -r test bw iops <<< "$row"
    printf "%-12s %12s %10s\n" "$test" "$bw" "$iops"
done
echo "==============================="
