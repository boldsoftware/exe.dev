#!/usr/bin/env bash
set -euo pipefail

NAME="${NAME:-ci-ubuntu-$(date +%Y%m%d%H%M%S)}"
VCPUS="${VCPUS:-4}"
RAM_MB="${RAM_MB:-4096}" # 4GiB
DISK_GB="${DISK_GB:-40}" # thin-provisioned
BASE_IMG="${BASE_IMG:-/var/lib/libvirt/images/ubuntu-24.04-base.qcow2}"
BASE_IMG_URL="${BASE_IMG_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"

WORKDIR="${WORKDIR:-/var/lib/libvirt/images}"
SSH_PUBKEY="${SSH_PUBKEY:-$HOME/.ssh/id_ed25519.pub}" # or inject via env
USER_NAME="${USER_NAME:-ubuntu}"

# Cache/snapshot settings (hash of ops/ as determined by git tree (must be checked in))
CACHE_DIR="${EXEDEV_CACHE:-$HOME/.cache/exedev}"
mkdir -p "${CACHE_DIR}"
sudo chown $USER "${CACHE_DIR}"

cp_clone_file() {
    # Clone/copy SRC to DEST efficiently if supported by FS
    local src="$1"
    local dest="$2"
    mkdir -p "$(dirname "$dest")"
    # Prefer Linux reflink clone on XFS/Btrfs
    if cp --reflink=always -a "$src" "$dest" 2>/dev/null; then
        return 0
    fi
    if cp --reflink=auto -a "$src" "$dest" 2>/dev/null; then
        return 0
    fi
    # macOS APFS clone
    if cp -c "$src" "$dest" 2>/dev/null; then
        return 0
    fi
    # Fallback to regular copy
    if cp -a "$src" "$dest" 2>/dev/null; then
        return 0
    fi
    # Retry with sudo if permission denied (e.g., copying into/out of /var/lib/libvirt/images)
    if sudo cp --reflink=always -a "$src" "$dest" 2>/dev/null ||
        sudo cp --reflink=auto -a "$src" "$dest" 2>/dev/null ||
        sudo cp -a "$src" "$dest" 2>/dev/null; then
        # If destination is under user cache, ensure ownership is the invoking user
        if [[ "$dest" == "$HOME/"* ]]; then
            sudo chown "$(id -u)":"$(id -g)" "$dest" 2>/dev/null || true
        fi
        return 0
    fi
    return 1
}

# Determine path to setup script to hash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_SCRIPT_PATH="${SCRIPT_DIR}/setup-containerd-clh-nydus.sh"
if [[ ! -f "${SETUP_SCRIPT_PATH}" ]]; then
    echo "Required setup script not found for hashing: ${SETUP_SCRIPT_PATH}" >&2
    exit 1
fi
SETUP_HASH="$(git rev-parse HEAD:ops/)"

# We re-build the VM snapshot once a day. If you want to disable
# using snapshots, change SNAPSHOT_DIR to be something unique, and, voila.
SNAPSHOT_DIR="${CACHE_DIR}/ci-vm-${SETUP_HASH}-$(date +%Y%m%d)"
SNAPSHOT_BASE="${SNAPSHOT_DIR}/base.qcow2"
LOCAL_BASE_COPY="${WORKDIR}/ci-base-${SETUP_HASH}.qcow2"
SNAPSHOT_AVAILABLE=0
if [[ -f "${SNAPSHOT_BASE}" ]]; then
    SNAPSHOT_AVAILABLE=1
fi

if [[ ! -f "${BASE_IMG}" ]]; then
    sudo curl "${BASE_IMG_URL}" -o "${BASE_IMG}"
fi
if [[ ! -f "${SSH_PUBKEY}" ]]; then
    echo "SSH pubkey not found: ${SSH_PUBKEY}" >&2
    exit 1
fi

# Ensure workdir exists (may require root)
sudo mkdir -p "${WORKDIR}"
DISK="${WORKDIR}/${NAME}.qcow2"
SEED="${WORKDIR}/${NAME}-seed.iso"

