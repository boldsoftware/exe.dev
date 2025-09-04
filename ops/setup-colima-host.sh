#!/bin/bash
set -euo pipefail

# Configuration
COLIMA_PROFILE="exe-ctr"
COLIMA_CPUS=4
COLIMA_MEMORY=8
COLIMA_DISK=100

# Prebake cache keyed by the contents of ops/setup-containerd-clh-nydus.sh
CACHE_DIR="${EXEDEV_CACHE:-$HOME/.cache/exedev}"
mkdir -p "$CACHE_DIR"

hash_file() {
  local f="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$f" | awk '{print $1}'
  else
    shasum -a 256 "$f" | awk '{print $1}'
  fi
}

# Determine repo ops dir and target setup script to hash
OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SETUP_SCRIPT_PATH="${OPS_DIR}/setup-containerd-clh-nydus.sh"
if [[ ! -f "$SETUP_SCRIPT_PATH" ]]; then
  echo "Required setup script not found for hashing: $SETUP_SCRIPT_PATH" >&2
  exit 1
fi
SETUP_HASH="$(hash_file "$SETUP_SCRIPT_PATH")"
PREBAKE_DIR="${CACHE_DIR}/colima-exe-ctr-host-${SETUP_HASH}.prebake"

LIMA_DIR="$HOME/.lima"
COLIMA_DIR="$HOME/.colima"
COLIMA_PROFILE_DIR="${COLIMA_DIR}/${COLIMA_PROFILE}"

