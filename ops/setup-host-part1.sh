#!/bin/bash

set -euo pipefail

# Check for machine name parameter
if [ $# -ne 1 ]; then
	echo "Usage: $0 <machine-name>"
	echo "Machine name must be in format: exe-ctr-NN (where NN is a number)"
	exit 1
fi

MACHINE_NAME="$1"

# Validate machine name format
if ! [[ "$MACHINE_NAME" =~ ^exe-ctr-[0-9]+$ ]]; then
	echo "Error: Machine name must be in format exe-ctr-NN (e.g., exe-ctr-01)"
	exit 1
fi

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Run the Tailscale OAuth preflight check
"${SCRIPT_DIR}/test-tailscale-oauth.sh"

# Configuration
REGION="us-west-2"
AZ="us-west-2b"
INSTANCE_TYPE="m5d.metal"
ROOT_VOLUME_SIZE="50"
DATA_VOLUME_SIZE="250"
SECURITY_GROUP_NAME="exe-ctr-sg"
INSTANCE_ROLE_NAME="exe-ctr-instance-role"
INSTANCE_PROFILE_NAME="exe-ctr-instance-profile"
# Use the private subnet with NAT Gateway
SUBNET_ID="subnet-0c7d538b08cd1cecd"

# Check if machine name already exists in AWS
echo "Checking if machine name ${MACHINE_NAME} is available..."
EXISTING_INSTANCE=$(aws ec2 describe-instances \
	--filters "Name=tag:Name,Values=${MACHINE_NAME}" \
	"Name=instance-state-name,Values=pending,running,stopping,stopped" \
	--query 'Reservations[].Instances[].InstanceId' \
	--output text \
	--region ${REGION})

if [ -n "$EXISTING_INSTANCE" ] && [ "$EXISTING_INSTANCE" != "None" ]; then
	echo "Error: Machine name ${MACHINE_NAME} is already taken by instance ${EXISTING_INSTANCE}"
	exit 1
fi

echo "Machine name ${MACHINE_NAME} is available"

# Check if security group exists
echo "Checking security group..."
SG_ID=$(aws ec2 describe-security-groups \
	--filters "Name=group-name,Values=${SECURITY_GROUP_NAME}" \
	--query 'SecurityGroups[0].GroupId' \
	--output text \
	--region ${REGION} 2>/dev/null || true)

if [ -z "$SG_ID" ] || [ "$SG_ID" = "None" ]; then
	echo "Creating security group ${SECURITY_GROUP_NAME}..."
	SG_ID=$(aws ec2 create-security-group \
		--group-name ${SECURITY_GROUP_NAME} \
		--description "Security group for exe containerd hosts" \
		--vpc-id $(aws ec2 describe-subnets --subnet-ids ${SUBNET_ID} --query 'Subnets[0].VpcId' --output text --region ${REGION}) \
		--query 'GroupId' \
		--output text \
		--region ${REGION})

	# Add rules
	# Allow SSH from anywhere (for Tailscale and initial setup)
	aws ec2 authorize-security-group-ingress \
		--group-id ${SG_ID} \
		--protocol tcp \
		--port 22 \
		--cidr 0.0.0.0/0 \
		--region ${REGION}

	# Allow HTTPS from anywhere
	aws ec2 authorize-security-group-ingress \
		--group-id ${SG_ID} \
		--protocol tcp \
		--port 443 \
		--cidr 0.0.0.0/0 \
		--region ${REGION}

	# Allow all traffic from within VPC (for internal communication including ping)
	aws ec2 authorize-security-group-ingress \
		--group-id ${SG_ID} \
		--protocol -1 \
		--cidr 172.31.0.0/16 \
		--region ${REGION}
fi

echo "Security group ID: ${SG_ID}"

# Check if IAM role exists
echo "Checking IAM role..."
if ! aws iam get-role --role-name ${INSTANCE_ROLE_NAME} >/dev/null 2>&1; then
	echo "Creating IAM role ${INSTANCE_ROLE_NAME}..."
	aws iam create-role \
		--role-name ${INSTANCE_ROLE_NAME} \
		--assume-role-policy-document '{
			"Version": "2012-10-17",
			"Statement": [
				{
					"Effect": "Allow",
					"Principal": {"Service": "ec2.amazonaws.com"},
					"Action": "sts:AssumeRole"
				}
			]
		}'

	# Create instance profile
	aws iam create-instance-profile --instance-profile-name ${INSTANCE_PROFILE_NAME}
	aws iam add-role-to-instance-profile \
		--instance-profile-name ${INSTANCE_PROFILE_NAME} \
		--role-name ${INSTANCE_ROLE_NAME}

	# Wait for profile to be ready
	sleep 10
