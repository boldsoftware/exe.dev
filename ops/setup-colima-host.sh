#!/bin/bash
set -euo pipefail

# Configuration
COLIMA_PROFILE="exe-ctr-colima"
COLIMA_CPUS=4
COLIMA_MEMORY=8
COLIMA_DISK=100
COLIMA_VM_TYPE="vz"  # Use VZ with nested virtualization for Kata support

echo "=== Setting up Colima host for exe.dev containerd testing ==="

# Check if colima is installed
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

# Check existing Colima profiles
echo "Checking existing Colima profiles..."
colima list

# Stop existing profile if running
if colima list 2>/dev/null | grep -q "^${COLIMA_PROFILE}"; then
    echo "Found existing ${COLIMA_PROFILE} profile"
    echo "Stopping ${COLIMA_PROFILE}..."
    echo "Running: colima stop -p ${COLIMA_PROFILE}"
    colima stop -p ${COLIMA_PROFILE} 2>/dev/null || true
    echo "Deleting ${COLIMA_PROFILE}..."
    echo "Running: colima delete -p ${COLIMA_PROFILE} --force"
    colima delete -p ${COLIMA_PROFILE} --force 2>/dev/null || true
fi

# Start Colima with Ubuntu (using vz for virtualization framework)
echo "Starting Colima with Ubuntu..."
echo "  Profile: ${COLIMA_PROFILE}"
echo "  CPUs: ${COLIMA_CPUS}"
echo "  Memory: ${COLIMA_MEMORY}GB"
echo "  Disk: ${COLIMA_DISK}GB"
echo "  VM Type: ${COLIMA_VM_TYPE}"

# Start colima with VZ virtualization framework
# VZ on Apple Silicon should provide virtualization extensions by default
echo "Note: Using VZ virtualization framework (may support nested virtualization on Apple Silicon)"
echo ""
echo "Starting new Colima profile (this may request your password for VM setup)..."
echo "Running: colima start -p ${COLIMA_PROFILE} --cpu ${COLIMA_CPUS} --memory ${COLIMA_MEMORY} --disk ${COLIMA_DISK} --vm-type ${COLIMA_VM_TYPE} --runtime containerd"
echo ""
colima start \
    -p ${COLIMA_PROFILE} \
    --cpu ${COLIMA_CPUS} \
    --memory ${COLIMA_MEMORY} \
    --disk ${COLIMA_DISK} \
    --vm-type ${COLIMA_VM_TYPE} \
    --runtime containerd \
    --kubernetes=false \
    --network-address \
    --arch aarch64

echo ""
echo "Waiting for Colima to be ready..."
sleep 5

# Check if KVM is available in the VM
echo "Checking for KVM support in VM..."
if colima ssh -p ${COLIMA_PROFILE} -- ls /dev/kvm 2>/dev/null; then
    echo "✓ KVM is available (/dev/kvm found) - Kata containers should work"
else
    echo "⚠️  KVM is not available (/dev/kvm not found) - Kata containers won't work"
    echo "   Note: VZ framework on macOS may not provide full nested virtualization"
fi

# Get the VM's SSH details
echo "Getting SSH configuration..."
COLIMA_SSH_CONFIG=$(colima ssh-config -p ${COLIMA_PROFILE})
SSH_HOST=$(echo "$COLIMA_SSH_CONFIG" | grep "Hostname" | awk '{print $2}')
SSH_PORT=$(echo "$COLIMA_SSH_CONFIG" | grep "Port" | awk '{print $2}')
SSH_USER=$(echo "$COLIMA_SSH_CONFIG" | grep "User" | awk '{print $2}')
SSH_KEY=$(echo "$COLIMA_SSH_CONFIG" | grep "IdentityFile" | awk '{print $2}')

# Verify we can connect
echo "Testing SSH connection..."
if ! colima ssh -p ${COLIMA_PROFILE} -- echo "SSH connection successful"; then
    echo "Error: Cannot connect to Colima VM"
    exit 1
fi

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Check if setup script exists
if [ ! -f "${SCRIPT_DIR}/setup-containerd-clh-nydus.sh" ]; then
    echo "Error: setup-containerd-clh-nydus.sh not found in ${SCRIPT_DIR}"
    exit 1
fi

echo ""
echo "Installing required packages in VM..."
# Install prerequisites in the VM
colima ssh -p ${COLIMA_PROFILE} -- sudo apt-get update
colima ssh -p ${COLIMA_PROFILE} -- sudo DEBIAN_FRONTEND=noninteractive apt-get install -y \
    ca-certificates \
    curl \
    gnupg \
    lsb-release \
    apt-transport-https \
    jq \
    build-essential \
    pkg-config \
    libseccomp-dev \
    wget \
    parted \
    xfsprogs \
    iptables \
    iptables-persistent

echo ""
echo "Setting up data volume..."
# Check if /data is already mounted
if colima ssh -p ${COLIMA_PROFILE} -- mount | grep -q "/data"; then
    echo "  /data is already mounted, skipping data volume setup"
