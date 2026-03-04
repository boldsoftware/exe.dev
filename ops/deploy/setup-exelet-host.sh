#!/bin/bash
set -euo pipefail

# Check for machine name parameter
if [ $# -ne 1 ]; then
    echo "Usage: $0 <machine-name>"
    echo "Machine name must be in format: exe-ctr-NN or exe-ctr-staging-NN (where NN is a number)"
    exit 1
fi

MACHINE_NAME="$1"

# Validate machine name format
if [ "${SKIP_NAME_CHECK:-}" = "1" ]; then
    echo "Warning: SKIP_NAME_CHECK is set, bypassing machine name validation"
else
    if ! [[ "$MACHINE_NAME" =~ ^exe-ctr-(staging-)?[0-9]+$ ]]; then
        echo "Error: Machine name must be in format exe-ctr-NN or exe-ctr-staging-NN (e.g., exe-ctr-01, exe-ctr-staging-01)"
        echo "Set SKIP_NAME_CHECK=1 to bypass this check"
        exit 1
    fi
fi

# Determine stage based on machine name
if [[ "$MACHINE_NAME" =~ ^exe-ctr-staging- ]] || [ "${SKIP_NAME_CHECK:-}" = "1" ]; then
    STAGE="staging"
else
    STAGE="production"
fi
ROLE="exelet"
echo "Machine role: ${ROLE}, stage: ${STAGE}"

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Run the Tailscale OAuth preflight check
"${SCRIPT_DIR}/test-tailscale-oauth.sh"

# Configuration
CLOUD_HYPERVISOR_VERSION="48.0"
VIRTIOFSD_VERSION="1.13.2"

REGION="${REGION:-us-west-2}"
AZ="${ZONE:-us-west-2b}"
INSTANCE_TYPE="${INSTANCE_TYPE:-m5d.metal}"
ROOT_VOLUME_SIZE="50"
BACKUP_VOLUME_SIZE="500"
SECURITY_GROUP_NAME="exe-ctr-sg"
INSTANCE_ROLE_NAME="exe-ctr-instance-role"
INSTANCE_PROFILE_NAME="exe-ctr-instance-profile"

# Look up a private subnet in the requested AZ (or create one)
if [ -n "${SUBNET_ID:-}" ]; then
    echo "Using provided SUBNET_ID: ${SUBNET_ID}"
else
    echo "Looking up private subnet in ${AZ}..."
    SUBNET_ID=$(aws ec2 describe-subnets \
        --filters "Name=availability-zone,Values=${AZ}" "Name=tag:Name,Values=*private*" \
        --query 'Subnets[0].SubnetId' \
        --output text \
        --region ${REGION})
    if [ -z "$SUBNET_ID" ] || [ "$SUBNET_ID" = "None" ]; then
        echo "No private subnet found in ${AZ}, creating one..."

        # Find the VPC by looking at an existing exe-ctr subnet in another AZ
        VPC_ID=$(aws ec2 describe-subnets \
            --filters "Name=tag:Name,Values=*private*" \
            --query 'Subnets[0].VpcId' \
            --output text \
            --region ${REGION})
        if [ -z "$VPC_ID" ] || [ "$VPC_ID" = "None" ]; then
            echo "Error: Could not find VPC from existing subnets. Set SUBNET_ID explicitly."
            exit 1
        fi
        echo "Found VPC: ${VPC_ID}"

        # Find existing subnet CIDRs to pick the next /20 block
        VPC_CIDR=$(aws ec2 describe-vpcs \
            --vpc-ids ${VPC_ID} \
            --query 'Vpcs[0].CidrBlock' \
            --output text \
            --region ${REGION})
        echo "VPC CIDR: ${VPC_CIDR}"

        EXISTING_CIDRS=$(aws ec2 describe-subnets \
            --filters "Name=vpc-id,Values=${VPC_ID}" \
            --query 'Subnets[].CidrBlock' \
            --output text \
            --region ${REGION})

        # Pick the next available /20 block within the VPC CIDR
        # VPC is typically 172.31.0.0/16, subnets are /20 (172.31.0.0/20, 172.31.16.0/20, ...)
        VPC_PREFIX=$(echo "${VPC_CIDR}" | cut -d. -f1-2)  # e.g. "172.31"
        for THIRD_OCTET in $(seq 0 16 240); do
            CANDIDATE="${VPC_PREFIX}.${THIRD_OCTET}.0/20"
            if ! echo "$EXISTING_CIDRS" | grep -q "$CANDIDATE"; then
                NEW_CIDR="$CANDIDATE"
                break
            fi
        done

        if [ -z "${NEW_CIDR:-}" ]; then
            echo "Error: Could not find an available /20 CIDR block in ${VPC_CIDR}"
            exit 1
        fi

        # Find the NAT route table before confirming so we can show it
        NAT_RT=$(aws ec2 describe-route-tables \
            --filters "Name=vpc-id,Values=${VPC_ID}" "Name=route.nat-gateway-id,Values=*" \
            --query 'RouteTables[0].RouteTableId' \
            --output text \
            --region ${REGION})

        echo ""
        echo "Will create a new private subnet:"
        echo "  VPC:         ${VPC_ID}"
        echo "  AZ:          ${AZ}"
        echo "  CIDR:        ${NEW_CIDR}"
        echo "  Name tag:    exe-ctr-private-${AZ}"
        if [ -n "$NAT_RT" ] && [ "$NAT_RT" != "None" ]; then
            echo "  Route table: ${NAT_RT} (NAT gateway)"
        else
            echo "  Route table: (none with NAT found — subnet may lack internet access)"
        fi
        echo ""
        read -r -p "Proceed? [y/N] " CONFIRM
        if [[ ! "$CONFIRM" =~ ^[Yy]$ ]]; then
            echo "Aborted."
            exit 1
        fi

        SUBNET_ID=$(aws ec2 create-subnet \
            --vpc-id ${VPC_ID} \
            --cidr-block ${NEW_CIDR} \
            --availability-zone ${AZ} \
            --query 'Subnet.SubnetId' \
            --output text \
            --region ${REGION})
        echo "Created subnet ${SUBNET_ID}"

        aws ec2 create-tags \
            --resources ${SUBNET_ID} \
            --tags Key=Name,Value="exe-ctr-private-${AZ}" \
            --region ${REGION}

        # Associate with the NAT route table found earlier
        if [ -n "$NAT_RT" ] && [ "$NAT_RT" != "None" ]; then
            aws ec2 associate-route-table \
                --subnet-id ${SUBNET_ID} \
                --route-table-id ${NAT_RT} \
                --region ${REGION} >/dev/null
            echo "Associated subnet with NAT route table ${NAT_RT}"
        else
            echo "Warning: No route table with NAT gateway found. Subnet may not have internet access."
        fi
    fi
    echo "Using subnet ${SUBNET_ID} in ${AZ}"
fi

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
        --description "Security group for exe hosts" \
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
#   package isal installs igzip which is supposed to speed up image
#   decompression
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
  - pv
  - docker.io
  - atop
  - btop
  - htop
  - isal

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

    # add ubuntu user to docker group
    usermod -aG docker ubuntu
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
    "DeviceName=/dev/xvdf,Ebs={VolumeSize=${BACKUP_VOLUME_SIZE},VolumeType=io2,Iops=12000,DeleteOnTermination=true}" \
    "DeviceName=/dev/xvdg,Ebs={VolumeSize=${BACKUP_VOLUME_SIZE},VolumeType=io2,Iops=12000,DeleteOnTermination=true}" \
    --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${MACHINE_NAME}},{Key=role,Value=${ROLE}},{Key=stage,Value=${STAGE}}]" \
    --query 'Instances[0].InstanceId' \
    --output text \
    --region ${REGION})

