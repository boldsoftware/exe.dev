#!/bin/bash
set -euo pipefail

# Create the exe-ctr-staging-replica EC2 instance with ZFS

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

REGION="${REGION:-us-west-2}"
SUBNET_ID="${SUBNET_ID:-}"
NAT_GATEWAY="${NAT_GATEWAY:-}"
SECURITY_GROUP_NAME="ctr-staging-replica"
INSTANCE_NAME="exe-ctr-staging-replica"
INSTANCE_TYPE="t2.xlarge"
ROOT_VOLUME_SIZE="64"
DATA_VOLUME_SIZE="250"

SG_ID=""

discover_subnet() {
    if [ -n "$SUBNET_ID" ]; then
        echo "Using specified subnet: ${SUBNET_ID}" >&2
        echo "$SUBNET_ID"
        return 0
    fi

    echo "Discovering subnets in ${REGION}..." >&2

    local subnet_info subnet_id subnet_name az
    subnet_info=$(aws ec2 describe-subnets \
        --query 'Subnets[0].[SubnetId,Tags[?Key==`Name`].Value|[0],AvailabilityZone]' \
        --output text \
        --region "${REGION}" 2>/dev/null || true)

    subnet_id=$(echo "$subnet_info" | awk '{print $1}')
    subnet_name=$(echo "$subnet_info" | awk '{print $2}')
    az=$(echo "$subnet_info" | awk '{print $3}')

    if [ -z "$subnet_id" ] || [ "$subnet_id" = "None" ]; then
        echo "ERROR: No subnets found in region ${REGION}" >&2
        return 1
    fi

    echo "" >&2
    echo "Found subnet:" >&2
    echo "  ID:   ${subnet_id}" >&2
    echo "  Name: ${subnet_name:-<unnamed>}" >&2
    echo "  AZ:   ${az}" >&2
    echo "" >&2
    read -p "Use this subnet? [Y/n] " -n 1 -r >&2
    echo >&2
    if [[ $REPLY =~ ^[Nn]$ ]]; then
        echo "Aborted. Specify a subnet with: SUBNET_ID=subnet-xxx $0 start" >&2
        return 1
    fi

    echo "$subnet_id"
}

discover_nat_gateway() {
    local vpc_id="$1"

    if [ -n "$NAT_GATEWAY" ]; then
        echo "Using specified NAT Gateway: ${NAT_GATEWAY}" >&2
        echo "$NAT_GATEWAY"
        return 0
    fi

    echo "Discovering NAT Gateways in VPC ${vpc_id}..." >&2

    local nat_info nat_gw_id nat_name nat_subnet
    nat_info=$(aws ec2 describe-nat-gateways \
        --filter "Name=vpc-id,Values=${vpc_id}" "Name=state,Values=available" \
        --query 'NatGateways[0].[NatGatewayId,Tags[?Key==`Name`].Value|[0],SubnetId]' \
        --output text \
        --region "${REGION}" 2>/dev/null || true)

    nat_gw_id=$(echo "$nat_info" | awk '{print $1}')
    nat_name=$(echo "$nat_info" | awk '{print $2}')
    nat_subnet=$(echo "$nat_info" | awk '{print $3}')

    if [ -n "$nat_gw_id" ] && [ "$nat_gw_id" != "None" ]; then
        echo "" >&2
        echo "Found NAT Gateway:" >&2
        echo "  ID:     ${nat_gw_id}" >&2
        echo "  Name:   ${nat_name:-<unnamed>}" >&2
        echo "  Subnet: ${nat_subnet}" >&2
        echo "" >&2
        read -p "Use this NAT Gateway? [Y/n] " -n 1 -r >&2
        echo >&2
        if [[ $REPLY =~ ^[Nn]$ ]]; then
            echo "Aborted. Specify a NAT Gateway with: NAT_GATEWAY=nat-xxx $0 start" >&2
            exit 1
        fi
        echo "$nat_gw_id"
        return 0
    fi

    echo ""
}

