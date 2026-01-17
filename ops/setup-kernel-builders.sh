#!/bin/bash
set -euo pipefail

# Setup kernel builders for exelet-fs cross-architecture builds
# Creates/manages buildkit-amd64 and buildkit-arm64 EC2 instances

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration (can be overridden via environment variables)
REGION="${REGION:-us-west-2}"
SUBNET_ID="${SUBNET_ID:-}"     # Auto-discover first subnet if not specified
NAT_GATEWAY="${NAT_GATEWAY:-}" # Auto-discover first NAT Gateway if not specified
SECURITY_GROUP_NAME="buildkit"
ROOT_VOLUME_SIZE="128"
BUILDKIT_PORT=9500
BUILDKIT_VERSION="v0.17.3"

# Helper functions to replace associative arrays (for macOS bash 3.x compatibility)
get_instance_type() {
    case "$1" in
    amd64) echo "t2.xlarge" ;;
    arm64) echo "t4g.xlarge" ;;
    esac
}

get_ami_arch() {
    case "$1" in
    amd64) echo "x86_64" ;;
    arm64) echo "arm64" ;;
    esac
}

get_ami_name_pattern() {
    case "$1" in
    amd64) echo "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*" ;;
    arm64) echo "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-*" ;;
    esac
}

# Global variable for security group ID
SG_ID=""

discover_subnet() {
    if [ -n "$SUBNET_ID" ]; then
        echo "Using specified subnet: ${SUBNET_ID}" >&2
        echo "$SUBNET_ID"
        return 0
    fi

    echo "Discovering subnets in ${REGION}..." >&2

    # Find the first available subnet with its details
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

    # Find the first available NAT Gateway in the VPC with its details
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

    # No NAT Gateway found
    echo ""
}

create_nat_gateway() {
    local vpc_id="$1"

    # Find a public subnet in the same VPC (one with an IGW route)
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

    # Allocate Elastic IP
    echo "Allocating Elastic IP..." >&2
    local eip_alloc_id
    eip_alloc_id=$(aws ec2 allocate-address \
        --domain vpc \
        --tag-specifications "ResourceType=elastic-ip,Tags=[{Key=Name,Value=buildkit-nat},{Key=role,Value=buildkit}]" \
        --query 'AllocationId' \
        --output text \
        --region "${REGION}")
    echo "Allocated Elastic IP: ${eip_alloc_id}" >&2

    # Create NAT Gateway
    echo "Creating NAT Gateway (this may take a few minutes)..." >&2
    local nat_gw_id
    nat_gw_id=$(aws ec2 create-nat-gateway \
        --subnet-id "${public_subnet_id}" \
        --allocation-id "${eip_alloc_id}" \
        --tag-specifications "ResourceType=natgateway,Tags=[{Key=Name,Value=buildkit-nat},{Key=role,Value=buildkit}]" \
        --query 'NatGateway.NatGatewayId' \
        --output text \
        --region "${REGION}")
    echo "Created NAT Gateway: ${nat_gw_id}" >&2

    # Wait for NAT Gateway to become available
    echo "Waiting for NAT Gateway to become available..." >&2
    aws ec2 wait nat-gateway-available --nat-gateway-ids "${nat_gw_id}" --region "${REGION}"
    echo "NAT Gateway is available" >&2

    echo "$nat_gw_id"
}

ensure_nat_route() {
    local route_table_id="$1"
    local nat_gw_id="$2"

    # Check if route already exists
    local existing_nat
    existing_nat=$(aws ec2 describe-route-tables \
        --route-table-ids "${route_table_id}" \
        --query "RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'].NatGatewayId" \
        --output text --region "${REGION}" 2>/dev/null || true)

    if [ "$existing_nat" = "$nat_gw_id" ]; then
        echo "Route to NAT Gateway already exists"
        return 0
    fi

    # Delete existing 0.0.0.0/0 route if it exists (to replace it)
    if [ -n "$existing_nat" ] && [ "$existing_nat" != "None" ]; then
        echo "Replacing existing NAT route..."
        aws ec2 delete-route \
            --route-table-id "${route_table_id}" \
            --destination-cidr-block 0.0.0.0/0 \
            --region "${REGION}" 2>/dev/null || true
    fi

    # Add route to NAT Gateway
    echo "Adding route to NAT Gateway..."
    aws ec2 create-route \
        --route-table-id "${route_table_id}" \
        --destination-cidr-block 0.0.0.0/0 \
        --nat-gateway-id "${nat_gw_id}" \
        --region "${REGION}" >/dev/null
}