echo "Instance ${INSTANCE_ID} created"

# Tag EBS volumes attached to the instance
echo "Tagging EBS volumes..."
ROOT_VOLUME_ID=$(aws ec2 describe-instances \
    --instance-ids ${INSTANCE_ID} \
    --query 'Reservations[0].Instances[0].BlockDeviceMappings[?DeviceName==`/dev/sda1`].Ebs.VolumeId' \
    --output text \
    --region ${REGION})
BACKUP_VOLUME_1_ID=$(aws ec2 describe-instances \
    --instance-ids ${INSTANCE_ID} \
    --query 'Reservations[0].Instances[0].BlockDeviceMappings[?DeviceName==`/dev/xvdf`].Ebs.VolumeId' \
    --output text \
    --region ${REGION})
BACKUP_VOLUME_2_ID=$(aws ec2 describe-instances \
    --instance-ids ${INSTANCE_ID} \
    --query 'Reservations[0].Instances[0].BlockDeviceMappings[?DeviceName==`/dev/xvdg`].Ebs.VolumeId' \
    --output text \
    --region ${REGION})

if [ -n "$ROOT_VOLUME_ID" ] && [ "$ROOT_VOLUME_ID" != "None" ]; then
    aws ec2 create-tags --resources ${ROOT_VOLUME_ID} --tags Key=Name,Value=${MACHINE_NAME}-root Key=role,Value=${ROLE} Key=stage,Value=${STAGE} --region ${REGION}
    echo "Tagged root volume ${ROOT_VOLUME_ID} as ${MACHINE_NAME}-root (role=${ROLE}, stage=${STAGE})"
