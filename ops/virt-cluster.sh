#!/usr/bin/env bash
set -euo pipefail

# Save terminal settings — SSH can corrupt termios (disabling onlcr),
# causing staircase output. Restore after each remote operation.
if [[ -t 1 ]]; then
    _SAVED_STTY="$(stty -g 2>/dev/null || true)"
    _restore_tty() { [[ -n "${_SAVED_STTY:-}" ]] && stty "$_SAVED_STTY" 2>/dev/null || true; }
else
    _restore_tty() { :; }
fi

###############################################################################
# ops/virt-cluster.sh — Local prod-like VM cluster using cloud-hypervisor
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
EXELET_RAMDISK_POOL_SIZE="${EXELET_RAMDISK_POOL_SIZE:-}"
SSH_PUBKEY_DIR="${SSH_PUBKEY_DIR:-$HOME/.ssh}"
CLUSTER_PREFIX="${CLUSTER_PREFIX:-exe-local}"

WORKDIR="${WORKDIR:-/var/lib/exe-vms}"
BASE_IMG="${BASE_IMG:-${WORKDIR}/ubuntu-24.04-base.raw}"
BASE_IMG_URL="${BASE_IMG_URL:-https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img}"
USER_NAME="ubuntu"
SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR"

CLOUD_HYPERVISOR_VERSION="${CLOUD_HYPERVISOR_VERSION:-48.0}"
VIRTIOFSD_VERSION="${VIRTIOFSD_VERSION:-1.13.2}"

CACHE_DIR="$HOME/.cache/exedops"
APT_CACHE_ENABLED="${APT_CACHE_ENABLED:-false}"
APT_CACHE_CONTAINER="${CLUSTER_PREFIX}-apt-cache"
APT_CACHE_PORT="3142"
APT_CACHE_HOST="192.168.122.1"

# ── Cloud-hypervisor / bridge configuration ──────────────────────────────────

VM_STATE_DIR="/tmp/${CLUSTER_PREFIX}-vms"
BRIDGE_NAME="exebr0"
BRIDGE_IP="192.168.122.1"
BRIDGE_SUBNET="192.168.122.0/24"
DNSMASQ_PID_FILE="/tmp/${CLUSTER_PREFIX}-dnsmasq.pid"
DNSMASQ_LEASE_FILE="/tmp/${CLUSTER_PREFIX}-dnsmasq.leases"
DNSMASQ_LOG_FILE="/tmp/${CLUSTER_PREFIX}-dnsmasq.log"
BOOT_DIR="${WORKDIR}/boot"

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

cleanup_jobs() {
    local pids
    pids="$(jobs -p 2>/dev/null)"
    if [[ -n "$pids" ]]; then
        # Kill background jobs and all their children
        for pid in $pids; do
            pkill -P "$pid" 2>/dev/null || true
            kill "$pid" 2>/dev/null || true
        done
    fi
}
trap 'cleanup_jobs; _restore_tty' EXIT
trap 'cleanup_jobs; _restore_tty; exit 130' INT TERM

log() { _restore_tty; echo "==> $*"; }
warn() { _restore_tty; echo "WARN: $*" >&2; }
die() {
    _restore_tty
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
    for cmd in qemu-img go sqlite3 dnsmasq; do
        command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
    done
    if ! command -v genisoimage >/dev/null 2>&1 && ! command -v mkisofs >/dev/null 2>&1; then
        missing+=("genisoimage/mkisofs")
    fi
    if [[ ${#missing[@]} -gt 0 ]]; then
        die "Missing prerequisites: ${missing[*]}"
    fi
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

# ── Static IP and MAC assignment ─────────────────────────────────────────────

vm_static_ip() {
    local name="$1"
    case "$name" in
        *-exed)                echo "192.168.122.10" ;;
        exeprox-local-dev-01)  echo "192.168.122.20" ;;
        exeprox-local-dev-02)  echo "192.168.122.21" ;;
        exeprox-local-dev-03)  echo "192.168.122.22" ;;
        exeprox-local-dev-04)  echo "192.168.122.23" ;;
        exelet-local-dev-01)   echo "192.168.122.30" ;;
        exelet-local-dev-02)   echo "192.168.122.31" ;;
        exelet-local-dev-03)   echo "192.168.122.32" ;;
        exelet-local-dev-04)   echo "192.168.122.33" ;;
        *-mon)                 echo "192.168.122.50" ;;
        *)                     die "No static IP for unknown VM: $name" ;;
    esac
}

vm_mac() {
    local name="$1"
    # Deterministic MAC from name: 52:54:00:xx:xx:xx
    local hash
    hash=$(echo -n "$name" | md5sum | cut -c1-6)
    printf '52:54:00:%s:%s:%s\n' "${hash:0:2}" "${hash:2:2}" "${hash:4:2}"
}

vm_tap_name() {
    local name="$1"
    # Must be ≤15 chars (IFNAMSIZ). Use a short deterministic suffix.
    local hash
    hash=$(echo -n "$name" | md5sum | cut -c1-8)
    echo "tap${hash}"
}

# ── Bridge and dnsmasq networking ────────────────────────────────────────────

ensure_bridge() {
    # Remove libvirt's default bridge if it exists on the same subnet
    if ip link show virbr0 >/dev/null 2>&1; then
        local virbr_subnet
        virbr_subnet=$(ip -4 addr show virbr0 2>/dev/null | grep -oP 'inet \K[0-9.]+/[0-9]+' || true)
        if [[ "${virbr_subnet}" == "${BRIDGE_IP}/24" ]]; then
            log "Removing conflicting virbr0 (same subnet as ${BRIDGE_NAME})..."
            sudo ip link set virbr0 down 2>/dev/null || true
            sudo ip link del virbr0 2>/dev/null || true
        fi
    fi

    if ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
        log "Bridge ${BRIDGE_NAME} already exists"
    else
        log "Creating bridge ${BRIDGE_NAME} (${BRIDGE_IP}/24)..."
        sudo ip link add "${BRIDGE_NAME}" type bridge
        sudo ip addr add "${BRIDGE_IP}/24" dev "${BRIDGE_NAME}"
        sudo ip link set "${BRIDGE_NAME}" up
    fi

    sudo sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true

    # NAT masquerade for the bridge subnet
    if ! sudo iptables -t nat -C POSTROUTING -s "${BRIDGE_SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE 2>/dev/null; then
        sudo iptables -t nat -A POSTROUTING -s "${BRIDGE_SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE
    fi
    # FORWARD rules (Docker often sets policy to DROP)
    if ! sudo iptables -C FORWARD -i "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null; then
        sudo iptables -I FORWARD -i "${BRIDGE_NAME}" -j ACCEPT
    fi
    if ! sudo iptables -C FORWARD -o "${BRIDGE_NAME}" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null; then
        sudo iptables -I FORWARD -o "${BRIDGE_NAME}" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
    fi
}