create_nat_gateway() {
    local vpc_id="$1"

    local public_subnet_id
    public_subnet_id=$(aws ec2 describe-route-tables \
        --filters "Name=vpc-id,Values=${vpc_id}" \
        --query "RouteTables[?Routes[?GatewayId!=null && starts_with(GatewayId, 'igw-')]].Associations[0].SubnetId" \
        --output text --region "${REGION}" | head -1)

    if [ -z "$public_subnet_id" ] || [ "$public_subnet_id" = "None" ]; then
        echo "ERROR: Could not find a public subnet in VPC ${vpc_id}" >&2
        echo "A public subnet (with Internet Gateway route) is required to create a NAT Gateway." >&2
        return 1
    fi

    echo "" >&2
    echo "No NAT Gateway found. A new one will be created:" >&2
    echo "  VPC:    ${vpc_id}" >&2
    echo "  Subnet: ${public_subnet_id}" >&2
    echo "" >&2
    echo "Note: NAT Gateways incur AWS charges (~\$0.045/hr + data transfer)." >&2
    echo "" >&2
    read -p "Create NAT Gateway? [y/N] " -n 1 -r >&2
    echo >&2
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Aborted." >&2
        exit 1
    fi

    echo "Creating NAT Gateway..." >&2
    echo "Using public subnet for NAT Gateway: ${public_subnet_id}" >&2

    local eip_alloc_id
    eip_alloc_id=$(aws ec2 allocate-address \
        --domain vpc \
        --tag-specifications "ResourceType=elastic-ip,Tags=[{Key=Name,Value=ctr-staging-replica-nat},{Key=role,Value=ctr-staging-replica}]" \
        --query 'AllocationId' \
        --output text \
        --region "${REGION}")
    echo "Allocated Elastic IP: ${eip_alloc_id}" >&2

    local nat_gw_id
    nat_gw_id=$(aws ec2 create-nat-gateway \
        --subnet-id "${public_subnet_id}" \
        --allocation-id "${eip_alloc_id}" \
        --tag-specifications "ResourceType=natgateway,Tags=[{Key=Name,Value=ctr-staging-replica-nat},{Key=role,Value=ctr-staging-replica}]" \
        --query 'NatGateway.NatGatewayId' \
        --output text \
        --region "${REGION}")
    echo "Created NAT Gateway: ${nat_gw_id}" >&2

    echo "Waiting for NAT Gateway to become available..." >&2
    aws ec2 wait nat-gateway-available --nat-gateway-ids "${nat_gw_id}" --region "${REGION}"
    echo "NAT Gateway is available" >&2

    echo "$nat_gw_id"
}

ensure_nat_route() {
    local route_table_id="$1"
    local nat_gw_id="$2"

    local existing_nat
    existing_nat=$(aws ec2 describe-route-tables \
        --route-table-ids "${route_table_id}" \
        --query "RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'].NatGatewayId" \
        --output text --region "${REGION}" 2>/dev/null || true)

    if [ "$existing_nat" = "$nat_gw_id" ]; then
        echo "Route to NAT Gateway already exists"
        return 0
    fi

    if [ -n "$existing_nat" ] && [ "$existing_nat" != "None" ]; then
        echo "Replacing existing NAT route..."
        aws ec2 delete-route \
            --route-table-id "${route_table_id}" \
            --destination-cidr-block 0.0.0.0/0 \
            --region "${REGION}" 2>/dev/null || true
    fi

    echo "Adding route to NAT Gateway..."
    aws ec2 create-route \
        --route-table-id "${route_table_id}" \
        --destination-cidr-block 0.0.0.0/0 \
        --nat-gateway-id "${nat_gw_id}" \
        --region "${REGION}" >/dev/null
}