# Detect the actual Lima instance directory used by Colima for this profile
detect_lima_instance_dir() {
  # Common locations
  local d1="${LIMA_DIR}/colima-${COLIMA_PROFILE}"
  local d2="${LIMA_DIR}/${COLIMA_PROFILE}"
  local d3="${LIMA_DIR}/colima" # fallback
  local c1="${COLIMA_DIR}/_lima/colima-${COLIMA_PROFILE}"
  local c2="${COLIMA_DIR}/_lima/${COLIMA_PROFILE}"

  if [[ -d "$d1" ]]; then echo "$d1"; return 0; fi
  if [[ -d "$d2" ]]; then echo "$d2"; return 0; fi
  if [[ -d "$c1" ]]; then echo "$c1"; return 0; fi
  if [[ -d "$c2" ]]; then echo "$c2"; return 0; fi

  # Heuristic searches
  local cand
  cand=$(ls -d "${LIMA_DIR}"/* 2>/dev/null | grep -F "${COLIMA_PROFILE}" | head -n1 || true)
  if [[ -n "$cand" && -d "$cand" ]]; then echo "$cand"; return 0; fi
  cand=$(ls -d "${COLIMA_DIR}/_lima"/* 2>/dev/null | grep -F "${COLIMA_PROFILE}" | head -n1 || true)
  if [[ -n "$cand" && -d "$cand" ]]; then echo "$cand"; return 0; fi

  if [[ -d "$d3" ]]; then echo "$d3"; return 0; fi
  echo ""; return 1
}

cp_clone_dir() {
  # Clone/copy directory SRC into DEST efficiently if supported by FS
  local src="$1"; local dest="$2"
  rm -rf "$dest"
  mkdir -p "$(dirname "$dest")"
  if cp -cR "$src" "$dest" 2>/dev/null; then
    return 0
  elif cp --reflink=auto -a "$src" "$dest" 2>/dev/null; then
    return 0
  elif command -v ditto >/dev/null 2>&1; then
    ditto "$src" "$dest"
    return 0
  elif command -v rsync >/dev/null 2>&1; then
    rsync -a "$src" "$dest"
    return 0
  else
    cp -R "$src" "$dest"
    return 0
  fi
}

pack_profile() {
  echo "Creating prebaked image at ${PREBAKE_DIR}..."
  rm -rf "${PREBAKE_DIR}"
  # Ensure directories exist
  local LIMA_INSTANCE_DIR
  LIMA_INSTANCE_DIR=$(detect_lima_instance_dir || true)
  if [[ -z "$LIMA_INSTANCE_DIR" || ! -d "$LIMA_INSTANCE_DIR" || ! -d "${COLIMA_PROFILE_DIR}" ]]; then
    echo "Expected Colima/Lima directories missing; cannot prebake" >&2
    return 1
  fi
  # Stop the VM to ensure consistent disk state
  colima stop -p ${COLIMA_PROFILE} || true
  # Remove ephemeral sockets that cannot be archived
  rm -f "${COLIMA_DIR}/docker.sock" 2>/dev/null || true
  rm -f "${LIMA_INSTANCE_DIR}/ssh.sock" \
        "${LIMA_INSTANCE_DIR}/ha.sock" \
        "${LIMA_INSTANCE_DIR}/guestagent.sock" 2>/dev/null || true
  # Clone both lima instance and colima profile metadata
  echo "Cloning: $LIMA_INSTANCE_DIR -> ${PREBAKE_DIR}/lima"
  cp_clone_dir "$LIMA_INSTANCE_DIR" "${PREBAKE_DIR}/lima"
  echo "Cloning: $COLIMA_PROFILE_DIR -> ${PREBAKE_DIR}/colima"
  cp_clone_dir "$COLIMA_PROFILE_DIR" "${PREBAKE_DIR}/colima"
  echo "Prebake image created at: ${PREBAKE_DIR}"
}

restore_profile() {
  echo "Restoring prebaked image from ${PREBAKE_DIR}..."
  # Remove any existing state for a clean restore
  colima stop -p ${COLIMA_PROFILE} >/dev/null 2>&1 || true
  colima delete -p ${COLIMA_PROFILE} --force >/dev/null 2>&1 || true
  local LIMA_INSTANCE_DIR
  LIMA_INSTANCE_DIR=$(detect_lima_instance_dir || true)
  if [[ -n "$LIMA_INSTANCE_DIR" ]]; then
    rm -rf "${LIMA_INSTANCE_DIR}" || true
  fi
  rm -rf "${COLIMA_PROFILE_DIR}" || true
  mkdir -p "${LIMA_DIR}" "${COLIMA_DIR}"
  # Recreate the original directories by cloning from prebake
  local PRE_LIMA="${PREBAKE_DIR}/lima"
  local PRE_COLIMA="${PREBAKE_DIR}/colima"
  if [[ ! -d "$PRE_LIMA" || ! -d "$PRE_COLIMA" ]]; then
    echo "Prebake image missing required directories: $PRE_LIMA or $PRE_COLIMA" >&2
    return 1
  fi
  # Determine target lima dir path
  if [[ -z "$LIMA_INSTANCE_DIR" ]]; then
    # If detection failed (paths removed), choose default under ~/.colima/_lima
    LIMA_INSTANCE_DIR="${COLIMA_DIR}/_lima/colima-${COLIMA_PROFILE}"
  fi
  echo "Restoring lima instance to $LIMA_INSTANCE_DIR"
  cp_clone_dir "$PRE_LIMA" "$LIMA_INSTANCE_DIR"
  echo "Restoring colima profile to $COLIMA_PROFILE_DIR"
  cp_clone_dir "$PRE_COLIMA" "$COLIMA_PROFILE_DIR"
  echo "Prebake image restored."
}

# Remove ephemeral sockets and stray listeners that confuse hostagent
cleanup_runtime_state() {
  # Try stopping the profile to clear hostagent if running
  colima stop -p ${COLIMA_PROFILE} >/dev/null 2>&1 || true
  # Remove sockets
  local LIMA_INSTANCE_DIR
  LIMA_INSTANCE_DIR=$(detect_lima_instance_dir || true)
  if [[ -n "$LIMA_INSTANCE_DIR" ]]; then
    rm -f "${LIMA_INSTANCE_DIR}/ssh.sock" \
          "${LIMA_INSTANCE_DIR}/ha.sock" \
          "${LIMA_INSTANCE_DIR}/guestagent.sock" 2>/dev/null || true
  fi
  rm -f "${COLIMA_DIR}/docker.sock" 2>/dev/null || true
  # Kill any process bound to SSH_PORT
  local PORT=${SSH_PORT:-22251}
  if command -v lsof >/dev/null 2>&1; then
    local PIDS
    PIDS=$(lsof -ti tcp:${PORT} 2>/dev/null || true)
    if [[ -n "$PIDS" ]]; then
      echo "Killing processes using TCP port ${PORT}: $PIDS"
      kill -9 $PIDS 2>/dev/null || true
    fi
  fi
}

# Robustly start the Colima profile with our desired settings, with one cleanup retry
start_profile() {
  local port
  port=${SSH_PORT:-22251}
  set -x
  if ! colima start -p ${COLIMA_PROFILE} --cpu ${COLIMA_CPUS} --memory ${COLIMA_MEMORY} --disk ${COLIMA_DISK} \
      --vm-type vz --nested-virtualization --runtime containerd --kubernetes=false --network-address \
      --ssh-port ${port} --arch aarch64; then
    set +x
    echo "First start failed; cleaning runtime state and retrying..."
    cleanup_runtime_state
    # Kill any lingering hostagent processes which may hold proxies
    if command -v pkill >/dev/null 2>&1; then
      pkill -f "[h]ostagent.*colima.*${COLIMA_PROFILE}" 2>/dev/null || true
    fi
    set -x
    colima start -p ${COLIMA_PROFILE} --cpu ${COLIMA_CPUS} --memory ${COLIMA_MEMORY} --disk ${COLIMA_DISK} \
      --vm-type vz --nested-virtualization --runtime containerd --kubernetes=false --network-address \
      --ssh-port ${port} --arch aarch64
  fi
  set +x
}

# Provision a fresh Colima VM with containerd + Kata + Nydus and prepare prebake
provision_fresh_vm() {
  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if [ ! -f "${script_dir}/setup-containerd-clh-nydus.sh" ]; then
    echo "Error: setup-containerd-clh-nydus.sh not found in ${script_dir}"
    return 1
  fi

  echo "Creating modified setup script for Colima environment..."
  cat > /tmp/setup-containerd-clh-nydus-colima.sh <<'SCRIPT_EOF'
#!/bin/bash
set -euo pipefail
echo "=== Starting setup for Colima VM with Cloud Hypervisor + Nydus ==="
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1
# Ensure prerequisites for data volume and tooling
sudo apt-get update -y
sudo apt-get install -y parted xfsprogs

# Set up data volume as loopback XFS if not mounted
echo "Setting up data volume (loopback XFS) for Colima..."
if mount | grep -q "/data"; then
  echo "  /data is already mounted, skipping"
else
  sudo mkdir -p /data
  if [ -f /data.img ]; then
    echo "  /data.img already exists, mounting it"
    sudo mount -o loop,pquota /data.img /data
  else
    echo "  Creating new /data.img file"
    sudo dd if=/dev/zero of=/data.img bs=1G count=20
    sudo mkfs.xfs /data.img
    sudo mount -o loop,pquota /data.img /data
  fi
  if ! grep -q '/data.img' /etc/fstab; then
    echo "  Adding /data mount to fstab"
    echo '/data.img /data xfs loop,pquota 0 0' | sudo tee -a /etc/fstab >/dev/null
  fi
fi
SCRIPT_EOF
  # Append the main setup content starting at the containerd install section,
  # to avoid the physical-device /data setup (Colima uses loopback /data.img).
  sed -n '/^echo "=== Installing containerd ==="/,$p' "${script_dir}/setup-containerd-clh-nydus.sh" \
    | sed 's/systemctl reload containerd/systemctl restart containerd/' \
    >> /tmp/setup-containerd-clh-nydus-colima.sh

  echo "Creating ubuntu user for compatibility with production..."
  colima ssh -p ${COLIMA_PROFILE} -- sudo useradd -m -s /bin/bash ubuntu 2>/dev/null || true
  echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' | colima ssh -p ${COLIMA_PROFILE} -- sudo tee /etc/sudoers.d/ubuntu > /dev/null

  echo "Copying setup script to VM..."
  cat /tmp/setup-containerd-clh-nydus-colima.sh | colima ssh -p ${COLIMA_PROFILE} tee ~/setup-containerd-clh-nydus.sh > /dev/null
  colima ssh -p ${COLIMA_PROFILE} chmod +x ~/setup-containerd-clh-nydus.sh

  echo "=========================================="
  echo "Starting containerd setup in VM"
  echo "=========================================="
  echo "Running setup script in VM (this will take a few minutes)..."
  if ! colima ssh -p ${COLIMA_PROFILE} -- bash ~/setup-containerd-clh-nydus.sh; then
    echo "Error: Setup script failed"
    echo "You can debug by running: colima ssh -p ${COLIMA_PROFILE}"
    return 1
  fi

  echo "Saving containerd configuration for persistence..."
  colima ssh -p ${COLIMA_PROFILE} -- sudo cp /etc/containerd/config.toml /home/ubuntu/containerd-config.toml.backup 2>/dev/null || true
  rm -f /tmp/setup-containerd-clh-nydus-colima.sh

  # Bake/cache the prepared profile for future runs
  pack_profile || echo "Prebake failed; continuing without cache"
}

echo "=== Setting up Colima host for exe.dev containerd testing ==="

if ! command -v colima &> /dev/null; then
    echo "Error: colima is not installed"
    echo "Install with: brew install colima"
    exit 1
fi

# Check if Docker Desktop is actually running (not just docker CLI from colima)
if pgrep -x "Docker Desktop" > /dev/null 2>&1; then
    echo "Docker Desktop appears to be running. It's recommended to stop it to avoid conflicts."
    echo "You can stop Docker Desktop from the menu bar icon."
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi


if [[ -d "${PREBAKE_DIR}" ]]; then
    echo "Found prebaked image for this script version: ${PREBAKE_DIR}"
    restore_profile
    SSH_PORT=22251
    cleanup_runtime_state
    start_profile
    echo "Prebaked profile restored and started. Updating local SSH config..."
    PREBAKED=1
else
    if colima list 2>/dev/null | grep -q "^${COLIMA_PROFILE}"; then
        set -x
        echo "Found existing ${COLIMA_PROFILE} profile"
        colima stop -p ${COLIMA_PROFILE} 2>/dev/null || true
        colima delete -p ${COLIMA_PROFILE} --force 2>/dev/null || true
        set +x
    fi
    SSH_PORT=22251
    cleanup_runtime_state
    start_profile
    sleep 5
    echo "Checking for KVM support in VM..."
    if colima ssh -p ${COLIMA_PROFILE} -- ls /dev/kvm 2>/dev/null; then
        echo "✓ KVM is available (/dev/kvm found) - Kata containers should work"
    else
        echo "⚠️  KVM is not available (/dev/kvm not found) - Kata containers won't work"
        exit 1
    fi
    echo "Testing colima SSH connection..."
    if ! colima ssh -p ${COLIMA_PROFILE} -- echo "SSH connection successful"; then
        echo "Error: Cannot connect to Colima VM"
        exit 1
    fi
    # Provision containerd + Kata + Nydus on fresh VM and create prebake snapshot
    provision_fresh_vm || exit 1
    # VM was stopped during prebake; bring it back up now
    start_profile
fi

# Duplicate heavy provisioning block removed: handled in single no-prebake branch above
echo ""
echo "=========================================="
echo "Configuring host SSH using Colima ssh_config"
echo "=========================================="

# Also ensure ~/.colima/ssh_config is included for all Colima hosts
mkdir -p "$HOME/.ssh"
if [ ! -f "$HOME/.ssh/config" ]; then
  touch "$HOME/.ssh/config"
  chmod 600 "$HOME/.ssh/config"
fi
if ! grep -q ".colima/ssh_config" "$HOME/.ssh/config" 2>/dev/null; then
  echo "Including ~/.colima/ssh_config in ~/.ssh/config"
  tmpcfg=$(mktemp)
  printf "Include ~/.colima/ssh_config\n\n" > "$tmpcfg"
  cat "$HOME/.ssh/config" >> "$tmpcfg"
  mv "$tmpcfg" "$HOME/.ssh/config"
  chmod 600 "$HOME/.ssh/config"
else
  echo "~/.colima/ssh_config already included"
fi

echo ""
echo "=========================================="
echo "Done!"
echo "=========================================="
echo ""
echo "VM setup complete: exe-ctr"
echo "  SSH Port: ${SSH_PORT} (stable)"
echo ""
echo "To start exed:"
echo "  go run ./cmd/exed -dev=local"
echo ""
echo "To reset if containers get stuck:"
echo "  ./ops/reset-colima.sh"
echo ""
echo "To restart the VM:"
echo "  colima restart -p ${COLIMA_PROFILE}"
echo ""
echo "To delete the VM:"
echo "  colima delete -p ${COLIMA_PROFILE}"
echo "=========================================="