destroy_bridge() {
    if ip link show "${BRIDGE_NAME}" >/dev/null 2>&1; then
        log "Removing bridge ${BRIDGE_NAME}..."
        sudo ip link set "${BRIDGE_NAME}" down 2>/dev/null || true
        sudo ip link delete "${BRIDGE_NAME}" 2>/dev/null || true
    fi
    sudo iptables -t nat -D POSTROUTING -s "${BRIDGE_SUBNET}" ! -o "${BRIDGE_NAME}" -j MASQUERADE 2>/dev/null || true
    sudo iptables -D FORWARD -i "${BRIDGE_NAME}" -j ACCEPT 2>/dev/null || true
    sudo iptables -D FORWARD -o "${BRIDGE_NAME}" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
}

ensure_dnsmasq() {
    if [[ -f "${DNSMASQ_PID_FILE}" ]] && kill -0 "$(cat "${DNSMASQ_PID_FILE}")" 2>/dev/null; then
        log "dnsmasq already running"
        return 0
    fi

    # Kill any stale dnsmasq bound to the bridge IP (e.g. leftover from libvirt)
    local stale_pids
    stale_pids=$(sudo ss -tlnp "src ${BRIDGE_IP}:53" 2>/dev/null | grep -oP 'pid=\K[0-9]+' || true)
    if [[ -n "$stale_pids" ]]; then
        log "Killing stale dnsmasq on ${BRIDGE_IP}:53 (PIDs: ${stale_pids})..."
        echo "$stale_pids" | xargs -r sudo kill 2>/dev/null || true
        sleep 1
    fi

    log "Starting dnsmasq on ${BRIDGE_NAME}..."

    # Ensure log/lease files are writable
    sudo rm -f "${DNSMASQ_LOG_FILE}" "${DNSMASQ_LEASE_FILE}"

    # Build --dhcp-host entries for every VM
    local dhcp_host_args=()
    for name in $(all_vm_names); do
        local mac ip
        mac="$(vm_mac "$name")"
        ip="$(vm_static_ip "$name")"
        dhcp_host_args+=("--dhcp-host=${mac},${ip}")
    done

    sudo dnsmasq \
        --strict-order \
        --bind-interfaces \
        --interface="${BRIDGE_NAME}" \
        --except-interface=lo \
        --dhcp-range=192.168.122.2,192.168.122.254,255.255.255.0,12h \
        --dhcp-option=option:router,${BRIDGE_IP} \
        --dhcp-option=option:dns-server,8.8.8.8,8.8.4.4 \
        "${dhcp_host_args[@]}" \
        --dhcp-leasefile="${DNSMASQ_LEASE_FILE}" \
        --pid-file="${DNSMASQ_PID_FILE}" \
        --log-facility="${DNSMASQ_LOG_FILE}" \
        --dhcp-authoritative

    log "dnsmasq started (PID $(cat "${DNSMASQ_PID_FILE}"))"
}

stop_dnsmasq() {
    if [[ -f "${DNSMASQ_PID_FILE}" ]]; then
        local pid
        pid="$(cat "${DNSMASQ_PID_FILE}" 2>/dev/null || true)"
        if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
            log "Stopping dnsmasq (PID $pid)..."
            sudo kill "$pid" 2>/dev/null || true
        fi
        sudo rm -f "${DNSMASQ_PID_FILE}" "${DNSMASQ_LEASE_FILE}" "${DNSMASQ_LOG_FILE}"
    fi
}

HOSTS_MARKER="virt-cluster ${CLUSTER_PREFIX}"

update_host_hosts() {
    remove_host_hosts
    local block="# BEGIN ${HOSTS_MARKER}"
    local name
    while read -r name; do
        block+=$'\n'"$(vm_static_ip "$name") ${name}"
    done < <(all_vm_names)
    block+=$'\n'"# END ${HOSTS_MARKER}"
    echo "${block}" | sudo tee -a /etc/hosts >/dev/null
}

remove_host_hosts() {
    sudo sed -i "/# BEGIN ${HOSTS_MARKER}/,/# END ${HOSTS_MARKER}/d" /etc/hosts
}

# ── TAP device management ────────────────────────────────────────────────────

create_tap() {
    local tap="$1"
    if ip link show "$tap" >/dev/null 2>&1; then
        return 0
    fi
    sudo ip tuntap add "$tap" mode tap
    sudo ip link set "$tap" master "${BRIDGE_NAME}"
    sudo ip link set "$tap" up
}

delete_tap() {
    local tap="$1"
    if ip link show "$tap" >/dev/null 2>&1; then
        sudo ip link set "$tap" down 2>/dev/null || true
        sudo ip link delete "$tap" 2>/dev/null || true
    fi
}

# ── VM lifecycle (cloud-hypervisor) ──────────────────────────────────────────

vm_pid_file()  { echo "${VM_STATE_DIR}/$1.pid"; }
vm_sock_file() { echo "${VM_STATE_DIR}/$1.sock"; }
vm_log_file()  { echo "${VM_STATE_DIR}/$1.log"; }

vm_exists() {
    [[ -f "$(vm_pid_file "$1")" ]]
}

vm_running() {
    local name="$1"
    if ! vm_exists "$name"; then return 1; fi
    local pid
    pid="$(cat "$(vm_pid_file "$name")")"
    sudo kill -0 "$pid" 2>/dev/null
}

get_vm_ip() {
    local name="$1"
    vm_static_ip "$name"
}

host_ch()     { echo "${CACHE_DIR}/bin/cloud-hypervisor"; }
host_ch_remote() { echo "${CACHE_DIR}/bin/ch-remote"; }

