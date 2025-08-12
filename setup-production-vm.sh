#!/bin/bash
# Setup script for production VM running exed
# This creates a VM in the same region as the GKE cluster with proper networking

set -e

# Configuration
PROJECT_ID="exe-dev-468515"
INSTANCE_NAME="exed-prod-01"
ZONE="us-west2-a"
REGION="us-west2"
MACHINE_TYPE="n2-standard-2"
NETWORK_TAG="exed-server"
SERVICE_ACCOUNT_NAME="exed-vm"

# Check for Tailscale auth key
if [ -z "$1" ]; then
    echo "ERROR: Tailscale auth key required"
    echo "Usage: $0 <tailscale-auth-key>"
    echo ""
    echo "Get an auth key from: https://login.tailscale.com/admin/settings/keys"
    echo "Make sure to create a key with 'tag:server' tag"
    exit 1
fi

TAILSCALE_AUTH_KEY="$1"

echo "==========================================="
echo "Production VM Setup for exe.dev"
echo "==========================================="
echo ""
echo "Project: $PROJECT_ID"
echo "Instance: $INSTANCE_NAME"
echo "Zone: $ZONE"
echo "Tailscale: Will configure with tag:server"
echo ""

# Check if gcloud is configured
if ! gcloud auth list --filter=status:ACTIVE --format="value(account)" | grep -q .; then
    echo "ERROR: No active gcloud authentication found."
    echo "Please run: gcloud auth login"
    exit 1
fi

# Set project
gcloud config set project $PROJECT_ID

# Create service account if it doesn't exist
echo "Step 1: Creating service account..."
if ! gcloud iam service-accounts describe ${SERVICE_ACCOUNT_NAME}@${PROJECT_ID}.iam.gserviceaccount.com &>/dev/null; then
    gcloud iam service-accounts create ${SERVICE_ACCOUNT_NAME} \
        --display-name="exe.dev VM Service Account" \
        --description="Service account for exed production VM"
    
    # Grant necessary permissions
    gcloud projects add-iam-policy-binding ${PROJECT_ID} \
        --member="serviceAccount:${SERVICE_ACCOUNT_NAME}@${PROJECT_ID}.iam.gserviceaccount.com" \
        --role="roles/container.developer"
    
    gcloud projects add-iam-policy-binding ${PROJECT_ID} \
        --member="serviceAccount:${SERVICE_ACCOUNT_NAME}@${PROJECT_ID}.iam.gserviceaccount.com" \
        --role="roles/logging.logWriter"
    
    gcloud projects add-iam-policy-binding ${PROJECT_ID} \
        --member="serviceAccount:${SERVICE_ACCOUNT_NAME}@${PROJECT_ID}.iam.gserviceaccount.com" \
        --role="roles/monitoring.metricWriter"
    
    echo "Service account created and configured"
else
    echo "Service account already exists"
fi

# Reserve static external IP
echo ""
echo "Step 2: Reserving static IP address..."
if ! gcloud compute addresses describe exed-prod-ip --region=$REGION &>/dev/null; then
    gcloud compute addresses create exed-prod-ip \
        --region=$REGION \
        --network-tier=PREMIUM
    echo "Static IP reserved"
else
    echo "Static IP already reserved"
fi

EXTERNAL_IP=$(gcloud compute addresses describe exed-prod-ip --region=$REGION --format="value(address)")
echo "External IP: $EXTERNAL_IP"

# Create firewall rules
echo ""
echo "Step 3: Creating firewall rules..."

# HTTP/HTTPS rule
if ! gcloud compute firewall-rules describe allow-exed-http &>/dev/null; then
    gcloud compute firewall-rules create allow-exed-http \
        --allow=tcp:80,tcp:443 \
        --source-ranges=0.0.0.0/0 \
        --target-tags=$NETWORK_TAG \
        --description="Allow HTTP/HTTPS to exed servers"
fi

# SSH rule (port 22 for the SSH service, not VM SSH)
if ! gcloud compute firewall-rules describe allow-exed-ssh &>/dev/null; then
    gcloud compute firewall-rules create allow-exed-ssh \
        --allow=tcp:22 \
        --source-ranges=0.0.0.0/0 \
        --target-tags=$NETWORK_TAG \
        --description="Allow SSH to exed service"
fi

# Admin SSH rule (port 22222 for VM management)
if ! gcloud compute firewall-rules describe allow-exed-admin-ssh &>/dev/null; then
    gcloud compute firewall-rules create allow-exed-admin-ssh \
        --allow=tcp:22222 \
        --source-ranges=0.0.0.0/0 \
        --target-tags=$NETWORK_TAG \
        --description="Allow admin SSH to VM"
fi

