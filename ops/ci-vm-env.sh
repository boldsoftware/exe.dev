# Environment variables shared by CI scripts that manage VMs.
# Use as "source ci-vm-env.sh".

NAME="${NAME:-ci-ubuntu-$(whoami)-$(date +%Y%m%d%H%M%S)}"
VCPUS="${VCPUS:-4}"
RAM_MB="${RAM_MB:-16384}"          # 16GiB
DISK_GB="${DISK_GB:-40}"           # thin-provisioned
DATA_DISK_GB="${DATA_DISK_GB:-50}" # ZFS data disk
BASE_IMG="${BASE_IMG:-/var/lib/libvirt/images/ubuntu-24.04-base.qcow2}"
BASE_IMG_URL="${BASE_IMG_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"

# Content hash of the base cloud image (written by ci-vm-start.sh).
# Lives on the same tmpfs, so both vanish together on reboot.
if [[ -f "${BASE_IMG}.sha256" ]]; then
    BASE_IMG_HASH="$(cat "${BASE_IMG}.sha256")"
else
    BASE_IMG_HASH="nobaseimg"
fi

WORKDIR="${WORKDIR:-/var/lib/libvirt/images}"
SSH_PUBKEY="${SSH_PUBKEY:-$HOME/.ssh/id_ed25519.pub}" # or inject via env
USER_NAME="${USER_NAME:-ubuntu}"

CLOUD_HYPERVISOR_VERSION="${CLOUD_HYPERVISOR_VERSION:-48.0}"
VIRTIOFSD_VERSION="${VIRTIOFSD_VERSION:-1.13.2}"

CACHE_DIR="${EXEDEV_CACHE:-/data/ci-snapshots}"

# Determine setup hash based on ops/ directory
SETUP_HASH="$(git rev-parse HEAD:ops/)"

# Detect current platform architecture
HOST_ARCH=$(uname -m)
if [ "$HOST_ARCH" = "x86_64" ]; then
    HOST_ARCH="amd64"
elif [ "$HOST_ARCH" = "aarch64" ] || [ "$HOST_ARCH" = "arm64" ]; then
    HOST_ARCH="arm64"
fi

# Get container image digest for current platform
EXEUNTU_IMAGE="ghcr.io/boldsoftware/exeuntu:latest"
EXEUNTU_DIGEST=$("${SCRIPT_DIR}/get-image-digest.sh" "$EXEUNTU_IMAGE" "$HOST_ARCH" | cut -d: -f2 | cut -c1-20)

# Combine ops tree hash with image digest for cache key
# We re-build the VM snapshot once a day. If you want to disable
# using snapshots, change SNAPSHOT_DIR to be something unique, and, voila.
SNAPSHOT_DIR="${CACHE_DIR}/ci-vm-${SETUP_HASH:0:20}-${EXEUNTU_DIGEST}-${BASE_IMG_HASH:0:12}-$(date +%Y%m%d)"
SNAPSHOT_BASE="${SNAPSHOT_DIR}/base.qcow2"
SNAPSHOT_DATA="${SNAPSHOT_DIR}/data.qcow2"
LOCAL_BASE_COPY="${WORKDIR}/ci-base-${SETUP_HASH:0:12}-${EXEUNTU_DIGEST:0:12}-${BASE_IMG_HASH:0:12}.qcow2"
LOCAL_DATA_COPY="${WORKDIR}/ci-data-${SETUP_HASH:0:12}-${EXEUNTU_DIGEST:0:12}-${BASE_IMG_HASH:0:12}.qcow2"
SNAPSHOT_AVAILABLE=0
if [[ -f "${SNAPSHOT_BASE}" && -f "${SNAPSHOT_DATA}" ]]; then
    SNAPSHOT_AVAILABLE=1
fi