ensure_internet_access() {
    SUBNET_ID=$(discover_subnet)
    if [ -z "$SUBNET_ID" ]; then
        exit 1
    fi

    local vpc_id
    vpc_id=$(aws ec2 describe-subnets --subnet-ids "${SUBNET_ID}" \
        --query 'Subnets[0].VpcId' --output text --region "${REGION}")

    local route_table_id
    route_table_id=$(aws ec2 describe-route-tables \
        --filters "Name=association.subnet-id,Values=${SUBNET_ID}" \
        --query 'RouteTables[0].RouteTableId' \
        --output text --region "${REGION}" 2>/dev/null || true)

    if [ -z "$route_table_id" ] || [ "$route_table_id" = "None" ]; then
        route_table_id=$(aws ec2 describe-route-tables \
            --filters "Name=vpc-id,Values=${vpc_id}" "Name=association.main,Values=true" \
            --query 'RouteTables[0].RouteTableId' \
            --output text --region "${REGION}")
    fi

    local igw_route
    igw_route=$(aws ec2 describe-route-tables \
        --route-table-ids "${route_table_id}" \
        --query "RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'].GatewayId" \
        --output text --region "${REGION}" 2>/dev/null || true)

    if [ -n "$igw_route" ] && [ "$igw_route" != "None" ] && [[ "$igw_route" == igw-* ]]; then
        echo "Subnet has Internet Gateway access: ${igw_route}"
        return 0
    fi

    local nat_gw_id
    nat_gw_id=$(discover_nat_gateway "$vpc_id")

    if [ -z "$nat_gw_id" ]; then
        nat_gw_id=$(create_nat_gateway "$vpc_id")
    fi

    ensure_nat_route "$route_table_id" "$nat_gw_id"
}

ensure_security_group() {
    echo "Checking security group..."
    SG_ID=$(aws ec2 describe-security-groups \
        --filters "Name=group-name,Values=${SECURITY_GROUP_NAME}" \
        --query 'SecurityGroups[0].GroupId' \
        --output text \
        --region "${REGION}" 2>/dev/null || true)

    if [ -z "$SG_ID" ] || [ "$SG_ID" = "None" ]; then
        echo "Creating security group ${SECURITY_GROUP_NAME}..."
        VPC_ID=$(aws ec2 describe-subnets --subnet-ids "${SUBNET_ID}" \
            --query 'Subnets[0].VpcId' --output text --region "${REGION}")

        SG_ID=$(aws ec2 create-security-group \
            --group-name "${SECURITY_GROUP_NAME}" \
            --description "Security group for ctr-staging-replica (SSH only)" \
            --vpc-id "${VPC_ID}" \
            --query 'GroupId' \
            --output text \
            --region "${REGION}")

        aws ec2 authorize-security-group-ingress \
            --group-id "${SG_ID}" \
            --protocol tcp \
            --port 22 \
            --cidr 0.0.0.0/0 \
            --region "${REGION}"

        echo "Created security group ${SG_ID}"
    else
        echo "Using existing security group ${SG_ID}"
    fi
}

get_instance_info() {
    local name="$1"
    aws ec2 describe-instances \
        --filters "Name=tag:Name,Values=${name}" \
        "Name=instance-state-name,Values=pending,running,stopping,stopped" \
        --query 'Reservations[].Instances[].[InstanceId,State.Name]' \
        --output text \
        --region "${REGION}" 2>/dev/null || true
}

get_ami() {
    aws ec2 describe-images \
        --owners 099720109477 \
        --filters \
        "Name=name,Values=ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*" \
        "Name=architecture,Values=x86_64" \
        "Name=virtualization-type,Values=hvm" \
        "Name=state,Values=available" \
        --query 'Images | sort_by(@, &CreationDate) | [-1].ImageId' \
        --output text \
        --region "${REGION}"
}

wait_for_instance() {
    local instance_id="$1"

    echo "Waiting for instance ${instance_id} to be running..."
    aws ec2 wait instance-running --instance-ids "${instance_id}" --region "${REGION}"

    echo "Waiting for Tailscale SSH access to ${INSTANCE_NAME}..."
    local max_wait=300
    local wait_interval=10
    local elapsed=0

    while [ $elapsed -lt $max_wait ]; do
        if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "ubuntu@${INSTANCE_NAME}" true 2>/dev/null; then
            echo "Machine ${INSTANCE_NAME} is accessible via Tailscale SSH"
            return 0
        fi

        echo "  Waiting for ${INSTANCE_NAME}... ($elapsed/$max_wait seconds)"
        sleep $wait_interval
        elapsed=$((elapsed + wait_interval))
    done

    echo "ERROR: Machine ${INSTANCE_NAME} not accessible via Tailscale after ${max_wait} seconds"
    return 1
}

