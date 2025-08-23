#!/bin/bash

set -euo pipefail

# Check for machine name parameter
if [ $# -ne 1 ]; then
	echo "Usage: $0 <machine-name>"
	echo "Machine name must be in format: exe-docker-NN (where NN is a number)"
	exit 1
fi

MACHINE_NAME="$1"

# Validate machine name format
if ! [[ "$MACHINE_NAME" =~ ^exe-docker-[0-9]+$ ]]; then
	echo "Error: Machine name must be in format exe-docker-NN (e.g., exe-docker-01)"
	exit 1
fi

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Run the Tailscale OAuth preflight check
"${SCRIPT_DIR}/test-tailscale-oauth.sh"

# Configuration
REGION="us-west-2"
AZ="us-west-2b"
INSTANCE_TYPE="m7gd.metal"
ROOT_VOLUME_SIZE="50"
DATA_VOLUME_SIZE="250"
SECURITY_GROUP_NAME="exe-docker-sg"
INSTANCE_ROLE_NAME="exe-docker-instance-role"
INSTANCE_PROFILE_NAME="exe-docker-instance-profile"
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

# Hardcoded SSH public key
SSH_PUBLIC_KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOEetwKXuTe+byx+VJTOn3ZxjVnpMe/82YroL111tTwK ubuntu@exed-01"

# Get the latest Ubuntu 24.04 LTS AMI ID for arm64
AMI_ID=$(aws ec2 describe-images \
	--owners 099720109477 \
	--filters "Name=name,Values=ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-*" \
	"Name=state,Values=available" \
	--query 'Images[0].ImageId' \
	--output text \
	--region ${REGION})

if [ "$AMI_ID" == "None" ] || [ -z "$AMI_ID" ]; then
	echo "Error: Could not find Ubuntu 24.04 LTS AMI"
	exit 1
fi

echo "Using AMI: $AMI_ID"

# Get default VPC and subnet for the AZ
VPC_ID=$(aws ec2 describe-vpcs \
	--filters "Name=is-default,Values=true" \
	--query 'Vpcs[0].VpcId' \
	--output text \
	--region ${REGION})

# Subnet is now hardcoded to the private subnet with NAT Gateway
echo "Using VPC: $VPC_ID, Subnet: $SUBNET_ID (private subnet with NAT Gateway)"

# Create or get security group for exe-docker instances
echo "Setting up security group..."
SECURITY_GROUP_ID=$(aws ec2 describe-security-groups \
	--filters "Name=group-name,Values=${SECURITY_GROUP_NAME}" \
	"Name=vpc-id,Values=${VPC_ID}" \
	--query 'SecurityGroups[0].GroupId' \
	--output text \
	--region ${REGION} 2>/dev/null)

if [ -z "$SECURITY_GROUP_ID" ] || [ "$SECURITY_GROUP_ID" == "None" ]; then
	echo "Creating security group ${SECURITY_GROUP_NAME}..."
	SECURITY_GROUP_ID=$(aws ec2 create-security-group \
		--group-name ${SECURITY_GROUP_NAME} \
		--description "Security group for exe-docker instances" \
		--vpc-id ${VPC_ID} \
		--query 'GroupId' \
		--output text \
		--region ${REGION})

	# Get VPC CIDR for SSH rule
	VPC_CIDR=$(aws ec2 describe-vpcs \
		--vpc-ids ${VPC_ID} \
		--query 'Vpcs[0].CidrBlock' \
		--output text \
		--region ${REGION})

	# Allow SSH from within VPC
	echo "Adding SSH rule for VPC CIDR ${VPC_CIDR}..."
	aws ec2 authorize-security-group-ingress \
		--group-id ${SECURITY_GROUP_ID} \
		--protocol tcp \
		--port 22 \
		--cidr ${VPC_CIDR} \
		--region ${REGION}

	echo "Security group created: ${SECURITY_GROUP_ID}"
else
	echo "Using existing security group: ${SECURITY_GROUP_ID}"
fi

# Create or get IAM role and instance profile for SSM access
echo "Setting up IAM role for SSM access..."

# Check if role exists
ROLE_EXISTS=$(aws iam get-role --role-name ${INSTANCE_ROLE_NAME} --query 'Role.RoleName' --output text 2>/dev/null || echo "")

if [ -z "$ROLE_EXISTS" ]; then
	echo "Creating IAM role ${INSTANCE_ROLE_NAME}..."

	# Create the role
	aws iam create-role \
		--role-name ${INSTANCE_ROLE_NAME} \
		--assume-role-policy-document '{
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Effect": "Allow",
                    "Principal": {
                        "Service": "ec2.amazonaws.com"
                    },
                    "Action": "sts:AssumeRole"
                }
            ]
        }' >/dev/null

	# Attach SSM managed policy
	aws iam attach-role-policy \
		--role-name ${INSTANCE_ROLE_NAME} \
		--policy-arn arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore

	# Create instance profile
	aws iam create-instance-profile \
		--instance-profile-name ${INSTANCE_PROFILE_NAME} >/dev/null

	# Add role to instance profile
	aws iam add-role-to-instance-profile \
		--instance-profile-name ${INSTANCE_PROFILE_NAME} \
		--role-name ${INSTANCE_ROLE_NAME}

	echo "IAM role and instance profile created"

	# Wait a bit for IAM propagation
	echo "Waiting for IAM changes to propagate..."
	sleep 10