fi
for BVOL_ID in "$BACKUP_VOLUME_1_ID" "$BACKUP_VOLUME_2_ID"; do
    if [ -n "$BVOL_ID" ] && [ "$BVOL_ID" != "None" ]; then
        aws ec2 create-tags --resources ${BVOL_ID} --tags Key=Name,Value=${MACHINE_NAME}-backup Key=role,Value=${ROLE} Key=stage,Value=${STAGE} Key=exe-volume-type,Value=exe-ctr-backup --region ${REGION}
        echo "Tagged backup volume ${BVOL_ID} as ${MACHINE_NAME}-backup (role=${ROLE}, stage=${STAGE})"
    fi
done

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
echo "Setting up volumes (swap, zpool)"
echo "=========================================="

# Create a script to setup the volumes on the remote machine
cat <<'VOLUME_SETUP_SCRIPT' >/tmp/setup-volumes.sh
#!/bin/bash
set -euo pipefail

echo "=== Setting up volumes on metal instance ==="

# First check if this is a metal instance (has NVMe drives)
if [ ! -e /dev/nvme0n1 ]; then
	echo "Non-metal instance detected"
	sudo mkdir -p /data
	exit 0
fi

# Install required packages
echo "Installing required packages..."
sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -qq -y parted socat zfsutils-linux >/dev/null 2>&1

# Resolve a /dev/ path to its /dev/disk/by-id/nvme-* symlink for stable device naming
resolve_by_id() {
  local dev="$1"
  local real
  real=$(readlink -f "$dev")
  for link in /dev/disk/by-id/nvme-*; do
    [ -L "$link" ] || continue
    if [ "$(readlink -f "$link")" = "$real" ]; then
      echo "$link"
      return 0
    fi
  done
  echo "WARNING: no /dev/disk/by-id link found for $dev, using raw path" >&2
  echo "$dev"
}

# Detect NVMe devices by model string
echo "=== Detecting NVMe devices ==="
INSTANCE_STORE_DEVICES=()
EBS_DATA_DEVICES=()

