#!/usr/bin/env bash
set -euo pipefail

###############################################################################
# ops/virt-cluster.sh — Local prod-like VM cluster using libvirt
#
# Subcommands: start, stop, status, destroy, deploy
#
# Usage:
#   ./ops/virt-cluster.sh start          # Boot cluster (idempotent)
#   ./ops/virt-cluster.sh stop           # Graceful shutdown (preserves disks)
#   ./ops/virt-cluster.sh status         # Show VM status and IPs
#   ./ops/virt-cluster.sh destroy        # Tear down everything
#   ./ops/virt-cluster.sh deploy         # Rebuild binaries, push, restart services
#
# Configuration via environment variables (see defaults below).
###############################################################################

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ── Configuration ────────────────────────────────────────────────────────────

NUM_EXELETS="${NUM_EXELETS:-2}"
NUM_EXEPROXES="${NUM_EXEPROXES:-1}"
EXED_VCPUS="${EXED_VCPUS:-2}"
EXED_RAM="${EXED_RAM:-4096}"
EXEPROX_VCPUS="${EXEPROX_VCPUS:-1}"
EXEPROX_RAM="${EXEPROX_RAM:-2048}"
EXELET_VCPUS="${EXELET_VCPUS:-4}"
EXELET_RAM="${EXELET_RAM:-8192}"
MON_VCPUS="${MON_VCPUS:-1}"
MON_RAM="${MON_RAM:-2048}"
DISK_GB="${DISK_GB:-40}"
EXELET_DATA_DISK_GB="${EXELET_DATA_DISK_GB:-50}"
EXELET_BACKUP_DISK_GB="${EXELET_BACKUP_DISK_GB:-50}"
EXELET_SWAP_SIZE="${EXELET_SWAP_SIZE:-16G}"
SSH_PUBKEY_DIR="${SSH_PUBKEY_DIR:-$HOME/.ssh}"
CLUSTER_PREFIX="${CLUSTER_PREFIX:-exe-local}"

WORKDIR="${WORKDIR:-/var/lib/libvirt/images}"
BASE_IMG="${BASE_IMG:-${WORKDIR}/ubuntu-24.04-base.qcow2}"
BASE_IMG_URL="${BASE_IMG_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
USER_NAME="ubuntu"
SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR"

CLOUD_HYPERVISOR_VERSION="${CLOUD_HYPERVISOR_VERSION:-48.0}"
VIRTIOFSD_VERSION="${VIRTIOFSD_VERSION:-1.13.2}"

CACHE_DIR="$HOME/.cache/exedops"
APT_CACHE_ENABLED="${APT_CACHE_ENABLED:-false}"
APT_CACHE_CONTAINER="${CLUSTER_PREFIX}-apt-cache"
APT_CACHE_PORT="3142"
# The host IP on the virbr0 bridge — reachable from all VMs
APT_CACHE_HOST="192.168.122.1"

# ── VM Names ─────────────────────────────────────────────────────────────────

vm_name_exed() { echo "${CLUSTER_PREFIX}-exed"; }
vm_name_exeprox() { printf 'exeprox-local-dev-%02d\n' "$1"; }
vm_name_exelet() { printf 'exelet-local-dev-%02d\n' "$1"; }
vm_name_mon() { echo "${CLUSTER_PREFIX}-mon"; }

all_vm_names() {
    vm_name_exed
    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        vm_name_exeprox "$i"
    done
    for i in $(seq 1 "${NUM_EXELETS}"); do
        vm_name_exelet "$i"
    done
    vm_name_mon
}

# ── Helpers ──────────────────────────────────────────────────────────────────

# Kill all background jobs on exit so nothing lingers after errors
cleanup_jobs() { jobs -p | xargs -r kill 2>/dev/null || true; }
trap cleanup_jobs EXIT

log() { echo "==> $*"; }
warn() { echo "WARN: $*" >&2; }
die() {
    echo "ERROR: $*" >&2
    exit 1
}