create_vm() {
    local name="$1" vcpus="$2" ram="$3" cloud_init_fn="$4"
    local has_data_disk="${5:-false}"

    if vm_running "$name"; then
        log "VM ${name} already running, skipping creation"
        return 0
    fi

    log "Creating VM ${name} (${vcpus} vCPUs, ${ram}MB RAM)..."

    local disk="${WORKDIR}/${name}.raw"
    local seed="${WORKDIR}/${name}-seed.iso"

    # Root disk — copy base and resize (raw; sparse on disk)
    sudo cp --sparse=always "${BASE_IMG}" "${disk}"
    sudo qemu-img resize -f raw "${disk}" "${DISK_GB}G" >/dev/null 2>&1

    # Extra disks for exelet VMs
    local extra_disks=""
    if [[ "$has_data_disk" == "true" ]]; then
        local data_disk="${WORKDIR}/${name}-data.raw"
        sudo qemu-img create -f raw "${data_disk}" "${EXELET_DATA_DISK_GB}G" >/dev/null 2>&1
        extra_disks+=" path=${data_disk}"

        local backup_disk="${WORKDIR}/${name}-backup.raw"
        sudo qemu-img create -f raw "${backup_disk}" "${EXELET_BACKUP_DISK_GB}G" >/dev/null 2>&1
        extra_disks+=" path=${backup_disk}"

        local dozer_disk="${WORKDIR}/${name}-dozer.raw"
        sudo qemu-img create -f raw "${dozer_disk}" "${EXELET_DATA_DISK_GB}G" >/dev/null 2>&1
        extra_disks+=" path=${dozer_disk}"
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

    # TAP device
    local tap
    tap="$(vm_tap_name "$name")"
    create_tap "$tap"

    local mac
    mac="$(vm_mac "$name")"

    local sock log_file
    sock="$(vm_sock_file "$name")"
    log_file="$(vm_log_file "$name")"

    # Remove stale socket
    sudo rm -f "$sock"

    # Boot the VM in its own session (setsid) so it survives script exit.
    sudo setsid "$(host_ch)" \
        --api-socket "path=${sock}" \
        --kernel "${BOOT_DIR}/vmlinuz" \
        --initramfs "${BOOT_DIR}/initrd.img" \
        --cmdline "root=LABEL=cloudimg-rootfs ro console=ttyS0" \
        --cpus "boot=${vcpus}" \
        --memory "size=${ram}M" \
        --disk "path=${disk}"${extra_disks} "path=${seed},readonly=on" \
        --net "tap=${tap},mac=${mac}" \
        --serial "file=${log_file}" \
        --console off \
        >> "${log_file}" 2>&1 &

    local sudo_pid=$!
    disown "$sudo_pid"

    # Record the actual cloud-hypervisor PID.
    # sudo will exit after setsid, so find CH by its socket.
    local ch_pid=""
    for _i in $(seq 1 40); do
        ch_pid="$(sudo lsof -t "${sock}" 2>/dev/null | head -1 || true)"
        [[ -n "$ch_pid" ]] && break
        sleep 0.1
    done
    if [[ -z "$ch_pid" ]]; then
        ch_pid="$sudo_pid"
    fi
    echo "$ch_pid" > "$(vm_pid_file "$name")"

    log "VM ${name} started (PID ${ch_pid}, IP $(vm_static_ip "$name"))"
}

wait_for_ip() {
    local name="$1"
    # IPs are static — just return it immediately
    vm_static_ip "$name"
}

wait_for_ssh() {
    local ip="$1"
    for i in $(seq 1 60); do
        if ssh -T ${SSH_OPTS} "${USER_NAME}@${ip}" 'true' 2>/dev/null; then
            return 0
        fi
        sleep 2
    done
    die "SSH not reachable on ${ip} after 120s"
}

wait_for_cloud_init() {
    local ip="$1"
    ssh -T ${SSH_OPTS} "${USER_NAME}@${ip}" 'sudo cloud-init status --wait' 2>/dev/null
}

# Wait for a single VM to get IP, SSH, and finish cloud-init.
# Writes progress to a status file (status_dir/name) for the display loop.
# Returns the IP on stdout.
wait_for_vm_ready() {
    local name="$1" status_dir="$2"
    local sf="${status_dir}/${name}"
    local ip
    ip="$(vm_static_ip "$name")"
    echo "IP=${ip}, waiting for SSH..." >"$sf"

    # SSH
    local ssh_ok=false
    for i in $(seq 1 60); do
        if ssh -T ${SSH_OPTS} "${USER_NAME}@${ip}" 'true' </dev/null 2>/dev/null; then
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
    timeout 300 ssh -T ${SSH_OPTS} "${USER_NAME}@${ip}" 'sudo cloud-init status --wait' </dev/null >/dev/null 2>&1 || ci_rc=$?
    if [[ "$ci_rc" -eq 124 ]]; then
        echo "FAILED (cloud-init timed out after 300s)" >"$sf"
        return 1
    elif [[ "$ci_rc" -ne 0 ]]; then
        local ci_status
        ci_status="$(ssh -T ${SSH_OPTS} "${USER_NAME}@${ip}" 'sudo cloud-init status --long' </dev/null 2>/dev/null || echo 'unknown')"
        echo "FAILED (cloud-init error: ${ci_status})" >"$sf"
        return 1
    fi

    echo "READY (${ip})" >"$sf"
    echo "$ip"
}