fi

# Get latest Ubuntu 24.04 AMI
echo "Finding latest Ubuntu 24.04 AMI..."
AMI_ID=$(aws ec2 describe-images \
	--owners 099720109477 \
	--filters \
	"Name=name,Values=ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*" \
	"Name=architecture,Values=x86_64" \
	"Name=virtualization-type,Values=hvm" \
	"Name=state,Values=available" \
	--query 'Images[0].[ImageId]' \
	--output text \
	--region ${REGION})

echo "Using AMI: ${AMI_ID}"

# Check for Tailscale OAuth credentials in environment variables
if [ -z "$TS_OAUTH_CLIENT_ID" ] || [ -z "$TS_OAUTH_CLIENT_SECRET" ]; then
	echo "ERROR: Tailscale OAuth credentials not set"
	echo "Please set the following environment variables:"
	echo "  export TS_OAUTH_CLIENT_ID=<your-client-id>"
	echo "  export TS_OAUTH_CLIENT_SECRET=<your-client-secret>"
	echo ""
	echo "You can get these credentials from the Tailscale admin console:"
	echo "  https://login.tailscale.com/admin/settings/oauth"
	exit 1
fi

# Create user data script with Tailscale setup
USER_DATA=$(
	cat <<EOF
#cloud-config
users:
  - name: ubuntu
    ssh_authorized_keys:
      - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOEetwKXuTe+byx+VJTOn3ZxjVnpMe/82YroL111tTwK ubuntu@exed-01
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash

hostname: ${MACHINE_NAME}

package_update: true
package_upgrade: false

packages:
  - curl
  - jq

runcmd:
  - echo "Starting Tailscale setup..."
  - curl -fsSL https://tailscale.com/install.sh | sh
  - |
    echo "Generating Tailscale auth key via OAuth..."
    # First get OAuth access token
    echo "Getting OAuth access token..."
    OAUTH_RESPONSE=\$(curl -s -w "\\n%{http_code}" -X POST \\
      "https://api.tailscale.com/api/v2/oauth/token" \\
      -d "client_id=${TS_OAUTH_CLIENT_ID}" \\
      -d "client_secret=${TS_OAUTH_CLIENT_SECRET}" \\
      -d "grant_type=client_credentials")

    OAUTH_HTTP=\$(echo "\$OAUTH_RESPONSE" | tail -n 1)
    OAUTH_BODY=\$(echo "\$OAUTH_RESPONSE" | head -n -1)

    if [ "\$OAUTH_HTTP" != "200" ]; then
        echo "ERROR: Failed to get OAuth token. HTTP code: \$OAUTH_HTTP"
        echo "Response body: \$OAUTH_BODY"
        exit 1
    fi

    ACCESS_TOKEN=\$(echo "\$OAUTH_BODY" | jq -r '.access_token')
    if [ -z "\$ACCESS_TOKEN" ] || [ "\$ACCESS_TOKEN" = "null" ]; then
        echo "ERROR: Failed to extract access token"
        echo "Response body: \$OAUTH_BODY"
        exit 1
    fi
    echo "Got OAuth access token successfully"

    # Now create auth key using Bearer auth
    echo "Creating Tailscale auth key..."
    KEY_RESPONSE=\$(curl -s -w "\\n%{http_code}" -X POST \\
      "https://api.tailscale.com/api/v2/tailnet/-/keys" \\
      -H "Authorization: Bearer \$ACCESS_TOKEN" \\
      -H "Content-Type: application/json" \\
      -d '{
        "capabilities": {
          "devices": {
            "create": {
              "reusable": false,
              "ephemeral": false,
              "tags": ["tag:server"]
            }
          }
        },
        "expirySeconds": 3600
      }')

    KEY_HTTP=\$(echo "\$KEY_RESPONSE" | tail -n 1)
    KEY_BODY=\$(echo "\$KEY_RESPONSE" | head -n -1)

    if [ "\$KEY_HTTP" != "200" ]; then
        echo "ERROR: Failed to create auth key. HTTP code: \$KEY_HTTP"
        echo "Response body: \$KEY_BODY"
        exit 1
    fi

    AUTH_KEY=\$(echo "\$KEY_BODY" | jq -r '.key')
    if [ -z "\$AUTH_KEY" ] || [ "\$AUTH_KEY" = "null" ]; then
        echo "ERROR: Failed to extract auth key from response"
        echo "Response body: \$KEY_BODY"
        exit 1
    fi

    echo "Auth key generated successfully (first 10 chars): \$(echo "\$AUTH_KEY" | cut -c1-10)..."
    echo "Starting Tailscale with hostname: ${MACHINE_NAME}"
    tailscale up --authkey=\$AUTH_KEY --advertise-tags=tag:server --ssh --hostname=${MACHINE_NAME} 2>&1
    echo "Tailscale up command completed with exit code: \$?"
    sleep 5
    tailscale status 2>&1
    echo "Tailscale initialization complete"
EOF
)

