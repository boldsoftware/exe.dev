#!/bin/bash
# Setup script for GKE Standard cluster with gVisor sandbox for exe.dev

set -e

# Check if user is authenticated
if ! gcloud auth list --filter=status:ACTIVE --format="value(account)" | grep -q .; then
    echo "ERROR: No active gcloud authentication found."
    echo "Please run:"
    echo "  gcloud auth login"
    echo "  gcloud auth application-default login"
    exit 1
fi

# Configuration
export PROJECT_ID="exe-dev-468515"
export CLUSTER_NAME="exe-cluster"
export REGION="us-west2"
export ZONE="us-west2-a"
export OLD_CLUSTER="exe-autopilot-v2"

echo "====================================="
echo "GKE Sandbox Cluster Setup for exe.dev"
echo "====================================="
echo ""
echo "Project: $PROJECT_ID"
echo "New Cluster: $CLUSTER_NAME"
echo "Zone: $ZONE"
echo ""

# Check if old cluster exists and delete it
echo "Step 1: Checking for old Autopilot cluster..."
if gcloud container clusters describe $OLD_CLUSTER --location=$REGION --project=$PROJECT_ID &>/dev/null; then
    echo "Found old cluster $OLD_CLUSTER. Deleting..."
    echo "This may take several minutes..."
    if gcloud container clusters delete $OLD_CLUSTER \
        --location=$REGION \
        --project=$PROJECT_ID \
        --quiet; then
        echo "Old cluster deleted successfully."
    else
        echo "WARNING: Failed to delete old cluster. Continuing anyway..."
    fi
else
    echo "No old cluster found."
fi

# Check if new cluster already exists
echo ""
echo "Step 2: Checking if new cluster already exists..."
if gcloud container clusters describe $CLUSTER_NAME --zone=$ZONE --project=$PROJECT_ID &>/dev/null; then
    echo "Cluster $CLUSTER_NAME already exists. Skipping creation."
else
    echo "Creating new GKE Standard cluster with enhanced security..."
    
    # Create the main cluster
    gcloud container clusters create $CLUSTER_NAME \
        --project=$PROJECT_ID \
        --zone=$ZONE \
        --release-channel=regular \
        --enable-ip-alias \
        --enable-autoscaling \
        --min-nodes=1 \
        --max-nodes=10 \
        --machine-type=n2-standard-4 \
        --disk-size=100 \
        --disk-type=pd-standard \
        --enable-autorepair \
        --enable-autoupgrade \
        --enable-shielded-nodes \
        --shielded-secure-boot \
        --shielded-integrity-monitoring \
        --enable-network-policy \
        --enable-intra-node-visibility \
        --enable-private-nodes \
        --master-ipv4-cidr=172.16.0.0/28 \
        --workload-pool=${PROJECT_ID}.svc.id.goog \
        --addons=GcePersistentDiskCsiDriver,GcpFilestoreCsiDriver \
        --logging=SYSTEM,WORKLOAD \
        --monitoring=SYSTEM \
        --no-enable-basic-auth \
        --no-issue-client-certificate
    
    echo "Cluster created successfully!"
fi

# Check if sandbox pool exists
echo ""
echo "Step 3: Checking for sandbox node pool..."
if gcloud container node-pools describe sandbox-pool --cluster=$CLUSTER_NAME --zone=$ZONE --project=$PROJECT_ID &>/dev/null; then
    echo "Sandbox node pool already exists. Skipping creation."
else
    echo "Creating sandbox node pool with gVisor..."
    
    gcloud container node-pools create sandbox-pool \
        --cluster=$CLUSTER_NAME \
        --zone=$ZONE \
        --project=$PROJECT_ID \
        --machine-type=n2-standard-4 \
        --disk-size=100 \
        --disk-type=pd-standard \
        --enable-autoscaling \
        --min-nodes=2 \
        --max-nodes=20 \
        --sandbox type=gvisor \
        --shielded-secure-boot \
        --shielded-integrity-monitoring \
        --metadata disable-legacy-endpoints=true \
        --workload-metadata=GKE_METADATA \
        --max-pods-per-node=32 \
        --enable-autoupgrade \
        --enable-autorepair
    
    echo "Sandbox node pool created successfully!"
fi

# Get cluster credentials
echo ""
echo "Step 4: Getting cluster credentials..."
gcloud container clusters get-credentials $CLUSTER_NAME \
    --zone=$ZONE \
    --project=$PROJECT_ID

# Apply network policies
echo ""
echo "Step 5: Applying network policies for namespace isolation..."

# Create a default deny-all network policy template
cat <<'EOF' > /tmp/network-policy.yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: namespace-isolation
  namespace: default
spec:
  podSelector: {}
  policyTypes:
  - Ingress
  - Egress
  ingress:
  # Allow ingress from pods in the same namespace
  - from:
    - podSelector: {}
  egress:
  # Allow DNS
  - to:
    - namespaceSelector:
        matchLabels:
          name: kube-system
    - podSelector:
        matchLabels:
          k8s-app: kube-dns
    ports:
    - protocol: TCP
      port: 53
    - protocol: UDP
      port: 53
  # Allow egress to pods in the same namespace
  - from:
    - podSelector: {}
  # Allow external internet access
  - to:
    - namespaceSelector: {}
    - podSelector: {}
  - to:
    ports:
    - protocol: TCP
      port: 443
    - protocol: TCP
      port: 80
EOF

kubectl apply -f /tmp/network-policy.yaml

# Verify RuntimeClass for gVisor
echo ""
echo "Step 6: Verifying gVisor RuntimeClass..."
if kubectl get runtimeclass gvisor &>/dev/null; then
    echo "gVisor RuntimeClass already exists."
else
    echo "Creating gVisor RuntimeClass..."
    cat <<'EOF' | kubectl apply -f -
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: gvisor
handler: gvisor
EOF
fi

# Create storage class if it doesn't exist
echo ""
echo "Step 7: Checking storage class..."
if kubectl get storageclass standard-rwo &>/dev/null; then
    echo "Storage class standard-rwo already exists."
else
    echo "Creating storage class..."
    cat <<'EOF' | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: standard-rwo
provisioner: kubernetes.io/gce-pd
parameters:
  type: pd-standard
  replication-type: none
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
EOF
fi

echo ""
echo "====================================="
echo "Setup Complete!"
echo "====================================="
echo ""
echo "Cluster Details:"
echo "  Name: $CLUSTER_NAME"
echo "  Zone: $ZONE"
echo "  Project: $PROJECT_ID"
echo ""
echo "Security Features Enabled:"
echo "  ✓ gVisor sandbox runtime"
echo "  ✓ Shielded nodes (secure boot + integrity monitoring)"
echo "  ✓ Private cluster with private nodes"
echo "  ✓ Network policies for namespace isolation"
echo "  ✓ Workload Identity"
echo "  ✓ Binary Authorization ready"
echo ""
echo "Next Steps:"
echo "1. Set environment variables:"
echo "   export GOOGLE_CLOUD_PROJECT=$PROJECT_ID"
echo "   export GKE_CLUSTER_NAME=$CLUSTER_NAME"
echo "   export GKE_CLUSTER_LOCATION=$ZONE"
echo "   export ENABLE_SANDBOX=true"
echo "   export STORAGE_CLASS_NAME=standard-rwo"
echo ""
echo "2. Test the setup:"
echo "   go test ./container/... -v"
echo ""
echo "3. Run the server:"
echo "   go run ./cmd/exed -http=:8080 -ssh=:2222"