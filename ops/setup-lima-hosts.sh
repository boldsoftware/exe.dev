#!/bin/bash
set -euo pipefail
set -E # inherit traps
trap 'echo Error in $0 at line $LINENO: $(cd "'"${PWD}"'" && awk "NR == $LINENO" $0)' ERR

# Configuration
LIMA_BASE="exe-ctr-base"
LIMA_HOST_A="exe-ctr"
LIMA_HOST_B="exe-ctr-tests"
DATA_DISK_SIZE="100GiB"
BACKUP_DISK_SIZE="50GiB"
CLOUD_HYPERVISOR_VERSION="48.0"
VIRTIOFSD_VERSION="1.13.2"

# Determine repo ops dir
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "${OPS_DIR}" rev-parse --show-toplevel)"
LIMA_CONFIG_PATH="${OPS_DIR}/lima-with-data.yaml"
if [[ ! -f "$LIMA_CONFIG_PATH" ]]; then
    echo "Required Lima config not found: $LIMA_CONFIG_PATH" >&2
    exit 1
fi
PROVISION_SCRIPT_PATH="${OPS_DIR}/lima-provision.sh"
if [[ ! -f "$PROVISION_SCRIPT_PATH" ]]; then
    echo "Required Lima provision script not found: $PROVISION_SCRIPT_PATH" >&2
    exit 1
fi

LIMA_DIR="$HOME/.lima"
BOOTSTRAP_STAGING="/tmp/exe-bootstrap"
DOCKER_CMD=()

ensure_cloud_hypervisor_artifacts() {
    local arch="$1"
    local cache_dir="$HOME/.cache/exedops"
    local artifact_name="cloud-hypervisor-${CLOUD_HYPERVISOR_VERSION}-${arch}.tar.gz"
    local artifact_path="${cache_dir}/${artifact_name}"
    local build_context="${OPS_DIR}/cloud-hypervisor"
    local platform=""
    if [[ -f "${artifact_path}" ]]; then
        echo "Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} (${arch}) cache hit"
        return 0
    fi

    if [[ ! -d "${build_context}" ]]; then
        echo "Cloud Hypervisor Docker context missing at ${build_context}" >&2
        exit 1
    fi

    if docker info >/dev/null 2>&1; then
        DOCKER_CMD=(docker)
    elif sudo docker info >/dev/null 2>&1; then
        DOCKER_CMD=(sudo docker)
    else
        echo "Docker is required to pre-build Cloud Hypervisor artifacts" >&2
        exit 1
    fi

    case "${arch}" in
    arm64) platform="linux/arm64" ;;
    amd64) platform="linux/amd64" ;;
    *)
        echo "Unsupported arch for Cloud Hypervisor build: ${arch}" >&2
        exit 1
        ;;
    esac

    mkdir -p "${cache_dir}"

    local image_tag="exe-cloud-hypervisor:${CLOUD_HYPERVISOR_VERSION}-${arch}"
    echo "Building Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} (${arch}) via Docker..."
    "${DOCKER_CMD[@]}" build \
        --platform "${platform}" \
        --tag "${image_tag}" \
        --build-arg "CLOUD_HYPERVISOR_VERSION=${CLOUD_HYPERVISOR_VERSION}" \
        --build-arg "VIRTIOFSD_VERSION=${VIRTIOFSD_VERSION}" \
        --build-arg "TARGETARCH=${arch}" \
        "${build_context}"

    local container_id
    if ! container_id=$("${DOCKER_CMD[@]}" create "${image_tag}" /bin/true); then
        echo "Failed to create container for Cloud Hypervisor artifacts" >&2
        exit 1
    fi

    local tmp_dir
    tmp_dir="$(mktemp -d)"

    set +e
    "${DOCKER_CMD[@]}" cp "${container_id}:/out/." "${tmp_dir}"
    local cp_status=$?
    set -e

    if [[ $cp_status -ne 0 ]]; then
        echo "Failed to copy Cloud Hypervisor artifacts from Docker image" >&2
        rm -rf "${tmp_dir}"
        "${DOCKER_CMD[@]}" rm "${container_id}" >/dev/null 2>&1 || true
        exit 1
    fi

    tar czf "${artifact_path}" -C "${tmp_dir}" .
    chmod 0644 "${artifact_path}"
    rm -rf "${tmp_dir}"
    "${DOCKER_CMD[@]}" rm "${container_id}" >/dev/null 2>&1 || true
    echo "Cached Cloud Hypervisor ${CLOUD_HYPERVISOR_VERSION} (${arch}) at ${artifact_path}"
}

