#!/bin/bash
set -euo pipefail

# Check for machine name parameter
if [ $# -ne 1 ]; then
    echo "Usage: $0 <machine-name>"
    echo "Machine name must be in format: exed-NN (where NN is a number)"
    exit 1
fi

MACHINE_NAME="$1"

# Validate machine name format
if ! [[ "$MACHINE_NAME" =~ ^exed-[0-9]+$ ]]; then
    echo "Error: Machine name must be in format exed-NN (e.g., exed-01)"
    exit 1
fi

# Get the directory of this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Run the Tailscale OAuth preflight check
"${SCRIPT_DIR}/test-tailscale-oauth.sh"

# Configuration
REGION="us-west-2"
AZ="us-west-2b"
INSTANCE_TYPE="c7a.xlarge"
ROOT_VOLUME_SIZE="75"
SECURITY_GROUP_NAME="exed"
INSTANCE_ROLE_NAME="exed-instance-role"
INSTANCE_PROFILE_NAME="exed-instance-profile"
# Use the private subnet with NAT Gateway
SUBNET_ID="subnet-0cd09e0183036c52b"

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
    # Trust policy: allows EC2 instances to assume this role
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
      }'

    # Permissions policy: what the role can do
    aws iam put-role-policy \
        --role-name ${INSTANCE_ROLE_NAME} \
        --policy-name "route53-access" \
        --policy-document '{
          "Version": "2012-10-17",
          "Statement": [
              {
                  "Effect": "Allow",
                  "Action": [
                      "route53:ChangeResourceRecordSets"
                  ],
                  "Resource": "arn:aws:route53:::hostedzone/*"
              },
              {
                  "Effect": "Allow",
                  "Action": [
                      "route53:ListHostedZones",
                      "route53:ListHostedZonesByName",
                      "route53:ListResourceRecordSets"
                  ],
                  "Resource": "*"
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
  - binutils
  - sqlite3
  - net-tools
  - docker.io

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

# Install and configure node_exporter for monitoring
# Note: exed listens on most ports for proxy, so use port 19100 instead of 9100
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
# Note: exed uses port 19100 instead of 9100 because exed listens on most ports for proxy
cat <<'WRAPPER' | sudo tee /usr/local/bin/node-exporter-wrapper > /dev/null
#!/bin/bash
TAILSCALE_IP=$(tailscale ip -4)
if [ -z "$TAILSCALE_IP" ]; then
    echo "ERROR: Failed to get Tailscale IP" >&2
    exit 1
fi
exec /usr/bin/prometheus-node-exporter --web.listen-address=${TAILSCALE_IP}:19100 --collector.cgroups --collector.systemd "$@"
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
echo "node_exporter should be listening on Tailscale IP: $TAILSCALE_IP:19100"
echo "Verifying node-exporter is running..."
for i in $(seq 1 300); do
    if curl -s http://${TAILSCALE_IP}:19100/metrics | head -n 3; then
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
echo "The machine is ready to deploy exed."
echo ""
echo "!! NOTE: make sure to place exed credentials in /etc/default/exed before deploy!!"
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
