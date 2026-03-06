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
EXED_VCPUS="${EXED_VCPUS:-2}"
EXED_RAM="${EXED_RAM:-4096}"
EXEPROX_VCPUS="${EXEPROX_VCPUS:-1}"
EXEPROX_RAM="${EXEPROX_RAM:-2048}"
EXELET_VCPUS="${EXELET_VCPUS:-4}"
EXELET_RAM="${EXELET_RAM:-8192}"
DISK_GB="${DISK_GB:-40}"
EXELET_DATA_DISK_GB="${EXELET_DATA_DISK_GB:-50}"
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

# ── VM Names ─────────────────────────────────────────────────────────────────

vm_name_exed() { echo "${CLUSTER_PREFIX}-exed"; }
vm_name_exeprox() { echo "${CLUSTER_PREFIX}-exeprox"; }
vm_name_exelet() { printf '%s-dev-ctr-%02d\n' "${CLUSTER_PREFIX}" "$1"; }

all_vm_names() {
    vm_name_exed
    vm_name_exeprox
    for i in $(seq 1 "${NUM_EXELETS}"); do
        vm_name_exelet "$i"
    done
}

# ── Helpers ──────────────────────────────────────────────────────────────────

# Kill all background jobs on exit so nothing lingers after errors
cleanup_jobs() { jobs -p | xargs -r kill 2>/dev/null || true; }
trap cleanup_jobs EXIT

log() { echo "==> $*"; }
warn() { echo "WARN: $*" >&2; }
die() { echo "ERROR: $*" >&2; exit 1; }

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
}

ensure_base_image() {
    sudo mkdir -p "${WORKDIR}"
    if [[ ! -f "${BASE_IMG}" ]]; then
        log "Downloading Ubuntu 24.04 cloud image..."
        sudo curl -L "${BASE_IMG_URL}" -o "${BASE_IMG}"
    fi
}

get_vm_ip() {
    local name="$1"
    local ip

    # Try lease-based lookup first (fastest when dnsmasq is working)
    ip=$(sudo virsh domifaddr "$name" --source lease 2>/dev/null \
        | awk '/ipv4/ {print $4}' | sed 's|/.*||' | head -n1)
    if [[ -n "$ip" ]]; then
        echo "$ip"
        return 0
    fi

    # Fallback: QEMU's own ARP table (works even when dnsmasq leases are empty)
    ip=$(sudo virsh domifaddr "$name" --source arp 2>/dev/null \
        | awk '/ipv4/ {print $4}' | sed 's|/.*||' | head -n1)
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
    echo "waiting for IP..." > "$sf"

    # IP
    local ip=""
    for i in $(seq 1 120); do
        ip="$(get_vm_ip "$name" || true)"
        if [[ -n "$ip" ]]; then break; fi
        sleep 1
    done
    if [[ -z "$ip" ]]; then
        echo "FAILED (no IP after 120s)" > "$sf"
        return 1
    fi
    echo "IP=${ip}, waiting for SSH..." > "$sf"

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
        echo "FAILED (SSH timeout)" > "$sf"
        return 1
    fi
    echo "cloud-init..." > "$sf"

    ssh ${SSH_OPTS} "${USER_NAME}@${ip}" 'sudo cloud-init status --wait' >/dev/null 2>&1

    echo "READY (${ip})" > "$sf"

    # Return the IP on stdout
    echo "$ip"
}

# Display loop: redraws VM status lines in-place until all are done.
# Args: status_dir vm_name1 vm_name2 ...
display_vm_status() {
    local status_dir="$1"; shift
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
    local ip="$1"; shift
    scp ${SSH_OPTS} "$@" "${USER_NAME}@${ip}:~/"
}