generate_user_data() {
    cat <<'USERDATA_START'
#cloud-config
users:
  - name: ubuntu
    ssh_authorized_keys:
      - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOEetwKXuTe+byx+VJTOn3ZxjVnpMe/82YroL111tTwK ubuntu@exed-01
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash

package_update: true
package_upgrade: false

packages:
  - curl
  - jq
  - zfsutils-linux

USERDATA_START

    cat <<USERDATA_SCRIPT
write_files:
  - path: /root/setup.sh
    content: |
      #!/bin/bash
      set -euo pipefail

      echo "Starting Tailscale setup..."
      curl -fsSL https://tailscale.com/install.sh | sh

      echo "Generating Tailscale auth key via OAuth..."
      OAUTH_RESPONSE=\$(curl -s -w "\n%{http_code}" -X POST \
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
          exit 1
      fi

      KEY_RESPONSE=\$(curl -s -w "\n%{http_code}" -X POST \
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
          exit 1
      fi

      AUTH_KEY=\$(echo "\$KEY_BODY" | jq -r '.key')
      tailscale up --authkey=\$AUTH_KEY --advertise-tags=tag:server --ssh --hostname=${INSTANCE_NAME}
      echo "Tailscale setup complete"

      echo "Creating ZFS pool..."
      zpool create tank /dev/xvdf
      echo "ZFS pool created"
    owner: root:root
    permissions: '0755'

hostname: ${INSTANCE_NAME}

runcmd:
  - /root/setup.sh
USERDATA_SCRIPT
}

create_instance() {
    echo "Creating instance ${INSTANCE_NAME}..." >&2

    local ami_id
    ami_id=$(get_ami)
    echo "Using AMI: ${ami_id}, Instance type: ${INSTANCE_TYPE}" >&2

    local user_data
    user_data=$(generate_user_data)

    local instance_id
    instance_id=$(aws ec2 run-instances \
        --image-id "${ami_id}" \
        --instance-type "${INSTANCE_TYPE}" \
        --subnet-id "${SUBNET_ID}" \
        --security-group-ids "${SG_ID}" \
        --user-data "${user_data}" \
        --block-device-mappings \
        "DeviceName=/dev/sda1,Ebs={VolumeSize=${ROOT_VOLUME_SIZE},VolumeType=gp3,DeleteOnTermination=true}" \
        "DeviceName=/dev/sdf,Ebs={VolumeSize=${DATA_VOLUME_SIZE},VolumeType=gp3,DeleteOnTermination=true}" \
        --tag-specifications \
        "ResourceType=instance,Tags=[{Key=Name,Value=${INSTANCE_NAME}},{Key=role,Value=ctr-staging-replica}]" \
        "ResourceType=volume,Tags=[{Key=Name,Value=${INSTANCE_NAME}-data},{Key=role,Value=ctr-staging-replica}]" \
        --query 'Instances[0].InstanceId' \
        --output text \
        --region "${REGION}")

    echo "Instance ${instance_id} created" >&2
    echo "$instance_id"
}

# Check if instance already exists
info=$(get_instance_info "$INSTANCE_NAME")
if [ -n "$info" ]; then
    instance_id=$(echo "$info" | awk '{print $1}')
    state=$(echo "$info" | awk '{print $2}')
    echo "Instance ${INSTANCE_NAME} already exists: ${instance_id} (${state})"
    exit 0
fi

"${SCRIPT_DIR}/deploy/test-tailscale-oauth.sh"
ensure_internet_access
ensure_security_group

instance_id=$(create_instance)
wait_for_instance "$instance_id"

echo ""
echo "=========================================="
echo "${INSTANCE_NAME} is ready!"
echo "=========================================="
echo ""
echo "Access via Tailscale SSH:"
echo "  ssh ubuntu@${INSTANCE_NAME}"
