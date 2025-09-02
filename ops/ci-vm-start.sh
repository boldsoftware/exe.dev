#!/usr/bin/env bash
set -euo pipefail

NAME="${NAME:-ci-ubuntu-$(date +%s)}"
VCPUS="${VCPUS:-4}"
RAM_MB="${RAM_MB:-4096}"           # 4GiB
DISK_GB="${DISK_GB:-20}"           # thin-provisioned
BASE_IMG="${BASE_IMG:-/var/lib/libvirt/images/ubuntu-24.04-base.qcow2}"
WORKDIR="${WORKDIR:-/var/lib/libvirt/images}"
SSH_PUBKEY="${SSH_PUBKEY:-$HOME/.ssh/id_ed25519.pub}"  # or inject via env
USER_NAME="${USER_NAME:-ubuntu}"

if [[ ! -f "${BASE_IMG}" ]]; then
  echo "Base image not found: ${BASE_IMG}" >&2
  exit 1
fi
if [[ ! -f "${SSH_PUBKEY}" ]]; then
  echo "SSH pubkey not found: ${SSH_PUBKEY}" >&2
  exit 1
fi

# Ensure workdir exists (may require root)
sudo mkdir -p "${WORKDIR}"
DISK="${WORKDIR}/${NAME}.qcow2"
SEED="${WORKDIR}/${NAME}-seed.iso"

# 1) Ephemeral COW disk
sudo qemu-img create -f qcow2 -F qcow2 -b "${BASE_IMG}" "${DISK}" "${DISK_GB}G"

# 2) Cloud-init seed (NoCloud)
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

cat > "${TMPDIR}/user-data" <<EOF
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
  - systemctl enable --now qemu-guest-agent
EOF

cat > "${TMPDIR}/meta-data" <<EOF
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

  if ! sudo virsh net-info default | grep -q "Active:.*yes"; then
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
  if [[ -z "${ip}" ]]; then
    # Fallback: correlate MAC from domiflist with default network DHCP leases
    mac=$(sudo virsh domiflist "${NAME}" 2>/dev/null | awk 'NR>2 && $0!~/^$/ {print $5; exit}')
    if [[ -n "${mac}" ]]; then
      ip=$(sudo virsh net-dhcp-leases default 2>/dev/null | awk -v m="$mac" '$0 ~ m {print $5}' | sed 's|/.*||' | head -n1 || true)
    fi
  fi
  if [[ -z "${ip}" ]]; then
    # Last resort: guest agent
    ip=$(sudo virsh domifaddr "${NAME}" --source agent 2>/dev/null | awk '/ipv4/ {print $4}' | sed 's|/.*||' | head -n1 || true)
  fi
  echo "${ip}"
}

echo "Waiting for IP..."
for i in $(seq 1 120); do
  IP="$(get_ip || true)"
  if [[ -n "${IP}" ]]; then break; fi
  echo "  attempt ${i}/120: no IP yet"
  sleep 2
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

# 6) Prepare containerd + nydus + kata on the VM (similar to setup-colima-host)
# Create a modified setup script that skips swap/data volume and restarts containerd
LOCAL_TMP_SCRIPT="$(mktemp)"
cat > "${LOCAL_TMP_SCRIPT}" <<'SCRIPT_EOF'
#!/bin/bash
set -euo pipefail

echo "=== Starting setup for CI VM with Cloud Hypervisor + Nydus ==="

# Prevent service restarts during package installation
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1

# Skip swap/data volume setup on this ephemeral CI VM
echo "=== Skipping swap and data volume setup for CI VM ==="

# Continue with the rest of the setup from the original script
SCRIPT_EOF

# Append containerd+kata+nydus install/config section from the original script, replacing reload with restart
sed -n '79,$p' "$(dirname "$0")/setup-containerd-clh-nydus.sh" | \
  sed 's/systemctl reload containerd/systemctl restart containerd/' | \
  sed -E 's/sudo\s+//g' | \
  sed 's/KATA_ARCH="x86_64"/KATA_ARCH="amd64"/' | \
  sed 's/NYDUS_ARCH="x86_64"/NYDUS_ARCH="amd64"/' >> "${LOCAL_TMP_SCRIPT}"

chmod +x "${LOCAL_TMP_SCRIPT}"

echo "Copying setup script to VM ${IP}..."
scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "${LOCAL_TMP_SCRIPT}" "${USER_NAME}@${IP}:~/setup-containerd-clh-nydus.sh"
ssh ${SSH_OPTS} ${USER_NAME}@"${IP}" 'sudo mv ~/setup-containerd-clh-nydus.sh /root/setup-containerd-clh-nydus.sh && sudo chmod +x /root/setup-containerd-clh-nydus.sh'

echo "Executing setup script on VM ${IP} (raw streaming output)..."
# Stream exact commands and output directly to CI logs
ssh ${SSH_OPTS} -o LogLevel=ERROR ${USER_NAME}@"${IP}" "sudo /bin/bash -x /root/setup-containerd-clh-nydus.sh"

rm -f "${LOCAL_TMP_SCRIPT}"

# 7) Emit a small envfile for subsequent steps (write to a readable location)
OUTDIR="${OUTDIR:-$PWD}"
mkdir -p "${OUTDIR}"
ENVFILE="${OUTDIR}/${NAME}.env"
cat > "${ENVFILE}" <<EOF
VM_NAME=${NAME}
VM_IP=${IP}
VM_USER=${USER_NAME}
VM_DISK=${DISK}
VM_SEED=${SEED}
EOF
echo "${ENVFILE}"