ensure_internet_access() {
    # Discover or use specified subnet
    SUBNET_ID=$(discover_subnet)
    if [ -z "$SUBNET_ID" ]; then
        exit 1
    fi

    # Get VPC ID for the subnet
    local vpc_id
    vpc_id=$(aws ec2 describe-subnets --subnet-ids "${SUBNET_ID}" \
        --query 'Subnets[0].VpcId' --output text --region "${REGION}")

    # Get route table for this subnet
    local route_table_id
    route_table_id=$(aws ec2 describe-route-tables \
        --filters "Name=association.subnet-id,Values=${SUBNET_ID}" \
        --query 'RouteTables[0].RouteTableId' \
        --output text --region "${REGION}" 2>/dev/null || true)

    if [ -z "$route_table_id" ] || [ "$route_table_id" = "None" ]; then
        # Use main route table for VPC
        route_table_id=$(aws ec2 describe-route-tables \
            --filters "Name=vpc-id,Values=${vpc_id}" "Name=association.main,Values=true" \
            --query 'RouteTables[0].RouteTableId' \
            --output text --region "${REGION}")
    fi

    # Check if there's already an Internet Gateway route (public subnet)
    local igw_route
    igw_route=$(aws ec2 describe-route-tables \
        --route-table-ids "${route_table_id}" \
        --query "RouteTables[0].Routes[?DestinationCidrBlock=='0.0.0.0/0'].GatewayId" \
        --output text --region "${REGION}" 2>/dev/null || true)

    if [ -n "$igw_route" ] && [ "$igw_route" != "None" ] && [[ "$igw_route" == igw-* ]]; then
        echo "Subnet has Internet Gateway access: ${igw_route}"
        return 0
    fi

    # Discover or use specified NAT Gateway
    local nat_gw_id
    nat_gw_id=$(discover_nat_gateway "$vpc_id")

    if [ -z "$nat_gw_id" ]; then
        # No NAT Gateway found, create one
        nat_gw_id=$(create_nat_gateway "$vpc_id")
    fi

    # Ensure route to NAT Gateway exists
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
            --description "Security group for BuildKit builders (SSH only)" \
            --vpc-id "${VPC_ID}" \
            --query 'GroupId' \
            --output text \
            --region "${REGION}")

        # Allow SSH only (BuildKit accessed via Tailscale)
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
    # Returns: INSTANCE_ID STATE (or empty if not found)
    aws ec2 describe-instances \
        --filters "Name=tag:Name,Values=${name}" \
        "Name=instance-state-name,Values=pending,running,stopping,stopped" \
        --query 'Reservations[].Instances[].[InstanceId,State.Name]' \
        --output text \
        --region "${REGION}" 2>/dev/null || true
}

get_ami() {
    local arch="$1"
    local ami_arch ami_pattern
    ami_arch=$(get_ami_arch "$arch")
    ami_pattern=$(get_ami_name_pattern "$arch")

    aws ec2 describe-images \
        --owners 099720109477 \
        --filters \
        "Name=name,Values=${ami_pattern}" \
        "Name=architecture,Values=${ami_arch}" \
        "Name=virtualization-type,Values=hvm" \
        "Name=state,Values=available" \
        --query 'Images | sort_by(@, &CreationDate) | [-1].ImageId' \
        --output text \
        --region "${REGION}"
}