data_disk_name() {
    echo "data-$1"
}

backup_disk_name() {
    echo "backup-$1"
}

data_disk_path() {
    local disk
    disk="$(data_disk_name "$1")"
    echo "${LIMA_DIR}/_disks/${disk}/datadisk"
}

backup_disk_path() {
    local disk
    disk="$(backup_disk_name "$1")"
    echo "${LIMA_DIR}/_disks/${disk}/datadisk"
}

set_disk_expr() {
    local data_disk="$1"
    local backup_disk="$2"
    printf '.additionalDisks[0].name = "%s" | .additionalDisks[1].name = "%s"' "${data_disk}" "${backup_disk}"
}

delete_data_disk() {
    local instance="$1"
    local disk
    disk="$(data_disk_name "$instance")"
    limactl --tty=false disk delete "${disk}" >/dev/null 2>&1 || true
    # Always clean up the directory in case limactl left an empty one behind
    rm -rf "${LIMA_DIR}/_disks/${disk}" >/dev/null 2>&1 || true
}

delete_backup_disk() {
    local instance="$1"
    local disk
    disk="$(backup_disk_name "$instance")"
    limactl --tty=false disk delete "${disk}" >/dev/null 2>&1 || true
    rm -rf "${LIMA_DIR}/_disks/${disk}" >/dev/null 2>&1 || true
}

create_fresh_data_disk() {
    local instance="$1"
    local disk
    disk="$(data_disk_name "$instance")"
    echo "Creating Lima disk ${disk} (${DATA_DISK_SIZE})..."
    delete_data_disk "${instance}"
    limactl --tty=false --log-level=warn disk create "${disk}" --size "${DATA_DISK_SIZE}"
}

create_fresh_backup_disk() {
    local instance="$1"
    local disk
    disk="$(backup_disk_name "$instance")"
    echo "Creating Lima disk ${disk} (${BACKUP_DISK_SIZE})..."
    delete_backup_disk "${instance}"
    limactl --tty=false --log-level=warn disk create "${disk}" --size "${BACKUP_DISK_SIZE}"
}

clone_data_disk() {
    local src_instance="$1"
    local dst_instance="$2"
    local src_disk
    src_disk="$(data_disk_name "$src_instance")"
    local dst_disk
    dst_disk="$(data_disk_name "$dst_instance")"
    local src_path
    src_path="$(data_disk_path "$src_instance")"
    if [[ ! -f "${src_path}" ]]; then
        echo "Error: source data disk not found at ${src_path}" >&2
        exit 1
    fi
    echo "Cloning Lima disk ${src_disk} -> ${dst_disk}..."
    delete_data_disk "${dst_instance}"
    limactl --tty=false --log-level=warn disk import "${dst_disk}" "${src_path}"
}

clone_backup_disk() {
    local src_instance="$1"
    local dst_instance="$2"
    local src_disk
    src_disk="$(backup_disk_name "$src_instance")"
    local dst_disk
    dst_disk="$(backup_disk_name "$dst_instance")"
    local src_path
    src_path="$(backup_disk_path "$src_instance")"
    if [[ ! -f "${src_path}" ]]; then
        echo "Error: source backup disk not found at ${src_path}" >&2
        exit 1
    fi
    echo "Cloning Lima disk ${src_disk} -> ${dst_disk}..."
    delete_backup_disk "${dst_instance}"
    limactl --tty=false --log-level=warn disk import "${dst_disk}" "${src_path}"
}