# 1) Ephemeral COW disk (from snapshot if available)
BACKING_IMG="${BASE_IMG}"
if [[ ${SNAPSHOT_AVAILABLE} -eq 1 ]]; then
    echo "Found snapshot for setup hash: ${SNAPSHOT_DIR}"
    # Keep a local copy in WORKDIR for qemu access and to avoid permission issues
    if [[ ! -f "${LOCAL_BASE_COPY}" ]]; then
        echo "Cloning snapshot base into WORKDIR (reflink if possible)..."
        cp_clone_file "${SNAPSHOT_BASE}" "${LOCAL_BASE_COPY}"
    fi
    BACKING_IMG="${LOCAL_BASE_COPY}"
fi

if [[ "${BACKING_IMG}" == "${BASE_IMG}" ]]; then
    sudo qemu-img create -f qcow2 -F qcow2 -b "${BACKING_IMG}" "${DISK}" "${DISK_GB}G"
else
    # Size is inherited from backing when provided
    sudo qemu-img create -f qcow2 -F qcow2 -b "${BACKING_IMG}" "${DISK}"
fi

# 2) Cloud-init seed (NoCloud)
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

cat >"${TMPDIR}/user-data" <<EOF
#cloud-config
hostname: ${NAME}
users:
  - name: ${USER_NAME}
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - $(cat "${SSH_PUBKEY}")
package_update: true
packages:
  - qemu-guest-agent