generate_user_data() {
    local machine_name="$1"

    cat <<'USERDATA_START'
#cloud-config
users:
  - name: ubuntu
    ssh_authorized_keys:
      - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOEetwKXuTe+byx+VJTOn3ZxjVnpMe/82YroL111tTwK ubuntu@exed-01
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    groups: [docker]

package_update: true
package_upgrade: false

packages:
  - curl
  - jq
  - docker.io
  - containerd

write_files:
  - path: /etc/buildkit/buildkitd.toml
    content: |
      [worker.oci]
        enabled = true
        gc = true
        gckeepbytes = 50000000000

      [worker.containerd]
        enabled = false
    owner: root:root
    permissions: '0644'

USERDATA_START

    # Add the systemd service file (needs variable interpolation)
    cat <<USERDATA_SERVICE
  - path: /etc/systemd/system/buildkitd.service
    content: |
      [Unit]
      Description=BuildKit daemon
      After=network.target docker.service
      Requires=docker.service

      [Service]
      Type=simple
      ExecStart=/usr/local/bin/buildkitd --addr tcp://0.0.0.0:${BUILDKIT_PORT} --config /etc/buildkit/buildkitd.toml
      Restart=always
      RestartSec=5

      [Install]
      WantedBy=multi-user.target
    owner: root:root
    permissions: '0644'

USERDATA_SERVICE

    # Add the setup script (needs variable interpolation for OAuth credentials)
    cat <<USERDATA_SCRIPT
  - path: /root/setup-buildkit.sh
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
      tailscale up --authkey=\$AUTH_KEY --advertise-tags=tag:server --ssh --hostname=${machine_name}
      echo "Tailscale setup complete"

      # Install BuildKit
      echo "Installing BuildKit ${BUILDKIT_VERSION}..."
      ARCH=\$(uname -m)
      if [ "\$ARCH" = "x86_64" ]; then
          ARCH="amd64"
      elif [ "\$ARCH" = "aarch64" ]; then
          ARCH="arm64"
      fi

      curl -fsSL "https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-\${ARCH}.tar.gz" | tar -xz -C /usr/local

      systemctl daemon-reload
      systemctl enable buildkitd
      systemctl start buildkitd

      echo "BuildKit setup complete"
    owner: root:root
    permissions: '0755'

USERDATA_SCRIPT

    # Add hostname and runcmd
    cat <<USERDATA_END

hostname: ${machine_name}

runcmd:
  - /root/setup-buildkit.sh
USERDATA_END
}

create_instance() {
    local arch="$1"
    local machine_name="buildkit-${arch}"

    echo "Creating instance ${machine_name}..." >&2

    local ami_id instance_type
    ami_id=$(get_ami "$arch")
    instance_type=$(get_instance_type "$arch")
    echo "Using AMI: ${ami_id}, Instance type: ${instance_type}" >&2

    local user_data
    user_data=$(generate_user_data "$machine_name")

    local instance_id
    instance_id=$(aws ec2 run-instances \
        --image-id "${ami_id}" \
        --instance-type "${instance_type}" \
        --subnet-id "${SUBNET_ID}" \
        --security-group-ids "${SG_ID}" \
        --user-data "${user_data}" \
        --block-device-mappings \
        "DeviceName=/dev/sda1,Ebs={VolumeSize=${ROOT_VOLUME_SIZE},VolumeType=gp3,DeleteOnTermination=true}" \
        --tag-specifications \
        "ResourceType=instance,Tags=[{Key=Name,Value=${machine_name}},{Key=role,Value=buildkit},{Key=arch,Value=${arch}}]" \
        "ResourceType=volume,Tags=[{Key=Name,Value=${machine_name}-data},{Key=role,Value=buildkit},{Key=arch,Value=${arch}}]" \
        --query 'Instances[0].InstanceId' \
        --output text \
        --region "${REGION}")

    echo "Instance ${instance_id} created" >&2
    echo "$instance_id"
}

start_instance() {
    local instance_id="$1"
    local machine_name="$2"

    echo "Starting stopped instance ${instance_id} (${machine_name})..."
    aws ec2 start-instances --instance-ids "${instance_id}" --region "${REGION}" >/dev/null
}

wait_for_instance() {
    local machine_name="$1"
    local instance_id="$2"

    echo "Waiting for instance ${instance_id} to be running..."
    aws ec2 wait instance-running --instance-ids "${instance_id}" --region "${REGION}"

    echo "Waiting for Tailscale SSH access to ${machine_name}..."
    local max_wait=300 # 5 minutes
    local wait_interval=10
    local elapsed=0

    while [ $elapsed -lt $max_wait ]; do
        if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null "ubuntu@${machine_name}" true 2>/dev/null; then
            echo "Machine ${machine_name} is accessible via Tailscale SSH"
            return 0
        fi

        echo "  Waiting for ${machine_name}... ($elapsed/$max_wait seconds)"
        sleep $wait_interval
        elapsed=$((elapsed + wait_interval))
    done

    echo "ERROR: Machine ${machine_name} not accessible via Tailscale after ${max_wait} seconds"
    return 1
}