else
    # Create a data directory in the VM (simulating /dev/xvdf)
    colima ssh -p ${COLIMA_PROFILE} -- sudo mkdir -p /data
    
    # Check if /data.img already exists
    if colima ssh -p ${COLIMA_PROFILE} test -f /data.img; then
        echo "  /data.img already exists, mounting it"
        colima ssh -p ${COLIMA_PROFILE} -- sudo mount -o loop,pquota /data.img /data
    else
        echo "  Creating new /data.img file"
        # Create a 20GB file to use as a loopback device for XFS with quotas
        colima ssh -p ${COLIMA_PROFILE} -- sudo dd if=/dev/zero of=/data.img bs=1G count=20
        colima ssh -p ${COLIMA_PROFILE} -- sudo mkfs.xfs /data.img
        colima ssh -p ${COLIMA_PROFILE} -- sudo mount -o loop,pquota /data.img /data
    fi
    
    # Add to fstab if not already there
    if ! colima ssh -p ${COLIMA_PROFILE} -- grep -q '/data.img' /etc/fstab; then
        echo "  Adding /data mount to fstab"
        echo '/data.img /data xfs loop,pquota 0 0' | colima ssh -p ${COLIMA_PROFILE} -- sudo tee -a /etc/fstab > /dev/null
    fi
fi

echo ""
echo "Creating modified setup script for Colima environment..."
# Create a modified version of the setup script that skips hardware-specific setup
cat > /tmp/setup-containerd-clh-nydus-colima.sh <<'SCRIPT_EOF'
#!/bin/bash
set -euo pipefail

echo "=== Starting setup for Colima VM with Cloud Hypervisor + Nydus ==="

# Prevent service restarts during package installation
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1

# Skip swap setup on Colima (no NVMe drives)
echo "=== Skipping swap setup (not needed for Colima testing) ==="

# Skip data volume setup (already mounted at /data)
echo "=== Data volume already configured at /data ==="

# Continue with the rest of the setup from the original script
SCRIPT_EOF

# Extract the containerd installation and onwards from the original script
# Starting from line 79 (Installing containerd) to the end, skipping data volume setup
# Keep 'ubuntu' user as-is (we'll create it) and fix reload to restart
sed -n '79,$p' "${SCRIPT_DIR}/setup-containerd-clh-nydus.sh" | sed 's/systemctl reload containerd/systemctl restart containerd/' >> /tmp/setup-containerd-clh-nydus-colima.sh

echo ""
echo "Creating ubuntu user for compatibility with production..."
# Create ubuntu user before running the setup script since it references this user
colima ssh -p ${COLIMA_PROFILE} -- sudo useradd -m -s /bin/bash ubuntu 2>/dev/null || true
echo 'ubuntu ALL=(ALL) NOPASSWD:ALL' | colima ssh -p ${COLIMA_PROFILE} -- sudo tee /etc/sudoers.d/ubuntu > /dev/null

echo "Copying setup script to VM..."
# Copy the modified script to the VM
cat /tmp/setup-containerd-clh-nydus-colima.sh | colima ssh -p ${COLIMA_PROFILE} tee ~/setup-containerd-clh-nydus.sh > /dev/null
colima ssh -p ${COLIMA_PROFILE} chmod +x ~/setup-containerd-clh-nydus.sh

echo ""
echo "=========================================="
echo "Starting containerd setup in VM"
echo "=========================================="

# Execute the setup script
echo "Running setup script in VM (this will take a few minutes)..."
if ! colima ssh -p ${COLIMA_PROFILE} -- bash ~/setup-containerd-clh-nydus.sh; then
    echo "Error: Setup script failed"
    echo "You can debug by running: colima ssh -p ${COLIMA_PROFILE}"
    exit 1
fi

# Clean up temp file
rm -f /tmp/setup-containerd-clh-nydus-colima.sh

echo ""
echo "=========================================="
echo "Setting up SSH access with ubuntu user"
echo "=========================================="

# Check if user has an SSH key
if [ ! -f ~/.ssh/id_ed25519.pub ] && [ ! -f ~/.ssh/id_rsa.pub ]; then
    echo "Error: No SSH public key found at ~/.ssh/id_ed25519.pub or ~/.ssh/id_rsa.pub"
    echo "Please generate an SSH key first with: ssh-keygen -t ed25519"
    exit 1
fi

# Determine which key to use (prefer ed25519)
if [ -f ~/.ssh/id_ed25519.pub ]; then
    SSH_KEY_FILE=~/.ssh/id_ed25519.pub
    SSH_KEY_PRIVATE=~/.ssh/id_ed25519
else
    SSH_KEY_FILE=~/.ssh/id_rsa.pub
    SSH_KEY_PRIVATE=~/.ssh/id_rsa
fi

echo "Using SSH key: $SSH_KEY_FILE"