# Provision a fresh Lima VM with exelet + Cloud Hypervisor
provision_base_vm() {
    local script_dir="${OPS_DIR}"
    local repo_root="${REPO_ROOT}"

    # Download dependencies locally if not cached
    VM_ARCH="arm64"
    echo "Ensuring dependencies are downloaded for $VM_ARCH..."
    "${script_dir}/download-ctr-host.sh" "$VM_ARCH"
    ensure_cloud_hypervisor_artifacts "$VM_ARCH"

    echo "Preparing bootstrap assets for VM..."
    limactl shell ${LIMA_BASE} -- sudo rm -rf "${BOOTSTRAP_STAGING}"
    limactl shell ${LIMA_BASE} -- sudo mkdir -p "${BOOTSTRAP_STAGING}"
    limactl shell ${LIMA_BASE} -- sudo chmod 1777 "${BOOTSTRAP_STAGING}"

    echo "Copying cloud-hypervisor and virtiofsd sources to VM..."
    CACHE_DIR="$HOME/.cache/exedops"

    for file in "$CACHE_DIR"/cloud-hypervisor-*.tar.gz "$CACHE_DIR"/virtiofsd-*.tar.gz "$CACHE_DIR"/*.tar; do
        if [ -f "$file" ]; then
            basename=$(basename "$file")
            echo "  Copying $basename..."
            limactl cp "$file" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/$basename"
        fi
    done

    # cloud hypervisor setup script
    limactl cp "${script_dir}/deploy/setup-cloud-hypervisor.sh" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/setup-cloud-hypervisor.sh"

    # build and cache a local exelet to be able to provision the base instance volumes
    echo "Building bootstrap exelet..."
    make -C "${repo_root}" GOOS=linux GOARCH=${VM_ARCH} exelet exelet-ctl
    limactl cp "${repo_root}/exeletd" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/exeletd-${VM_ARCH}"
    limactl cp "${repo_root}/exelet-ctl" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/exelet-ctl-${VM_ARCH}"
    limactl cp "${script_dir}/setup-exelet.sh" "${LIMA_BASE}:${BOOTSTRAP_STAGING}/setup-exelet.sh"

    echo "Running bootstrap script in VM (this will take a few minutes)..."
    limactl shell ${LIMA_BASE} -- sudo bash /usr/local/bin/lima-provision.sh bootstrap

    # Copy default SSH keys to root's login, so ssh root@lima-exe-ctr.local works
    (cat ~/.ssh/id_*.pub | limactl shell ${LIMA_BASE} sudo tee /root/.authorized_keys) || true
}

setup_base() {
    local start_time=$SECONDS
    echo "=== Setting up Lima base instance ==="

    # Clean up existing base if it exists
    limactl stop --tty=false exe-ctr-base -f 2>/dev/null || true
    sleep 2
    limactl delete exe-ctr-base --tty=false -f 2>/dev/null || true

    delete_data_disk "${LIMA_BASE}"
    delete_backup_disk "${LIMA_BASE}"
    create_fresh_data_disk "${LIMA_BASE}"
    create_fresh_backup_disk "${LIMA_BASE}"

    echo "Creating base Lima instance: ${LIMA_BASE}"
    base_data_disk="$(data_disk_name "${LIMA_BASE}")"
    base_backup_disk="$(backup_disk_name "${LIMA_BASE}")"
    # Ensure mount location referenced by template exists
    mkdir -p /tmp/lima
    limactl create --plain --tty=false --log-level=warn --name=${LIMA_BASE} \
        --set "$(set_disk_expr "${base_data_disk}" "${base_backup_disk}")" \
        "${LIMA_CONFIG_PATH}"
    limactl start --tty=false --log-level=warn ${LIMA_BASE}

    echo "Checking for KVM support in VM..."
    if limactl shell ${LIMA_BASE} -- ls /dev/kvm 2>/dev/null; then
        echo "✓ KVM is available (/dev/kvm found) - Kata containers should work"
    else
        echo "⚠️  KVM is not available (/dev/kvm not found) - Kata containers won't work"
        exit 1
    fi

    echo "Testing Lima SSH connection..."
    limactl shell ${LIMA_BASE} -- echo "SSH connection successful"

    # Provision the base VM
    provision_base_vm

    echo "Stopping base instance before cloning..."
    limactl stop --log-level=warn ${LIMA_BASE}

    local elapsed=$((SECONDS - start_time))
    echo ""
    echo "=========================================="
    echo "Lima base instance ready (${elapsed}s)"
    echo "=========================================="
}

reset_instance() {
    local instance="$1"
    echo "=== Resetting Lima instance: ${instance} ==="

    # Check if base instance exists
    if ! limactl list | grep "${LIMA_BASE}" >/dev/null 2>&1; then
        echo "Error: Base instance ${LIMA_BASE} not found"
        echo "Please run './ops/setup-lima-hosts.sh base' first to create the base instance"
        exit 1
    fi

    echo "Stopping instances..."
    limactl stop --tty=false ${LIMA_BASE} -f 2>/dev/null || true
    limactl stop --tty=false ${instance} -f 2>/dev/null || true

    sleep 2

    echo "Removing cloned instance ${instance}..."
    limactl delete ${instance} --tty=false -f 2>/dev/null || true

    # Clean up cloned disks
    delete_data_disk "${instance}"
    delete_backup_disk "${instance}"

    echo "Cloning ${LIMA_BASE} to ${instance}..."
    limactl clone --tty=false --log-level=warn --set "$(set_disk_expr "$(data_disk_name "${instance}")" "$(backup_disk_name "${instance}")")" ${LIMA_BASE} ${instance}

    clone_data_disk "${LIMA_BASE}" "${instance}"
    clone_backup_disk "${LIMA_BASE}" "${instance}"

    echo "Starting ${instance}..."
    limactl start --log-level=warn --tty=false ${instance}

    echo ""
    echo "=========================================="
    echo "Lima instance ${instance} reset"
    echo "=========================================="
}

reset_images() {
    echo "=== Resetting Lima image instances ==="
    reset_instance "${LIMA_HOST_A}"
    reset_instance "${LIMA_HOST_B}"
    echo ""
    echo "=========================================="
    echo "Lima image instances reset"
    echo "=========================================="
}

# Check arguments
if [[ $# -ne 1 ]]; then
    echo "Usage: $0 {base|reset|reset-exe-ctr|reset-exe-ctr-tests|all}"
    echo ""
    echo "  base                 - Build only the base instance"
    echo "  reset                - Delete and re-clone both image instances from base"
    echo "  reset-exe-ctr        - Reset only the exe-ctr instance"
    echo "  reset-exe-ctr-tests  - Reset only the exe-ctr-tests instance"
    echo "  all                  - Build base and create image instances"
    exit 1
fi

MODE="$1"

echo "=== Setting up Lima hosts for exe.dev testing ==="

if ! command -v limactl &>/dev/null; then
    echo "Error: lima is not installed"
    echo "Install with: brew install lima"
    exit 1
fi

case "${MODE}" in
base)
    setup_base
    ;;
reset)
    reset_images
    ;;
reset-exe-ctr)
    reset_instance "${LIMA_HOST_A}"
    ;;
reset-exe-ctr-tests)
    reset_instance "${LIMA_HOST_B}"
    ;;
all)
    setup_base
    reset_images
    ;;
*)
    echo "Error: Invalid mode '${MODE}'"
    echo "Usage: $0 {base|reset|reset-exe-ctr|reset-exe-ctr-tests|all}"
    exit 1
    ;;
esac

if [[ "${MODE}" == "all" || "${MODE}" == "reset" || "${MODE}" == "reset-exe-ctr" || "${MODE}" == "reset-exe-ctr-tests" ]]; then
    echo "Configuring SSH access..."
    echo "Adding Lima SSH config includes..."
    mkdir -p "$HOME/.ssh"
    touch "$HOME/.ssh/config"

    # Check if includes are already present
    if ! grep -q "Include ~/.lima/\*/ssh.config" "$HOME/.ssh/config"; then
        # Add at the beginning of the file
        echo "Include ~/.lima/*/ssh.config" | cat - "$HOME/.ssh/config" >"$HOME/.ssh/config.tmp" && mv "$HOME/.ssh/config.tmp" "$HOME/.ssh/config"
        echo "✓ Added Lima SSH config includes"
    else
        echo "✓ Lima SSH config includes already present"
    fi

    # Add IdentityFile configuration for .local hosts
    if ! grep -q "Host lima-exe-ctr.local" "$HOME/.ssh/config"; then
        cat >>"$HOME/.ssh/config" <<EOF

Host lima-exe-ctr.local
    IdentityFile ${HOME}/.lima/_config/user

Host lima-exe-ctr-tests.local
    IdentityFile ${HOME}/.lima/_config/user
EOF
        echo "✓ Added IdentityFile configuration for lima-exe-ctr.local and lima-exe-ctr-tests.local"
    else
        echo "✓ IdentityFile configuration for .local hosts already present"
    fi

    echo ""
    echo "=========================================="
    echo "Lima hosts ready"
    echo "=========================================="
    echo ""
    echo "To access the VMs:"
    echo "  ssh lima-exe-ctr          # Main host"
    echo "  ssh lima-exe-ctr-tests    # Tests host"
    echo ""
    echo "To reset VMs to initial state:"
    echo "  ${OPS_DIR}/setup-lima-hosts.sh reset                 # Reset both"
    echo "  ${OPS_DIR}/setup-lima-hosts.sh reset-exe-ctr        # Reset exe-ctr only"
    echo "  ${OPS_DIR}/setup-lima-hosts.sh reset-exe-ctr-tests  # Reset exe-ctr-tests only"
    echo ""
    echo "=========================================="
fi