wait_for_buildkit() {
    local machine_name="$1"

    echo "Waiting for BuildKit to be ready on ${machine_name}..."
    local max_wait=120
    local elapsed=0

    while [ $elapsed -lt $max_wait ]; do
        if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
            "ubuntu@${machine_name}" "systemctl is-active buildkitd" 2>/dev/null | grep -q "active"; then
            echo "BuildKit is running on ${machine_name}"
            return 0
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done

    echo "WARNING: BuildKit not ready on ${machine_name} after ${max_wait} seconds"
    return 1
}

stop_builders() {
    # First, collect info about all instances
    local instances_to_stop=""

    for arch in amd64 arm64; do
        local machine_name="buildkit-${arch}"
        local info
        info=$(get_instance_info "$machine_name")

        if [ -n "$info" ]; then
            local instance_id state
            instance_id=$(echo "$info" | awk '{print $1}')
            state=$(echo "$info" | awk '{print $2}')

            if [ "$state" = "running" ]; then
                instances_to_stop="${instances_to_stop}  ${machine_name} (${instance_id})\n"
            fi
        fi
    done

    if [ -z "$instances_to_stop" ]; then
        echo "All BuildKit builders are already stopped."
        return 0
    fi

    echo "The following instances will be stopped:"
    printf "$instances_to_stop"
    echo ""
    read -p "Stop these instances? [y/N] " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Aborted."
        return 0
    fi

    # Now stop the running instances
    for arch in amd64 arm64; do
        local machine_name="buildkit-${arch}"
        local info
        info=$(get_instance_info "$machine_name")

        if [ -n "$info" ]; then
            local instance_id state
            instance_id=$(echo "$info" | awk '{print $1}')
            state=$(echo "$info" | awk '{print $2}')

            if [ "$state" = "running" ]; then
                echo "Stopping ${machine_name} (${instance_id})..."
                aws ec2 stop-instances --instance-ids "${instance_id}" --region "${REGION}" >/dev/null
                echo "  Stopped"
            fi
        fi
    done
}

stop_builders_no_confirm() {
    local stopped_any=false

    for arch in amd64 arm64; do
        local machine_name="buildkit-${arch}"
        local info
        info=$(get_instance_info "$machine_name")

        if [ -n "$info" ]; then
            local instance_id state
            instance_id=$(echo "$info" | awk '{print $1}')
            state=$(echo "$info" | awk '{print $2}')

            if [ "$state" = "running" ]; then
                echo "Stopping ${machine_name} (${instance_id})..."
                aws ec2 stop-instances --instance-ids "${instance_id}" --region "${REGION}" >/dev/null
                echo "  Stopped"
                stopped_any=true
            fi
        fi
    done

    if [ "$stopped_any" = "false" ]; then
        echo "All BuildKit builders are already stopped."
    fi
}

ensure_buildx_builder() {
    local builder_name="exe"

    # Check if builder already exists with correct endpoints
    if docker buildx inspect "${builder_name}" >/dev/null 2>&1; then
        local inspect_output
        inspect_output=$(docker buildx inspect "${builder_name}" 2>/dev/null)

        # Check if both endpoints are configured
        if echo "$inspect_output" | grep -q "buildkit-amd64:${BUILDKIT_PORT}" &&
            echo "$inspect_output" | grep -q "buildkit-arm64:${BUILDKIT_PORT}"; then
            echo "Builder '${builder_name}' is already configured."
            return 0
        fi

        # Builder exists but misconfigured, remove it
        echo "Removing misconfigured builder '${builder_name}'..."
        docker buildx rm "${builder_name}" >/dev/null 2>&1 || true
    fi

    echo "Setting up docker buildx builder '${builder_name}'..."

    # Create builder with amd64 node
    echo "Creating builder with amd64 node..."
    docker buildx create \
        --name "${builder_name}" \
        --driver remote \
        --platform linux/amd64 \
        "tcp://buildkit-amd64:${BUILDKIT_PORT}"

    # Append arm64 node
    echo "Appending arm64 node..."
    docker buildx create \
        --name "${builder_name}" \
        --append \
        --driver remote \
        --platform linux/arm64 \
        "tcp://buildkit-arm64:${BUILDKIT_PORT}"

    echo ""
    echo "Multi-arch builder '${builder_name}' is ready!"
    echo "Platforms: linux/amd64, linux/arm64"
    docker buildx inspect "${builder_name}"
}