collect_ssh_pubkeys() {
    local keys=()
    for f in "${SSH_PUBKEY_DIR}"/*.pub; do
        [[ -f "$f" ]] || continue
        keys+=("$(cat "$f")")
    done
    if [[ ${#keys[@]} -eq 0 ]]; then
        die "No SSH pubkeys found in ${SSH_PUBKEY_DIR}"
    fi
    printf '%s\n' "${keys[@]}"
}

ssh_authorized_keys_yaml() {
    collect_ssh_pubkeys | while IFS= read -r key; do
        echo "      - ${key}"
    done
}

check_prerequisites() {
    local missing=()
    for cmd in virt-install virsh qemu-img go sqlite3; do
        command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
    done
    # Need genisoimage or mkisofs
    if ! command -v genisoimage >/dev/null 2>&1 && ! command -v mkisofs >/dev/null 2>&1; then
        missing+=("genisoimage/mkisofs")
    fi
    if [[ ${#missing[@]} -gt 0 ]]; then
        die "Missing prerequisites: ${missing[*]}"
    fi
    # Verify at least one SSH pubkey exists
    collect_ssh_pubkeys >/dev/null
}

iso_tool() {
    if command -v genisoimage >/dev/null 2>&1; then
        echo "genisoimage"
    elif command -v mkisofs >/dev/null 2>&1; then
        echo "mkisofs"
    else
        die "Neither genisoimage nor mkisofs found"
    fi
}

vm_exists() {
    sudo virsh dominfo "$1" >/dev/null 2>&1
}

vm_running() {
    sudo virsh domstate "$1" 2>/dev/null | grep -q "running"
}

ensure_libvirt_default_net() {
    if ! sudo virsh net-info default >/dev/null 2>&1; then
        log "Defining libvirt 'default' NAT network..."
        local tmpnet
        tmpnet=$(mktemp)
        cat >"$tmpnet" <<'XML'
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
        sudo virsh net-define "$tmpnet"
        rm -f "$tmpnet"
    fi

    if ! sudo virsh net-info default 2>/dev/null | grep -qi "active.*yes"; then
        log "Starting libvirt 'default' NAT network..."
        sudo virsh net-start default 2>/dev/null || true
    fi
    sudo virsh net-autostart default >/dev/null 2>&1 || true
    sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true

    # Ensure iptables FORWARD chain allows virbr0 traffic.
    # Docker sets the FORWARD policy to DROP which blocks libvirt NAT.
    if ! sudo iptables -C FORWARD -o virbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null; then
        log "Adding iptables FORWARD rules for virbr0 (Docker compat)..."
        sudo iptables -I FORWARD -o virbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
        sudo iptables -I FORWARD -i virbr0 -j ACCEPT
    fi
}

ensure_base_image() {
    sudo mkdir -p "${WORKDIR}"
    if [[ ! -f "${BASE_IMG}" ]]; then
        log "Downloading Ubuntu 24.04 cloud image..."
        sudo curl -L "${BASE_IMG_URL}" -o "${BASE_IMG}"
    fi
}

# ── Apt cache (Docker, optional) ──────────────────────────────────────────

ensure_apt_cache() {
    [[ "${APT_CACHE_ENABLED}" == "true" ]] || return 0

    # Already running?
    if docker inspect -f '{{.State.Running}}' "${APT_CACHE_CONTAINER}" 2>/dev/null | grep -q true; then
        log "Apt cache container already running"
        return 0
    fi

    # Exists but stopped — remove and recreate (cache volume survives)
    docker rm -f "${APT_CACHE_CONTAINER}" 2>/dev/null || true

    log "Starting apt-cacher-ng container (${APT_CACHE_HOST}:${APT_CACHE_PORT})..."
    docker run -d \
        --name "${APT_CACHE_CONTAINER}" \
        --restart unless-stopped \
        -p "${APT_CACHE_PORT}:3142" \
        -v "${CLUSTER_PREFIX}-apt-cache:/var/cache/apt-cacher-ng" \
        sameersbn/apt-cacher-ng:latest >/dev/null

    # Wait for the proxy to be ready
    for i in $(seq 1 30); do
        if curl -sf "http://${APT_CACHE_HOST}:${APT_CACHE_PORT}/acng-report.html" >/dev/null 2>&1; then
            log "Apt cache ready"
            return 0
        fi
        sleep 1
    done
    warn "Apt cache may not be ready yet, continuing anyway"
}

stop_apt_cache() {
    [[ "${APT_CACHE_ENABLED}" == "true" ]] || return 0
    if docker inspect "${APT_CACHE_CONTAINER}" >/dev/null 2>&1; then
        log "Stopping apt cache container..."
        docker stop "${APT_CACHE_CONTAINER}" 2>/dev/null || true
        docker rm "${APT_CACHE_CONTAINER}" 2>/dev/null || true
    fi
}

destroy_apt_cache() {
    # Leave the apt cache container and volume intact so cached packages
    # survive cluster destroy/recreate cycles. Manage separately with:
    #   docker stop/rm ${CLUSTER_PREFIX}-apt-cache
    #   docker volume rm ${CLUSTER_PREFIX}-apt-cache
    :
}

# Returns bootcmd cloud-init snippet that:
#   1. Kills apt-daily/unattended-upgrades before cloud-init's package module
#      grabs the dpkg lock (the #1 cause of cloud-init hangs on Ubuntu).
#   2. Optionally configures the apt-cacher-ng proxy.
bootcmd_yaml() {
    cat <<'BOOTCMD'
bootcmd:
  - |
    # Mask apt timers/services so they cannot start, even if systemd hasn't
    # fully initialised yet (bootcmd runs very early).  Masking is a symlink
    # to /dev/null and works whether the unit is already loaded or not.
    systemctl mask --now apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service 2>/dev/null || true
    # Kill anything that already started before we could mask it.
    killall -q apt-get dpkg unattended-upgr 2>/dev/null || true
    # Wait for any lingering dpkg lock holder to exit.
    while fuser /var/lib/dpkg/lock-frontend /var/lib/apt/lists/lock >/dev/null 2>&1; do sleep 1; done
BOOTCMD
    if [[ "${APT_CACHE_ENABLED}" == "true" ]]; then
        cat <<APTPROXY
  - echo 'Acquire::http::Proxy "http://${APT_CACHE_HOST}:${APT_CACHE_PORT}";' > /etc/apt/apt.conf.d/00apt-cacher-proxy
APTPROXY
    fi
}

get_vm_ip() {
    local name="$1"
    local ip

    # Try lease-based lookup first (fastest when dnsmasq is working)
    ip=$(sudo virsh domifaddr "$name" --source lease 2>/dev/null |
        awk '/ipv4/ {print $4}' | sed 's|/.*||' | head -n1)
    if [[ -n "$ip" ]]; then
        echo "$ip"
        return 0
    fi

    # Fallback: QEMU's own ARP table (works even when dnsmasq leases are empty)
    ip=$(sudo virsh domifaddr "$name" --source arp 2>/dev/null |
        awk '/ipv4/ {print $4}' | sed 's|/.*||' | head -n1)
    if [[ -n "$ip" ]]; then
        echo "$ip"
        return 0
    fi

    # Last resort: get the VM's MAC, scan the subnet to populate ARP cache,
    # then match the MAC in the host's neighbor table.
    local mac
    mac=$(sudo virsh domiflist "$name" 2>/dev/null | awk '/virtio/ {print $5}')
    if [[ -z "$mac" ]]; then
        return 1
    fi
    # Ping a few addresses in the DHCP range to populate ARP
    for octet in $(seq 2 50); do
        ping -c1 -W1 "192.168.122.${octet}" >/dev/null 2>&1 &
    done
    wait
    ip=$(ip neigh show dev virbr0 2>/dev/null | grep -i "$mac" | awk '{print $1}' | head -n1)
    if [[ -n "$ip" ]]; then
        echo "$ip"
        return 0
    fi
}

wait_for_ip() {
    local name="$1"
    local ip=""
    for i in $(seq 1 120); do
        ip="$(get_vm_ip "$name" || true)"
        if [[ -n "$ip" ]]; then
            echo "$ip"
            return 0
        fi
        sleep 1
    done
    die "Failed to obtain IP for ${name} after 120s"
}

wait_for_ssh() {
    local ip="$1"
    for i in $(seq 1 60); do
        if ssh ${SSH_OPTS} "${USER_NAME}@${ip}" 'true' 2>/dev/null; then
            return 0
        fi
        sleep 2
    done
    die "SSH not reachable on ${ip} after 120s"
}

wait_for_cloud_init() {
    local ip="$1"
    ssh ${SSH_OPTS} "${USER_NAME}@${ip}" 'sudo cloud-init status --wait' 2>/dev/null
}

# Wait for a single VM to get IP, SSH, and finish cloud-init.
# Writes progress to a status file (status_dir/name) for the display loop.
# Returns the IP on stdout.
wait_for_vm_ready() {
    local name="$1" status_dir="$2"
    local sf="${status_dir}/${name}"
    echo "waiting for IP..." >"$sf"

    # IP
    local ip=""
    for i in $(seq 1 120); do
        ip="$(get_vm_ip "$name" || true)"
        if [[ -n "$ip" ]]; then break; fi
        sleep 1
    done
    if [[ -z "$ip" ]]; then
        echo "FAILED (no IP after 120s)" >"$sf"
        return 1
    fi
    echo "IP=${ip}, waiting for SSH..." >"$sf"

    # SSH
    local ssh_ok=false
    for i in $(seq 1 60); do
        if ssh ${SSH_OPTS} "${USER_NAME}@${ip}" 'true' 2>/dev/null; then
            ssh_ok=true
            break
        fi
        sleep 2
    done
    if [[ "$ssh_ok" != "true" ]]; then
        echo "FAILED (SSH timeout)" >"$sf"
        return 1
    fi
    echo "cloud-init..." >"$sf"

    local ci_rc=0
    timeout 300 ssh ${SSH_OPTS} "${USER_NAME}@${ip}" 'sudo cloud-init status --wait' >/dev/null 2>&1 || ci_rc=$?
    if [[ "$ci_rc" -eq 124 ]]; then
        echo "FAILED (cloud-init timed out after 300s)" >"$sf"
        return 1
    elif [[ "$ci_rc" -ne 0 ]]; then
        local ci_status
        ci_status="$(ssh ${SSH_OPTS} "${USER_NAME}@${ip}" 'sudo cloud-init status --long' 2>/dev/null || echo 'unknown')"
        echo "FAILED (cloud-init error: ${ci_status})" >"$sf"
        return 1
    fi

    echo "READY (${ip})" >"$sf"

    # Return the IP on stdout
    echo "$ip"
}

# Display loop: redraws VM status lines in-place until all are done.
# Args: status_dir vm_name1 vm_name2 ...
display_vm_status() {
    local status_dir="$1"
    shift
    local names=("$@")
    local n=${#names[@]}

    # Print initial blank lines to reserve space
    for name in "${names[@]}"; do
        printf '  [%-25s] waiting...\n' "$name" >&2
    done

    while true; do
        # Move cursor up n lines
        printf '\033[%dA' "$n" >&2
        local all_done=true
        for name in "${names[@]}"; do
            local status
            status="$(cat "${status_dir}/${name}" 2>/dev/null || echo "waiting...")"
            printf '\033[2K  [%-25s] %s\n' "$name" "$status" >&2
            if [[ "$status" != READY* ]] && [[ "$status" != FAILED* ]]; then
                all_done=false
            fi
        done
        if [[ "$all_done" == "true" ]]; then break; fi
        sleep 0.5
    done
}

scp_to() {
    local ip="$1"
    shift
    scp ${SSH_OPTS} "$@" "${USER_NAME}@${ip}:~/"
}

ssh_run() {
    local ip="$1"
    shift
    ssh ${SSH_OPTS} "${USER_NAME}@${ip}" "$@"
}

# ── Cloud-init generation ────────────────────────────────────────────────────

# bootcmd shared by all VMs: regenerate machine-id so each COW-cloned VM gets
# a unique DHCP DUID, then configure MAC-based DHCP so libvirt dnsmasq can
# distinguish them. Without this, all VMs from the same base image share one
# DUID and only one gets a lease.
# Generate the network-config file for the NoCloud seed ISO.
# Uses MAC-based DHCP identifier so libvirt dnsmasq can distinguish
# multiple VMs cloned from the same base image (which share a machine-id
# and thus the same DUID). Without this, all VMs appear as the same client.
generate_network_config() {
    local tmpdir="$1"
    cat >"${tmpdir}/network-config" <<'NETCFG'
version: 2
ethernets:
  all-en:
    match:
      name: en*
    dhcp4: true
    dhcp-identifier: mac
NETCFG
}

generate_cloud_init_exed() {
    local tmpdir="$1" name="$2"
    cat >"${tmpdir}/user-data" <<EOF
#cloud-config
hostname: ${name}
users:
  - name: ${USER_NAME}
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
$(ssh_authorized_keys_yaml)
$(bootcmd_yaml)
package_update: true
packages:
  - curl
  - jq
  - sqlite3
  - net-tools
  - socat
  - prometheus-node-exporter
runcmd:
  - systemctl enable --now qemu-guest-agent || true
EOF
    cat >"${tmpdir}/meta-data" <<EOF
instance-id: ${name}
local-hostname: ${name}
EOF
    generate_network_config "$tmpdir"
}

generate_cloud_init_exeprox() {
    local tmpdir="$1" name="$2"
    cat >"${tmpdir}/user-data" <<EOF
#cloud-config
hostname: ${name}
users:
  - name: ${USER_NAME}
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
$(ssh_authorized_keys_yaml)
$(bootcmd_yaml)
package_update: true
packages:
  - curl
  - jq
  - socat
  - prometheus-node-exporter
runcmd:
  - systemctl enable --now qemu-guest-agent || true
EOF
    cat >"${tmpdir}/meta-data" <<EOF
instance-id: ${name}
local-hostname: ${name}
EOF
    generate_network_config "$tmpdir"
}

generate_cloud_init_exelet() {
    local tmpdir="$1" name="$2"
    cat >"${tmpdir}/user-data" <<EOF
#cloud-config
hostname: ${name}
users:
  - name: ${USER_NAME}
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
$(ssh_authorized_keys_yaml)
$(bootcmd_yaml)
package_update: true
packages:
  - qemu-guest-agent
  - zfsutils-linux
  - socat
  - net-tools
  - libisal2
  - curl
  - jq
  - prometheus-node-exporter
runcmd:
  - systemctl enable --now qemu-guest-agent || true
  # Hugepages (50% of RAM)
  - |
    HUGEPAGE_TARGET=\$(awk '/MemTotal/ { print int(\$2/4096); exit(0); }' /proc/meminfo)
    echo "\$HUGEPAGE_TARGET" > /proc/sys/vm/nr_hugepages
    mkdir -p /etc/sysctl.d
    echo "vm.nr_hugepages=\$HUGEPAGE_TARGET" > /etc/sysctl.d/90-exe-hugepages.conf
  # Kernel modules for cloud-hypervisor
  - modprobe vhost_vsock || true
  - modprobe vsock || true
  - echo -e 'vhost_vsock\nvsock' > /etc/modules-load.d/cloud-hypervisor.conf
  # ZFS pool on /dev/vdb
  - |
    if ! zpool list tank >/dev/null 2>&1; then
      zpool create -f -m none tank /dev/vdb
      zfs create -o mountpoint=/data tank/data
    fi
  # Backup ZFS pool on /dev/vdc
  - |
    if ! zpool list backup >/dev/null 2>&1; then
      zpool create -f -m none backup /dev/vdc
    fi
swap:
  filename: /swapfile
  size: ${EXELET_SWAP_SIZE}
EOF
    cat >"${tmpdir}/meta-data" <<EOF
instance-id: ${name}
local-hostname: ${name}
EOF
    generate_network_config "$tmpdir"
}

generate_cloud_init_mon() {
    local tmpdir="$1" name="$2"
    cat >"${tmpdir}/user-data" <<EOF
#cloud-config
hostname: ${name}
users:
  - name: ${USER_NAME}
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
$(ssh_authorized_keys_yaml)
$(bootcmd_yaml)
package_update: true
packages:
  - prometheus
  - prometheus-node-exporter
runcmd:
  - systemctl enable --now qemu-guest-agent || true
  # Add Grafana APT repo and install
  - |
    apt-get install -y apt-transport-https software-properties-common
    mkdir -p /etc/apt/keyrings
    curl -fsSL https://apt.grafana.com/gpg.key | gpg --dearmor -o /etc/apt/keyrings/grafana.gpg
    echo "deb [signed-by=/etc/apt/keyrings/grafana.gpg] https://apt.grafana.com stable main" > /etc/apt/sources.list.d/grafana.list
    apt-get update
    apt-get install -y grafana
  - systemctl enable --now prometheus
  - systemctl enable --now prometheus-node-exporter
  - systemctl enable --now grafana-server
EOF
    cat >"${tmpdir}/meta-data" <<EOF
instance-id: ${name}
local-hostname: ${name}
EOF
    generate_network_config "$tmpdir"
}

# ── VM creation ──────────────────────────────────────────────────────────────

create_vm() {
    local name="$1" vcpus="$2" ram="$3" cloud_init_fn="$4"
    local has_data_disk="${5:-false}"

    if vm_exists "$name"; then
        if vm_running "$name"; then
            log "VM ${name} already running, skipping creation"
            return 0
        else
            log "VM ${name} exists but not running, starting..."
            sudo virsh start "$name"
            return 0
        fi
    fi

    log "Creating VM ${name} (${vcpus} vCPUs, ${ram}MB RAM)..."

    local disk="${WORKDIR}/${name}.qcow2"
    local seed="${WORKDIR}/${name}-seed.iso"

    # Root disk (COW overlay on base image)
    sudo qemu-img create -f qcow2 -F qcow2 -b "${BASE_IMG}" "${disk}" "${DISK_GB}G"

    # Data disk for exelet VMs
    local data_disk_args=()
    if [[ "$has_data_disk" == "true" ]]; then
        local data_disk="${WORKDIR}/${name}-data.qcow2"
        sudo qemu-img create -f qcow2 "${data_disk}" "${EXELET_DATA_DISK_GB}G"
        data_disk_args=(--disk "path=${data_disk},format=qcow2,cache=none,discard=unmap")

        local backup_disk="${WORKDIR}/${name}-backup.qcow2"
        sudo qemu-img create -f qcow2 "${backup_disk}" "${EXELET_BACKUP_DISK_GB}G"
        data_disk_args+=(--disk "path=${backup_disk},format=qcow2,cache=none,discard=unmap")
    fi

    # Cloud-init seed ISO
    local tmpdir
    tmpdir="$(mktemp -d)"
    "$cloud_init_fn" "$tmpdir" "$name"

    local tool
    tool="$(iso_tool)"
    sudo "$tool" -output "${seed}" -volid cidata -joliet -rock \
        "${tmpdir}/user-data" "${tmpdir}/meta-data" "${tmpdir}/network-config" >/dev/null 2>&1
    rm -rf "$tmpdir"

    # Boot the VM (persistent, not transient, so stop/start works)
    sudo virt-install \
        --name "${name}" \
        --memory "${ram}" \
        --vcpus "${vcpus}" \
        --import \
        --disk "path=${disk},format=qcow2,cache=none,discard=unmap" \
        "${data_disk_args[@]}" \
        --disk "path=${seed},device=cdrom" \
        --os-variant ubuntu24.04 \
        --network network=default,model=virtio \
        --graphics none \
        --noautoconsole ||
        die "Failed to create VM ${name}"
}

# ── Binary building ──────────────────────────────────────────────────────────

build_binaries() {
    log "Building binaries..."
    mkdir -p "${CACHE_DIR}"

    log "  Building exed..."
    (cd "${REPO_ROOT}" && GOOS=linux go build -o "${CACHE_DIR}/exed" ./cmd/exed)

    log "  Building exeprox..."
    (cd "${REPO_ROOT}" && GOOS=linux go build -o "${CACHE_DIR}/exeprox" ./cmd/exeprox)

    log "  Building exeletd..."
    (cd "${REPO_ROOT}" && make exe-init && GOOS=linux go build -o "${CACHE_DIR}/exeletd" ./cmd/exelet)

    log "  Building exelet-ctl..."
    (cd "${REPO_ROOT}" && GOOS=linux go build -o "${CACHE_DIR}/exelet-ctl" ./cmd/exelet-ctl)

    log "  Building sshpiperd..."
    (cd "${REPO_ROOT}/deps/sshpiper" && GOOS=linux go build -o "${CACHE_DIR}/sshpiperd" ./cmd/sshpiperd)

    log "Binaries built in ${CACHE_DIR}"
}

# ── Cloud Hypervisor artifacts ───────────────────────────────────────────────

ensure_cloud_hypervisor_artifacts() {
    local arch="amd64"
    local artifact_name="cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}-${arch}.tar.gz"
    local artifact_path="${CACHE_DIR}/${artifact_name}"
    local build_context="${SCRIPT_DIR}/cloud-hypervisor"

    if [[ -f "${artifact_path}" ]]; then
        log "Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} cache hit"
        return 0
    fi

    if [[ ! -d "${build_context}" ]]; then
        die "Cloud Hypervisor Docker context missing at ${build_context}"
    fi

    local DOCKER_CMD=()
    if docker info >/dev/null 2>&1; then
        DOCKER_CMD=(docker)
    elif sudo docker info >/dev/null 2>&1; then
        DOCKER_CMD=(sudo docker)
    else
        die "Docker is required to build Cloud Hypervisor artifacts"
    fi

    mkdir -p "${CACHE_DIR}"
    local image_tag="exe-cloud-hypervisor:${CLOUD_HYPERVISOR_VERSION}-${arch}"
    log "Building Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} via Docker..."
    "${DOCKER_CMD[@]}" build \
        --platform "linux/amd64" \
        --tag "${image_tag}" \
        --build-arg "CLOUD_HYPERVISOR_VERSION=${CLOUD_HYPERVISOR_VERSION}" \
        --build-arg "VIRTIOFSD_VERSION=${VIRTIOFSD_VERSION}" \
        --build-arg "TARGETARCH=${arch}" \
        "${build_context}"

    local container_id
    container_id=$("${DOCKER_CMD[@]}" create "${image_tag}" /bin/true)
    local tmp_dir
    tmp_dir="$(mktemp -d)"
    "${DOCKER_CMD[@]}" cp "${container_id}:/out/." "${tmp_dir}"
    tar czf "${artifact_path}" -C "${tmp_dir}" .
    chmod 0644 "${artifact_path}"
    rm -rf "${tmp_dir}"
    "${DOCKER_CMD[@]}" rm "${container_id}" >/dev/null 2>&1 || true
    log "Cached Cloud Hypervisor at ${artifact_path}"
}

# ── Provisioning ─────────────────────────────────────────────────────────────

provision_exelet() {
    local ip="$1" name="$2" exelet_ip="$3"
    log "Provisioning exelet VM ${name} (${ip})..."

    # Copy Cloud Hypervisor artifacts
    local arch="amd64"
    local ch_archive="${CACHE_DIR}/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}-${arch}.tar.gz"
    ssh_run "$ip" 'mkdir -p ~/.cache/exedops'
    scp_to "$ip" "$ch_archive"
    ssh_run "$ip" "mv ~/$(basename "$ch_archive") ~/.cache/exedops/"

    # Copy setup-cloud-hypervisor.sh and run it
    scp_to "$ip" "${SCRIPT_DIR}/deploy/setup-cloud-hypervisor.sh"
    ssh_run "$ip" 'sudo mv ~/setup-cloud-hypervisor.sh /root/ && sudo chmod +x /root/setup-cloud-hypervisor.sh && sudo /bin/bash /root/setup-cloud-hypervisor.sh'

    # Copy exeletd and exelet-ctl
    scp_to "$ip" "${CACHE_DIR}/exeletd" "${CACHE_DIR}/exelet-ctl"
    ssh_run "$ip" 'sudo mv ~/exeletd /usr/local/bin/exeletd.latest && sudo chmod +x /usr/local/bin/exeletd.latest'
    ssh_run "$ip" 'sudo mv ~/exelet-ctl /usr/local/bin/exelet-ctl && sudo chmod +x /usr/local/bin/exelet-ctl'

    # Copy setup-exelet.sh — run a modified version for the cluster
    # (We start exelet briefly to preload images, then stop it and install the real systemd unit)
    scp_to "$ip" "${SCRIPT_DIR}/setup-exelet.sh"
    # Patch the script to use /usr/local/bin paths
    ssh_run "$ip" "sudo bash -c 'sed -e \"s|/home/ubuntu/.cache/exedops/exeletd-amd64|/usr/local/bin/exeletd.latest|\" -e \"s|/home/ubuntu/.cache/exedops/exelet-ctl-amd64|/usr/local/bin/exelet-ctl|\" -e \"s|ASSETS_DIR=.*|ASSETS_DIR=/home/ubuntu/.cache/exedops|\" ~/setup-exelet.sh > /root/setup-exelet.sh && chmod +x /root/setup-exelet.sh'"
    ssh_run "$ip" 'sudo /bin/bash /root/setup-exelet.sh'

    # Ensure data directory exists (ZFS mounts /data but /data/exelet must be created)
    ssh_run "$ip" 'sudo mkdir -p /data/exelet'

    # Install systemd unit
    ssh_run "$ip" "sudo tee /etc/systemd/system/exelet.service >/dev/null" <<EOF
[Unit]
Description=exeletd (virt-cluster)
After=network.target zfs.target
Wants=network-online.target

[Service]
Type=simple
CPUWeight=1024
IOWeight=1024
KillMode=process
LimitNOFILE=1048576
WorkingDirectory=/data/exelet

ExecStart=/usr/local/bin/exeletd.latest -D --stage=local --name=${name} --listen-address=tcp://0.0.0.0:9080 --http-addr=0.0.0.0:9081 --data-dir=/data/exelet --storage-manager-address=zfs:///data/exelet/storage?dataset=tank --network-manager-address=nat:///data/exelet/network?network=10.42.0.0/16 --runtime-address=cloudhypervisor:///data/exelet/runtime --exed-url=http://EXED_IP_PLACEHOLDER:8080 --instance-domain=exe.cloud --enable-hugepages --reserved-cpus=0 --storage-replication-enabled --storage-replication-target=zpool:///backup

Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

    ssh_run "$ip" 'sudo systemctl daemon-reload'
    log "Exelet ${name} provisioned (service not yet started — waiting for exed IP)"
}

provision_exed() {
    local ip="$1" exelet_addresses="$2"
    local name
    name="$(vm_name_exed)"
    log "Provisioning exed VM ${name} (${ip})..."

    # Copy exed binary
    scp_to "$ip" "${CACHE_DIR}/exed"
    ssh_run "$ip" 'sudo mv ~/exed /usr/local/bin/exed && sudo chmod +x /usr/local/bin/exed'

    # Copy sshpiperd binary
    scp_to "$ip" "${CACHE_DIR}/sshpiperd"
    ssh_run "$ip" 'sudo mv ~/sshpiperd /usr/local/bin/sshpiperd && sudo chmod +x /usr/local/bin/sshpiperd'

    # Create sshpiper.sh adapted for VM paths
    ssh_run "$ip" "cat > ~/sshpiper.sh" <<'SSHPIPER_SCRIPT'
#!/bin/bash
set -e

PIPER_PLUGIN_PORT="${1:-2224}"
DB_PATH="${2:-/home/ubuntu/exe.db}"

HOST_PRIVATE_KEY=$(sqlite3 "$DB_PATH" "SELECT private_key FROM ssh_host_key WHERE id = 1;")
[ -z "$HOST_PRIVATE_KEY" ] && { echo "No SSH host key found"; exit 1; }
HOST_CERT_SIG=$(sqlite3 "$DB_PATH" "SELECT cert_sig FROM ssh_host_key WHERE id = 1;")

echo "Waiting for piper plugin on port $PIPER_PLUGIN_PORT..."
while ! timeout 1 bash -c "</dev/tcp/localhost/$PIPER_PLUGIN_PORT" 2>/dev/null; do
    sleep 0.1
done
echo "Port $PIPER_PLUGIN_PORT is ready"

HOST_CERT_ARGS=()
if [ -n "$HOST_CERT_SIG" ]; then
    HOST_CERT_ARGS+=(--server-cert-data="$(printf '%s' "$HOST_CERT_SIG" | base64 -w 0)")
fi

exec /usr/local/bin/sshpiperd \
    --log-level=DEBUG \
    --drop-hostkeys-message \
    --port=2222 \
    --address=0.0.0.0 \
    --server-key-data="$(printf '%s' "$HOST_PRIVATE_KEY" | base64 -w 0)" \
    "${HOST_CERT_ARGS[@]}" \
    grpc --endpoint=localhost:$PIPER_PLUGIN_PORT --insecure
SSHPIPER_SCRIPT
    ssh_run "$ip" 'chmod +x ~/sshpiper.sh'

    # Install exed systemd unit
    ssh_run "$ip" "sudo tee /etc/systemd/system/exed.service >/dev/null" <<EOF
[Unit]
Description=exed (virt-cluster)
After=network.target

[Service]
Type=simple
User=${USER_NAME}
Group=${USER_NAME}
WorkingDirectory=/home/${USER_NAME}

ExecStart=/usr/local/bin/exed -stage=local -http=:8080 -ssh=:2223 -piper-plugin=localhost:2224 -piperd-port=2222 -db=/home/${USER_NAME}/exe.db -exelet-addresses=${exelet_addresses}

Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

NoNewPrivileges=false
SecureBits=keep-caps
ProtectHome=no
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

    ssh_run "$ip" 'sudo systemctl daemon-reload && sudo systemctl enable --now exed'

    log "exed provisioned and started on ${ip}"
}

provision_exeprox() {
    local ip="$1" exed_ip="$2" name="$3"
    log "Provisioning exeprox VM ${name} (${ip})..."

    # Copy exeprox binary
    scp_to "$ip" "${CACHE_DIR}/exeprox"
    ssh_run "$ip" 'sudo mv ~/exeprox /usr/local/bin/exeprox.latest && sudo chmod +x /usr/local/bin/exeprox.latest'

    # Install exeprox systemd unit
    ssh_run "$ip" "sudo tee /etc/systemd/system/exeprox.service >/dev/null" <<EOF
[Unit]
Description=exeprox (virt-cluster)
After=network.target

[Service]
Type=simple
User=${USER_NAME}
Group=${USER_NAME}
WorkingDirectory=/home/${USER_NAME}

ExecStart=/usr/local/bin/exeprox.latest -exed-grpc-addr=tcp://${exed_ip}:2225 -http=:8080 -https=:443 -stage=local

Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

    ssh_run "$ip" 'sudo systemctl daemon-reload && sudo systemctl enable --now exeprox'

    log "exeprox provisioned and started on ${ip}"
}

provision_mon() {
    local mon_ip="$1" exed_ip="$2" exeprox_1_ip="$3"
    shift 3
    local exelet_ips=("$@")
    local name
    name="$(vm_name_mon)"
    log "Provisioning mon VM ${name} (${mon_ip})..."

    # a) Generate and deploy prometheus.yml with actual cluster IPs
    local prom_config
    prom_config="global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: \"prometheus\"
    static_configs:
      - targets: [\"localhost:9090\"]
  - job_name: \"node\"
    static_configs:
      - targets: [\"localhost:9100\"]
        labels:
          role: \"mon\"
          stage: \"local\"
      - targets: [\"${exed_ip}:9100\"]
        labels:
          role: \"exed\"
          stage: \"local\""
    for eip in "${exelet_ips[@]}"; do
        prom_config+="
      - targets: [\"${eip}:9100\"]
        labels:
          role: \"exelet\"
          stage: \"local\""
    done
    prom_config+="
      - targets: [\"${exeprox_1_ip}:9100\"]
        labels:
          role: \"exeprox\"
          stage: \"local\"
  - job_name: \"grafana\"
    static_configs:
      - targets: [\"localhost:3000\"]
  - job_name: \"exed\"
    scheme: http
    metrics_path: \"/metrics\"
    static_configs:
      - targets: [\"${exed_ip}:9091\"]
        labels:
          stage: \"local\"
  - job_name: \"exelet\"
    scheme: http
    metrics_path: \"/metrics\"
    static_configs:"
    for eip in "${exelet_ips[@]}"; do
        prom_config+="
      - targets: [\"${eip}:9081\"]
        labels:
          stage: \"local\""
    done
    prom_config+="
  - job_name: \"exeprox\"
    scheme: http
    metrics_path: \"/metrics\"
    static_configs:
      - targets: [\"${exeprox_1_ip}:9091\"]
        labels:
          stage: \"local\""

    ssh_run "$mon_ip" "sudo tee /etc/prometheus/prometheus.yml >/dev/null" <<<"$prom_config"
    ssh_run "$mon_ip" 'sudo systemctl restart prometheus'

    # d) Deploy metrics proxy to exed/exeprox VMs
    # exed and exeprox restrict /metrics to localhost via RequireLocalAccess
    # (checks RemoteAddr is loopback) and isRequestOnMainPort (checks Host
    # header port matches 8080). A Python proxy on each VM listens on 9091,
    # forwards to localhost:8080 with the correct Host header, satisfying both.
    for target_ip in "$exed_ip" "$exeprox_1_ip"; do
        ssh_run "$target_ip" "sudo tee /usr/local/bin/metrics-proxy.py >/dev/null" <<'PYEOF'
#!/usr/bin/env python3
"""Reverse proxy that rewrites Host header for metrics scraping."""
import sys
from http.server import HTTPServer, BaseHTTPRequestHandler
from urllib.request import Request, urlopen

LISTEN_PORT = int(sys.argv[1])
TARGET_PORT = int(sys.argv[2])

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        url = f"http://localhost:{TARGET_PORT}{self.path}"
        req = Request(url, headers={"Host": f"localhost:{TARGET_PORT}", "User-Agent": "Prometheus/metrics-proxy"})
        try:
            resp = urlopen(req, timeout=10)
            body = resp.read()
            self.send_response(resp.status)
            for k, v in resp.getheaders():
                if k.lower() not in ("transfer-encoding", "connection"):
                    self.send_header(k, v)
            self.end_headers()
            self.wfile.write(body)
        except Exception as e:
            self.send_error(502, str(e))
    def log_message(self, format, *args):
        pass

HTTPServer(("0.0.0.0", LISTEN_PORT), Handler).serve_forever()
PYEOF
        ssh_run "$target_ip" 'sudo chmod +x /usr/local/bin/metrics-proxy.py'
        ssh_run "$target_ip" "sudo tee /etc/systemd/system/metrics-proxy.service >/dev/null" <<'MPEOF'
[Unit]
Description=Metrics proxy (Host-header rewrite for remote /metrics scraping)
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/python3 /usr/local/bin/metrics-proxy.py 9091 8080
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
MPEOF
        ssh_run "$target_ip" 'sudo systemctl daemon-reload && sudo systemctl enable --now metrics-proxy'
    done

    # b) Provision Grafana datasource (idempotent — only restart if changed)
    ssh_run "$mon_ip" "sudo mkdir -p /etc/grafana/provisioning/datasources"
    local ds_yaml
    ds_yaml="$(
        cat <<'DSEOF'
apiVersion: 1
datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://localhost:9090
    isDefault: true
DSEOF
    )"
    local existing_ds
    existing_ds="$(ssh_run "$mon_ip" 'cat /etc/grafana/provisioning/datasources/prometheus.yaml 2>/dev/null' || true)"
    if [[ "$existing_ds" != "$ds_yaml" ]]; then
        ssh_run "$mon_ip" "sudo tee /etc/grafana/provisioning/datasources/prometheus.yaml >/dev/null" <<<"$ds_yaml"
        ssh_run "$mon_ip" 'sudo systemctl restart grafana-server'
    fi

    # c) Create Grafana service account + API token (skip if token already exists)
    if ssh_run "$mon_ip" 'test -s /home/ubuntu/grafana-token' 2>/dev/null; then
        log "Grafana token already exists, skipping service account setup"
    else
        log "Waiting for Grafana to become ready..."
        for i in $(seq 1 60); do
            if ssh_run "$mon_ip" 'curl -sf http://localhost:3000/api/health' >/dev/null 2>&1; then
                break
            fi
            sleep 2
        done

        local sa_response sa_id token_response token
        sa_response="$(ssh_run "$mon_ip" "curl -s -X POST http://admin:admin@localhost:3000/api/serviceaccounts -H 'Content-Type: application/json' -d '{\"name\":\"virt-cluster\",\"role\":\"Admin\"}'" 2>/dev/null || true)"
        sa_id="$(echo "$sa_response" | grep -o '"id":[0-9]*' | head -1 | cut -d: -f2)"

        if [[ -n "$sa_id" ]]; then
            token_response="$(ssh_run "$mon_ip" "curl -sf -X POST http://admin:admin@localhost:3000/api/serviceaccounts/${sa_id}/tokens -H 'Content-Type: application/json' -d '{\"name\":\"virt-cluster-token\"}'" 2>/dev/null || true)"
            token="$(echo "$token_response" | grep -o '"key":"[^"]*"' | cut -d'"' -f4)"
            if [[ -n "$token" ]]; then
                ssh_run "$mon_ip" "echo '${token}' > /home/ubuntu/grafana-token"
                log "Grafana service account token saved to /home/ubuntu/grafana-token"
            else
                warn "Failed to create Grafana API token"
            fi
        else
            warn "Failed to create Grafana service account"
        fi
    fi

    log "mon VM provisioned"
}

# ── Port forwarding (socat-based) ────────────────────────────────────────────

SOCAT_PID_DIR="/tmp/${CLUSTER_PREFIX}-socat"

setup_port_forwarding() {
    local exed_ip="$1"
    local mon_ip="${2:-}"
    log "Setting up port forwarding to exed VM (${exed_ip})..."

    # Kill existing socat forwarders for this cluster
    teardown_port_forwarding 2>/dev/null || true

    mkdir -p "${SOCAT_PID_DIR}"

    # Host :2222 → exed :2222 (SSH via sshpiper)
    # Port 2222 is unprivileged — no sudo needed
    socat TCP-LISTEN:2222,fork,reuseaddr "TCP:${exed_ip}:2222" &
    echo $! >"${SOCAT_PID_DIR}/ssh.pid"
    disown

    # Host :8080 → exed :8080 (HTTP) via SSH tunnel so exed sees localhost origin
    # (requireLocalAccess gates /debug and other endpoints on loopback)
    ssh ${SSH_OPTS} -f -N -o ExitOnForwardFailure=yes -L 8080:localhost:8080 "${USER_NAME}@${exed_ip}"
    pgrep -n -f "ssh.*-L 8080:localhost:8080.*${exed_ip}" >"${SOCAT_PID_DIR}/http.pid"

    log "Port forwarding active:"
    log "  SSH:   localhost:2222 -> ${exed_ip}:2222"
    log "  HTTP:  localhost:8080 -> ${exed_ip}:8080"

    # Mon VM port forwarding (Grafana + Prometheus)
    if [[ -n "$mon_ip" ]]; then
        socat TCP-LISTEN:3000,fork,reuseaddr "TCP:${mon_ip}:3000" &
        echo $! >"${SOCAT_PID_DIR}/grafana.pid"
        disown

        socat TCP-LISTEN:9090,fork,reuseaddr "TCP:${mon_ip}:9090" &
        echo $! >"${SOCAT_PID_DIR}/prometheus.pid"
        disown

        log "  Grafana:    localhost:3000 -> ${mon_ip}:3000"
        log "  Prometheus: localhost:9090 -> ${mon_ip}:9090"
    fi
}

teardown_port_forwarding() {
    if [[ -d "${SOCAT_PID_DIR}" ]]; then
        for pidfile in "${SOCAT_PID_DIR}"/*.pid; do
            [[ -f "$pidfile" ]] || continue
            local pid
            pid=$(cat "$pidfile" 2>/dev/null || true)
            if [[ -n "$pid" ]]; then
                kill "$pid" 2>/dev/null || true
            fi
        done
        rm -rf "${SOCAT_PID_DIR}"
    fi
    # Kill any orphaned socat/ssh forwarders on our ports
    pkill -f 'socat TCP-LISTEN:2222,' 2>/dev/null || true
    pkill -f 'socat TCP-LISTEN:3000,' 2>/dev/null || true
    pkill -f 'socat TCP-LISTEN:9090,' 2>/dev/null || true
    # Kill SSH tunnel for HTTP port forwarding
    pkill -f 'ssh.*-L 8080:localhost:8080' 2>/dev/null || true
    pkill -f 'ssh.*-L 8080:localhost:8080' 2>/dev/null || true
}

# ── Envfile ──────────────────────────────────────────────────────────────────

write_envfile() {
    local envfile="${REPO_ROOT}/${CLUSTER_PREFIX}.env"
    log "Writing envfile: ${envfile}"
    {
        echo "# Generated by ops/virt-cluster.sh on $(date -Iseconds)"
        echo "CLUSTER_PREFIX=${CLUSTER_PREFIX}"
        echo "NUM_EXELETS=${NUM_EXELETS}"
        echo "NUM_EXEPROXES=${NUM_EXEPROXES}"
        echo ""

        local exed_ip
        exed_ip="$(get_vm_ip "$(vm_name_exed)")"
        echo "EXED_VM=$(vm_name_exed)"
        echo "EXED_IP=${exed_ip}"
        echo ""

        for i in $(seq 1 "${NUM_EXEPROXES}"); do
            local pname pip
            pname="$(vm_name_exeprox "$i")"
            pip="$(get_vm_ip "$pname")"
            echo "EXEPROX_${i}_VM=${pname}"
            echo "EXEPROX_${i}_IP=${pip}"
        done
        echo ""

        for i in $(seq 1 "${NUM_EXELETS}"); do
            local ename eip
            ename="$(vm_name_exelet "$i")"
            eip="$(get_vm_ip "$ename")"
            echo "EXELET_${i}_VM=${ename}"
            echo "EXELET_${i}_IP=${eip}"
        done
        echo ""

        local mon_ip
        mon_ip="$(get_vm_ip "$(vm_name_mon)")"
        echo "MON_VM=$(vm_name_mon)"
        echo "MON_IP=${mon_ip}"
        if [[ -n "$mon_ip" ]]; then
            local grafana_token
            grafana_token="$(ssh_run "$mon_ip" 'cat /home/ubuntu/grafana-token 2>/dev/null' 2>/dev/null || true)"
            echo "GRAFANA_URL=http://localhost:3000/"
            echo "GRAFANA_BEARER_TOKEN=${grafana_token}"
        fi
        echo ""
        echo "# Access:"
        echo "# HTTP:       http://localhost:8080"
        echo "# SSH:        ssh -p 2222 <box>@localhost"
        echo "# Grafana:    http://localhost:3000 (admin/admin)"
        echo "# Prometheus: http://localhost:9090"
    } >"${envfile}"
    echo "${envfile}"
}

# ── Subcommands ──────────────────────────────────────────────────────────────

cmd_start() {
    check_prerequisites
    ensure_base_image
    ensure_libvirt_default_net
    ensure_apt_cache
    ensure_cloud_hypervisor_artifacts

    # Build all binaries
    build_binaries

    # ── Create VMs ───────────────────────────────────────────────────────

    # Create exed VM
    create_vm "$(vm_name_exed)" "${EXED_VCPUS}" "${EXED_RAM}" generate_cloud_init_exed false

    # Create exeprox VMs
    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        create_vm "$(vm_name_exeprox "$i")" "${EXEPROX_VCPUS}" "${EXEPROX_RAM}" generate_cloud_init_exeprox false
    done

    # Create exelet VMs
    for i in $(seq 1 "${NUM_EXELETS}"); do
        create_vm "$(vm_name_exelet "$i")" "${EXELET_VCPUS}" "${EXELET_RAM}" generate_cloud_init_exelet true
    done

    # Create mon VM
    create_vm "$(vm_name_mon)" "${MON_VCPUS}" "${MON_RAM}" generate_cloud_init_mon false

    # ── Wait for all VMs (IP + SSH + cloud-init) ────────────────────────

    log "Waiting for VMs to become ready..."

    # Launch all waits in parallel, collecting IPs via temp files
    local wait_dir
    wait_dir="$(mktemp -d)"
    local status_dir="${wait_dir}/status"
    mkdir -p "$status_dir"

    # Collect all VM names for the display loop
    local all_names=()
    all_names+=("$(vm_name_exed)")
    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        all_names+=("$(vm_name_exeprox "$i")")
    done
    for i in $(seq 1 "${NUM_EXELETS}"); do
        all_names+=("$(vm_name_exelet "$i")")
    done
    all_names+=("$(vm_name_mon)")

    wait_for_vm_ready "$(vm_name_exed)" "$status_dir" >"${wait_dir}/exed" &
    local pid_exed=$!
    declare -A exeprox_pids
    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        wait_for_vm_ready "$(vm_name_exeprox "$i")" "$status_dir" >"${wait_dir}/exeprox-${i}" &
        exeprox_pids[$i]=$!
    done

    declare -A exelet_pids
    for i in $(seq 1 "${NUM_EXELETS}"); do
        wait_for_vm_ready "$(vm_name_exelet "$i")" "$status_dir" >"${wait_dir}/exelet-${i}" &
        exelet_pids[$i]=$!
    done

    wait_for_vm_ready "$(vm_name_mon)" "$status_dir" >"${wait_dir}/mon" &
    local pid_mon=$!

    # Redraw status lines in-place until all VMs are done
    display_vm_status "$status_dir" "${all_names[@]}"

    # Wait for all background jobs
    wait "$pid_exed" || die "exed VM failed to become ready"
    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        wait "${exeprox_pids[$i]}" || die "exeprox-${i} VM failed to become ready"
    done
    for i in $(seq 1 "${NUM_EXELETS}"); do
        wait "${exelet_pids[$i]}" || die "exelet-${i} VM failed to become ready"
    done
    wait "$pid_mon" || die "mon VM failed to become ready"

    # Read IPs from temp files
    local exed_ip
    exed_ip="$(cat "${wait_dir}/exed")"

    declare -A exeprox_ips
    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        exeprox_ips[$i]="$(cat "${wait_dir}/exeprox-${i}")"
    done

    declare -A exelet_ips
    for i in $(seq 1 "${NUM_EXELETS}"); do
        exelet_ips[$i]="$(cat "${wait_dir}/exelet-${i}")"
    done

    local mon_ip
    mon_ip="$(cat "${wait_dir}/mon")"

    log "All VMs ready"

    # ── Check if already provisioned (idempotent) ────────────────────────

    local needs_provision=true
    if ssh_run "$exed_ip" 'test -f /usr/local/bin/exed' 2>/dev/null; then
        log "Cluster appears already provisioned. Use 'deploy' to update binaries."
        needs_provision=false
    fi

    # Build exelet address list using hostnames (region is parsed from hostname)
    local exelet_addr_list=""
    for i in $(seq 1 "${NUM_EXELETS}"); do
        local ename
        ename="$(vm_name_exelet "$i")"
        if [[ -n "$exelet_addr_list" ]]; then
            exelet_addr_list+=","
        fi
        exelet_addr_list+="tcp://${ename}:9080"
    done

    # Write /etc/hosts entries on exed VM so it can resolve exelet hostnames
    update_exed_hosts() {
        local target_ip="$1"
        local hosts_block="# BEGIN virt-cluster exelets"
        for i in $(seq 1 "${NUM_EXELETS}"); do
            hosts_block+=$'\n'"${exelet_ips[$i]} $(vm_name_exelet "$i")"
        done
        hosts_block+=$'\n'"# END virt-cluster exelets"
        ssh_run "$target_ip" "sudo sed -i '/# BEGIN virt-cluster exelets/,/# END virt-cluster exelets/d' /etc/hosts"
        ssh_run "$target_ip" "echo '${hosts_block}' | sudo tee -a /etc/hosts >/dev/null"
    }

    if [[ "$needs_provision" == "true" ]]; then
        # ── Provision exelet VMs ─────────────────────────────────────────
        for i in $(seq 1 "${NUM_EXELETS}"); do
            local ename
            ename="$(vm_name_exelet "$i")"
            provision_exelet "${exelet_ips[$i]}" "$ename" "${exelet_ips[$i]}"
        done

        # ── Provision exed VM ────────────────────────────────────────────
        provision_exed "$exed_ip" "$exelet_addr_list"
        update_exed_hosts "$exed_ip"

        # Patch exelet units with actual exed IP and start them
        for i in $(seq 1 "${NUM_EXELETS}"); do
            ssh_run "${exelet_ips[$i]}" "sudo sed -i 's|EXED_IP_PLACEHOLDER|${exed_ip}|g' /etc/systemd/system/exelet.service"
            ssh_run "${exelet_ips[$i]}" 'sudo systemctl daemon-reload && sudo systemctl enable --now exelet'
            log "Started exeletd on $(vm_name_exelet "$i")"
        done

        # ── Provision exeprox VMs ────────────────────────────────────────
        for i in $(seq 1 "${NUM_EXEPROXES}"); do
            provision_exeprox "${exeprox_ips[$i]}" "$exed_ip" "$(vm_name_exeprox "$i")"
        done

        # ── Provision mon VM ─────────────────────────────────────────
        local mon_exelet_ip_list=()
        for i in $(seq 1 "${NUM_EXELETS}"); do
            mon_exelet_ip_list+=("${exelet_ips[$i]}")
        done
        provision_mon "$mon_ip" "$exed_ip" "${exeprox_ips[1]}" "${mon_exelet_ip_list[@]}"
    else
        # ── Refresh IPs in systemd units (DHCP may have reassigned) ──────
        log "Refreshing service configs with current IPs..."

        # Update /etc/hosts and exelet addresses on exed
        update_exed_hosts "$exed_ip"
        ssh_run "$exed_ip" "sudo sed -i 's|-exelet-addresses=[^ ]*|-exelet-addresses=${exelet_addr_list}|' /etc/systemd/system/exed.service"
        ssh_run "$exed_ip" 'sudo systemctl daemon-reload && sudo systemctl restart exed'

        # Update exelets with current exed IP
        for i in $(seq 1 "${NUM_EXELETS}"); do
            ssh_run "${exelet_ips[$i]}" "sudo sed -i 's|--exed-url=http://[^:]*:8080|--exed-url=http://${exed_ip}:8080|' /etc/systemd/system/exelet.service"
            ssh_run "${exelet_ips[$i]}" 'sudo systemctl daemon-reload && sudo systemctl restart exelet'
        done

        # Update exeproxes with current exed IP
        for i in $(seq 1 "${NUM_EXEPROXES}"); do
            ssh_run "${exeprox_ips[$i]}" "sudo sed -i 's|-exed-grpc-addr=tcp://[^:]*:2225|-exed-grpc-addr=tcp://${exed_ip}:2225|' /etc/systemd/system/exeprox.service"
            ssh_run "${exeprox_ips[$i]}" 'sudo systemctl daemon-reload && sudo systemctl restart exeprox'
        done

        # Refresh mon prometheus config with current IPs
        local mon_exelet_ip_list=()
        for i in $(seq 1 "${NUM_EXELETS}"); do
            mon_exelet_ip_list+=("${exelet_ips[$i]}")
        done
        provision_mon "$mon_ip" "$exed_ip" "${exeprox_ips[1]}" "${mon_exelet_ip_list[@]}"
    fi

    # ── Port forwarding ──────────────────────────────────────────────────
    setup_port_forwarding "$exed_ip" "$mon_ip"

    # ── Write envfile ────────────────────────────────────────────────────
    write_envfile

    rm -rf "$wait_dir"

    log ""
    log "Cluster is ready!"
    log "  HTTP:       http://localhost:8080"
    log "  SSH:        ssh -p 2222 <box>@localhost"
    log "  Grafana:    http://localhost:3000 (admin/admin)"
    log "  Prometheus: http://localhost:9090"
}

cmd_deploy() {
    log "Deploying updated binaries to cluster..."

    # Build fresh binaries
    build_binaries

    # Discover IPs from running VMs
    local exed_ip
    exed_ip="$(get_vm_ip "$(vm_name_exed)")"

    if [[ -z "$exed_ip" ]]; then
        die "exed VM not running. Run 'start' first."
    fi

    # ── Deploy to exelet VMs ─────────────────────────────────────────────
    for i in $(seq 1 "${NUM_EXELETS}"); do
        local ename eip
        ename="$(vm_name_exelet "$i")"
        eip="$(get_vm_ip "$ename")"
        if [[ -z "$eip" ]]; then
            warn "Exelet VM ${ename} not running, skipping"
            continue
        fi
        log "Deploying exeletd to ${ename} (${eip})..."
        scp_to "$eip" "${CACHE_DIR}/exeletd" "${CACHE_DIR}/exelet-ctl"
        ssh_run "$eip" 'sudo systemctl stop exelet || true'
        ssh_run "$eip" 'sudo mv ~/exeletd /usr/local/bin/exeletd.latest && sudo chmod +x /usr/local/bin/exeletd.latest'
        ssh_run "$eip" 'sudo mv ~/exelet-ctl /usr/local/bin/exelet-ctl && sudo chmod +x /usr/local/bin/exelet-ctl'
        ssh_run "$eip" 'sudo systemctl start exelet'
        log "  ${ename}: exeletd restarted"
    done

    # ── Deploy to exed VM ────────────────────────────────────────────────
    log "Deploying exed to $(vm_name_exed) (${exed_ip})..."
    scp_to "$exed_ip" "${CACHE_DIR}/exed" "${CACHE_DIR}/sshpiperd"
    ssh_run "$exed_ip" 'sudo systemctl stop exed || true'
    ssh_run "$exed_ip" 'sudo mv ~/exed /usr/local/bin/exed && sudo chmod +x /usr/local/bin/exed'
    ssh_run "$exed_ip" 'sudo mv ~/sshpiperd /usr/local/bin/sshpiperd && sudo chmod +x /usr/local/bin/sshpiperd'
    ssh_run "$exed_ip" 'sudo systemctl start exed'
    log "  exed restarted"

    # ── Deploy to exeprox VMs ────────────────────────────────────────────
    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        local pname pip
        pname="$(vm_name_exeprox "$i")"
        pip="$(get_vm_ip "$pname")"
        if [[ -z "$pip" ]]; then
            warn "Exeprox VM ${pname} not running, skipping"
            continue
        fi
        log "Deploying exeprox to ${pname} (${pip})..."
        scp_to "$pip" "${CACHE_DIR}/exeprox"
        ssh_run "$pip" 'sudo systemctl stop exeprox || true'
        ssh_run "$pip" 'sudo mv ~/exeprox /usr/local/bin/exeprox.latest && sudo chmod +x /usr/local/bin/exeprox.latest'
        ssh_run "$pip" 'sudo systemctl start exeprox'
        log "  ${pname}: exeprox restarted"
    done

    # ── Refresh mon prometheus config ────────────────────────────────────
    local mon_ip
    mon_ip="$(get_vm_ip "$(vm_name_mon)")"
    if [[ -n "$mon_ip" ]]; then
        log "Refreshing prometheus config on mon VM..."
        local mon_exelet_ip_list=()
        for i in $(seq 1 "${NUM_EXELETS}"); do
            local ename eip
            ename="$(vm_name_exelet "$i")"
            eip="$(get_vm_ip "$ename")"
            [[ -n "$eip" ]] && mon_exelet_ip_list+=("$eip")
        done
        local exeprox_1_ip
        exeprox_1_ip="$(get_vm_ip "$(vm_name_exeprox 1)")"
        provision_mon "$mon_ip" "$exed_ip" "${exeprox_1_ip}" "${mon_exelet_ip_list[@]}"
    fi

    # Refresh port forwarding (IPs may have changed after reboot, though unlikely)
    setup_port_forwarding "$exed_ip" "$mon_ip"

    log "Deploy complete!"
}

cmd_deploy_metrics() {
    local mon_ip
    mon_ip="$(get_vm_ip "$(vm_name_mon)")"
    if [[ -z "$mon_ip" ]]; then
        die "mon VM not running. Run 'start' first."
    fi

    # ── Regenerate prometheus config with current IPs ─────────────────
    local exed_ip
    exed_ip="$(get_vm_ip "$(vm_name_exed)")"
    if [[ -z "$exed_ip" ]]; then
        die "exed VM not running. Run 'start' first."
    fi

    local mon_exelet_ip_list=()
    for i in $(seq 1 "${NUM_EXELETS}"); do
        local eip
        eip="$(get_vm_ip "$(vm_name_exelet "$i")")"
        [[ -n "$eip" ]] && mon_exelet_ip_list+=("$eip")
    done
    local exeprox_1_ip
    exeprox_1_ip="$(get_vm_ip "$(vm_name_exeprox 1)")"

    provision_mon "$mon_ip" "$exed_ip" "${exeprox_1_ip}" "${mon_exelet_ip_list[@]}"

    # ── Deploy node scripts to all VMs ───────────────────────────────
    # os-updates and system-health go to every VM; zpool-metrics only to exelets.
    for name in $(all_vm_names); do
        local vip
        vip="$(get_vm_ip "$name")"
        if [[ -z "$vip" ]]; then
            warn "VM ${name} not running, skipping node scripts"
            continue
        fi
        log "Deploying node scripts to ${name} (${vip})..."
        scp_to "$vip" "${REPO_ROOT}/observability/scripts/os-updates.sh"
        scp_to "$vip" "${REPO_ROOT}/observability/scripts/system-health.sh"
        ssh_run "$vip" "sudo mkdir -p /var/lib/prometheus/node-exporter && \
            sudo mv ~/os-updates.sh /usr/local/bin/os-updates.sh && \
            sudo chmod +x /usr/local/bin/os-updates.sh && \
            sudo /usr/local/bin/os-updates.sh && \
            sudo mv ~/system-health.sh /usr/local/bin/system-health.sh && \
            sudo chmod +x /usr/local/bin/system-health.sh && \
            sudo /usr/local/bin/system-health.sh"
        local cron_entries="'*/15 * * * * /usr/local/bin/os-updates.sh'; echo '* * * * * /usr/local/bin/system-health.sh'"
        local cron_filter="grep -v os-updates | grep -v system-health"
        # Exelet-specific: zpool-metrics script
        if [[ "$name" == exelet-* ]]; then
            scp_to "$vip" "${REPO_ROOT}/observability/scripts/zpool-metrics.sh"
            ssh_run "$vip" "sudo mv ~/zpool-metrics.sh /usr/local/bin/zpool-metrics.sh && \
                sudo chmod +x /usr/local/bin/zpool-metrics.sh && \
                sudo /usr/local/bin/zpool-metrics.sh"
            cron_entries="'* * * * * /usr/local/bin/zpool-metrics.sh'; echo ${cron_entries}"
            cron_filter="grep -v zpool-metrics | ${cron_filter}"
        fi
        # Install cron jobs
        ssh_run "$vip" "(sudo crontab -l 2>/dev/null | ${cron_filter}; echo ${cron_entries}) | sudo crontab -"
        # Enable textfile collector on node-exporter if not already configured
        if ! ssh_run "$vip" 'grep -q textfile /etc/default/prometheus-node-exporter' 2>/dev/null; then
            ssh_run "$vip" "sudo sed -i 's|^ARGS=.*|ARGS=\"--collector.textfile --collector.textfile.directory=/var/lib/prometheus/node-exporter\"|' /etc/default/prometheus-node-exporter"
            ssh_run "$vip" 'sudo systemctl restart prometheus-node-exporter'
        fi
    done

    # ── Ensure port forwarding is active ─────────────────────────────
    setup_port_forwarding "$exed_ip" "$mon_ip"

    # ── Deploy Grafana dashboards ─────────────────────────────────────
    local grafana_token
    grafana_token="$(ssh_run "$mon_ip" 'cat /home/ubuntu/grafana-token 2>/dev/null' 2>/dev/null || true)"
    if [[ -z "$grafana_token" ]]; then
        die "No Grafana token found on mon VM. Was the cluster provisioned?"
    fi

    log "Deploying Grafana dashboards to local Grafana (http://localhost:3000)..."
    (cd "${REPO_ROOT}/observability" &&
        DEFAULT_STAGE=local make deploy-grafana \
            GRAFANA_URL="http://localhost:3000/" \
            GRAFANA_BEARER_TOKEN="${grafana_token}")
}