ssh_run() {
    local ip="$1"; shift
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
package_update: false
packages:
  - curl
  - jq
  - sqlite3
  - net-tools
runcmd:
  - systemctl enable --now qemu-guest-agent || true
  - systemctl disable --now apt-daily.timer apt-daily-upgrade.timer || true
  - systemctl mask apt-daily.service apt-daily-upgrade.service || true
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
package_update: false
packages:
  - curl
  - jq
runcmd:
  - systemctl enable --now qemu-guest-agent || true
  - systemctl disable --now apt-daily.timer apt-daily-upgrade.timer || true
  - systemctl mask apt-daily.service apt-daily-upgrade.service || true
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
package_update: false
packages:
  - qemu-guest-agent
  - zfsutils-linux
  - socat
  - net-tools
  - isal
  - curl
  - jq
runcmd:
  - systemctl enable --now qemu-guest-agent || true
  - systemctl disable --now apt-daily.timer apt-daily-upgrade.timer || true
  - systemctl mask apt-daily.service apt-daily-upgrade.service || true
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
        --noautoconsole \
    || die "Failed to create VM ${name}"
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
    ssh_run "$ip" 'sudo mv ~/exeletd /usr/local/bin/exeletd && sudo chmod +x /usr/local/bin/exeletd'
    ssh_run "$ip" 'sudo mv ~/exelet-ctl /usr/local/bin/exelet-ctl && sudo chmod +x /usr/local/bin/exelet-ctl'

    # Copy setup-exelet.sh — run a modified version for the cluster
    # (We start exelet briefly to preload images, then stop it and install the real systemd unit)
    scp_to "$ip" "${SCRIPT_DIR}/setup-exelet.sh"
    # Patch the script to use /usr/local/bin paths
    ssh_run "$ip" "sudo bash -c 'sed -e \"s|/home/ubuntu/.cache/exedops/exeletd-amd64|/usr/local/bin/exeletd|\" -e \"s|/home/ubuntu/.cache/exedops/exelet-ctl-amd64|/usr/local/bin/exelet-ctl|\" -e \"s|ASSETS_DIR=.*|ASSETS_DIR=/home/ubuntu/.cache/exedops|\" ~/setup-exelet.sh > /root/setup-exelet.sh && chmod +x /root/setup-exelet.sh'"
    ssh_run "$ip" 'sudo /bin/bash /root/setup-exelet.sh'

    # Ensure data directory exists (ZFS mounts /data but /data/exelet must be created)
    ssh_run "$ip" 'sudo mkdir -p /data/exelet'

    # Install systemd unit
    ssh_run "$ip" "sudo tee /etc/systemd/system/exeletd.service >/dev/null" <<EOF
[Unit]
Description=exeletd (virt-cluster)
After=network.target zfs.target
Wants=network-online.target

[Service]
Type=simple
CPUWeight=1024
IOWeight=1024
LimitNOFILE=1048576
WorkingDirectory=/data/exelet

ExecStart=/usr/local/bin/exeletd -D --stage=local --name=${name} --listen-address=tcp://0.0.0.0:9080 --http-addr=0.0.0.0:9081 --data-dir=/data/exelet --storage-manager-address=zfs:///data/exelet/storage?dataset=tank --network-manager-address=nat:///data/exelet/network?network=10.42.0.0/16 --runtime-address=cloudhypervisor:///data/exelet/runtime --exed-url=http://EXED_IP_PLACEHOLDER:8080 --instance-domain=exe.cloud --enable-hugepages --reserved-cpus=0

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
    local ip="$1" exed_ip="$2"
    local name
    name="$(vm_name_exeprox)"
    log "Provisioning exeprox VM ${name} (${ip})..."

    # Copy exeprox binary
    scp_to "$ip" "${CACHE_DIR}/exeprox"
    ssh_run "$ip" 'sudo mv ~/exeprox /usr/local/bin/exeprox && sudo chmod +x /usr/local/bin/exeprox'

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

ExecStart=/usr/local/bin/exeprox -exed-grpc-addr=tcp://${exed_ip}:2225 -http=:8080 -https=:443 -stage=local

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

# ── Port forwarding (socat-based) ────────────────────────────────────────────

SOCAT_PID_DIR="/tmp/${CLUSTER_PREFIX}-socat"

setup_port_forwarding() {
    local exed_ip="$1"
    log "Setting up port forwarding to exed VM (${exed_ip})..."

    # Kill existing socat forwarders for this cluster
    teardown_port_forwarding 2>/dev/null || true

    mkdir -p "${SOCAT_PID_DIR}"

    # Host :2222 → exed :2222 (SSH via sshpiper)
    sudo sh -c "socat TCP-LISTEN:2222,fork,reuseaddr TCP:${exed_ip}:2222 & echo \$! > '${SOCAT_PID_DIR}/ssh.pid'"

    # Host :8080 → exed :8080 (HTTP) via SSH tunnel so exed sees localhost origin
    # (requireLocalAccess gates /debug and other endpoints on loopback)
    ssh ${SSH_OPTS} -f -N -o ExitOnForwardFailure=yes -L 8080:localhost:8080 "${USER_NAME}@${exed_ip}"
    pgrep -n -f "ssh.*-L 8080:localhost:8080.*${exed_ip}" > "${SOCAT_PID_DIR}/http.pid"

    log "Port forwarding active:"
    log "  SSH:   localhost:2222 -> ${exed_ip}:2222"
    log "  HTTP:  localhost:8080 -> ${exed_ip}:8080"
}

teardown_port_forwarding() {
    if [[ -d "${SOCAT_PID_DIR}" ]]; then
        for pidfile in "${SOCAT_PID_DIR}"/*.pid; do
            [[ -f "$pidfile" ]] || continue
            local pid
            pid=$(sudo cat "$pidfile" 2>/dev/null || true)
            if [[ -n "$pid" ]]; then
                sudo kill "$pid" 2>/dev/null || true
            fi
        done
        sudo rm -rf "${SOCAT_PID_DIR}"
    fi
}

# ── Envfile ──────────────────────────────────────────────────────────────────

write_envfile() {
    local envfile="${REPO_ROOT}/${CLUSTER_PREFIX}.env"
    log "Writing envfile: ${envfile}"
    {
        echo "# Generated by ops/virt-cluster.sh on $(date -Iseconds)"
        echo "CLUSTER_PREFIX=${CLUSTER_PREFIX}"
        echo "NUM_EXELETS=${NUM_EXELETS}"
        echo ""

        local exed_ip
        exed_ip="$(get_vm_ip "$(vm_name_exed)")"
        echo "EXED_VM=$(vm_name_exed)"
        echo "EXED_IP=${exed_ip}"
        echo ""

        local exeprox_ip
        exeprox_ip="$(get_vm_ip "$(vm_name_exeprox)")"
        echo "EXEPROX_VM=$(vm_name_exeprox)"
        echo "EXEPROX_IP=${exeprox_ip}"
        echo ""

        for i in $(seq 1 "${NUM_EXELETS}"); do
            local ename eip
            ename="$(vm_name_exelet "$i")"
            eip="$(get_vm_ip "$ename")"
            echo "EXELET_${i}_VM=${ename}"
            echo "EXELET_${i}_IP=${eip}"
        done
        echo ""
        echo "# Access:"
        echo "# HTTP:  http://localhost:8080"
        echo "# SSH:   ssh -p 2222 <box>@localhost"
    } >"${envfile}"
    echo "${envfile}"
}

# ── Subcommands ──────────────────────────────────────────────────────────────

cmd_start() {
    check_prerequisites
    ensure_base_image
    ensure_libvirt_default_net
    ensure_cloud_hypervisor_artifacts

    # Build all binaries
    build_binaries

    # ── Create VMs ───────────────────────────────────────────────────────

    # Create exed VM
    create_vm "$(vm_name_exed)" "${EXED_VCPUS}" "${EXED_RAM}" generate_cloud_init_exed false

    # Create exeprox VM
    create_vm "$(vm_name_exeprox)" "${EXEPROX_VCPUS}" "${EXEPROX_RAM}" generate_cloud_init_exeprox false

    # Create exelet VMs
    for i in $(seq 1 "${NUM_EXELETS}"); do
        create_vm "$(vm_name_exelet "$i")" "${EXELET_VCPUS}" "${EXELET_RAM}" generate_cloud_init_exelet true
    done

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
    all_names+=("$(vm_name_exeprox)")
    for i in $(seq 1 "${NUM_EXELETS}"); do
        all_names+=("$(vm_name_exelet "$i")")
    done

    wait_for_vm_ready "$(vm_name_exed)" "$status_dir" >"${wait_dir}/exed" &
    local pid_exed=$!
    wait_for_vm_ready "$(vm_name_exeprox)" "$status_dir" >"${wait_dir}/exeprox" &
    local pid_exeprox=$!

    declare -A exelet_pids
    for i in $(seq 1 "${NUM_EXELETS}"); do
        wait_for_vm_ready "$(vm_name_exelet "$i")" "$status_dir" >"${wait_dir}/exelet-${i}" &
        exelet_pids[$i]=$!
    done

    # Redraw status lines in-place until all VMs are done
    display_vm_status "$status_dir" "${all_names[@]}"

    # Wait for all background jobs
    wait "$pid_exed" || die "exed VM failed to become ready"
    wait "$pid_exeprox" || die "exeprox VM failed to become ready"
    for i in $(seq 1 "${NUM_EXELETS}"); do
        wait "${exelet_pids[$i]}" || die "exelet-${i} VM failed to become ready"
    done

    # Read IPs from temp files
    local exed_ip exeprox_ip
    exed_ip="$(cat "${wait_dir}/exed")"
    exeprox_ip="$(cat "${wait_dir}/exeprox")"

    declare -A exelet_ips
    for i in $(seq 1 "${NUM_EXELETS}"); do
        exelet_ips[$i]="$(cat "${wait_dir}/exelet-${i}")"
    done

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
            ssh_run "${exelet_ips[$i]}" "sudo sed -i 's|EXED_IP_PLACEHOLDER|${exed_ip}|g' /etc/systemd/system/exeletd.service"
            ssh_run "${exelet_ips[$i]}" 'sudo systemctl daemon-reload && sudo systemctl enable --now exeletd'
            log "Started exeletd on $(vm_name_exelet "$i")"
        done

        # ── Provision exeprox VM ─────────────────────────────────────────
        provision_exeprox "$exeprox_ip" "$exed_ip"
    else
        # ── Refresh IPs in systemd units (DHCP may have reassigned) ──────
        log "Refreshing service configs with current IPs..."

        # Update /etc/hosts and exelet addresses on exed
        update_exed_hosts "$exed_ip"
        ssh_run "$exed_ip" "sudo sed -i 's|-exelet-addresses=[^ ]*|-exelet-addresses=${exelet_addr_list}|' /etc/systemd/system/exed.service"
        ssh_run "$exed_ip" 'sudo systemctl daemon-reload && sudo systemctl restart exed'

        # Update exelets with current exed IP
        for i in $(seq 1 "${NUM_EXELETS}"); do
            ssh_run "${exelet_ips[$i]}" "sudo sed -i 's|--exed-url=http://[^:]*:8080|--exed-url=http://${exed_ip}:8080|' /etc/systemd/system/exeletd.service"
            ssh_run "${exelet_ips[$i]}" 'sudo systemctl daemon-reload && sudo systemctl restart exeletd'
        done

        # Update exeprox with current exed IP
        ssh_run "$exeprox_ip" "sudo sed -i 's|-exed-grpc-addr=tcp://[^:]*:2225|-exed-grpc-addr=tcp://${exed_ip}:2225|' /etc/systemd/system/exeprox.service"
        ssh_run "$exeprox_ip" 'sudo systemctl daemon-reload && sudo systemctl restart exeprox'
    fi

    # ── Port forwarding ──────────────────────────────────────────────────
    setup_port_forwarding "$exed_ip"

    # ── Write envfile ────────────────────────────────────────────────────
    write_envfile

    rm -rf "$wait_dir"

    log ""
    log "Cluster is ready!"
    log "  HTTP:  http://localhost:8080"
    log "  SSH:   ssh -p 2222 <box>@localhost"
}

cmd_deploy() {
    log "Deploying updated binaries to cluster..."

    # Build fresh binaries
    build_binaries

    # Discover IPs from running VMs
    local exed_ip exeprox_ip
    exed_ip="$(get_vm_ip "$(vm_name_exed)")"
    exeprox_ip="$(get_vm_ip "$(vm_name_exeprox)")"

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
        ssh_run "$eip" 'sudo systemctl stop exeletd || true'
        ssh_run "$eip" 'sudo mv ~/exeletd /usr/local/bin/exeletd && sudo chmod +x /usr/local/bin/exeletd'
        ssh_run "$eip" 'sudo mv ~/exelet-ctl /usr/local/bin/exelet-ctl && sudo chmod +x /usr/local/bin/exelet-ctl'
        ssh_run "$eip" 'sudo systemctl start exeletd'
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

    # ── Deploy to exeprox VM ─────────────────────────────────────────────
    if [[ -n "$exeprox_ip" ]]; then
        log "Deploying exeprox to $(vm_name_exeprox) (${exeprox_ip})..."
        scp_to "$exeprox_ip" "${CACHE_DIR}/exeprox"
        ssh_run "$exeprox_ip" 'sudo systemctl stop exeprox || true'
        ssh_run "$exeprox_ip" 'sudo mv ~/exeprox /usr/local/bin/exeprox && sudo chmod +x /usr/local/bin/exeprox'
        ssh_run "$exeprox_ip" 'sudo systemctl start exeprox'
        log "  exeprox restarted"
    fi

    # Refresh port forwarding (IPs may have changed after reboot, though unlikely)
    setup_port_forwarding "$exed_ip"

    log "Deploy complete!"
}

cmd_stop() {
    log "Stopping cluster VMs..."
    teardown_port_forwarding

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
                    elif [[ "$name" == *-exeprox ]]; then
                        service="exeprox"
                        if ssh_run "$ip" 'systemctl is-active exeprox' 2>/dev/null | grep -q "^active$"; then
                            service="exeprox (active)"
                        else
                            service="exeprox (inactive)"
                        fi
                    elif [[ "$name" == *-ctr-* ]]; then
                        service="exeletd"
                        if ssh_run "$ip" 'systemctl is-active exeletd' 2>/dev/null | grep -q "^active$"; then
                            service="exeletd (active)"
                        else
                            service="exeletd (inactive)"
                        fi
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
            pid=$(sudo cat "$pidfile" 2>/dev/null || true)
            if [[ -n "$pid" ]] && sudo kill -0 "$pid" 2>/dev/null; then
                echo "  ${label}: active (pid ${pid})"
            else
                echo "  ${label}: dead"
            fi
        done
    else
        echo "  none"
    fi
}

cmd_destroy() {
    log "Destroying cluster..."
    teardown_port_forwarding

    local names
    names="$(all_vm_names)"
    for name in $names; do
        if vm_exists "$name"; then
            log "Destroying ${name}..."
            sudo virsh destroy "$name" 2>/dev/null || true
            sudo virsh undefine "$name" --remove-all-storage 2>/dev/null || true
        else
            log "VM ${name} not found, skipping"
        fi

        # Clean up any leftover disk images and seed ISOs
        for f in "${WORKDIR}/${name}.qcow2" "${WORKDIR}/${name}-data.qcow2" "${WORKDIR}/${name}-seed.iso"; do
            sudo rm -f "$f" 2>/dev/null || true
        done
    done

    # Remove envfile
    rm -f "${REPO_ROOT}/${CLUSTER_PREFIX}.env"

    log "Cluster destroyed"
}

# ── Main ─────────────────────────────────────────────────────────────────────

case "${1:-}" in
    start)   cmd_start ;;
    stop)    cmd_stop ;;
    status)  cmd_status ;;
    destroy) cmd_destroy ;;
    deploy)  cmd_deploy ;;
    *)
        echo "Usage: $0 {start|stop|status|destroy|deploy}"
        echo ""
        echo "Subcommands:"
        echo "  start    Create and provision the VM cluster (idempotent)"
        echo "  stop     Gracefully stop all VMs (preserves disks)"
        echo "  status   Show cluster status, IPs, and services"
        echo "  destroy  Tear down all VMs and remove disks"
        echo "  deploy   Rebuild binaries, push to VMs, restart services"
        echo ""
        echo "Environment variables:"
        echo "  NUM_EXELETS=${NUM_EXELETS}  CLUSTER_PREFIX=${CLUSTER_PREFIX}"
        echo "  EXED_VCPUS=${EXED_VCPUS}  EXED_RAM=${EXED_RAM}  EXEPROX_VCPUS=${EXEPROX_VCPUS}  EXEPROX_RAM=${EXEPROX_RAM}"
        echo "  EXELET_VCPUS=${EXELET_VCPUS}  EXELET_RAM=${EXELET_RAM}  DISK_GB=${DISK_GB}  EXELET_DATA_DISK_GB=${EXELET_DATA_DISK_GB}"
        exit 1
        ;;
esac