else
	echo "Using existing IAM role: ${INSTANCE_ROLE_NAME}"
fi

# Create user data script with embedded variables
USER_DATA=$(
	cat <<EOF
#cloud-config
users:
  - name: ubuntu
    ssh_authorized_keys:
      - ${SSH_PUBLIC_KEY}
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash

chpasswd:
  list: |
    ubuntu:${MACHINE_NAME}-console
  expire: False

hostname: ${MACHINE_NAME}

package_update: true
package_upgrade: true

packages:
  - xfsprogs
  - nvme-cli
  - curl
  - jq

runcmd:
  - echo "Starting custom setup..."
  - curl -fsSL https://tailscale.com/install.sh | sh
  - |
    echo "Generating Tailscale auth key via OAuth..."
    # First get OAuth access token
    echo "Getting OAuth access token..."
    OAUTH_RESPONSE=\$(curl -s -w "\\n%{http_code}" -X POST \
      "https://api.tailscale.com/api/v2/oauth/token" \
      -d "client_id=${TS_OAUTH_CLIENT_ID}" \
      -d "client_secret=${TS_OAUTH_CLIENT_SECRET}" \
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
    KEY_RESPONSE=\$(curl -s -w "\\n%{http_code}" -X POST \
      "https://api.tailscale.com/api/v2/tailnet/-/keys" \
      -H "Authorization: Bearer \$ACCESS_TOKEN" \
      -H "Content-Type: application/json" \
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
  - |
    # Setup 500GB swap on each NVMe drive with equal priority for I/O interleaving
    echo "Setting up dual swap partitions on NVMe drives..."
    
    # First NVMe drive
    NVME1="/dev/nvme0n1"
    echo "Setting up 500GB swap on \${NVME1}..."
    parted -s \${NVME1} mklabel gpt
    parted -s \${NVME1} mkpart primary linux-swap 1MiB 501GiB
    mkswap \${NVME1}p1
    
    # Second NVMe drive
    NVME2="/dev/nvme1n1"
    echo "Setting up 500GB swap on \${NVME2}..."
    parted -s \${NVME2} mklabel gpt
    parted -s \${NVME2} mkpart primary linux-swap 1MiB 501GiB
    mkswap \${NVME2}p1
    
    # Enable both swaps with equal priority for I/O interleaving
    swapon -p 1 \${NVME1}p1
    swapon -p 1 \${NVME2}p1
    
    # Add to fstab with priority
    echo "\${NVME1}p1 none swap sw,pri=1 0 0" >> /etc/fstab
    echo "\${NVME2}p1 none swap sw,pri=1 0 0" >> /etc/fstab
    
    echo "Dual swap setup complete (2x 500GB with equal priority)"
  - |
    # Setup data volume
    echo "Setting up data volume..."
    # For metal instances, EBS volumes appear as NVMe devices
    # We need to find the 250GB EBS volume
    DATA_DEVICE=""
    
    # First check if xvdf exists (non-metal instances)
    if [ -e /dev/xvdf ]; then
        DATA_DEVICE="/dev/xvdf"
    else
        # On metal instances, find the 250GB NVMe device
        echo "Looking for 250GB NVMe data volume..."
        # Use awk to avoid bash arithmetic overflow with large devices
        for nvme in /dev/nvme*n1; do
            if [ -b "\$nvme" ]; then
                # Get size in human-readable format (first line only for whole disk)
                SIZE_HR=\$(lsblk -n -d -o SIZE "\$nvme" 2>/dev/null | tr -d ' ')
                echo "Checking NVMe device \$nvme with size \${SIZE_HR}"
                
                # Check if this is the 250GB device (allowing for some variance)
                # Convert to GB for comparison using awk to handle large numbers
                # Use -d flag to get only the disk size, not partitions
                SIZE_GB=\$(lsblk -b -n -d -o SIZE "\$nvme" 2>/dev/null | awk '{printf "%.0f", \$1/1073741824}')
                
                if [ -n "\$SIZE_GB" ] && [ "\$SIZE_GB" -ge 245 ] && [ "\$SIZE_GB" -le 255 ]; then
                    DATA_DEVICE="\$nvme"
                    echo "Found data volume at \$DATA_DEVICE (\${SIZE_GB}GB)"
                    break
                fi
            fi
        done
    fi
    
    if [ -z "\$DATA_DEVICE" ]; then
        echo "ERROR: Could not find data volume (250GB device)"
        echo "Available block devices:"
        lsblk
        exit 1
    fi
    
    echo "Using data device: \$DATA_DEVICE"
    mkfs.xfs \$DATA_DEVICE
    mkdir -p /data
    mount -o pquota \$DATA_DEVICE /data
    echo "\$DATA_DEVICE /data xfs defaults,pquota 0 0" >> /etc/fstab
    xfs_quota -x -c 'state' /data
    echo "Data volume setup complete"
EOF
)

# Create block device mappings for root and data volumes
BLOCK_DEVICE_MAPPINGS='[
    {
        "DeviceName": "/dev/sda1",
        "Ebs": {
            "VolumeSize": '"${ROOT_VOLUME_SIZE}"',
            "VolumeType": "gp3",
            "DeleteOnTermination": true
        }
    },
    {
        "DeviceName": "/dev/xvdf",
        "Ebs": {
            "VolumeSize": '"${DATA_VOLUME_SIZE}"',
            "VolumeType": "gp3",
            "DeleteOnTermination": false
        }
    }
]'

# Launch the instance (without public IP)
echo "Launching instance ${MACHINE_NAME}..."
INSTANCE_ID=$(aws ec2 run-instances \
	--image-id ${AMI_ID} \
	--instance-type ${INSTANCE_TYPE} \
	--subnet-id ${SUBNET_ID} \
	--security-group-ids ${SECURITY_GROUP_ID} \
	--iam-instance-profile Name=${INSTANCE_PROFILE_NAME} \
	--no-associate-public-ip-address \
	--block-device-mappings "${BLOCK_DEVICE_MAPPINGS}" \
	--user-data "${USER_DATA}" \
	--tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=${MACHINE_NAME}}]" \
	--query 'Instances[0].InstanceId' \
	--output text \
	--region ${REGION})