cmd_stop() {
    log "Stopping cluster VMs..."
    teardown_port_forwarding
    stop_apt_cache

    for name in $(all_vm_names); do
        if vm_running "$name"; then
            log "Shutting down ${name}..."
            sudo virsh shutdown "$name" 2>/dev/null || true
        fi
    done

    # Wait briefly for graceful shutdown
    log "Waiting for VMs to shut down..."
    for attempt in $(seq 1 30); do
        local still_running=false
        for name in $(all_vm_names); do
            if vm_running "$name"; then
                still_running=true
                break
            fi
        done
        if [[ "$still_running" == "false" ]]; then
            break
        fi
        sleep 2
    done

    # Force-stop any that didn't shut down gracefully
    for name in $(all_vm_names); do
        if vm_running "$name"; then
            warn "Force-stopping ${name}..."
            sudo virsh destroy "$name" 2>/dev/null || true
        fi
    done

    log "All VMs stopped"
}

cmd_status() {
    echo "Cluster: ${CLUSTER_PREFIX}"
    echo ""
    printf "%-30s %-12s %-18s %s\n" "VM" "STATE" "IP" "SERVICE"
    printf "%-30s %-12s %-18s %s\n" "--" "-----" "--" "-------"

    for name in $(all_vm_names); do
        local state="not found"
        local ip="-"
        local service="-"

        if vm_exists "$name"; then
            state="$(sudo virsh domstate "$name" 2>/dev/null || echo "unknown")"
            state="$(echo "$state" | tr -d '[:space:]')"
            if [[ "$state" == "running" ]]; then
                ip="$(get_vm_ip "$name" || echo "-")"
                [[ -z "$ip" ]] && ip="-"

                # Check service status
                if [[ "$ip" != "-" ]]; then
                    if [[ "$name" == *-exed ]]; then
                        service="exed"
                        if ssh_run "$ip" 'systemctl is-active exed' 2>/dev/null | grep -q "^active$"; then
                            service="exed (active)"
                        else
                            service="exed (inactive)"
                        fi
                    elif [[ "$name" == exeprox-* ]]; then
                        service="exeprox"
                        if ssh_run "$ip" 'systemctl is-active exeprox' 2>/dev/null | grep -q "^active$"; then
                            service="exeprox (active)"
                        else
                            service="exeprox (inactive)"
                        fi
                    elif [[ "$name" == exelet-* ]]; then
                        service="exeletd"
                        if ssh_run "$ip" 'systemctl is-active exelet' 2>/dev/null | grep -q "^active$"; then
                            service="exeletd (active)"
                        else
                            service="exeletd (inactive)"
                        fi
                    elif [[ "$name" == *-mon ]]; then
                        local prom_status graf_status
                        prom_status="$(ssh_run "$ip" 'systemctl is-active prometheus' 2>/dev/null || echo "inactive")"
                        graf_status="$(ssh_run "$ip" 'systemctl is-active grafana-server' 2>/dev/null || echo "inactive")"
                        service="prometheus (${prom_status}), grafana (${graf_status})"
                    fi
                fi
            fi
        fi

        printf "%-30s %-12s %-18s %s\n" "$name" "$state" "$ip" "$service"
    done

    echo ""

    # Port forwarding status
    echo "Port forwarding:"
    if [[ -d "${SOCAT_PID_DIR}" ]]; then
        for pidfile in "${SOCAT_PID_DIR}"/*.pid; do
            [[ -f "$pidfile" ]] || continue
            local label pid
            label="$(basename "$pidfile" .pid)"
            pid=$(cat "$pidfile" 2>/dev/null || true)
            if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
                echo "  ${label}: active (pid ${pid})"
            else
                echo "  ${label}: dead"
            fi
        done
    else
        echo "  none"
    fi

    # Grafana access info
    local mon_ip
    mon_ip="$(get_vm_ip "$(vm_name_mon)" || true)"
    if [[ -n "$mon_ip" ]]; then
        echo ""
        echo "Grafana: http://localhost:3000 (admin/admin)"
        local grafana_token
        grafana_token="$(ssh_run "$mon_ip" 'cat /home/ubuntu/grafana-token 2>/dev/null' 2>/dev/null || true)"
        if [[ -n "$grafana_token" ]]; then
            echo "Grafana Bearer Token: ${grafana_token}"
        fi
    fi
}

cmd_destroy() {
    log "Destroying cluster..."
    teardown_port_forwarding
    destroy_apt_cache

    local names
    names="$(all_vm_names)"
    for name in $names; do
        if vm_exists "$name"; then
            log "Destroying ${name}..."
            sudo virsh destroy "$name" >/dev/null 2>&1 || true
            sudo virsh undefine "$name" --remove-all-storage >/dev/null 2>&1 || true
        else
            log "VM ${name} not found, skipping"
        fi

        # Clean up any leftover disk images and seed ISOs via virsh
        for f in "${name}.qcow2" "${name}-data.qcow2" "${name}-backup.qcow2" "${name}-seed.iso"; do
            sudo virsh vol-delete --pool default "${f}" >/dev/null 2>&1 || true
        done
    done

    # Remove envfile
    rm -f "${REPO_ROOT}/${CLUSTER_PREFIX}.env"

    log "Cluster destroyed"
}

# ── Main ─────────────────────────────────────────────────────────────────────

case "${1:-}" in
start) cmd_start ;;
stop) cmd_stop ;;
status) cmd_status ;;
destroy) cmd_destroy ;;
deploy) cmd_deploy ;;
deploy-metrics) cmd_deploy_metrics ;;
*)
    echo "Usage: $0 {start|stop|status|destroy|deploy|deploy-metrics}"
    echo ""
    echo "Subcommands:"
    echo "  start           Create and provision the VM cluster (idempotent)"
    echo "  stop            Gracefully stop all VMs (preserves disks)"
    echo "  status          Show cluster status, IPs, and services"
    echo "  destroy         Tear down all VMs and remove disks"
    echo "  deploy          Rebuild binaries, push to VMs, restart services"
    echo "  deploy-metrics  Update prometheus config and deploy Grafana dashboards"
    echo ""
    echo "Environment variables:"
    echo "  NUM_EXELETS=${NUM_EXELETS}  NUM_EXEPROXES=${NUM_EXEPROXES}  CLUSTER_PREFIX=${CLUSTER_PREFIX}"
    echo "  EXED_VCPUS=${EXED_VCPUS}  EXED_RAM=${EXED_RAM}  EXEPROX_VCPUS=${EXEPROX_VCPUS}  EXEPROX_RAM=${EXEPROX_RAM}"
    echo "  EXELET_VCPUS=${EXELET_VCPUS}  EXELET_RAM=${EXELET_RAM}  MON_VCPUS=${MON_VCPUS}  MON_RAM=${MON_RAM}"
    echo "  DISK_GB=${DISK_GB}  EXELET_DATA_DISK_GB=${EXELET_DATA_DISK_GB}  EXELET_BACKUP_DISK_GB=${EXELET_BACKUP_DISK_GB}  EXELET_SWAP_SIZE=${EXELET_SWAP_SIZE}"
    echo "  APT_CACHE_ENABLED=${APT_CACHE_ENABLED}  (run apt-cacher-ng in Docker for faster/offline package installs)"
    exit 1
    ;;
esac