# Create the instance
echo "Creating instance ${MACHINE_NAME}..."
INSTANCE_ID=$(aws ec2 run-instances \
	--image-id ${AMI_ID} \
	--instance-type ${INSTANCE_TYPE} \
	--subnet-id ${SUBNET_ID} \
	--security-group-ids ${SG_ID} \
	--iam-instance-profile Name=${INSTANCE_PROFILE_NAME} \
	--user-data "${USER_DATA}" \
	--block-device-mappings \
	"DeviceName=/dev/sda1,Ebs={VolumeSize=${ROOT_VOLUME_SIZE},VolumeType=gp3,DeleteOnTermination=true}" \
	"DeviceName=/dev/xvdf,Ebs={VolumeSize=${DATA_VOLUME_SIZE},VolumeType=gp3,DeleteOnTermination=true}" \
	--tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${MACHINE_NAME}}]" \
	--query 'Instances[0].InstanceId' \
	--output text \
	--region ${REGION})

echo "Instance ${INSTANCE_ID} created"

# Wait for instance to be running
echo "Waiting for instance to start..."
aws ec2 wait instance-running --instance-ids ${INSTANCE_ID} --region ${REGION}

# Get instance IP (private IP since we're using a private subnet)
INSTANCE_IP=$(aws ec2 describe-instances \
	--instance-ids ${INSTANCE_ID} \
	--query 'Reservations[0].Instances[0].PrivateIpAddress' \
	--output text \
	--region ${REGION})

echo "Instance is running at ${INSTANCE_IP} (private IP)"

# Wait for Tailscale to be connected
echo ""
echo "Waiting for Tailscale to connect..."

MAX_WAIT=300 # 5 minutes
WAIT_INTERVAL=10
ELAPSED=0

