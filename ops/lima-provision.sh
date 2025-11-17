#!/bin/bash
# Runs inside a Lima VM to prepare persistent data and bootstrap exelet hosts.
set -euo pipefail

STAGING_DIR="/tmp/exe-bootstrap"
ASSETS_DIR="/home/ubuntu/.cache/exedops"
CLOUD_HYPERVISOR_SETUP_SCRIPT_NAME="setup-cloud-hypervisor.sh"
EXELET_SETUP_SCRIPT_NAME="setup-exelet.sh"

wait_for_device() {
    local attempt=0
    while [ "${attempt}" -lt 60 ]; do
        if [ -b /dev/vdb ] || [ -b /dev/vdb1 ]; then
            return 0
        fi
        sleep 0.5
        attempt=$((attempt + 1))
    done
    echo "timed out waiting for /dev/vdb" >&2
    return 1
}

pick_data_device() {
    if [ -b /dev/vdb1 ]; then
        echo "/dev/vdb1"
        return 0
    fi
    echo "/dev/vdb"
}

current_mount_point() {
    local dev="$1"
    awk -v dev="${dev}" '$1==dev {print $2; exit}' /proc/mounts || true
}

unmount_device() {
    local mount_point="$1"
    umount "${mount_point}"
    local unit_name
    unit_name="$(systemd-escape -p --suffix=mount "${mount_point}")"
    systemctl stop "${unit_name}" || true
    systemctl disable "${unit_name}" || true
}

ensure_zfs() {
    local device="$1"
    local fs_type
    fs_type="$(blkid -o value -s TYPE "${device}" || true)"
    if [ "${fs_type}" != "zfs_member" ]; then
        zpool create -f -m none tank "${device}"
        zfs create -o mountpoint=/data tank/data
    fi
}

setup_data_disk() {
    apt-get update
    apt-get install -y zfsutils-linux

    wait_for_device
    local data_device
    data_device="$(pick_data_device)"

    local current_mount
    current_mount="$(current_mount_point "${data_device}")"
    if [ -n "${current_mount}" ]; then
        unmount_device "${current_mount}"
    fi

    ensure_zfs "${data_device}"
}

ensure_root() {
    if [ "$(id -u)" -ne 0 ]; then
        echo "lima-provision.sh must run as root for bootstrap stage" >&2
        exit 1
    fi
}

ensure_packages() {
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get install -y -q \
        avahi-daemon \
        docker-registry \
        zfsutils-linux
}

ensure_ubuntu_user() {
    if ! id -u ubuntu >/dev/null 2>&1; then
        useradd -m -s /bin/bash ubuntu
    fi
    echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/ubuntu
    chmod 440 /etc/sudoers.d/ubuntu
}

prepare_directories() {
    mkdir -p /data /local "${ASSETS_DIR}"
    chmod 755 /data /local
    chown ubuntu:ubuntu "${ASSETS_DIR}"
}

configure_hugepages() {
    # Configure Huge Pages. cloud-hypervisor refuses to boot if huge pages are enabled but not
    # actually reserved on the host. /proc/meminfo is reported in KB; default hugepages are 2MB, so
    # divide by 4096 to reserve ~50% of RAM.
    HUGEPAGE_TARGET=$(awk '/MemTotal/ { print int($2/4096); exit(0); }' /proc/meminfo)
    echo "Setting vm.nr_hugepages=${HUGEPAGE_TARGET}"
    echo "${HUGEPAGE_TARGET}" > /proc/sys/vm/nr_hugepages
    mkdir -p /etc/sysctl.d
    cat <<EOF > /etc/sysctl.d/90-exe-hugepages.conf
# Ensure huge pages survive reboots; required for Cloud Hypervisor.
vm.nr_hugepages=${HUGEPAGE_TARGET}
EOF
    sysctl --system >/dev/null
}

stage_exists() {
    local path="$1"
    if [ ! -f "${path}" ]; then
        echo "expected bootstrap asset missing: ${path}" >&2
        exit 1
    fi
}

install_assets() {
    stage_exists "${STAGING_DIR}/${CLOUD_HYPERVISOR_SETUP_SCRIPT_NAME}"
    stage_exists "${STAGING_DIR}/${EXELET_SETUP_SCRIPT_NAME}"

    mv "${STAGING_DIR}/${CLOUD_HYPERVISOR_SETUP_SCRIPT_NAME}" /root/${CLOUD_HYPERVISOR_SETUP_SCRIPT_NAME}
    chmod +x /root/${CLOUD_HYPERVISOR_SETUP_SCRIPT_NAME}

    mv "${STAGING_DIR}/${EXELET_SETUP_SCRIPT_NAME}" /root/${EXELET_SETUP_SCRIPT_NAME}
    chmod +x /root/${EXELET_SETUP_SCRIPT_NAME}

    # Move any cached tarballs and sources into the asset cache.
    if compgen -G "${STAGING_DIR}/*" >/dev/null; then
        shopt -s nullglob
        for f in "${STAGING_DIR}"/*; do
            base="$(basename "${f}")"
            case "${base}" in
            "${EXELET_SETUP_SCRIPT_NAME}" | "${CLOUD_HYPERVISOR_SETUP_SCRIPT_NAME}" | "lima-provision.sh")
                continue
                ;;
            esac
            mv "${f}" "${ASSETS_DIR}/${base}"
            chown ubuntu:ubuntu "${ASSETS_DIR}/${base}"
        done
        shopt -u nullglob
    fi
}

run_cloud_hypervisor_setup() {
    /root/${CLOUD_HYPERVISOR_SETUP_SCRIPT_NAME}
}

run_exelet_setup() {
    /root/${EXELET_SETUP_SCRIPT_NAME}
}

finalize_bootstrap() {
    rm -rf "${STAGING_DIR}"
}

bootstrap_vm() {
    ensure_root
    echo "=========================================="
    echo "Preparing Lima VM environment"
    echo "=========================================="
    ensure_packages
    ensure_ubuntu_user
    prepare_directories
    echo "=========================================="
    echo "Configuring hugepages for Cloud Hypervisor"
    echo "=========================================="
    configure_hugepages
    install_assets
    echo "=========================================="
    echo "Starting cloud-hypervisor setup in VM"
    echo "=========================================="
    run_cloud_hypervisor_setup
    echo "=========================================="
    echo "Starting exelet initial setup in VM"
    echo "=========================================="
    run_exelet_setup
    echo "=========================================="
    echo "Finalizing configuration"
    echo "=========================================="
    finalize_bootstrap
}

usage() {
    echo "usage: lima-provision.sh [stage]" >&2
    echo "stages:" >&2
    echo "  data-disk   (default) prepare /data using the attached disk" >&2
    echo "  bootstrap   finalize VM setup after host copies assets" >&2
    exit 1
}

main() {
    local stage="${1:-data-disk}"
    case "${stage}" in
    data-disk)
        setup_data_disk
        ;;
    bootstrap)
        bootstrap_vm
        ;;
    *)
        usage
        ;;
    esac
}

main "$@"
