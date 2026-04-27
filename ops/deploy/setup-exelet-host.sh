#!/bin/bash
set -euo pipefail

# Check for machine name parameter
if [ $# -ne 1 ]; then
    echo "Usage: $0 <machine-name>"
    echo "Machine name must be in format: exe-ctr-NN, exe-ctr-staging-NN, or exelet-<region>-<prod|staging>-NN"
    exit 1
fi

MACHINE_NAME="$1"

# Validate machine name format
if [ "${SKIP_NAME_CHECK:-}" = "1" ]; then
    echo "Warning: SKIP_NAME_CHECK is set, bypassing machine name validation"
else
    if ! [[ "$MACHINE_NAME" =~ ^exe-ctr-(staging-)?[0-9]+$ ]] && ! [[ "$MACHINE_NAME" =~ ^exelet-[a-z]+-((prod|staging))-[0-9]+$ ]]; then
        echo "Error: Machine name must be in format exe-ctr-NN, exe-ctr-staging-NN, or exelet-<region>-<prod|staging>-NN"
        echo "Set SKIP_NAME_CHECK=1 to bypass this check"
        exit 1
    fi
fi

# Determine stage based on machine name
if [[ "$MACHINE_NAME" =~ -prod- ]]; then
    STAGE="production"
elif [[ "$MACHINE_NAME" =~ -staging- ]] || [[ "$MACHINE_NAME" =~ ^exe-ctr-staging- ]]; then
    STAGE="staging"
elif [ "${SKIP_NAME_CHECK:-}" = "1" ]; then
    STAGE="staging"
else
    echo "ERROR: Cannot determine stage from machine name '${MACHINE_NAME}'. Name must contain '-prod-' or '-staging-'."
    exit 1
fi
ROLE="exelet"
echo "Machine role: ${ROLE}, stage: ${STAGE}"

# Tailscale tags applied to the host: tag:exelet for role, plus a
# stage tag matching prod/staging.
if [ "$STAGE" = "production" ]; then
    TS_STAGE_TAG="tag:prod"
else
    TS_STAGE_TAG="tag:staging"
fi
TS_ROLE_TAG="tag:exelet"
TS_ADVERTISE_TAGS="${TS_ROLE_TAG},${TS_STAGE_TAG}"

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Tailscale OAuth preflight check is done after existing-instance check
# so we can skip it during re-provisioning.

# Configuration
CLOUD_HYPERVISOR_VERSION="48.0"
VIRTIOFSD_VERSION="1.13.2"

REGION="${REGION:-us-west-2}"
AZ="${ZONE:-${REGION}b}"
# Subnet type and name prefix.
# - exelet hosts in us-west-2 use a dedicated IGW-routed public subnet
#   (exelet-public-<az>) so EIPs route both directions cleanly.
# - exe-ctr hosts in us-west-2 keep the existing NAT-routed private subnet.
# - All other regions default to public (exe-<az>), which matches the
#   existing exe-us-east-1b subnet.
if [ "$REGION" = "us-west-2" ]; then
    if [[ "$MACHINE_NAME" == exelet-* ]]; then
        SUBNET_TYPE="${SUBNET_TYPE:-public}"
        SUBNET_NAME_PREFIX="exelet-public"
    else
        SUBNET_TYPE="${SUBNET_TYPE:-private}"
        SUBNET_NAME_PREFIX="exe-ctr-${SUBNET_TYPE}"
    fi
else
    SUBNET_TYPE="${SUBNET_TYPE:-public}"
    SUBNET_NAME_PREFIX="exe"
fi
INSTANCE_TYPE="${INSTANCE_TYPE:-m5d.metal}"
ROOT_VOLUME_SIZE="50"
BACKUP_VOLUME_SIZE="500"
if [ "$REGION" = "us-west-2" ]; then
    SECURITY_GROUP_NAME="exe-ctr-sg"
else
    SECURITY_GROUP_NAME="exe-sg"
fi
if [ "$REGION" = "us-west-2" ]; then
    INSTANCE_ROLE_NAME="exe-ctr-instance-role"
    INSTANCE_PROFILE_NAME="exe-ctr-instance-profile"
    BACKUP_VOLUME_TYPE="exe-ctr-backup"
    # Snapshot taxonomy: us-west-2 DLM matches exe-volume-type=exe-ctr-data
    TANK_VOLUME_TYPE="exe-ctr-data"
else
    INSTANCE_ROLE_NAME="exe-instance-role"
    INSTANCE_PROFILE_NAME="exe-instance-profile"
    BACKUP_VOLUME_TYPE="exe-backup"
    # us-east-1 DLM matches exe-volume-type=exe-data
    TANK_VOLUME_TYPE="exe-data"
fi
# tank EBS gp3 sizing
TANK_VOLUME_SIZE="2048" # GiB
TANK_VOLUME_IOPS="12000"
TANK_VOLUME_THROUGHPUT="250" # MB/s

# EC2 SSH key pair attached to new instances. Override per-region via env var
# if aws-bold-common isn't imported there yet.
SSH_KEY_NAME="${SSH_KEY_NAME:-aws-bold-common}"

# Look up a subnet in the requested AZ (or create one)
if [ -n "${SUBNET_ID:-}" ]; then
    echo "Using provided SUBNET_ID: ${SUBNET_ID}"
else
    echo "Looking up ${SUBNET_TYPE} subnet in ${AZ}..."
    SUBNET_ID=$(aws ec2 describe-subnets \
        --filters "Name=availability-zone,Values=${AZ}" "Name=tag:Name,Values=${SUBNET_NAME_PREFIX}*" \
        --query 'Subnets[0].SubnetId' \
        --output text \
        --region ${REGION})
    if [ -z "$SUBNET_ID" ] || [ "$SUBNET_ID" = "None" ]; then
        echo "No ${SUBNET_TYPE} subnet found in ${AZ}, creating one..."

        # Find the VPC. First try a subnet matching the requested prefix (so
        # we can grow an existing subnet family by adding a new AZ). If none
        # match (e.g. very first exelet-public-* subnet), fall back to the
        # default VPC, then to the only VPC in the region.
        VPC_ID=$(aws ec2 describe-subnets \
            --filters "Name=tag:Name,Values=${SUBNET_NAME_PREFIX}*" \
            --query 'Subnets[0].VpcId' \
            --output text \
            --region ${REGION} 2>/dev/null || true)
        if [ -z "$VPC_ID" ] || [ "$VPC_ID" = "None" ]; then
            VPC_ID=$(aws ec2 describe-vpcs \
                --filters "Name=is-default,Values=true" \
                --query 'Vpcs[0].VpcId' \
                --output text \
                --region ${REGION} 2>/dev/null || true)
        fi
        if [ -z "$VPC_ID" ] || [ "$VPC_ID" = "None" ]; then
            ALL_VPCS=$(aws ec2 describe-vpcs \
                --query 'Vpcs[].VpcId' \
                --output text \
                --region ${REGION})
            VPC_COUNT=$(echo "$ALL_VPCS" | wc -w)
            if [ "$VPC_COUNT" = "1" ]; then
                VPC_ID="$ALL_VPCS"
            fi
        fi
        if [ -z "$VPC_ID" ] || [ "$VPC_ID" = "None" ]; then
            echo "Error: Could not auto-detect VPC. Set SUBNET_ID explicitly."
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
        VPC_PREFIX=$(echo "${VPC_CIDR}" | cut -d. -f1-2) # e.g. "172.31"
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

        # Pick the route table for the new subnet:
        # - public:  the IGW-routed table (defaults to the VPC main RT, which
        #            is what the existing default-VPC public subnets use).
        # - private: the NAT-routed table (matches existing exe-ctr-private-*).
        if [ "$SUBNET_TYPE" = "public" ]; then
            TARGET_RT=$(aws ec2 describe-route-tables \
                --filters "Name=vpc-id,Values=${VPC_ID}" "Name=association.main,Values=true" \
                --query 'RouteTables[0].RouteTableId' \
                --output text \
                --region ${REGION})
            HAS_IGW=$(aws ec2 describe-route-tables \
                --route-table-ids "${TARGET_RT}" \
                --query "RouteTables[0].Routes[?starts_with(GatewayId, \`igw-\`)].GatewayId | [0]" \
                --output text \
                --region ${REGION} 2>/dev/null || true)
            if [ -z "$HAS_IGW" ] || [ "$HAS_IGW" = "None" ]; then
                echo "Error: VPC ${VPC_ID} main route table ${TARGET_RT} has no IGW route."
                echo "A public subnet needs an Internet Gateway. Aborting."
                exit 1
            fi
            RT_LABEL="${TARGET_RT} (IGW: ${HAS_IGW})"
        else
            TARGET_RT=$(aws ec2 describe-route-tables \
                --filters "Name=vpc-id,Values=${VPC_ID}" "Name=route.nat-gateway-id,Values=*" \
                --query 'RouteTables[0].RouteTableId' \
                --output text \
                --region ${REGION})
            if [ -n "$TARGET_RT" ] && [ "$TARGET_RT" != "None" ]; then
                RT_LABEL="${TARGET_RT} (NAT gateway)"
            else
                RT_LABEL="(none with NAT found — subnet may lack internet access)"
            fi
        fi

        echo ""
        echo "Will create a new ${SUBNET_TYPE} subnet:"
        echo "  VPC:         ${VPC_ID}"
        echo "  AZ:          ${AZ}"
        echo "  CIDR:        ${NEW_CIDR}"
        echo "  Name tag:    ${SUBNET_NAME_PREFIX}-${AZ}"
        echo "  Route table: ${RT_LABEL}"
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
            --tags Key=Name,Value="${SUBNET_NAME_PREFIX}-${AZ}" \
            --region ${REGION}

        # Associate the chosen route table.
        if [ -n "$TARGET_RT" ] && [ "$TARGET_RT" != "None" ]; then
            aws ec2 associate-route-table \
                --subnet-id ${SUBNET_ID} \
                --route-table-id ${TARGET_RT} \
                --region ${REGION} >/dev/null
            echo "Associated subnet with route table ${RT_LABEL}"
        else
            echo "Warning: no usable route table found. Subnet may lack internet access."
        fi
    fi
    echo "Using subnet ${SUBNET_ID} in ${AZ}"
fi

# Two-phase flow:
#   Phase 1 — no AWS instance yet: launch one with the Tailscale package
#     installed (but not joined), print instructions for the operator to run
#     `tailscale up` manually, and exit.
#   Phase 2 — instance exists and is reachable on the tailnet: refuse to
#     overwrite an already-provisioned host, otherwise run the rest of setup.
echo "Checking if instance ${MACHINE_NAME} already exists..."
EXISTING_INSTANCE=$(aws ec2 describe-instances \
    --filters "Name=tag:Name,Values=${MACHINE_NAME}" \
    "Name=instance-state-name,Values=pending,running,stopping,stopped" \
    --query 'Reservations[].Instances[].InstanceId' \
    --output text \
    --region ${REGION})

NEW_INSTANCE=true
if [ -n "$EXISTING_INSTANCE" ] && [ "$EXISTING_INSTANCE" != "None" ]; then
    NEW_INSTANCE=false
    INSTANCE_ID="$EXISTING_INSTANCE"
    echo "Found existing instance ${INSTANCE_ID} for ${MACHINE_NAME}"

    # Require Tailscale SSH connectivity. If it fails, the operator hasn't
    # authenticated this host into the tailnet yet — print instructions and stop.
    echo "Verifying ${MACHINE_NAME} is reachable via Tailscale SSH..."
    if ! ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null ubuntu@${MACHINE_NAME} true 2>/dev/null; then
        EXISTING_PRIVATE_IP=$(aws ec2 describe-instances \
            --instance-ids ${INSTANCE_ID} \
            --query 'Reservations[0].Instances[0].PrivateIpAddress' \
            --output text \
            --region ${REGION})
        EXISTING_PUBLIC_IP=$(aws ec2 describe-instances \
            --instance-ids ${INSTANCE_ID} \
            --query 'Reservations[0].Instances[0].PublicIpAddress' \
            --output text \
            --region ${REGION})
        if [ -z "$EXISTING_PUBLIC_IP" ] || [ "$EXISTING_PUBLIC_IP" = "None" ]; then
            EXISTING_PUBLIC_IP=""
        fi
        EXISTING_TARGET="${EXISTING_PUBLIC_IP:-${EXISTING_PRIVATE_IP}}"
        cat <<INSTRUCTIONS
ERROR: Cannot reach ${MACHINE_NAME} via Tailscale SSH.
The instance exists in AWS (${INSTANCE_ID}, private ${EXISTING_PRIVATE_IP}, public ${EXISTING_PUBLIC_IP:-<none>})
but is not on the tailnet yet.

Connect to the instance and run:

  ssh ubuntu@${EXISTING_TARGET}
  sudo tailscale up \\
    --advertise-tags=${TS_ADVERTISE_TAGS} \\
    --ssh \\
    --hostname=${MACHINE_NAME}

Authenticate via the URL it prints, then re-run:

  $0 ${MACHINE_NAME}
INSTRUCTIONS
        exit 1
    fi
    echo "✓ ${MACHINE_NAME} is reachable via Tailscale SSH"

    # Safety: refuse to clobber an already-provisioned host. The volume
    # setup script destroys 'tank', 'backup', and 'dozer' unconditionally,
    # so finding any of them here means we'd be wiping a live node's data.
    echo "Checking for existing zpools (tank, backup, dozer)..."
    EXISTING_POOLS=$(ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        ubuntu@${MACHINE_NAME} \
        "sudo zpool list -H -o name 2>/dev/null | grep -E '^(tank|backup|dozer)\$' || true" |
        tr '\n' ' ' | sed 's/ *$//')
    if [ -n "$EXISTING_POOLS" ]; then
        cat <<POOL_ERR
ERROR: ${MACHINE_NAME} already has zpool(s): ${EXISTING_POOLS}
Refusing to overwrite an existing node's data.

If you really intend to re-provision this host, destroy the pools manually
first (this is destructive — make sure the host has nothing you need):

  ssh ubuntu@${MACHINE_NAME} 'sudo swapoff -a; sudo zpool destroy -f tank backup dozer 2>/dev/null; true'

Then re-run this script.
POOL_ERR
        exit 1
    fi
    echo "✓ No existing zpools — continuing with provisioning"
else
    echo "No existing AWS instance found for ${MACHINE_NAME}"
fi

if [ "$NEW_INSTANCE" = "true" ]; then

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

    # Check for root password
    if [ -z "${ROOT_PASSWORD:-}" ]; then
        echo "ERROR: ROOT_PASSWORD environment variable not set"
        echo "Please set it:  export ROOT_PASSWORD=<password-for-root-account>"
        exit 1
    fi

    # Verify the SSH key pair exists in this region before launching.
    if ! aws ec2 describe-key-pairs --key-names "${SSH_KEY_NAME}" --region "${REGION}" >/dev/null 2>&1; then
        echo "ERROR: EC2 key pair '${SSH_KEY_NAME}' not found in ${REGION}."
        echo "Either import it into this region or override with SSH_KEY_NAME=<name>."
        exit 1
    fi
    echo "Using SSH key pair: ${SSH_KEY_NAME}"

    # Fetch the public key for SSH_KEY_NAME from AWS so we can install it into
    # ~ubuntu/.ssh/authorized_keys via cloud-init. The cloud-init `users:`
    # block below replaces the default ubuntu user setup, which suppresses
    # the SSH key cloud-init would otherwise auto-install from the EC2
    # keypair, so we have to inject it explicitly.
    SSH_KEY_PUBLIC=$(aws ec2 describe-key-pairs \
        --key-names "${SSH_KEY_NAME}" \
        --include-public-key \
        --query 'KeyPairs[0].PublicKey' \
        --output text \
        --region "${REGION}")
    SSH_KEY_PUBLIC="${SSH_KEY_PUBLIC%$'\n'}" # strip trailing newline AWS includes
    if [ -z "$SSH_KEY_PUBLIC" ] || [ "$SSH_KEY_PUBLIC" = "None" ]; then
        echo "ERROR: Could not retrieve public key material for ${SSH_KEY_NAME} in ${REGION}."
        exit 1
    fi

    # Build cloud-init user data. The instance gets the Tailscale package
    # installed but is NOT joined to the tailnet here — the operator runs
    # `tailscale up` manually on the host (see post-launch instructions
    # printed at the end of this section).
    #   package isal installs igzip which is supposed to speed up image
    #   decompression
    USER_DATA=$(
        cat <<EOF
#cloud-config
users:
  - name: ubuntu
    ssh_authorized_keys:
      - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOEetwKXuTe+byx+VJTOn3ZxjVnpMe/82YroL111tTwK ubuntu@exed-01
      - ${SSH_KEY_PUBLIC}
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash

chpasswd:
  list:
    - root:${ROOT_PASSWORD}
  expire: false

hostname: ${MACHINE_NAME}

package_update: true
package_upgrade: false

packages:
  - curl
  - jq
  - pv
  - atop
  - btop
  - htop
  - isal

runcmd:
  - curl -fsSL https://tailscale.com/install.sh | sh
  - apt-get install -y build-essential git libcap-ng-dev libseccomp-dev pkg-config
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
        --key-name "${SSH_KEY_NAME}" \
        --user-data "${USER_DATA}" \
        --block-device-mappings \
        "DeviceName=/dev/sda1,Ebs={VolumeSize=${ROOT_VOLUME_SIZE},VolumeType=gp3,DeleteOnTermination=true}" \
        "DeviceName=/dev/xvdf,Ebs={VolumeSize=${BACKUP_VOLUME_SIZE},VolumeType=io2,Iops=12000,DeleteOnTermination=true}" \
        "DeviceName=/dev/xvdg,Ebs={VolumeSize=${TANK_VOLUME_SIZE},VolumeType=gp3,Iops=${TANK_VOLUME_IOPS},Throughput=${TANK_VOLUME_THROUGHPUT},DeleteOnTermination=true}" \
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
    BACKUP_VOLUME_ID=$(aws ec2 describe-instances \
        --instance-ids ${INSTANCE_ID} \
        --query 'Reservations[0].Instances[0].BlockDeviceMappings[?DeviceName==`/dev/xvdf`].Ebs.VolumeId' \
        --output text \
        --region ${REGION})
    TANK_VOLUME_ID=$(aws ec2 describe-instances \
        --instance-ids ${INSTANCE_ID} \
        --query 'Reservations[0].Instances[0].BlockDeviceMappings[?DeviceName==`/dev/xvdg`].Ebs.VolumeId' \
        --output text \
        --region ${REGION})

    if [ -n "$ROOT_VOLUME_ID" ] && [ "$ROOT_VOLUME_ID" != "None" ]; then
        aws ec2 create-tags --resources ${ROOT_VOLUME_ID} --tags Key=Name,Value=${MACHINE_NAME}-root Key=role,Value=${ROLE} Key=stage,Value=${STAGE} --region ${REGION}
        echo "Tagged root volume ${ROOT_VOLUME_ID} as ${MACHINE_NAME}-root (role=${ROLE}, stage=${STAGE})"
    fi
    if [ -n "$BACKUP_VOLUME_ID" ] && [ "$BACKUP_VOLUME_ID" != "None" ]; then
        aws ec2 create-tags --resources ${BACKUP_VOLUME_ID} --tags Key=Name,Value=${MACHINE_NAME}-backup Key=role,Value=${ROLE} Key=stage,Value=${STAGE} Key=exe-volume-type,Value=${BACKUP_VOLUME_TYPE} --region ${REGION}
        echo "Tagged backup volume ${BACKUP_VOLUME_ID} as ${MACHINE_NAME}-backup (role=${ROLE}, stage=${STAGE})"
    fi
    if [ -n "$TANK_VOLUME_ID" ] && [ "$TANK_VOLUME_ID" != "None" ]; then
        aws ec2 create-tags --resources ${TANK_VOLUME_ID} --tags Key=Name,Value=${MACHINE_NAME}-tank Key=role,Value=${ROLE} Key=stage,Value=${STAGE} Key=exe-volume-type,Value=${TANK_VOLUME_TYPE} --region ${REGION}
        echo "Tagged tank volume ${TANK_VOLUME_ID} as ${MACHINE_NAME}-tank (role=${ROLE}, stage=${STAGE}, exe-volume-type=${TANK_VOLUME_TYPE})"
    fi

    # Wait for instance to be running
    echo "Waiting for instance to start..."
    aws ec2 wait instance-running --instance-ids ${INSTANCE_ID} --region ${REGION}

    INSTANCE_IP=$(aws ec2 describe-instances \
        --instance-ids ${INSTANCE_ID} \
        --query 'Reservations[0].Instances[0].PrivateIpAddress' \
        --output text \
        --region ${REGION})

    echo "Instance is running at ${INSTANCE_IP} (private IP)"

    # Allocate (or reuse) an Elastic IP and associate it with the instance.
    # Skipped on NAT-only private subnets where an EIP would be split-brain
    # (inbound via IGW, outbound via NAT).
    PUBLIC_IP=""
    if [ "$SUBNET_TYPE" = "public" ]; then
        EIP_ALLOC_ID=$(aws ec2 describe-addresses \
            --filters "Name=tag:Name,Values=${MACHINE_NAME}" \
            --query 'Addresses[0].AllocationId' \
            --output text \
            --region ${REGION} 2>/dev/null || true)
        if [ -z "$EIP_ALLOC_ID" ] || [ "$EIP_ALLOC_ID" = "None" ]; then
            echo "Allocating new Elastic IP for ${MACHINE_NAME}..."
            EIP_ALLOC_ID=$(aws ec2 allocate-address \
                --domain vpc \
                --tag-specifications "ResourceType=elastic-ip,Tags=[{Key=Name,Value=${MACHINE_NAME}},{Key=role,Value=${ROLE}},{Key=stage,Value=${STAGE}}]" \
                --query 'AllocationId' \
                --output text \
                --region ${REGION})
            echo "Allocated EIP ${EIP_ALLOC_ID}"
        else
            echo "Reusing existing EIP ${EIP_ALLOC_ID} (tagged ${MACHINE_NAME})"
        fi
        aws ec2 associate-address \
            --instance-id ${INSTANCE_ID} \
            --allocation-id ${EIP_ALLOC_ID} \
            --region ${REGION} >/dev/null
        PUBLIC_IP=$(aws ec2 describe-addresses \
            --allocation-ids ${EIP_ALLOC_ID} \
            --query 'Addresses[0].PublicIp' \
            --output text \
            --region ${REGION})
        echo "✓ EIP ${PUBLIC_IP} associated with ${INSTANCE_ID}"
    fi

    SSH_TARGET="${PUBLIC_IP:-${INSTANCE_IP}}"
    cat <<INSTRUCTIONS

==========================================
Instance launched
==========================================

  Name:       ${MACHINE_NAME}
  ID:         ${INSTANCE_ID}
  Private IP: ${INSTANCE_IP}
  Public IP:  ${PUBLIC_IP:-<none>}
  Type:       ${INSTANCE_TYPE}

Cloud-init is installing the Tailscale package; it will not auto-join.
Wait ~2-5 min for cloud-init to finish, then connect to the instance and
authenticate it into the tailnet:

  ssh ubuntu@${SSH_TARGET}
  sudo tailscale up \\
    --advertise-tags=${TS_ADVERTISE_TAGS} \\
    --ssh \\
    --hostname=${MACHINE_NAME}

Visit the auth URL it prints to sign the device into the tailnet.

Then re-run this script to finish provisioning:

  $0 ${MACHINE_NAME}

INSTRUCTIONS
    exit 0

fi # end of new-instance provisioning

# Setup volumes on metal instances
echo ""
echo "=========================================="
echo "Setting up volumes (swap, zpool)"
echo "=========================================="

# Create a script to setup the volumes on the remote machine.
# Args (passed by the parent setup-exelet-host.sh):
#   $1 = expected tank EBS volume size in GiB   (default 2048)
#   $2 = expected backup EBS volume size in GiB (default 500)
# Layout:
#   - tank   ← EBS gp3 (the larger non-root EBS volume), single-disk pool
#   - backup ← EBS io2 (the smaller non-root EBS volume), single-disk pool
#   - dozer  ← instance-store NVMe (75% partition each), RAID layout depends on count
cat <<'VOLUME_SETUP_SCRIPT' >/tmp/setup-volumes.sh
#!/bin/bash
set -euo pipefail

TANK_GIB="${1:-2048}"
BACKUP_GIB="${2:-500}"
SIZE_TOLERANCE_GIB=16  # EBS rounding wiggle room

echo "=== Setting up volumes on metal instance ==="
echo "Expected tank EBS size:   ${TANK_GIB} GiB"
echo "Expected backup EBS size: ${BACKUP_GIB} GiB"

# First check if this is a metal instance (has NVMe drives)
if [ ! -e /dev/nvme0n1 ]; then
	echo "Non-metal instance detected"
	sudo mkdir -p /data
	exit 0
fi

# Install required packages
echo "Installing required packages..."
sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq
sudo DEBIAN_FRONTEND=noninteractive apt-get install -qq -y binutils net-tools parted socat zfsutils-linux >/dev/null 2>&1

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

# Clean up any existing volume state from a previous run
echo "=== Cleaning up previous volume state ==="
# Disable all swap partitions on NVMe devices
for swp in $(swapon --show=NAME --noheadings 2>/dev/null | grep '/dev/nvme'); do
  echo "Disabling swap on $swp"
  sudo swapoff "$swp" || true
done
# Remove NVMe swap entries from fstab
sudo sed -i '\|^/dev/nvme.*swap|d' /etc/fstab

# Destroy existing ZFS pools (including dozer in case of partial prior run)
for pool in tank backup dozer; do
  if sudo zpool list "$pool" &>/dev/null; then
    echo "Destroying ZFS pool $pool"
    sudo zpool destroy -f "$pool"
  fi
done

# Detect NVMe devices and classify by model + (for EBS) size
echo "=== Detecting NVMe devices ==="
INSTANCE_STORE_DEVICES=()
EBS_TANK_DEVICE=""
EBS_BACKUP_DEVICE=""

for dev in /dev/nvme*n1; do
  [ -b "$dev" ] || continue
  devname=$(basename "$dev")
  model=$(cat "/sys/block/${devname}/device/model" 2>/dev/null | xargs)
  size_gib=$(lsblk -b -n -d -o SIZE "$dev" 2>/dev/null | awk '{printf "%.0f", $1/1073741824}')

  # Safety: never touch a device that has mounted filesystems
  if lsblk -n -o MOUNTPOINT "$dev" 2>/dev/null | grep -q '/'; then
    echo "Mounted: $dev (${size_gib}GiB) - skipping"
    continue
  fi

  if [ "$model" = "Amazon EC2 NVMe Instance Storage" ]; then
    echo "Instance-store: $dev (${size_gib}GiB)"
    INSTANCE_STORE_DEVICES+=("$dev")
  elif [ "$model" = "Amazon Elastic Block Store" ]; then
    # Match by expected size with a small tolerance to account for EBS rounding.
    if [ "$size_gib" -ge $((TANK_GIB - SIZE_TOLERANCE_GIB)) ] && [ "$size_gib" -le $((TANK_GIB + SIZE_TOLERANCE_GIB)) ]; then
      echo "EBS tank:   $dev (${size_gib}GiB)"
      EBS_TANK_DEVICE="$dev"
    elif [ "$size_gib" -ge $((BACKUP_GIB - SIZE_TOLERANCE_GIB)) ] && [ "$size_gib" -le $((BACKUP_GIB + SIZE_TOLERANCE_GIB)) ]; then
      echo "EBS backup: $dev (${size_gib}GiB)"
      EBS_BACKUP_DEVICE="$dev"
    else
      echo "EBS (unrecognized size): $dev (${size_gib}GiB) - skipping"
    fi
  else
    echo "Unknown NVMe: $dev (model: $model, ${size_gib}GiB) - skipping"
  fi
done

if [ ${#INSTANCE_STORE_DEVICES[@]} -eq 0 ]; then
  echo "ERROR: No instance-store NVMe devices found (cannot build 'dozer' pool)"
  lsblk
  exit 1
fi
if [ -z "$EBS_TANK_DEVICE" ]; then
  echo "ERROR: No EBS data volume of expected tank size (${TANK_GIB} GiB) found"
  lsblk
  exit 1
fi

echo ""
echo "Found ${#INSTANCE_STORE_DEVICES[@]} instance-store device(s) for dozer"
echo "tank   EBS: ${EBS_TANK_DEVICE:-<missing>}"
echo "backup EBS: ${EBS_BACKUP_DEVICE:-<missing>}"

# Partition each instance-store drive: 25% swap, 75% data (for dozer)
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

# Create ZFS pool 'dozer' from instance-store data partitions
echo ""
echo "=== Setting up ZFS pool 'dozer' (instance-store NVMe) ==="
NDISKS=${#DATA_PARTS[@]}

if [ "$NDISKS" -eq 1 ]; then
  echo "Single drive, creating dozer with no redundancy"
  sudo zpool create -o ashift=12 -m none dozer "${DATA_PARTS[0]}"
elif [ "$NDISKS" -eq 2 ]; then
  echo "Two drives, creating dozer as mirror"
  sudo zpool create -o ashift=12 -m none dozer mirror "${DATA_PARTS[@]}"
elif [ "$NDISKS" -le 6 ]; then
  # 3-6 drives: raidz1 for more usable space
  echo "Creating dozer as raidz1 with $NDISKS drives"
  sudo zpool create -o ashift=12 -m none dozer raidz1 "${DATA_PARTS[@]}"
else
  # >6 drives: mirrored vdevs (pairs of 2 drives each)
  if [ $((NDISKS % 2)) -ne 0 ]; then
    echo "ERROR: odd number of drives ($NDISKS), cannot create mirrored vdevs"
    exit 1
  fi

  ZPOOL_ARGS=()
  for ((i = 0; i < NDISKS; i += 2)); do
    ZPOOL_ARGS+=("mirror" "${DATA_PARTS[$i]}" "${DATA_PARTS[$((i + 1))]}")
  done

  echo "Creating dozer: ${ZPOOL_ARGS[*]}"
  sudo zpool create -o ashift=12 -m none dozer "${ZPOOL_ARGS[@]}"
fi

sudo zfs set compression=lz4 dozer
sudo zfs set atime=off dozer
sudo zfs set xattr=sa dozer

echo "ZFS pool 'dozer' ready:"
zpool status dozer

# Create ZFS pool 'tank' from the EBS gp3 volume
echo ""
echo "=== Setting up ZFS pool 'tank' (EBS gp3) ==="
TANK_DEV=$(resolve_by_id "$EBS_TANK_DEVICE")
echo "  ${EBS_TANK_DEVICE} -> ${TANK_DEV}"
sudo zpool create -o ashift=12 -m none tank "${TANK_DEV}"
sudo zfs set compression=lz4 tank
sudo zfs set atime=off tank
sudo zfs set xattr=sa tank

# /data dataset on tank (durable across instance restarts)
sudo zfs create -o mountpoint=/data tank/data
sudo mkdir -p /data/exelet

echo "ZFS pool 'tank' ready:"
zpool status tank

# Create backup pool from the io2 EBS volume (if attached)
echo ""
echo "=== Setting up ZFS backup pool ==="
if [ -n "$EBS_BACKUP_DEVICE" ]; then
  BACKUP_DEV=$(resolve_by_id "$EBS_BACKUP_DEVICE")
  echo "  ${EBS_BACKUP_DEVICE} -> ${BACKUP_DEV}"
  sudo zpool create -o ashift=12 -m none backup "${BACKUP_DEV}"
  sudo zfs set compression=lz4 backup
  sudo zfs set atime=off backup
  echo "Backup pool ready:"
  zpool status backup
else
  echo "No EBS backup volume found, skipping backup pool"
fi

# Configure ZFS ARC (min 16GB, max 24GB)
echo ""
echo "Configuring ZFS ARC limits..."
cat <<EOF | sudo tee /etc/modprobe.d/zfs.conf >/dev/null
options zfs zfs_arc_min=17179869184
options zfs zfs_arc_max=25769803776
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
    "chmod +x ~/setup-volumes.sh && ~/setup-volumes.sh ${TANK_VOLUME_SIZE} ${BACKUP_VOLUME_SIZE}"; then
    echo "ERROR: Volume setup failed"
    rm -f /tmp/setup-volumes.sh
    exit 1
fi

rm -f /tmp/setup-volumes.sh

###############################################
# Build cloud-hypervisor artifacts on remote
###############################################

# Copy setup script to the remote host
echo "Copying setup scripts to ${MACHINE_NAME}..."
if ! scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    "${SCRIPT_DIR}/setup-cloud-hypervisor.sh" \
    "ubuntu@${MACHINE_NAME}:~/"; then
    echo "ERROR: Failed to copy scripts to remote"
    exit 1
fi

# Build artifacts on remote using cargo
echo "Building Cloud Hypervisor artifacts on ${MACHINE_NAME}..."
REMOTE_BUILD_CMD="set -euo pipefail
CLOUD_HYPERVISOR_VERSION=${CLOUD_HYPERVISOR_VERSION}
VIRTIOFSD_VERSION=${VIRTIOFSD_VERSION}
CACHE_DIR=\"\$HOME/.cache/exedops\"
ARTIFACT_NAME=\"cloud-hypervisor-\${CLOUD_HYPERVISOR_VERSION}-amd64.tar.gz\"

mkdir -p \"\$CACHE_DIR\"

# Check if artifact already exists
if [ -f \"\$CACHE_DIR/\$ARTIFACT_NAME\" ]; then
    echo \"Cloud Hypervisor \${CLOUD_HYPERVISOR_VERSION} (amd64) cache hit\"
else
    echo \"Building Cloud Hypervisor \${CLOUD_HYPERVISOR_VERSION} (amd64) from source...\"

    # Install Rust nightly if not present
    if ! command -v rustup &>/dev/null; then
        curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --default-toolchain nightly
        source \"\$HOME/.cargo/env\"
    fi
    rustup toolchain install nightly --profile minimal
    rustup component add rustfmt --toolchain nightly
    rustup default nightly

    BUILD_DIR=\$(mktemp -d)
    OUT_DIR=\"\$BUILD_DIR/out\"
    mkdir -p \"\$OUT_DIR/bin\"

    # Build Cloud Hypervisor
    echo \"Downloading cloud-hypervisor v\${CLOUD_HYPERVISOR_VERSION}...\"
    curl -fsSL \\
        \"https://github.com/cloud-hypervisor/cloud-hypervisor/archive/refs/tags/v\${CLOUD_HYPERVISOR_VERSION}.tar.gz\" \\
        -o \"\$BUILD_DIR/cloud-hypervisor.tar.gz\"
    tar xzf \"\$BUILD_DIR/cloud-hypervisor.tar.gz\" -C \"\$BUILD_DIR\"

    echo \"Building cloud-hypervisor...\"
    cargo +nightly build --release --manifest-path \"\$BUILD_DIR/cloud-hypervisor-\${CLOUD_HYPERVISOR_VERSION}/Cargo.toml\"
    install -m 0755 \"\$BUILD_DIR/cloud-hypervisor-\${CLOUD_HYPERVISOR_VERSION}/target/release/cloud-hypervisor\" \"\$OUT_DIR/bin/cloud-hypervisor\"
    install -m 0755 \"\$BUILD_DIR/cloud-hypervisor-\${CLOUD_HYPERVISOR_VERSION}/target/release/ch-remote\" \"\$OUT_DIR/bin/ch-remote\"

    # Build virtiofsd
    echo \"Cloning virtiofsd v\${VIRTIOFSD_VERSION}...\"
    git clone --depth=1 --branch \"v\${VIRTIOFSD_VERSION}\" \\
        https://gitlab.com/virtio-fs/virtiofsd.git \"\$BUILD_DIR/virtiofsd\"

    echo \"Building virtiofsd...\"
    cargo +nightly build --release --manifest-path \"\$BUILD_DIR/virtiofsd/Cargo.toml\"
    install -m 0755 \"\$BUILD_DIR/virtiofsd/target/release/virtiofsd\" \"\$OUT_DIR/bin/virtiofsd\"

    # Write metadata
    printf 'cloud_hypervisor_version=%s\nvirtiofsd_version=%s\narch=%s\n' \\
        \"\${CLOUD_HYPERVISOR_VERSION}\" \\
        \"\${VIRTIOFSD_VERSION}\" \\
        \"amd64\" > \"\$OUT_DIR/metadata\"

    tar czf \"\$CACHE_DIR/\$ARTIFACT_NAME\" -C \"\$OUT_DIR\" .
    rm -rf \"\$BUILD_DIR\"

    echo \"Cached Cloud Hypervisor \${CLOUD_HYPERVISOR_VERSION} (amd64) at \$CACHE_DIR/\$ARTIFACT_NAME\"
fi

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
cat <<EOF >/etc/sysctl.d/99-tcp-buffers.conf
net.core.rmem_max = 33554432
net.core.wmem_max = 33554432
net.ipv4.tcp_rmem = 4096 131072 33554432
net.ipv4.tcp_wmem = 4096 131072 33554432
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

# Disable IPv6
echo ""
echo "=========================================="
echo "Disabling IPv6"
echo "=========================================="

ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    "ubuntu@${MACHINE_NAME}" 'bash -s' <<'DISABLE_IPV6'
set -euo pipefail

# Disable IPv6 immediately via sysctl
cat <<EOF | sudo tee /etc/sysctl.d/99-disable-ipv6.conf > /dev/null
net.ipv6.conf.all.disable_ipv6 = 1
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.lo.disable_ipv6 = 1
EOF
sudo sysctl --system > /dev/null

# Persist via GRUB kernel command line so IPv6 is disabled early at boot
GRUB_FILE="/etc/default/grub"
if grep -q 'ipv6.disable=1' "$GRUB_FILE"; then
    echo "GRUB already has ipv6.disable=1"
else
    sudo sed -i 's/^GRUB_CMDLINE_LINUX_DEFAULT="\(.*\)"/GRUB_CMDLINE_LINUX_DEFAULT="\1 ipv6.disable=1"/' "$GRUB_FILE"
    sudo update-grub
fi

# Watchdog: if IPv6 ever comes back (e.g. tailscaled reload), re-disable it.
sudo tee /usr/local/sbin/disable-ipv6-if-needed.sh > /dev/null <<'WATCHDOG'
#!/bin/bash
set -e
if ip -6 addr show 2>/dev/null | grep -q 'inet6'; then
    systemd-run --on-active=30 /bin/systemctl restart tailscaled
    sysctl -w net.ipv6.conf.default.disable_ipv6=1
    sysctl -w net.ipv6.conf.all.disable_ipv6=1
    sysctl -w net.ipv6.conf.all.accept_ra=0
    sysctl -w net.ipv6.conf.default.accept_ra=0
fi
WATCHDOG
sudo chmod 0755 /usr/local/sbin/disable-ipv6-if-needed.sh

sudo tee /etc/cron.d/disable-ipv6-watchdog > /dev/null <<'CRON'
SHELL=/bin/bash
PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
*/5 * * * * root /usr/local/sbin/disable-ipv6-if-needed.sh >/dev/null 2>&1
CRON
sudo chmod 0644 /etc/cron.d/disable-ipv6-watchdog

echo "IPv6 disabled"
DISABLE_IPV6

# Install and configure node_exporter for monitoring
# Config is single-sourced in observability/deploy-node-exporter.py
echo ""
echo "=========================================="
echo "Installing node_exporter for monitoring"
echo "=========================================="
python3 "${SCRIPT_DIR}/../../observability/deploy-node-exporter.py" "${MACHINE_NAME}"

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
echo "  - ZFS pool 'tank' on EBS gp3 (${TANK_VOLUME_SIZE} GiB, ${TANK_VOLUME_IOPS} IOPS, ${TANK_VOLUME_THROUGHPUT} MB/s) — durable"
echo "  - ZFS pool 'dozer' (raidz1 if <=6 drives, mirrored vdevs if >6) on 75% of instance-store NVMe drives — ephemeral"
echo "  - ZFS pool 'backup' on EBS io2 volume"
echo "  - ZFS ARC limits set to 16GB min / 24GB max (requires reboot)"
echo ""
echo "Instance details:"
echo "  Name: ${MACHINE_NAME}"
echo "  ID: ${INSTANCE_ID}"
echo ""
echo "You can now connect via:"
echo "  ssh ubuntu@${MACHINE_NAME}"
echo "=========================================="