while [ $ELAPSED -lt $MAX_WAIT ]; do
	# Try to SSH to the machine via Tailscale
	if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@${MACHINE_NAME} true 2>/dev/null; then
		echo "✓ Machine is accessible via Tailscale SSH"
		break
	fi

	echo "  Waiting for ${MACHINE_NAME} to be accessible via Tailscale... ($ELAPSED/$MAX_WAIT seconds)"
	sleep $WAIT_INTERVAL
	ELAPSED=$((ELAPSED + WAIT_INTERVAL))
done

if [ $ELAPSED -ge $MAX_WAIT ]; then
	echo "WARNING: Machine is not accessible via Tailscale after ${MAX_WAIT} seconds"
	echo "You may need to check the Tailscale setup manually"
	echo "To debug, you can SSH via exed-01:"
	echo "  ssh exed-01 'ssh ubuntu@${INSTANCE_IP} sudo tail -100 /var/log/cloud-init-output.log'"
	exit 1
fi

# Setup volumes on metal instances
echo ""
echo "=========================================="
echo "Setting up volumes (swap, /local RAID, /data)"
echo "=========================================="

# Create a script to setup the volumes on the remote machine
cat <<'VOLUME_SETUP_SCRIPT' >/tmp/setup-volumes.sh
#!/bin/bash
set -euo pipefail

echo "=== Setting up volumes on metal instance ==="

# First check if this is a metal instance (has NVMe drives)
if [ ! -e /dev/nvme0n1 ]; then
	echo "Non-metal instance detected, data volume already mounted via xvdf"
	# Just create /local as a directory for non-metal instances
	sudo mkdir -p /local
	exit 0
fi

# Install required packages
echo "Installing required packages..."
sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -qq -y mdadm parted xfsprogs >/dev/null 2>&1