if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" == "None" ]; then
	echo "Error: Failed to launch instance"
	exit 1
fi

echo "Instance launched: $INSTANCE_ID"
echo "Waiting for instance to be running..."

aws ec2 wait instance-running \
	--instance-ids ${INSTANCE_ID} \
	--region ${REGION}

echo "Instance is running. Getting instance details..."

# Get instance details (no public IP expected)
PRIVATE_IP=$(aws ec2 describe-instances \
	--instance-ids ${INSTANCE_ID} \
	--query 'Reservations[0].Instances[0].PrivateIpAddress' \
	--output text \
	--region ${REGION})

echo ""
echo "========================================="
echo "Instance successfully created!"
echo "========================================="
echo "Instance ID: $INSTANCE_ID"
echo "Instance Name: $MACHINE_NAME"
echo "Private IP: $PRIVATE_IP"
echo "Region: $REGION"
echo "Availability Zone: $AZ"
echo "Security Group: ${SECURITY_GROUP_NAME} (${SECURITY_GROUP_ID})"
echo "SSH Key: Hardcoded (ubuntu@exed-01)"
echo ""
echo "The instance is configuring itself with:"
echo "  - 2x 500GB swap partitions on dual NVMe drives (equal priority for I/O interleaving)"
echo "  - 250GB XFS data volume at /data with pquota"
echo "  - Tailscale with tag:server and SSH enabled"
echo ""
echo "NOTE: This instance has no public IP (private subnet with NAT Gateway)"
echo "The instance has internet access via NAT Gateway for:"
echo "  - Package downloads and updates"
echo "  - Tailscale connectivity"
echo "  - AWS services"
echo ""
echo "Access options:"
echo "  1. Via Tailscale (once connected):"
echo "     ssh ubuntu@${MACHINE_NAME}"
echo ""
echo "  2. From within VPC:"
echo "     ssh ubuntu@${PRIVATE_IP}"
echo ""
echo "  3. Via SSM Session Manager (if configured):"
echo "     aws ssm start-session --target ${INSTANCE_ID} --region ${REGION}"
echo ""
echo "  4. Via EC2 Serial Console:"
echo "     Username: ubuntu"
echo "     Password: ${MACHINE_NAME}-console"
echo "     (Change this password after first login!)"
echo ""
echo "To check cloud-init status (from within VPC or via Tailscale):"
echo "  ssh ubuntu@${MACHINE_NAME} 'cloud-init status'"
echo ""
echo "To debug Tailscale setup, check logs via SSM:"
echo "  aws ssm start-session --target ${INSTANCE_ID} --region ${REGION}"
echo "  Then run: sudo cat /var/log/cloud-init-output.log | grep -A20 'Tailscale'"
echo ""
echo "Or send command via SSM:"
echo "  aws ssm send-command --instance-ids ${INSTANCE_ID} --document-name AWS-RunShellScript --parameters 'commands=[\"sudo tail -100 /var/log/cloud-init-output.log | grep -A20 Tailscale\"]' --region ${REGION}"
echo "========================================="