for dev in /dev/nvme*n1; do
  [ -b "$dev" ] || continue
  devname=$(basename "$dev")
  model=$(cat "/sys/block/${devname}/device/model" 2>/dev/null | xargs)
  size_gb=$(lsblk -b -n -d -o SIZE "$dev" 2>/dev/null | awk '{printf "%.0f", $1/1073741824}')

  # Safety: never touch a device that has mounted filesystems
  if lsblk -n -o MOUNTPOINT "$dev" 2>/dev/null | grep -q '/'; then
    echo "Mounted: $dev (${size_gb}GB) - skipping"
    continue
  fi

  if [ "$model" = "Amazon EC2 NVMe Instance Storage" ]; then
    echo "Instance-store: $dev (${size_gb}GB)"
    INSTANCE_STORE_DEVICES+=("$dev")
  elif [ "$model" = "Amazon Elastic Block Store" ]; then
    echo "EBS data: $dev (${size_gb}GB)"
    EBS_DATA_DEVICES+=("$dev")
  else
    echo "Unknown NVMe: $dev (model: $model, ${size_gb}GB) - skipping"
  fi
done

if [ ${#INSTANCE_STORE_DEVICES[@]} -eq 0 ]; then
  echo "ERROR: No instance-store NVMe devices found"
  lsblk
  exit 1
fi

echo ""
echo "Found ${#INSTANCE_STORE_DEVICES[@]} instance-store device(s)"
echo "Found ${#EBS_DATA_DEVICES[@]} EBS data device(s)"

# Partition each instance-store drive: 25% swap, 75% data
echo ""
echo "=== Partitioning instance-store NVMe devices (25% swap, 75% data) ==="
SWAP_PARTS=()
DATA_PARTS=()

for dev in "${INSTANCE_STORE_DEVICES[@]}"; do
  size_bytes=$(lsblk -b -n -d -o SIZE "$dev")
  swap_gib=$((size_bytes / 4 / 1024 / 1024 / 1024))

  echo "Partitioning ${dev}: ${swap_gib}GiB swap, remainder for ZFS..."
  sudo wipefs -a "$dev" >/dev/null 2>&1 || true
  sudo parted -s "$dev" mklabel gpt
  sudo parted -s "$dev" mkpart primary linux-swap 1MiB "${swap_gib}GiB"
  sudo parted -s "$dev" mkpart primary "${swap_gib}GiB" 100%
  sudo udevadm settle || sleep 1

  sudo mkswap "${dev}p1"
  SWAP_PARTS+=("${dev}p1")
  DATA_PARTS+=("${dev}p2")
done

# Resolve data partitions to /dev/disk/by-id paths for stable zpool device names
echo ""
echo "=== Resolving device paths to /dev/disk/by-id ==="
RESOLVED_PARTS=()
for part in "${DATA_PARTS[@]}"; do
  resolved=$(resolve_by_id "$part")
  echo "  $part -> $resolved"
  RESOLVED_PARTS+=("$resolved")
done
DATA_PARTS=("${RESOLVED_PARTS[@]}")

# Enable swap with equal priority for I/O interleaving
echo ""
echo "=== Enabling swap ==="
for part in "${SWAP_PARTS[@]}"; do
  sudo swapon -p 1 "$part"
  echo "$part none swap sw,pri=1 0 0" | sudo tee -a /etc/fstab >/dev/null
done
echo "Swap enabled on ${#SWAP_PARTS[@]} partition(s)"

# Create ZFS pool 'tank' from instance-store data partitions
echo ""
echo "=== Setting up ZFS pool 'tank' ==="
NDISKS=${#DATA_PARTS[@]}

if [ "$NDISKS" -eq 1 ]; then
  echo "Single drive, creating tank with no redundancy"
  sudo zpool create -o ashift=12 -m none tank "${DATA_PARTS[0]}"
elif [ "$NDISKS" -eq 2 ]; then
  echo "Two drives, creating tank as mirror"
  sudo zpool create -o ashift=12 -m none tank mirror "${DATA_PARTS[@]}"
else
  # Build mirrored vdevs (pairs of 2 drives each)
  if [ $((NDISKS % 2)) -ne 0 ]; then
    echo "ERROR: odd number of drives ($NDISKS), cannot create mirrored vdevs"
    exit 1
  fi

  ZPOOL_ARGS=()
  for ((i = 0; i < NDISKS; i += 2)); do
    ZPOOL_ARGS+=("mirror" "${DATA_PARTS[$i]}" "${DATA_PARTS[$((i + 1))]}")
  done

  echo "Creating tank: ${ZPOOL_ARGS[*]}"
  sudo zpool create -o ashift=12 -m none tank "${ZPOOL_ARGS[@]}"
fi

# Configure ZFS properties on tank
sudo zfs set compression=lz4 tank
sudo zfs set atime=off tank
sudo zfs set xattr=sa tank

# Create /data dataset
sudo zfs create -o mountpoint=/data tank/data
sudo mkdir -p /data/exelet

echo "ZFS pool 'tank' ready:"
zpool status tank

# Resolve EBS devices to /dev/disk/by-id paths
RESOLVED_EBS=()
for dev in "${EBS_DATA_DEVICES[@]}"; do
  resolved=$(resolve_by_id "$dev")
  echo "  $dev -> $resolved"
  RESOLVED_EBS+=("$resolved")
done
EBS_DATA_DEVICES=("${RESOLVED_EBS[@]}")

# Create backup pool from EBS volumes (striped for throughput)
echo ""
echo "=== Setting up ZFS backup pool ==="
if [ ${#EBS_DATA_DEVICES[@]} -ge 2 ]; then
  echo "Creating backup pool (striped) from ${#EBS_DATA_DEVICES[@]} EBS volumes: ${EBS_DATA_DEVICES[*]}"
  sudo zpool create -o ashift=12 -m none backup "${EBS_DATA_DEVICES[@]}"
  sudo zfs set compression=lz4 backup
  sudo zfs set atime=off backup
  echo "Backup pool ready:"
  zpool status backup
elif [ ${#EBS_DATA_DEVICES[@]} -eq 1 ]; then
  echo "Creating backup pool from single EBS volume: ${EBS_DATA_DEVICES[0]}"
  sudo zpool create -o ashift=12 -m none backup "${EBS_DATA_DEVICES[0]}"
  sudo zfs set compression=lz4 backup
  sudo zfs set atime=off backup
  echo "Backup pool ready:"
  zpool status backup
else
  echo "No EBS data volumes found, skipping backup pool"
fi

# Configure ZFS ARC (min 16GB, max 64GB)
echo ""
echo "Configuring ZFS ARC limits..."
cat <<EOF | sudo tee /etc/modprobe.d/zfs.conf >/dev/null
options zfs zfs_arc_min=17179869184
options zfs zfs_arc_max=68719476736
EOF
sudo update-initramfs -u

echo ""
echo "=== Volume setup complete ==="
swapon --show
VOLUME_SETUP_SCRIPT

# Copy and execute the volume setup script
echo "Setting up volumes (swap, zpool) on ${MACHINE_NAME}..."
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
# Build cloud-hypervisor artifacts on remote
###############################################

# Copy Dockerfile and setup script to the remote host
echo "Copying build context and setup scripts to ${MACHINE_NAME}..."
if ! scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    "${SCRIPT_DIR}/setup-cloud-hypervisor.sh" \
    "${SCRIPT_DIR}/cloud-hypervisor/Dockerfile" \
    "ubuntu@${MACHINE_NAME}:~/"; then
    echo "ERROR: Failed to copy scripts to remote"
    exit 1
fi

# Build artifacts on remote using Docker
echo "Building Cloud Hypervisor artifacts on ${MACHINE_NAME}..."
REMOTE_BUILD_CMD="set -euo pipefail
CLOUD_HYPERVISOR_VERSION=${CLOUD_HYPERVISOR_VERSION}
VIRTIOFSD_VERSION=${VIRTIOFSD_VERSION}
CACHE_DIR=\"\$HOME/.cache/exedops\"
ARTIFACT_NAME=\"cloud-hypervisor-\${CLOUD_HYPERVISOR_VERSION}-amd64.tar.gz\"

mkdir -p \"\$CACHE_DIR\"
mkdir -p \"\$HOME/cloud-hypervisor-build\"
mv \"\$HOME/Dockerfile\" \"\$HOME/cloud-hypervisor-build/\"

# Check if artifact already exists
if [ -f \"\$CACHE_DIR/\$ARTIFACT_NAME\" ]; then
    echo \"Cloud Hypervisor \${CLOUD_HYPERVISOR_VERSION} (amd64) cache hit\"
else
    echo \"Building Cloud Hypervisor \${CLOUD_HYPERVISOR_VERSION} (amd64) via Docker...\"

    IMAGE_TAG=\"exe-cloud-hypervisor:\${CLOUD_HYPERVISOR_VERSION}-amd64\"

    docker build \\
        --tag \"\$IMAGE_TAG\" \\
        --build-arg \"CLOUD_HYPERVISOR_VERSION=\${CLOUD_HYPERVISOR_VERSION}\" \\
        --build-arg \"VIRTIOFSD_VERSION=\${VIRTIOFSD_VERSION}\" \\
        --build-arg \"TARGETARCH=amd64\" \\
        \"\$HOME/cloud-hypervisor-build\"

    CONTAINER_ID=\$(docker create \"\$IMAGE_TAG\" /bin/true)
    TMP_DIR=\$(mktemp -d)

    docker cp \"\$CONTAINER_ID:/out/.\" \"\$TMP_DIR\"
    docker rm \"\$CONTAINER_ID\" >/dev/null 2>&1 || true

    tar czf \"\$CACHE_DIR/\$ARTIFACT_NAME\" -C \"\$TMP_DIR\" .
    rm -rf \"\$TMP_DIR\"

    echo \"Cached Cloud Hypervisor \${CLOUD_HYPERVISOR_VERSION} (amd64) at \$CACHE_DIR/\$ARTIFACT_NAME\"
fi

rm -rf \"\$HOME/cloud-hypervisor-build\"

# Place the setup script for execution
sudo mv \"\$HOME/setup-cloud-hypervisor.sh\" /root/setup-cloud-hypervisor.sh
sudo chmod +x /root/setup-cloud-hypervisor.sh

echo \"Artifacts ready in \$CACHE_DIR\"
ls -la \"\$CACHE_DIR\"
"

if ! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "ubuntu@${MACHINE_NAME}" "$REMOTE_BUILD_CMD"; then
    echo "ERROR: Remote build of artifacts failed"
    exit 1
fi

echo ""
echo "=========================================="
echo "Starting cloud-hypervisor setup via SSH"
echo "=========================================="

# Execute the exelet setup script from /root
echo "Executing exelet setup script on ${MACHINE_NAME}..."
if ! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    "ubuntu@${MACHINE_NAME}" \
    'sudo /root/setup-cloud-hypervisor.sh'; then
    echo "ERROR: Setup script failed"
    exit 1
fi

# sysctl
cat <<'EXELET_SYSCTL' >/tmp/sysctl.sh
#!/bin/bash
set -euo pipefail
echo "Setting sysctl"
cat <<EOF >/etc/sysctl.d/90-exe.conf
net.ipv4.neigh.default.gc_thresh1=4096
net.ipv4.neigh.default.gc_thresh2=8192
net.ipv4.neigh.default.gc_thresh3=16384
vm.max_map_count=1048576
EOF
sysctl --system >/dev/null
EXELET_SYSCTL

if ! scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    /tmp/sysctl.sh \
    "ubuntu@${MACHINE_NAME}:~/"; then
    echo "ERROR: Failed to copy sysctl setup script"
    rm -f /tmp/sysctl.sh
    exit 1
fi

# Execute the sysctl script
echo "Executing sysctl script on ${MACHINE_NAME}..."
if ! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    "ubuntu@${MACHINE_NAME}" \
    'chmod +x ~/sysctl.sh && sudo ~/sysctl.sh'; then
    echo "ERROR: Setup script failed"
    exit 1
fi

# needrestart: auto-restart services without prompting
cat <<'EXELET_NEEDRESTART' >/tmp/needrestart.sh
#!/bin/bash
set -euo pipefail
echo "Configuring needrestart"
mkdir -p /etc/needrestart/conf.d
cat <<'EOF' >/etc/needrestart/conf.d/exe.conf
$nrconf{restart} = 'l';
EOF
EXELET_NEEDRESTART

if ! scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    /tmp/needrestart.sh \
    "ubuntu@${MACHINE_NAME}:~/"; then
    echo "ERROR: Failed to copy needrestart setup script"
    rm -f /tmp/needrestart.sh
    exit 1
fi

echo "Executing needrestart script on ${MACHINE_NAME}..."
if ! ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    "ubuntu@${MACHINE_NAME}" \
    'chmod +x ~/needrestart.sh && sudo ~/needrestart.sh'; then
    echo "ERROR: needrestart setup script failed"
    exit 1
fi

# Install and configure node_exporter for monitoring
echo ""
echo "=========================================="
echo "Installing node_exporter for monitoring"
echo "=========================================="

ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    "ubuntu@${MACHINE_NAME}" 'bash -s' <<'NODE_EXPORTER_SCRIPT'
set -euo pipefail
if ! dpkg -l | grep -q prometheus-node-exporter; then
    echo "Installing prometheus-node-exporter..."
    sudo apt-get update && sudo apt-get install -y prometheus-node-exporter
else
    echo "prometheus-node-exporter already installed"
fi

# Create wrapper script that dynamically gets Tailscale IP at start time
cat <<'WRAPPER' | sudo tee /usr/local/bin/node-exporter-wrapper > /dev/null
#!/bin/bash
TAILSCALE_IP=$(tailscale ip -4)
if [ -z "$TAILSCALE_IP" ]; then
    echo "ERROR: Failed to get Tailscale IP" >&2
    exit 1
fi
exec /usr/bin/prometheus-node-exporter --web.listen-address=${TAILSCALE_IP}:9100 --collector.cgroups --collector.systemd --collector.netdev.device-exclude='^(tap|ifb)' --collector.diskstats.device-exclude='^zd' "$@"
WRAPPER
sudo chmod +x /usr/local/bin/node-exporter-wrapper

sudo mkdir -p /etc/systemd/system/prometheus-node-exporter.service.d
cat <<EOF | sudo tee /etc/systemd/system/prometheus-node-exporter.service.d/override.conf > /dev/null
[Unit]
After=tailscaled.service
Wants=tailscaled.service

[Service]
ExecStart=
ExecStart=/usr/local/bin/node-exporter-wrapper
EOF
sudo systemctl daemon-reload
sudo systemctl enable prometheus-node-exporter
sudo systemctl restart prometheus-node-exporter

TAILSCALE_IP=$(tailscale ip -4)
echo "node_exporter should be listening on Tailscale IP: $TAILSCALE_IP"
echo "Verifying node-exporter is running..."
for i in $(seq 1 300); do
    if curl -sf -o /dev/null --max-time 2 http://${TAILSCALE_IP}:9100/metrics; then
        echo "node-exporter is responding on ${TAILSCALE_IP}:9100"
        break
    fi
    if [ $i -eq 300 ]; then
        echo "ERROR: node-exporter failed to start after 30 seconds"
        exit 1
    fi
    sleep 0.1
done
NODE_EXPORTER_SCRIPT

echo ""
echo "=========================================="
echo "Setup complete!"
echo "=========================================="
echo ""
echo "The machine is ready to deploy the exelet."
echo ""
echo "${MACHINE_NAME} is now fully configured with:"
echo "  - Cloud Hypervisor"
echo "  - Swap on 25% of each instance-store NVMe drive"
echo "  - ZFS pool 'tank' (raidz1) on 75% of instance-store NVMe drives"
echo "  - ZFS pool 'backup' (striped) on 2x EBS io2 volumes"
echo "  - ZFS ARC limits set to 16GB min / 64GB max (requires reboot)"
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