echo "=== Detecting instance-store NVMe devices (~900GB) ==="
# Select the 4x ~900GB instance-store NVMe disks; exclude EBS root (50GB) and data (250GB)
mapfile -t NVME_DEVICES < <(lsblk -b -n -d -o NAME,SIZE,TYPE 2>/dev/null | awk '$3=="disk" && $1 ~ /^nvme/ { if ($2 >= 800*1024*1024*1024) print "/dev/"$1 }' | sort -V)
if [ ${#NVME_DEVICES[@]} -lt 4 ]; then
  echo "ERROR: Expected 4 NVMe instance-store disks (~900GB), found ${#NVME_DEVICES[@]}"
  lsblk
  exit 1
fi
# Use the first 4 detected devices
NVME_DEVICES=("${NVME_DEVICES[@]:0:4}")
printf "Using instance-store devices:\n%s\n" "${NVME_DEVICES[@]}"

echo "=== Setting up swap on instance-store NVMe devices ==="
for dev in "${NVME_DEVICES[@]}"; do
  echo "Setting up 225GB swap on ${dev}..."
  # Ensure the device has no lingering signatures/partitions
  sudo wipefs -a "$dev" >/dev/null 2>&1 || true
  sudo parted -s "$dev" mklabel gpt
  sudo parted -s "$dev" mkpart primary linux-swap 1MiB 226GiB
  # Wait for kernel to create the partition node
  sudo udevadm settle || sleep 1
  sudo mkswap "${dev}p1"
done

# Enable all swaps with equal priority for I/O interleaving
for dev in "${NVME_DEVICES[@]}"; do
  sudo swapon -p 1 "${dev}p1"
done

# Add to fstab with priority
for dev in "${NVME_DEVICES[@]}"; do
  echo "${dev}p1 none swap sw,pri=1 0 0" | sudo tee -a /etc/fstab >/dev/null
done

echo "Swap setup complete (4x 225GB with equal priority, 900GB total)"

# Setup RAID 0 XFS volume for /local (Nydus snapshotting)
echo "=== Setting up RAID 0 XFS volume for /local (Nydus snapshotting) ==="

# Create remaining space partitions on each NVMe drive for /local
for dev in "${NVME_DEVICES[@]}"; do
  echo "Creating RAID partition on ${dev}..."
  sudo parted -s "$dev" mkpart primary 226GiB 100%
  sudo parted -s "$dev" set 2 raid on
done

# Build device list for RAID creation
PARTS=()
for dev in "${NVME_DEVICES[@]}"; do
  PARTS+=("${dev}p2")
done

echo "Creating RAID 0 array with: ${PARTS[*]}"
sudo mdadm --create /dev/md0 --level=0 --raid-devices=4 ${PARTS[*]} --force

# Wait for RAID to be ready
sleep 2

# Create XFS filesystem on the RAID array
sudo mkfs.xfs -f /dev/md0

# Create /local directory
sudo mkdir -p /local

# Mount the RAID array
sudo mount /dev/md0 /local

# Save RAID configuration
sudo mdadm --detail --scan | sudo tee -a /etc/mdadm/mdadm.conf
sudo update-initramfs -u

# Add to fstab
echo "/dev/md0 /local xfs defaults 0 0" | sudo tee -a /etc/fstab

echo "RAID 0 XFS volume mounted at /local (~2.7TB total)"

# Setup /data volume
echo "=== Setting up /data volume ==="

# Find the 250GB NVMe device for data volume
DATA_DEVICE=""
echo "Looking for 250GB NVMe data volume..."
for nvme in /dev/nvme*n1; do
	if [ -b "$nvme" ]; then
		SIZE_HR=$(lsblk -n -d -o SIZE "$nvme" 2>/dev/null | tr -d ' ')
		echo "Checking NVMe device $nvme with size ${SIZE_HR}"

		SIZE_GB=$(lsblk -b -n -d -o SIZE "$nvme" 2>/dev/null | awk '{printf "%.0f", $1/1073741824}')

		if [ -n "$SIZE_GB" ] && [ "$SIZE_GB" -ge 245 ] && [ "$SIZE_GB" -le 255 ]; then
			DATA_DEVICE="$nvme"
			echo "Found data volume at $DATA_DEVICE (${SIZE_GB}GB)"
			break
		fi
	fi
done

if [ -z "$DATA_DEVICE" ]; then
	echo "ERROR: Could not find data volume (250GB NVMe device)"
	echo "Available block devices:"
	lsblk
	exit 1
fi

echo "Using data device: $DATA_DEVICE"
sudo mkfs.xfs -f $DATA_DEVICE
sudo mkdir -p /data
sudo mount -o pquota $DATA_DEVICE /data
echo "$DATA_DEVICE /data xfs defaults,pquota 0 0" | sudo tee -a /etc/fstab
sudo xfs_quota -x -c 'state' /data
echo "Data volume setup complete"
VOLUME_SETUP_SCRIPT

# Copy and execute the volume setup script
echo "Setting up volumes (swap, /local, /data) on ${MACHINE_NAME}..."
if ! scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	/tmp/setup-volumes.sh \
	"ubuntu@${MACHINE_NAME}:~/"; then
	echo "ERROR: Failed to copy volume setup script"
	rm -f /tmp/setup-volumes.sh
	exit 1
fi

if ! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	"ubuntu@${MACHINE_NAME}" \
	'chmod +x ~/setup-volumes.sh && ~/setup-volumes.sh'; then
	echo "ERROR: Volume setup failed"
	rm -f /tmp/setup-volumes.sh
	exit 1
fi

rm -f /tmp/setup-volumes.sh

###############################################
# Download deps on the metal host (not locally)
###############################################

# Copy setup script, config, and downloader to the remote host
echo "Copying setup scripts to ${MACHINE_NAME}..."
if ! scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	"${SCRIPT_DIR}/setup-containerd-clh-nydus.sh" \
	"${SCRIPT_DIR}/kata-config-clh.toml" \
	"${SCRIPT_DIR}/download-ctr-host.sh" \
	"ubuntu@${MACHINE_NAME}:~/"; then
	echo "ERROR: Failed to copy scripts and config to remote"
	exit 1
fi

echo "Running downloads on ${MACHINE_NAME} to cache dependencies..."
REMOTE_DOWNLOAD_CMD='set -euo pipefail
ARCH=amd64
CACHE_DIR="$HOME/.cache/exedops"

# Ensure tools needed by downloader
if ! command -v wget >/dev/null 2>&1; then
  sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -qq -y wget >/dev/null 2>&1
fi

chmod +x "$HOME/download-ctr-host.sh"
set +e
"$HOME/download-ctr-host.sh" "$ARCH"
ret=$?
set -e

# The downloader also tries to prefetch images using docker/crane, which may not be installed on fresh metal.
# Proceed if core dependency tarballs were downloaded; otherwise, fail clearly.
missing=0
deps=(
  "$CACHE_DIR"/containerd-*-linux-"$ARCH".tar.gz
  "$CACHE_DIR"/containerd.service
  "$CACHE_DIR"/runc-*."$ARCH"
  "$CACHE_DIR"/kata-static-*-"$ARCH".tar.xz
  "$CACHE_DIR"/ch-remote-static-*-"$ARCH"
  "$CACHE_DIR"/nydus-snapshotter-v*-linux-"$ARCH".tar.gz
  "$CACHE_DIR"/nydus-static-v*-linux-"$ARCH".tgz
  "$CACHE_DIR"/nerdctl-*-linux-"$ARCH".tar.gz
  "$CACHE_DIR"/cni-plugins-linux-"$ARCH"-v*.tgz
)
for f in "${deps[@]}"; do
  if ! ls $f >/dev/null 2>&1; then
    echo "ERROR: Required dependency missing in cache: pattern $f"
    missing=1
  fi
done

if [ $missing -ne 0 ]; then
  echo "Downloader exit code: $ret"
  echo "Aborting because required files are missing."
  exit 1
fi

echo "All required dependency artifacts are present in $CACHE_DIR"

# Stage config into the cache directory used by the setup script
mkdir -p "$CACHE_DIR"
mv "$HOME"/kata-config-clh.toml "$CACHE_DIR"/kata-config-clh.toml

# Place the setup script for execution
sudo mv "$HOME"/setup-containerd-clh-nydus.sh /root/setup-containerd-clh-nydus.sh
sudo chmod +x /root/setup-containerd-clh-nydus.sh
'

if ! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "ubuntu@${MACHINE_NAME}" "$REMOTE_DOWNLOAD_CMD"; then
	echo "ERROR: Remote download/setup of dependencies failed"
	exit 1
fi

echo ""
echo "=========================================="
echo "Starting part 2 setup via SSH"
echo "=========================================="

# Execute the part 2 script from /root
echo "Executing containerd setup script on ${MACHINE_NAME}..."
if ! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
	"ubuntu@${MACHINE_NAME}" \
	'sudo /root/setup-containerd-clh-nydus.sh'; then
	echo "ERROR: Setup script failed"
	exit 1
fi

echo ""
echo "=========================================="
echo "Setup complete!"
echo "=========================================="
echo "${MACHINE_NAME} is now fully configured with:"
echo "  - Containerd with Kata Containers (Cloud Hypervisor)"
echo "  - Nydus snapshotter"
echo "  - 900GB swap (4x 225GB with equal priority for I/O interleaving)"
echo "  - ~2.7TB /local XFS volume (RAID 0 across 4 disks) for Nydus cache"
echo "  - 250GB /data XFS volume with project quotas"
echo ""
echo "Instance details:"
echo "  Name: ${MACHINE_NAME}"
echo "  ID: ${INSTANCE_ID}"
echo "  Private IP: ${INSTANCE_IP}"
echo "  Type: ${INSTANCE_TYPE}"
echo ""
echo "You can now connect via:"
echo "  ssh ubuntu@${MACHINE_NAME}"
echo "=========================================="