start_single_builder() {
    local arch="$1"
    local machine_name="buildkit-${arch}"
    local info instance_id state

    info=$(get_instance_info "$machine_name")

    if [ -z "$info" ]; then
        # Instance doesn't exist - create it
        echo "[${arch}] Instance not found, creating..."
        instance_id=$(create_instance "$arch")
        wait_for_instance "$machine_name" "$instance_id"
    else
        instance_id=$(echo "$info" | awk '{print $1}')
        state=$(echo "$info" | awk '{print $2}')

        case "$state" in
        running)
            echo "[${arch}] Instance is already running"
            ;;
        stopped)
            start_instance "$instance_id" "$machine_name"
            wait_for_instance "$machine_name" "$instance_id"
            ;;
        stopping)
            echo "[${arch}] Instance is stopping, waiting..."
            aws ec2 wait instance-stopped --instance-ids "${instance_id}" --region "${REGION}"
            start_instance "$instance_id" "$machine_name"
            wait_for_instance "$machine_name" "$instance_id"
            ;;
        pending)
            echo "[${arch}] Instance is starting..."
            wait_for_instance "$machine_name" "$instance_id"
            ;;
        esac
    fi

    wait_for_buildkit "$machine_name"
    echo "[${arch}] Builder ready"
}

start_builders() {
    # First pass: check instance states
    local all_running=true
    local need_create=false

    for arch in amd64 arm64; do
        local machine_name="buildkit-${arch}"
        local info
        info=$(get_instance_info "$machine_name")

        if [ -z "$info" ]; then
            all_running=false
            need_create=true
        else
            local state
            state=$(echo "$info" | awk '{print $2}')
            if [ "$state" != "running" ]; then
                all_running=false
            fi
        fi
    done

    # If all running, just ensure builder is configured
    if [ "$all_running" = "true" ]; then
        echo "All BuildKit builders are already running:"
        for arch in amd64 arm64; do
            local machine_name="buildkit-${arch}"
            local info instance_id
            info=$(get_instance_info "$machine_name")
            instance_id=$(echo "$info" | awk '{print $1}')
            echo "  ${machine_name} (${instance_id})"
        done
        echo ""
        ensure_buildx_builder
        return 0
    fi

    # Only validate OAuth and setup infra if we need to create instances
    if [ "$need_create" = "true" ]; then
        "${SCRIPT_DIR}/deploy/test-tailscale-oauth.sh"
        ensure_internet_access
        ensure_security_group
    fi

    # Start both builders in parallel
    echo "Starting builders in parallel..."
    start_single_builder "amd64" &
    local pid_amd64=$!
    start_single_builder "arm64" &
    local pid_arm64=$!

    # Wait for both to complete
    local failed=false
    if ! wait $pid_amd64; then
        echo "ERROR: amd64 builder failed to start"
        failed=true
    fi
    if ! wait $pid_arm64; then
        echo "ERROR: arm64 builder failed to start"
        failed=true
    fi

    if [ "$failed" = "true" ]; then
        exit 1
    fi

    echo ""
    echo "=========================================="
    echo "BuildKit builders are ready!"
    echo "=========================================="
    echo ""
    echo "Builder endpoints (via Tailscale):"
    echo "  AMD64: tcp://buildkit-amd64:${BUILDKIT_PORT}"
    echo "  ARM64: tcp://buildkit-arm64:${BUILDKIT_PORT}"
    echo ""

    # Ensure multi-arch buildx builder is configured
    ensure_buildx_builder

    echo ""
    echo "To stop builders when done:"
    echo "  $0 stop"
}

main() {
    local action="${1:-start}"
    local yes_flag="${2:-}"

    case "$action" in
    start)
        start_builders
        ;;
    stop)
        if [ "$yes_flag" = "-y" ] || [ "$yes_flag" = "--yes" ]; then
            stop_builders_no_confirm
        else
            stop_builders
        fi
        ;;
    *)
        echo "Usage: $0 [start|stop] [-y|--yes]"
        echo "  start      - Start or create BuildKit builder instances (default)"
        echo "  stop       - Stop BuildKit builder instances (with confirmation)"
        echo "  stop -y    - Stop without confirmation"
        exit 1
        ;;
    esac
}

main "$@"