# Display loop: redraws VM status lines in-place until all are done.
display_vm_status() {
    local status_dir="$1"
    shift
    local names=("$@")
    local n=${#names[@]}

    for name in "${names[@]}"; do
        printf '  [%-25s] waiting...\n' "$name" >&2
    done

    while true; do
        printf '\033[%dA' "$n" >&2
        local all_done=true
        for name in "${names[@]}"; do
            local status
            status="$(cat "${status_dir}/${name}" 2>/dev/null || echo "waiting...")"
            printf '\r\033[2K  [%-25s] %s\n' "$name" "$status" >&2
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
    scp ${SSH_OPTS} "$@" "${USER_NAME}@${ip}:~/" </dev/null
}

ssh_run() {
    local ip="$1"
    shift
    # When stdin is a tty, redirect from /dev/null to prevent SSH from
    # entering raw mode and corrupting terminal settings. Heredocs and
    # herestrings already replace stdin with a pipe, so they pass through.
    if [[ -t 0 ]]; then
        ssh -T ${SSH_OPTS} "${USER_NAME}@${ip}" "$@" </dev/null
    else
        ssh -T ${SSH_OPTS} "${USER_NAME}@${ip}" "$@"
    fi
}

# ── Cloud-init generation ────────────────────────────────────────────────────

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

bootcmd_yaml() {
    cat <<'BOOTCMD'
bootcmd:
  - |
    systemctl mask --now apt-daily.timer apt-daily-upgrade.timer apt-daily.service apt-daily-upgrade.service unattended-upgrades.service 2>/dev/null || true
    killall -q apt-get dpkg unattended-upgr 2>/dev/null || true
    while fuser /var/lib/dpkg/lock-frontend /var/lib/apt/lists/lock >/dev/null 2>&1; do sleep 1; done
BOOTCMD
    if [[ "${APT_CACHE_ENABLED}" == "true" ]]; then
        cat <<APTPROXY
  - echo 'Acquire::http::Proxy "http://${APT_CACHE_HOST}:${APT_CACHE_PORT}";' > /etc/apt/apt.conf.d/00apt-cacher-proxy
APTPROXY
    fi
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

ramdisk_pool_cloud_init() {
    [[ -n "${EXELET_RAMDISK_POOL_SIZE}" ]] || return 0
    cat <<RAMDISK
  # Ramdisk-backed ZFS pool (tmpfs, ephemeral)
  - |
    if ! zpool list ramdisk >/dev/null 2>&1; then
      mkdir -p /mnt/ramdisk
      mount -t tmpfs -o size=${EXELET_RAMDISK_POOL_SIZE} tmpfs /mnt/ramdisk
      truncate -s ${EXELET_RAMDISK_POOL_SIZE} /mnt/ramdisk/ramdisk.img
      zpool create -f -m none ramdisk /mnt/ramdisk/ramdisk.img
    fi
RAMDISK
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
  # Dozer ZFS pool on /dev/vdd
  - |
    if ! zpool list dozer >/dev/null 2>&1; then
      zpool create -f -m none dozer /dev/vdd
    fi
$(ramdisk_pool_cloud_init)
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

# ── Binary building ──────────────────────────────────────────────────────────

build_binaries() {
    log "Building binaries..."
    mkdir -p "${CACHE_DIR}"

    if command -v pnpm >/dev/null 2>&1; then
        log "  Building dashboard UI..."
        (export PNPM_HOME="${CACHE_DIR}/pnpm" && cd "${REPO_ROOT}/ui" && pnpm install --frozen-lockfile && pnpm build)
    elif [[ -d "${REPO_ROOT}/ui/dist" ]]; then
        log "  pnpm not found; using existing ui/dist"
    else
        die "pnpm not found and ui/dist does not exist. Install pnpm or build the UI first."
    fi
    log "  Building exed..."
    (cd "${REPO_ROOT}" && GOOS=linux go build -o "${CACHE_DIR}/exed" ./cmd/exed)

    log "  Building exeprox..."
    (cd "${REPO_ROOT}" && GOOS=linux go build -o "${CACHE_DIR}/exeprox" ./cmd/exeprox)

    log "  Building exeletd..."
    (cd "${REPO_ROOT}" && make exe-init && GOOS=linux go build -o "${CACHE_DIR}/exeletd" ./cmd/exelet)

    log "  Building exelet-ctl..."
    (cd "${REPO_ROOT}" && GOOS=linux go build -o "${CACHE_DIR}/exelet-ctl" ./cmd/exelet-ctl)

    log "  Building sshpiperd..."
    (cd "${REPO_ROOT}/deps/sshpiper" && GOTOOLCHAIN=go1.26.1 GOOS=linux go build -o "${CACHE_DIR}/sshpiperd" ./cmd/sshpiperd)

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

ensure_host_cloud_hypervisor() {
    local ch_bin="${CACHE_DIR}/bin/cloud-hypervisor"
    local chr_bin="${CACHE_DIR}/bin/ch-remote"
    if [[ -x "${ch_bin}" && -x "${chr_bin}" ]]; then
        return 0
    fi
    ensure_cloud_hypervisor_artifacts
    mkdir -p "${CACHE_DIR}/bin"
    tar xzf "${CACHE_DIR}/cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}-amd64.tar.gz" \
        -C "${CACHE_DIR}/bin" --strip-components=2 ./bin/cloud-hypervisor ./bin/ch-remote
    chmod +x "${ch_bin}" "${chr_bin}"
    log "Extracted host cloud-hypervisor and ch-remote to ${CACHE_DIR}/bin/"
}

ensure_base_image() {
    sudo mkdir -p "${WORKDIR}" "${BOOT_DIR}"
    if [[ ! -f "${BASE_IMG}" ]]; then
        log "Downloading Ubuntu 24.04 cloud image..."
        local tmp_img="${BASE_IMG}.download"
        sudo curl -L "${BASE_IMG_URL}" -o "${tmp_img}"
        # Cloud-hypervisor's qcow2 support is limited (no compressed blocks,
        # no backing-file size mismatch).  Use raw format for everything.
        log "Converting base image to raw..."
        sudo qemu-img convert -f qcow2 -O raw "${tmp_img}" "${BASE_IMG}"
        sudo rm -f "${tmp_img}"
    fi

    # Extract kernel and initrd from the cloud image for direct kernel boot.
    # Cloud-hypervisor cannot boot UEFI disk images directly; it needs an
    # explicit kernel + initramfs passed on the command line.
    if [[ ! -f "${BOOT_DIR}/vmlinuz" || ! -f "${BOOT_DIR}/initrd.img" ]]; then
        log "Extracting kernel and initrd from base image..."

        # Find the Linux extended boot partition (type "Linux extended boot")
        # and mount it to extract the kernel and initrd.
        local part_info
        part_info=$(sudo sfdisk -J "${BASE_IMG}" | \
            python3 -c 'import json,sys; p=next(p for p in json.load(sys.stdin)["partitiontable"]["partitions"] if p.get("type","") == "BC13C2FF-59E6-4262-A352-B275FD6F7172"); print(p["start"], p["size"])')
        local start_sector size_sectors
        start_sector="${part_info%% *}"
        size_sectors="${part_info##* }"

        local loop_dev
        loop_dev=$(sudo losetup --find --show \
            --offset $(( start_sector * 512 )) \
            --sizelimit $(( size_sectors * 512 )) \
            "${BASE_IMG}")

        local mnt="${WORKDIR}/boot-mnt"
        sudo mkdir -p "${mnt}"
        sudo mount -o ro "${loop_dev}" "${mnt}"
        sudo cp "${mnt}/vmlinuz" "${BOOT_DIR}/vmlinuz"
        sudo cp "${mnt}/initrd.img" "${BOOT_DIR}/initrd.img"
        sudo chmod 644 "${BOOT_DIR}/vmlinuz" "${BOOT_DIR}/initrd.img"
        sudo umount "${mnt}"
        sudo losetup -d "${loop_dev}"
        sudo rmdir "${mnt}"
        log "Extracted kernel and initrd to ${BOOT_DIR}/"
    fi
}

# ── Apt cache (Docker, optional) ──────────────────────────────────────────

ensure_apt_cache() {
    [[ "${APT_CACHE_ENABLED}" == "true" ]] || return 0

    if docker inspect -f '{{.State.Running}}' "${APT_CACHE_CONTAINER}" 2>/dev/null | grep -q true; then
        log "Apt cache container already running"
        return 0
    fi

    docker rm -f "${APT_CACHE_CONTAINER}" 2>/dev/null || true

    log "Starting apt-cacher-ng container (${APT_CACHE_HOST}:${APT_CACHE_PORT})..."
    docker run -d \
        --name "${APT_CACHE_CONTAINER}" \
        --restart unless-stopped \
        -p "${APT_CACHE_PORT}:3142" \
        -v "${CLUSTER_PREFIX}-apt-cache:/var/cache/apt-cacher-ng" \
        sameersbn/apt-cacher-ng:latest >/dev/null

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
    :
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
    scp_to "$ip" "${SCRIPT_DIR}/setup-exelet.sh"
    ssh_run "$ip" "sudo bash -c 'sed -e \"s|/home/ubuntu/.cache/exedops/exeletd-amd64|/usr/local/bin/exeletd.latest|\" -e \"s|/home/ubuntu/.cache/exedops/exelet-ctl-amd64|/usr/local/bin/exelet-ctl|\" -e \"s|ASSETS_DIR=.*|ASSETS_DIR=/home/ubuntu/.cache/exedops|\" ~/setup-exelet.sh > /root/setup-exelet.sh && chmod +x /root/setup-exelet.sh'"
    ssh_run "$ip" 'sudo /bin/bash /root/setup-exelet.sh'

    # Ensure data directory exists
    ssh_run "$ip" 'sudo mkdir -p /data/exelet'

    # Build storage tier args for additional ZFS pools
    local storage_tier_args=""
    storage_tier_args+=" --storage-tier zfs:///data/exelet/storage?dataset=dozer"
    storage_tier_args+=" --storage-tier zfs:///data/exelet/storage?dataset=backup"
    if [[ -n "${EXELET_RAMDISK_POOL_SIZE}" ]]; then
        storage_tier_args+=" --storage-tier zfs:///data/exelet/storage?dataset=ramdisk"
    fi

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

ExecStart=/usr/local/bin/exeletd.latest -D --stage=local --name=${name} --listen-address=tcp://0.0.0.0:9080 --http-addr=0.0.0.0:9081 --data-dir=/data/exelet --storage-manager-address=zfs:///data/exelet/storage?dataset=tank${storage_tier_args} --network-manager-address=nat:///data/exelet/network?network=10.42.0.0/16 --runtime-address=cloudhypervisor:///data/exelet/runtime --exed-url=http://EXED_IP_PLACEHOLDER:8080 --instance-domain=exe.cloud --enable-hugepages --reserved-cpus=0 --storage-replication-enabled --storage-replication-target=zpool:///backup

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

    # Create sshpiper.sh
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

    # Generate prometheus config
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

    # Deploy metrics proxy to exed/exeprox VMs
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

    # Provision Grafana datasource
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

    # Create Grafana service account + API token
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

    teardown_port_forwarding 2>/dev/null || true

    mkdir -p "${SOCAT_PID_DIR}"

    # Host :2222 → exed :2222 (SSH via sshpiper)
    socat TCP-LISTEN:2222,fork,reuseaddr "TCP:${exed_ip}:2222" &
    echo $! >"${SOCAT_PID_DIR}/ssh.pid"
    disown

    # Host :8080 → exed :8080 (HTTP) via SSH tunnel so exed sees localhost origin
    ssh -T ${SSH_OPTS} -f -N -o ExitOnForwardFailure=yes -L 8080:localhost:8080 "${USER_NAME}@${exed_ip}" </dev/null
    pgrep -n -f "ssh.*-L 8080:localhost:8080.*${exed_ip}" >"${SOCAT_PID_DIR}/http.pid"

    log "Port forwarding active:"
    log "  SSH:   localhost:2222 -> ${exed_ip}:2222"
    log "  HTTP:  localhost:8080 -> ${exed_ip}:8080"

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
    pkill -f 'socat TCP-LISTEN:2222,' 2>/dev/null || true
    pkill -f 'socat TCP-LISTEN:3000,' 2>/dev/null || true
    pkill -f 'socat TCP-LISTEN:9090,' 2>/dev/null || true
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
        exed_ip="$(vm_static_ip "$(vm_name_exed)")"
        echo "EXED_VM=$(vm_name_exed)"
        echo "EXED_IP=${exed_ip}"
        echo ""

        for i in $(seq 1 "${NUM_EXEPROXES}"); do
            local pname pip
            pname="$(vm_name_exeprox "$i")"
            pip="$(vm_static_ip "$pname")"
            echo "EXEPROX_${i}_VM=${pname}"
            echo "EXEPROX_${i}_IP=${pip}"
        done
        echo ""

        for i in $(seq 1 "${NUM_EXELETS}"); do
            local ename eip
            ename="$(vm_name_exelet "$i")"
            eip="$(vm_static_ip "$ename")"
            echo "EXELET_${i}_VM=${ename}"
            echo "EXELET_${i}_IP=${eip}"
        done
        echo ""

        local mon_ip
        mon_ip="$(vm_static_ip "$(vm_name_mon)")"
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
    ensure_bridge
    ensure_apt_cache
    ensure_cloud_hypervisor_artifacts
    ensure_host_cloud_hypervisor

    mkdir -p "${VM_STATE_DIR}"

    # Build all binaries
    build_binaries

    # Start dnsmasq (must happen after bridge is up, before VMs boot)
    ensure_dnsmasq

    # Populate /etc/hosts so VM names resolve on the host
    update_host_hosts

    # ── Create VMs ───────────────────────────────────────────────────────

    create_vm "$(vm_name_exed)" "${EXED_VCPUS}" "${EXED_RAM}" generate_cloud_init_exed false

    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        create_vm "$(vm_name_exeprox "$i")" "${EXEPROX_VCPUS}" "${EXEPROX_RAM}" generate_cloud_init_exeprox false
    done

    for i in $(seq 1 "${NUM_EXELETS}"); do
        create_vm "$(vm_name_exelet "$i")" "${EXELET_VCPUS}" "${EXELET_RAM}" generate_cloud_init_exelet true
    done

    create_vm "$(vm_name_mon)" "${MON_VCPUS}" "${MON_RAM}" generate_cloud_init_mon false

    # ── Wait for all VMs (SSH + cloud-init) ─────────────────────────────

    log "Waiting for VMs to become ready..."

    local wait_dir
    wait_dir="$(mktemp -d)"
    local status_dir="${wait_dir}/status"
    mkdir -p "$status_dir"

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

    display_vm_status "$status_dir" "${all_names[@]}"

    wait "$pid_exed" || die "exed VM failed to become ready"
    for i in $(seq 1 "${NUM_EXEPROXES}"); do
        wait "${exeprox_pids[$i]}" || die "exeprox-${i} VM failed to become ready"
    done
    for i in $(seq 1 "${NUM_EXELETS}"); do
        wait "${exelet_pids[$i]}" || die "exelet-${i} VM failed to become ready"
    done
    wait "$pid_mon" || die "mon VM failed to become ready"

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

    # Build exelet address list using hostnames
    local exelet_addr_list=""
    for i in $(seq 1 "${NUM_EXELETS}"); do
        local ename
        ename="$(vm_name_exelet "$i")"
        if [[ -n "$exelet_addr_list" ]]; then
            exelet_addr_list+=","
        fi
        exelet_addr_list+="tcp://${ename}:9080"
    done

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
        # ── Already provisioned — just update IPs ────────────────────────
        log "Refreshing service configs with current IPs..."

        update_exed_hosts "$exed_ip"
        ssh_run "$exed_ip" "sudo sed -i 's|-exelet-addresses=[^ ]*|-exelet-addresses=${exelet_addr_list}|' /etc/systemd/system/exed.service"
        ssh_run "$exed_ip" 'sudo systemctl daemon-reload && sudo systemctl restart exed'

        for i in $(seq 1 "${NUM_EXELETS}"); do
            ssh_run "${exelet_ips[$i]}" "sudo sed -i 's|--exed-url=http://[^:]*:8080|--exed-url=http://${exed_ip}:8080|' /etc/systemd/system/exelet.service"
            ssh_run "${exelet_ips[$i]}" 'sudo systemctl daemon-reload && sudo systemctl restart exelet'
        done

        for i in $(seq 1 "${NUM_EXEPROXES}"); do
            ssh_run "${exeprox_ips[$i]}" "sudo sed -i 's|-exed-grpc-addr=tcp://[^:]*:2225|-exed-grpc-addr=tcp://${exed_ip}:2225|' /etc/systemd/system/exeprox.service"
            ssh_run "${exeprox_ips[$i]}" 'sudo systemctl daemon-reload && sudo systemctl restart exeprox'
        done

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

    build_binaries

    local exed_ip
    exed_ip="$(vm_static_ip "$(vm_name_exed)")"

    # ── Deploy to exelet VMs ─────────────────────────────────────────────
    for i in $(seq 1 "${NUM_EXELETS}"); do
        local ename eip
        ename="$(vm_name_exelet "$i")"
        eip="$(vm_static_ip "$ename")"
        if ! vm_running "$ename"; then
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
        pip="$(vm_static_ip "$pname")"
        if ! vm_running "$pname"; then
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
    mon_ip="$(vm_static_ip "$(vm_name_mon)")"
    if vm_running "$(vm_name_mon)"; then
        log "Refreshing prometheus config on mon VM..."
        local mon_exelet_ip_list=()
        for i in $(seq 1 "${NUM_EXELETS}"); do
            local ename eip
            ename="$(vm_name_exelet "$i")"
            eip="$(vm_static_ip "$ename")"
            mon_exelet_ip_list+=("$eip")
        done
        local exeprox_1_ip
        exeprox_1_ip="$(vm_static_ip "$(vm_name_exeprox 1)")"
        provision_mon "$mon_ip" "$exed_ip" "${exeprox_1_ip}" "${mon_exelet_ip_list[@]}"
    fi

    setup_port_forwarding "$exed_ip" "$mon_ip"

    log "Deploy complete!"
}

cmd_deploy_metrics() {
    local mon_ip
    mon_ip="$(vm_static_ip "$(vm_name_mon)")"
    if ! vm_running "$(vm_name_mon)"; then
        die "mon VM not running. Run 'start' first."
    fi

    local exed_ip
    exed_ip="$(vm_static_ip "$(vm_name_exed)")"

    local mon_exelet_ip_list=()
    for i in $(seq 1 "${NUM_EXELETS}"); do
        local eip
        eip="$(vm_static_ip "$(vm_name_exelet "$i")")"
        mon_exelet_ip_list+=("$eip")
    done
    local exeprox_1_ip
    exeprox_1_ip="$(vm_static_ip "$(vm_name_exeprox 1)")"

    provision_mon "$mon_ip" "$exed_ip" "${exeprox_1_ip}" "${mon_exelet_ip_list[@]}"

    for name in $(all_vm_names); do
        local vip
        vip="$(vm_static_ip "$name")"
        if ! vm_running "$name"; then
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
        if [[ "$name" == exelet-* ]]; then
            scp_to "$vip" "${REPO_ROOT}/observability/scripts/zpool-metrics.sh"
            ssh_run "$vip" "sudo mv ~/zpool-metrics.sh /usr/local/bin/zpool-metrics.sh && \
                sudo chmod +x /usr/local/bin/zpool-metrics.sh && \
                sudo /usr/local/bin/zpool-metrics.sh"
            cron_entries="'* * * * * /usr/local/bin/zpool-metrics.sh'; echo ${cron_entries}"
            cron_filter="grep -v zpool-metrics | ${cron_filter}"
        fi
        ssh_run "$vip" "(sudo crontab -l 2>/dev/null | ${cron_filter}; echo ${cron_entries}) | sudo crontab -"
        if ! ssh_run "$vip" 'grep -q textfile /etc/default/prometheus-node-exporter' 2>/dev/null; then
            ssh_run "$vip" "sudo sed -i 's|^ARGS=.*|ARGS=\"--collector.textfile --collector.textfile.directory=/var/lib/prometheus/node-exporter\"|' /etc/default/prometheus-node-exporter"
            ssh_run "$vip" 'sudo systemctl restart prometheus-node-exporter'
        fi
    done

    setup_port_forwarding "$exed_ip" "$mon_ip"

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
            local sock
            sock="$(vm_sock_file "$name")"
            if [[ -S "$sock" ]]; then
                "$(host_ch_remote)" --api-socket "$sock" shutdown-vmm 2>/dev/null || true
            fi
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

    # Force-kill any that didn't shut down gracefully
    for name in $(all_vm_names); do
        if vm_running "$name"; then
            warn "Force-stopping ${name}..."
            local pid
            pid="$(cat "$(vm_pid_file "$name")" 2>/dev/null || true)"
            if [[ -n "$pid" ]]; then
                sudo kill -9 "$pid" 2>/dev/null || true
            fi
        fi
        # Clean up TAP devices
        delete_tap "$(vm_tap_name "$name")"
    done

    stop_dnsmasq

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
            if vm_running "$name"; then
                state="running"
            else
                state="stopped"
            fi
            ip="$(vm_static_ip "$name")"

            if [[ "$state" == "running" ]]; then
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

        printf "%-30s %-12s %-18s %s\n" "$name" "$state" "$ip" "$service"
    done

    echo ""

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

    local mon_ip
    mon_ip="$(vm_static_ip "$(vm_name_mon)")"
    echo ""
    echo "Grafana: http://localhost:3000 (admin/admin)"
    local grafana_token
    grafana_token="$(ssh_run "$mon_ip" 'cat /home/ubuntu/grafana-token 2>/dev/null' 2>/dev/null || true)"
    if [[ -n "$grafana_token" ]]; then
        echo "Grafana Bearer Token: ${grafana_token}"
    fi
}

cmd_destroy() {
    log "Destroying cluster..."
    teardown_port_forwarding
    destroy_apt_cache

    for name in $(all_vm_names); do
        if vm_running "$name"; then
            log "Stopping ${name}..."
            local pid
            pid="$(cat "$(vm_pid_file "$name")" 2>/dev/null || true)"
            if [[ -n "$pid" ]]; then
                sudo kill -9 "$pid" 2>/dev/null || true
            fi
        fi
        # Clean up TAP
        delete_tap "$(vm_tap_name "$name")"
        # Remove state files
        rm -f "$(vm_pid_file "$name")" "$(vm_sock_file "$name")" "$(vm_log_file "$name")"
        # Remove disks
        for f in "${WORKDIR}/${name}.raw" "${WORKDIR}/${name}-data.raw" "${WORKDIR}/${name}-backup.raw" "${WORKDIR}/${name}-dozer.raw" "${WORKDIR}/${name}-seed.iso"; do
            sudo rm -f "$f"
        done
    done

    stop_dnsmasq
    destroy_bridge
    remove_host_hosts

    rm -rf "${VM_STATE_DIR}"
    rm -f "${REPO_ROOT}/${CLUSTER_PREFIX}.env"

    log "Cluster destroyed"
}

cmd_os_upgrade() {
    local name ip pids=() names=()
    for name in $(all_vm_names); do
        ip="$(vm_static_ip "$name")"
        if ! vm_running "$name"; then
            log "WARN: ${name} not running, skipping"
            continue
        fi
        log "Upgrading $name ($ip)..."
        ssh_run "$ip" 'sudo apt update && sudo NEEDRESTART_MODE=l apt upgrade -y' &
        pids+=("$!")
        names+=("$name")
    done
    local failed=0
    for i in "${!pids[@]}"; do
        if ! wait "${pids[$i]}"; then
            log "ERROR: upgrade failed on ${names[$i]}"
            failed=1
        fi
    done
    if [[ "$failed" -eq 1 ]]; then
        log "Some nodes failed to upgrade"
        exit 1
    fi
    log "All nodes upgraded"
}

# ── Main ─────────────────────────────────────────────────────────────────────

cmd_install_deps() {
    log "Installing dependencies for virt-cluster development..."

    if [[ "$(id -u)" -ne 0 ]] && ! sudo -n true 2>/dev/null; then
        die "This command requires sudo access"
    fi

    # APT packages
    local pkgs=(
        qemu-utils        # qemu-img
        dnsmasq           # DHCP server for bridge network
        genisoimage       # cloud-init seed ISO creation
        sqlite3           # database management
        socat             # port forwarding
        iproute2          # ip command for bridge/tap management
        iptables          # NAT rules
        curl              # downloading base images
        openssh-client    # ssh/scp to VMs
    )

    log "  Installing APT packages..."
    sudo apt-get update -qq
    sudo apt-get install -y -qq "${pkgs[@]}"

    # Disable the system dnsmasq service — we run our own instance
    if systemctl is-enabled dnsmasq >/dev/null 2>&1; then
        log "  Disabling system dnsmasq service (we manage our own)..."
        sudo systemctl disable --now dnsmasq 2>/dev/null || true
    fi

    # Node.js / pnpm (for UI build)
    if ! command -v node >/dev/null 2>&1; then
        log "  Node.js not found. Install Node.js 18+ and re-run, or pre-build ui/dist."
    elif ! command -v pnpm >/dev/null 2>&1; then
        log "  Installing pnpm..."
        sudo npm install -g pnpm
    else
        log "  pnpm already installed ($(pnpm --version))"
    fi

    # Go
    if ! command -v go >/dev/null 2>&1; then
        log "  Go not found. Install Go 1.26+ from https://go.dev/dl/"
    else
        log "  Go already installed ($(go version))"
    fi

    # Docker (needed for building cloud-hypervisor artifacts)
    if ! command -v docker >/dev/null 2>&1; then
        log "  Docker not found. Install Docker for building cloud-hypervisor artifacts."
    else
        log "  Docker already installed"
    fi

    # Build cloud-hypervisor artifacts + extract host binaries
    ensure_cloud_hypervisor_artifacts
    ensure_host_cloud_hypervisor

    log ""
    log "Dependencies installed. Run '$0 start' to create the cluster."
}

cmd_install_vnc() {
    log "Installing VNC/noVNC stack for browser-based remote desktop..."

    if [[ "$(id -u)" -ne 0 ]] && ! sudo -n true 2>/dev/null; then
        die "This command requires sudo access"
    fi

    # APT packages
    local pkgs=(
        xvfb              # virtual framebuffer
        x11vnc            # VNC server
        novnc             # browser-based VNC client
        websockify        # WebSocket-to-TCP proxy for noVNC
        openbox           # window manager (needed for keyboard focus)
        x11-xkb-utils     # setxkbmap
        xdotool           # X11 automation
    )

    log "  Installing APT packages..."
    sudo apt-get update -qq
    sudo apt-get install -y -qq "${pkgs[@]}"

    # Google Chrome
    if ! command -v google-chrome-stable >/dev/null 2>&1; then
        log "  Installing Google Chrome..."
        if ! grep -q "dl.google.com/linux/chrome" /etc/apt/sources.list.d/*.list 2>/dev/null; then
            curl -fsSL https://dl.google.com/linux/linux_signing_key.pub \
                | sudo gpg --dearmor -o /usr/share/keyrings/google-chrome.gpg
            echo "deb [arch=amd64 signed-by=/usr/share/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main" \
                | sudo tee /etc/apt/sources.list.d/google-chrome.list >/dev/null
            sudo apt-get update -qq
        fi
        sudo apt-get install -y -qq google-chrome-stable
    else
        log "  Google Chrome already installed"
    fi

    # Systemd unit files
    log "  Installing systemd services..."

    sudo tee /etc/systemd/system/xvfb.service >/dev/null <<'UNIT'
[Unit]
Description=Xvfb virtual framebuffer

[Service]
ExecStart=/usr/bin/Xvfb :99 -screen 0 1280x720x24
Restart=always

[Install]
WantedBy=multi-user.target
UNIT

    sudo tee /etc/systemd/system/x11vnc.service >/dev/null <<'UNIT'
[Unit]
Description=x11vnc VNC server
After=xvfb.service
Requires=xvfb.service

[Service]
ExecStart=/usr/bin/x11vnc -display :99 -forever -shared -nopw -rfbport 5900
Restart=always

[Install]
WantedBy=multi-user.target
UNIT

    sudo tee /etc/systemd/system/openbox.service >/dev/null <<'UNIT'
[Unit]
Description=Openbox window manager
After=xvfb.service
Requires=xvfb.service

[Service]
Environment=DISPLAY=:99
ExecStart=/usr/bin/openbox
Restart=always
User=exedev

[Install]
WantedBy=multi-user.target
UNIT

    sudo tee /etc/systemd/system/novnc.service >/dev/null <<'UNIT'
[Unit]
Description=noVNC websocket proxy
After=x11vnc.service
Requires=x11vnc.service

[Service]
ExecStart=/usr/bin/websockify --web=/opt/novnc/ 8000 localhost:5900
Restart=always

[Install]
WantedBy=multi-user.target
UNIT

    sudo tee /etc/systemd/system/chromium.service >/dev/null <<'UNIT'
[Unit]
Description=Chrome browser
After=xvfb.service openbox.service
Requires=xvfb.service openbox.service

[Service]
Environment=DISPLAY=:99
ExecStartPre=/usr/bin/setxkbmap -option caps:ctrl_modifier
ExecStart=/usr/bin/google-chrome-stable --no-first-run --disable-gpu --no-sandbox --disable-dev-shm-usage --window-size=1280,720 --window-position=0,0
Restart=on-failure
RestartSec=3
User=exedev

[Install]
WantedBy=multi-user.target
UNIT

    sudo systemctl daemon-reload
    sudo systemctl enable --now xvfb x11vnc openbox novnc chromium

    log ""
    log "VNC stack installed. Access via noVNC on port 8000."
}

case "${1:-}" in
start) cmd_start ;;
stop) cmd_stop ;;
status) cmd_status ;;
destroy) cmd_destroy ;;
deploy) cmd_deploy ;;
deploy-metrics) cmd_deploy_metrics ;;
os-upgrade) cmd_os_upgrade ;;
install-deps) cmd_install_deps ;;
install-vnc) cmd_install_vnc ;;
*)
    echo "Usage: $0 {start|stop|status|destroy|deploy|deploy-metrics|os-upgrade|install-deps|install-vnc}"
    echo ""
    echo "Subcommands:"
    echo "  install-deps    Install all host dependencies (apt packages, pnpm, cloud-hypervisor)"
    echo "  install-vnc     Install VNC/noVNC stack with Chrome for browser-based remote desktop"
    echo "  start           Create and provision the VM cluster (idempotent)"
    echo "  stop            Gracefully stop all VMs (preserves disks)"
    echo "  status          Show cluster status, IPs, and services"
    echo "  destroy         Tear down all VMs and remove disks"
    echo "  deploy          Rebuild binaries, push to VMs, restart services"
    echo "  deploy-metrics  Update prometheus config and deploy Grafana dashboards"
    echo "  os-upgrade      Run apt upgrade on all cluster VMs"
    echo ""
    echo "Environment variables:"
    echo "  NUM_EXELETS=${NUM_EXELETS}  NUM_EXEPROXES=${NUM_EXEPROXES}  CLUSTER_PREFIX=${CLUSTER_PREFIX}"
    echo "  EXED_VCPUS=${EXED_VCPUS}  EXED_RAM=${EXED_RAM}  EXEPROX_VCPUS=${EXEPROX_VCPUS}  EXEPROX_RAM=${EXEPROX_RAM}"
    echo "  EXELET_VCPUS=${EXELET_VCPUS}  EXELET_RAM=${EXELET_RAM}  MON_VCPUS=${MON_VCPUS}  MON_RAM=${MON_RAM}"
    echo "  DISK_GB=${DISK_GB}  EXELET_DATA_DISK_GB=${EXELET_DATA_DISK_GB}  EXELET_BACKUP_DISK_GB=${EXELET_BACKUP_DISK_GB}  EXELET_SWAP_SIZE=${EXELET_SWAP_SIZE}"
    echo "  EXELET_RAMDISK_POOL_SIZE=${EXELET_RAMDISK_POOL_SIZE}  (tmpfs-backed 'ramdisk' zpool, ephemeral)"
    echo "  APT_CACHE_ENABLED=${APT_CACHE_ENABLED}  (run apt-cacher-ng in Docker for faster/offline package installs)"
    exit 1
    ;;
esac