runcmd:
  - echo runcmd
  - systemctl enable --now qemu-guest-agent
  - mkdir -p /data && chmod 755 /data
  - mkdir -p /local && chmod 755 /local
  # Clean up stale container state from the snapshot before restarting services.
  # Containerd/nydus runtime state (task directories, metadata) persists in the snapshot
  # and becomes stale when the machine-id changes on clone, causing kata verification to fail.
  # Stop services, clean persistent task state, prune container metadata, then restart fresh.
  - systemctl stop containerd || true
  - rm -rf /var/lib/containerd/io.containerd.runtime.v2.task/exe/* || true
  - rm -rf /run/containerd/io.containerd.runtime.v2.task/exe/* || true
  - systemctl start nydus-snapshotter || true
  - systemctl start containerd || true
  # Clean up stale container metadata in the exe namespace from the snapshot.
  # This removes container references but doesn't affect cached images.
  - nerdctl --namespace exe container prune --force || true
bootcmd:
  # Regenerate machine-id so DHCP/leases don't persist across clones
  - [bash, -c, 'rm -f /etc/machine-id /var/lib/dbus/machine-id; systemd-machine-id-setup']
  # Remove systemd-networkd DUID to force fresh DHCP client ID on each clone
  - [bash, -c, 'rm -rf /var/lib/systemd/networkd/*']
  # Configure MAC-based DHCP BEFORE network starts (must match actual interface name)
  - |
    cat >/etc/netplan/60-dhcp-mac.yaml <<'NETPLAN'
    network:
      version: 2
      renderer: networkd
      ethernets:
        enp1s0:
          match:
            name: enp1s0
          dhcp4: true
          dhcp6: false
          dhcp-identifier: mac
    NETPLAN
  - [netplan, generate]
EOF

cat >"${TMPDIR}/meta-data" <<EOF
instance-id: ${NAME}
local-hostname: ${NAME}
EOF

# Create cloud-init ISO (requires permission to write into ${WORKDIR})
if command -v genisoimage >/dev/null 2>&1; then
    sudo genisoimage -output "${SEED}" -volid cidata -joliet -rock \
        "${TMPDIR}/user-data" "${TMPDIR}/meta-data" >/dev/null 2>&1
elif command -v mkisofs >/dev/null 2>&1; then
    sudo mkisofs -output "${SEED}" -volid cidata -joliet -rock \
        "${TMPDIR}/user-data" "${TMPDIR}/meta-data" >/dev/null 2>&1
else
    echo "Neither genisoimage nor mkisofs found on host" >&2
    exit 1
fi

ensure_libvirt_default_net() {
    if ! sudo virsh net-info default >/dev/null 2>&1; then
        echo "Libvirt 'default' network not found; defining a NAT network..."
        TMPNET=$(mktemp)
        cat >"$TMPNET" <<'XML'
<network>
  <name>default</name>
  <forward mode='nat'/>
  <bridge name='virbr0' stp='on' delay='0'/>
  <ip address='192.168.122.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='192.168.122.2' end='192.168.122.254'/>
    </dhcp>
  </ip>
</network>
XML
        sudo virsh net-define "$TMPNET"
        rm -f "$TMPNET"
    fi

    if ! sudo virsh net-info default | grep "Active:.*yes"; then
        echo "Starting libvirt 'default' NAT network..."
        sudo virsh net-start default
    fi
    sudo virsh net-autostart default >/dev/null 2>&1 || true

    # Ensure IP forwarding is enabled for NAT
    sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true
}

# 3) Boot transient VM
ensure_libvirt_default_net
echo "Starting VM with virt-install..."
sudo virt-install \
    --name "${NAME}" \
    --memory "${RAM_MB}" \
    --vcpus "${VCPUS}" \
    --import \
    --disk "path=${DISK},format=qcow2,cache=none,discard=unmap" \
    --disk "path=${SEED},device=cdrom" \
    --os-variant ubuntu24.04 \
    --network network=default,model=virtio \
    --graphics none \
    --noautoconsole \
    --wait 0 \
    --transient

echo "VM launched; current domains:"
sudo virsh list --all || true

# 4) Get the VM IP (via DHCP lease; no agent required)
# Fallback to agent if available.
get_ip() {
    # Prefer libvirt lease-based lookup (does not require guest agent)
    ip=$(sudo virsh domifaddr "${NAME}" --source lease 2>/dev/null | awk '/ipv4/ {print $4}' | sed 's|/.*||' | head -n1 || true)
    echo "${ip}"
}

echo "Waiting for IP..."
for i in $(seq 1 120); do
    IP="$(get_ip || true)"
    if [[ -n "${IP}" ]]; then break; fi
    echo "  attempt ${i}/120: no IP yet"
    sleep 1
done
if [[ -z "${IP:-}" ]]; then
    echo "Failed to obtain VM IP" >&2
    exit 1
fi
echo "VM IP: ${IP}"

# 5) Wait for SSH + cloud-init
SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5"
for i in $(seq 1 60); do
    if ssh ${SSH_OPTS} ${USER_NAME}@"${IP}" 'true' 2>/dev/null; then break; fi
    sleep 2
done
ssh ${SSH_OPTS} ${USER_NAME}@"${IP}" 'sudo cloud-init status --wait || true'

if [[ ${SNAPSHOT_AVAILABLE} -eq 0 ]]; then
    echo "No snapshot found; provisioning VM and creating snapshot cache..."
    # 6) Prepare containerd + nydus + kata on the VM

    # Detect VM architecture
    VM_ARCH=$(ssh ${SSH_OPTS} ${USER_NAME}@"${IP}" 'uname -m')
    if [ "$VM_ARCH" = "x86_64" ]; then
        VM_ARCH="amd64"
    elif [ "$VM_ARCH" = "aarch64" ] || [ "$VM_ARCH" = "arm64" ]; then
        VM_ARCH="arm64"
    fi
    echo "VM architecture: $VM_ARCH"

    # Download dependencies locally if not cached
    echo "Ensuring dependencies are downloaded for $VM_ARCH..."

    "${SCRIPT_DIR}/download-ctr-host.sh" "$VM_ARCH"

    echo "Copying setup script and config files to VM ${IP}..."
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "${SETUP_SCRIPT_PATH}" "${USER_NAME}@${IP}:~/setup-containerd-clh-nydus.sh"
    ssh ${SSH_OPTS} ${USER_NAME}@"${IP}" 'mkdir -p ~/.cache/exedops'
    scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "${SCRIPT_DIR}/kata-config-clh.toml" "${USER_NAME}@${IP}:~/.cache/exedops/kata-config-clh.toml"

    # Build custom kernel if not cached
    KERNEL_BUILDER_DIR="${SCRIPT_DIR}/kernel-builder/output"
    KERNEL_CACHE_DIR="${CACHE_DIR}/kernel"
    mkdir -p "${KERNEL_CACHE_DIR}"

    if [ ! -f "${KERNEL_CACHE_DIR}/vmlinux-6.12.42-nftables" ]; then
        echo "Custom kernel not found in cache, building it now..."
        (cd "${SCRIPT_DIR}/kernel-builder" && make)
        if [ -f "${KERNEL_BUILDER_DIR}/vmlinux-6.12.42-nftables" ]; then
            echo "Caching kernel build..."
            cp "${KERNEL_BUILDER_DIR}/vmlinux-6.12.42-nftables" "${KERNEL_CACHE_DIR}/vmlinux-6.12.42-nftables"
            cp "${KERNEL_BUILDER_DIR}/config-6.12.42-nftables" "${KERNEL_CACHE_DIR}/config-6.12.42-nftables"
        else
            echo "ERROR: Failed to build custom kernel"
            exit 1
        fi
    fi

    # Copy custom kernel from cache to VM
    if [ -f "${KERNEL_CACHE_DIR}/vmlinux-6.12.42-nftables" ]; then
        echo "Copying custom kernel with nftables support..."
        scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "${KERNEL_CACHE_DIR}/vmlinux-6.12.42-nftables" "${USER_NAME}@${IP}:~/.cache/exedops/vmlinux-6.12.42-nftables"
        scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "${KERNEL_CACHE_DIR}/config-6.12.42-nftables" "${USER_NAME}@${IP}:~/.cache/exedops/config-6.12.42-nftables"
    else
        echo "ERROR: Custom kernel not found in cache"
        exit 1
    fi

    # Copy pre-downloaded tarballs to VM
    echo "Copying pre-downloaded dependencies to VM ${IP}..."
    CACHE_DIR="$HOME/.cache/exedops"
    mapfile -t files < <(find "$CACHE_DIR" -maxdepth 1 -type f \
        \( -name '*.tar.gz' -o -name '*.tar.xz' -o -name '*.tgz' -o -name '*.service' -o -name 'runc-*' -o -name 'ch-remote-static-*' -o -name '*.tar' \))
    rsync -avq \
        -e "ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null" \
        "${files[@]}" "${USER_NAME}@${IP}:~/.cache/exedops/"

    # Keep assets in canonical ASSETS_DIR (~/.cache/exedops); just place the setup script
    ssh ${SSH_OPTS} ${USER_NAME}@"${IP}" 'sudo mv ~/setup-containerd-clh-nydus.sh /root/setup-containerd-clh-nydus.sh && sudo chmod +x /root/setup-containerd-clh-nydus.sh'

    echo "Executing setup script on VM ${IP} (raw streaming output)..."
    # Stream exact commands and output directly to CI logs
    # Set CI environment variable to trigger CI mode in the script
    ssh ${SSH_OPTS} -o LogLevel=ERROR ${USER_NAME}@"${IP}" "sudo CI=1 /bin/bash -x /root/setup-containerd-clh-nydus.sh"

    # 6b) Create snapshot cache of the prepared disk (clone, leveraging XFS reflink when available)
    echo "Creating snapshot cache at ${SNAPSHOT_DIR}..."
    mkdir -p "${SNAPSHOT_DIR}"
    # Copy/clone the prepared disk into the snapshot location
    # Note: This clones the qcow2 backing with current state; safe for reuse with overlays.
    cp_clone_file "${DISK}" "${SNAPSHOT_BASE}"

    # Also maintain a local copy in WORKDIR for fast reuse within libvirt
    cp_clone_file "${SNAPSHOT_BASE}" "${LOCAL_BASE_COPY}"
fi

# 7) Emit a small envfile for subsequent steps (write to a readable location)
OUTDIR="${OUTDIR:-$PWD}"
mkdir -p "${OUTDIR}"
ENVFILE="${OUTDIR}/${NAME}.env"
tee "${ENVFILE}" <<EOF
VM_NAME=${NAME}
VM_IP=${IP}
VM_USER=${USER_NAME}
VM_DISK=${DISK}
VM_SEED=${SEED}
EOF
echo "${ENVFILE}"