# Create VM instance
echo ""
echo "Step 4: Creating VM instance..."

# Check if instance already exists
if gcloud compute instances describe $INSTANCE_NAME --zone=$ZONE &>/dev/null; then
    echo "Instance $INSTANCE_NAME already exists. Skipping creation."
else
    # Create startup script with Tailscale key embedded
    cat > /tmp/startup-script.sh << STARTUP_SCRIPT
#!/bin/bash
set -e

# Update system
apt-get update
apt-get upgrade -y

# Install required packages
apt-get install -y \
    curl \
    wget \
    git \
    build-essential \
    supervisor \
    htop \
    net-tools \
    jq

# Install Tailscale
echo "Installing Tailscale..."
curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/jammy.noarmor.gpg | tee /usr/share/keyrings/tailscale-archive-keyring.gpg >/dev/null
curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/jammy.tailscale-keyring.list | tee /etc/apt/sources.list.d/tailscale.list
apt-get update
apt-get install -y tailscale

# Configure Tailscale with auth key, tag:server, and SSH enabled
echo "Configuring Tailscale..."
tailscale up --auth-key="${TAILSCALE_AUTH_KEY}" --advertise-tags=tag:server --hostname=exed-prod-01 --accept-routes --accept-dns=false --ssh

# Wait for Tailscale to connect
sleep 5
tailscale status

# Ensure Tailscale SSH is enabled
tailscale set --ssh

# Install Google Cloud SDK and GKE auth plugin
if ! command -v gcloud &> /dev/null; then
    echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | tee -a /etc/apt/sources.list.d/google-cloud-sdk.list
    curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key --keyring /usr/share/keyrings/cloud.google.gpg add -
    apt-get update && apt-get install -y google-cloud-sdk google-cloud-sdk-gke-gcloud-auth-plugin kubectl
else
    # Ensure GKE auth plugin is installed even if gcloud already exists
    if ! command -v gke-gcloud-auth-plugin &> /dev/null; then
        echo "Installing GKE auth plugin..."
        apt-get update && apt-get install -y google-cloud-sdk-gke-gcloud-auth-plugin
    fi
fi

# Configure SSH to run on port 22222 (to free up port 22 for exed)
sed -i 's/^#*Port .*/Port 22222/' /etc/ssh/sshd_config
sed -i 's/^#*PubkeyAuthentication .*/PubkeyAuthentication yes/' /etc/ssh/sshd_config
sed -i 's/^#*PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config
systemctl restart sshd || systemctl restart ssh

# Create ubuntu user if it doesn't exist
if ! id -u ubuntu &>/dev/null; then
    useradd -m -s /bin/bash ubuntu
    usermod -aG sudo ubuntu
    echo "ubuntu ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/ubuntu
fi

# Create directories
mkdir -p /home/ubuntu/.ssh
mkdir -p /var/log/exed

# Copy SSH keys from metadata to ubuntu user
echo "Setting up SSH keys for ubuntu user..."
# Get SSH keys from instance metadata
curl -s "http://metadata.google.internal/computeMetadata/v1/instance/attributes/ssh-keys" -H "Metadata-Flavor: Google" | while IFS= read -r line; do
    # Extract username and key
    if [[ "$line" =~ ^([^:]+):(.+)$ ]]; then
        user="${BASH_REMATCH[1]}"
        key="${BASH_REMATCH[2]}"
        # Add all keys to ubuntu user
        echo "$key" >> /home/ubuntu/.ssh/authorized_keys
    fi
done

# Also check for project-wide SSH keys
curl -s "http://metadata.google.internal/computeMetadata/v1/project/attributes/ssh-keys" -H "Metadata-Flavor: Google" | while IFS= read -r line; do
    if [[ "$line" =~ ^([^:]+):(.+)$ ]]; then
        key="${BASH_REMATCH[2]}"
        echo "$key" >> /home/ubuntu/.ssh/authorized_keys
    fi
done 2>/dev/null || true

# Fix permissions
chown -R ubuntu:ubuntu /home/ubuntu/.ssh
chmod 700 /home/ubuntu/.ssh
chmod 600 /home/ubuntu/.ssh/authorized_keys 2>/dev/null || true

# Set up systemd service for exed
cat > /etc/systemd/system/exed.service << 'EOF'
[Unit]
Description=exe.dev server
After=network.target
Wants=network-online.target