# Ubuntu user should already exist from earlier setup
echo "Configuring ubuntu user for SSH access..."

# Set up SSH for ubuntu user
echo "Setting up SSH access for ubuntu user..."
colima ssh -p ${COLIMA_PROFILE} -- sudo mkdir -p /home/ubuntu/.ssh
colima ssh -p ${COLIMA_PROFILE} -- sudo chmod 700 /home/ubuntu/.ssh
cat "$SSH_KEY_FILE" | colima ssh -p ${COLIMA_PROFILE} -- sudo tee /home/ubuntu/.ssh/authorized_keys > /dev/null
colima ssh -p ${COLIMA_PROFILE} -- sudo chmod 600 /home/ubuntu/.ssh/authorized_keys
colima ssh -p ${COLIMA_PROFILE} -- sudo chown -R ubuntu:ubuntu /home/ubuntu/.ssh

# Ensure SSH server is running and configured
echo "Configuring SSH server..."
colima ssh -p ${COLIMA_PROFILE} -- sudo sed -i 's/#PasswordAuthentication yes/PasswordAuthentication no/' /etc/ssh/sshd_config
colima ssh -p ${COLIMA_PROFILE} -- sudo sed -i 's/#PubkeyAuthentication yes/PubkeyAuthentication yes/' /etc/ssh/sshd_config
colima ssh -p ${COLIMA_PROFILE} -- sudo systemctl restart ssh 2>/dev/null || colima ssh -p ${COLIMA_PROFILE} -- sudo systemctl restart sshd

# Get the actual SSH port from colima
SSH_PORT=$(colima ssh-config -p ${COLIMA_PROFILE} | grep "Port" | awk '{print $2}')
SSH_HOST="127.0.0.1"

# Create SSH config entry for easier connection
echo "Creating SSH config entry..."
SSH_CONFIG_ENTRY="Host exe-ctr-colima
    HostName ${SSH_HOST}
    Port ${SSH_PORT}
    User ubuntu
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    IdentityFile ${SSH_KEY_PRIVATE}"

# Check if config already exists
if ! grep -q "Host exe-ctr-colima" ~/.ssh/config 2>/dev/null; then
    echo "" >> ~/.ssh/config
    echo "# Added by setup-colima-host.sh" >> ~/.ssh/config
    echo "$SSH_CONFIG_ENTRY" >> ~/.ssh/config
    echo "✓ Added exe-ctr-colima to ~/.ssh/config"
else
    echo "✓ SSH config entry already exists"
fi

# Test SSH connection
echo "Testing SSH connection to exe-ctr-colima..."
if ssh -o ConnectTimeout=5 exe-ctr-colima "echo 'SSH connection successful'" 2>/dev/null; then
    echo "✓ SSH connection to ubuntu user established"
else
    echo "Warning: Could not establish direct SSH connection to ubuntu user"
    echo "You may need to use: colima ssh -p ${COLIMA_PROFILE}"
fi

echo ""
echo "=========================================="
echo "Setup complete!"
echo "=========================================="
echo ""
echo "Colima VM '${COLIMA_PROFILE}' is now configured with:"
echo "  - Containerd"
echo "  - Nydus snapshotter"
echo "  - XFS data volume at /data"
echo "  - Ubuntu user with SSH access"
echo ""
echo "⚠️  IMPORTANT: Kata Containers limitation on macOS"
echo "  Kata requires KVM virtualization which is not available in QEMU on macOS."
echo "  This means containers won't run in secure Kata VMs."
echo ""
echo "Connection details:"
echo "  CTR_HOST=ssh://exe-ctr-colima"
echo ""
echo "To use with exed (development mode - NOT SECURE):"
echo "  export SKIP_KATA_CHECK=true  # Bypass Kata requirement for development"
echo "  export CTR_USE_SUDO=true"
echo "  export CTR_HOST=ssh://exe-ctr-colima"
echo "  go run ./cmd/exed -dev=local"
echo ""
echo "For production-like testing with Kata:"
echo "  Use a real Linux machine or AWS EC2 instance where KVM is available"
echo ""
echo "To access the VM directly:"
echo "  ssh exe-ctr-colima"
echo "  # or via colima: colima ssh -p ${COLIMA_PROFILE}"
echo ""
echo "To test containerd in the VM:"
echo "  ssh exe-ctr-colima 'sudo ctr -n exe images pull docker.io/library/alpine:latest'"
echo "  ssh exe-ctr-colima 'sudo nerdctl -n exe run --rm alpine:latest echo \"Hello from Colima\"'"
echo ""
echo "To stop the VM:"
echo "  colima stop -p ${COLIMA_PROFILE}"
echo ""
echo "To restart the VM:"
echo "  colima start -p ${COLIMA_PROFILE}"
echo ""
echo "To delete the VM:"
echo "  colima delete -p ${COLIMA_PROFILE}"
echo "=========================================="