[Service]
Type=simple
User=ubuntu
Group=ubuntu
WorkingDirectory=/home/ubuntu
Environment="GOOGLE_CLOUD_PROJECT=exe-dev-468515"
Environment="GKE_CLUSTER_NAME=exe-cluster"
Environment="GKE_CLUSTER_LOCATION=us-west2-a"
Environment="ENABLE_SANDBOX=true"
Environment="STORAGE_CLASS_NAME=standard-rwo"
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
Environment="USE_GKE_GCLOUD_AUTH_PLUGIN=True"
# Porkbun API credentials for wildcard certificates (replace with actual values)
# Environment="PORKBUN_API_KEY=your-api-key-here"
# Environment="PORKBUN_SECRET_API_KEY=your-secret-key-here"

# Use the latest timestamp version
ExecStart=/bin/bash -c 'exec "$(ls -t /home/ubuntu/exed.* | head -n1)" -http= -https=:443 -ssh=:22 -db=/home/ubuntu/exe.db'

Restart=always
RestartSec=5
StandardOutput=append:/var/log/exed/exed.log
StandardError=append:/var/log/exed/exed.error.log

# Security settings
NoNewPrivileges=true
ProtectHome=no

# Allow binding to privileged ports
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

# Set up log rotation
cat > /etc/logrotate.d/exed << 'EOF'
/var/log/exed/*.log {
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    create 0644 ubuntu ubuntu
    sharedscripts
    postrotate
        systemctl reload exed 2>/dev/null || true
    endscript
}
EOF

# Note: nginx configuration removed - exed handles HTTPS directly on port 443

# Set proper permissions
chown -R ubuntu:ubuntu /home/ubuntu
chown -R ubuntu:ubuntu /var/log/exed

# Enable and reload systemd
systemctl daemon-reload
systemctl enable exed

echo "VM setup complete. Ready for exed deployment."

# Show Tailscale status
echo ""
echo "Tailscale configuration:"
tailscale status || echo "Tailscale status will be available after VM boots"
STARTUP_SCRIPT

    # Get current user's SSH key for initial access
    if [ -f ~/.ssh/id_rsa.pub ]; then
        SSH_KEY=$(cat ~/.ssh/id_rsa.pub)
    elif [ -f ~/.ssh/id_ed25519.pub ]; then
        SSH_KEY=$(cat ~/.ssh/id_ed25519.pub)
    else
        echo "WARNING: No SSH key found in ~/.ssh/"
        echo "You may need to use 'gcloud compute ssh' for initial access"
        SSH_KEY=""
    fi
    
    # Create the instance
    if [ -n "$SSH_KEY" ]; then
        # Create with SSH key
        gcloud compute instances create $INSTANCE_NAME \
            --zone=$ZONE \
            --machine-type=$MACHINE_TYPE \
            --network-interface=address=$EXTERNAL_IP,network-tier=PREMIUM,subnet=default \
            --maintenance-policy=MIGRATE \
            --provisioning-model=STANDARD \
            --service-account=${SERVICE_ACCOUNT_NAME}@${PROJECT_ID}.iam.gserviceaccount.com \
            --scopes=https://www.googleapis.com/auth/cloud-platform \
            --tags=$NETWORK_TAG \
            --create-disk=auto-delete=yes,boot=yes,device-name=$INSTANCE_NAME,image-project=ubuntu-os-cloud,image-family=ubuntu-2204-lts,mode=rw,size=50,type=pd-standard \
            --metadata-from-file startup-script=/tmp/startup-script.sh \
            --metadata ssh-keys="$(whoami):$SSH_KEY" \
            --shielded-secure-boot \
            --shielded-vtpm \
            --shielded-integrity-monitoring \
            --reservation-affinity=any
    else
        # Create without SSH key (will use gcloud SSH)
        gcloud compute instances create $INSTANCE_NAME \
            --zone=$ZONE \
            --machine-type=$MACHINE_TYPE \
            --network-interface=address=$EXTERNAL_IP,network-tier=PREMIUM,subnet=default \
            --maintenance-policy=MIGRATE \
            --provisioning-model=STANDARD \
            --service-account=${SERVICE_ACCOUNT_NAME}@${PROJECT_ID}.iam.gserviceaccount.com \
            --scopes=https://www.googleapis.com/auth/cloud-platform \
            --tags=$NETWORK_TAG \
            --create-disk=auto-delete=yes,boot=yes,device-name=$INSTANCE_NAME,image-project=ubuntu-os-cloud,image-family=ubuntu-2204-lts,mode=rw,size=50,type=pd-standard \
            --metadata-from-file startup-script=/tmp/startup-script.sh \
            --shielded-secure-boot \
            --shielded-vtpm \
            --shielded-integrity-monitoring \
            --reservation-affinity=any
    fi
    
    echo "Instance created. Waiting for startup script to complete..."
    sleep 60
fi

# Get instance details
echo ""
echo "Step 5: Instance Information"
echo "============================="
echo "Instance name: $INSTANCE_NAME"
echo "External IP: $EXTERNAL_IP"
echo "SSH access (admin): ssh -p 22222 ubuntu@$EXTERNAL_IP"
echo ""

# Set up Cloud Load Balancer for HTTPS
echo "Step 6: Setting up HTTPS Load Balancer..."

# Create health check
if ! gcloud compute health-checks describe exed-http-health &>/dev/null; then
    gcloud compute health-checks create http exed-http-health \
        --port=80 \
        --request-path=/health \
        --check-interval=10s \
        --timeout=5s \
        --healthy-threshold=2 \
        --unhealthy-threshold=3
fi

# Create instance group
if ! gcloud compute instance-groups unmanaged describe exed-group --zone=$ZONE &>/dev/null; then
    gcloud compute instance-groups unmanaged create exed-group --zone=$ZONE
    gcloud compute instance-groups unmanaged add-instances exed-group \
        --instances=$INSTANCE_NAME \
        --zone=$ZONE
fi

# Create backend service
if ! gcloud compute backend-services describe exed-backend --global &>/dev/null; then
    gcloud compute backend-services create exed-backend \
        --protocol=HTTP \
        --port-name=http \
        --health-checks=exed-http-health \
        --global
    
    gcloud compute backend-services add-backend exed-backend \
        --instance-group=exed-group \
        --instance-group-zone=$ZONE \
        --global
fi

# Set up named ports
gcloud compute instance-groups unmanaged set-named-ports exed-group \
    --named-ports=http:80,https:443 \
    --zone=$ZONE

# Configure GKE cluster to allow access from this VM
echo ""
echo "Step 7: Configuring GKE cluster network access..."

# Check if GKE cluster exists
if gcloud container clusters describe exe-cluster --zone=$ZONE --project=$PROJECT_ID &>/dev/null; then
    echo "Found GKE cluster 'exe-cluster', configuring network access..."
    
    # Get current authorized networks
    CURRENT_AUTH_NETS=$(gcloud container clusters describe exe-cluster \
        --zone=$ZONE \
        --project=$PROJECT_ID \
        --format="value(masterAuthorizedNetworksConfig.cidrBlocks[].cidrBlock)" 2>/dev/null | tr '\n' ',' | tr ';' ',' | sed 's/,$//')
    
    # Build new authorized networks list
    if [ -z "$CURRENT_AUTH_NETS" ]; then
        AUTH_NETWORKS="$EXTERNAL_IP/32,10.0.0.0/8"
    elif [[ ! "$CURRENT_AUTH_NETS" =~ "$EXTERNAL_IP" ]]; then
        AUTH_NETWORKS="$CURRENT_AUTH_NETS,$EXTERNAL_IP/32"
        if [[ ! "$AUTH_NETWORKS" =~ "10.0.0.0/8" ]]; then
            AUTH_NETWORKS="$AUTH_NETWORKS,10.0.0.0/8"
        fi
    else
        AUTH_NETWORKS="$CURRENT_AUTH_NETS"
        echo "VM IP already in authorized networks"
    fi
    
    # Update cluster to allow access from VM
    gcloud container clusters update exe-cluster \
        --zone=$ZONE \
        --project=$PROJECT_ID \
        --enable-master-authorized-networks \
        --master-authorized-networks="$AUTH_NETWORKS" \
        --no-enable-private-endpoint || echo "Warning: Could not update cluster authorized networks"
    
    echo "GKE cluster configured to allow access from VM IP: $EXTERNAL_IP"
else
    echo "No GKE cluster found, skipping network configuration"
fi

echo ""
echo "==========================================="
echo "Production VM Setup Complete!"
echo "==========================================="
echo ""
echo "Next steps:"
echo "1. Run deploy-binary.sh to deploy the exed binary"
echo "2. Configure DNS to point exe.dev to $EXTERNAL_IP"
echo "3. Set up SSL certificates with: ./setup-tls.sh"
echo ""
echo "VM Access:"
echo "  Public: ssh -p 22222 ubuntu@$EXTERNAL_IP"
echo "  Tailscale: ssh ubuntu@exed-prod-01 (after Tailscale connects)"
echo ""
echo "Service will be available at:"
echo "  HTTP: http://$EXTERNAL_IP"
echo "  HTTPS: https://$EXTERNAL_IP (after SSL setup)"
echo "  SSH: ssh -p 22 user@$EXTERNAL_IP"
echo ""
echo "Tailscale:"
echo "  The VM will join your Tailnet with tag:server"
echo "  Check status: ssh -p 22222 ubuntu@$EXTERNAL_IP 'tailscale status'"
echo "  Access VM privately: ssh ubuntu@exed-prod-01"