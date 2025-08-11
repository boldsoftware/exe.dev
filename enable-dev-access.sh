#!/bin/bash
# Script to enable development access to the GKE cluster
# This adds your local IP to the authorized networks for the master

set -e

# Configuration
PROJECT_ID="exe-dev-468515"
CLUSTER_NAME="exe-cluster"
ZONE="us-west2-a"

echo "======================================="
echo "Enabling Development Access to GKE Cluster"
echo "======================================="
echo ""

# Get current external IP
echo "Getting your current external IP address..."
CURRENT_IP=$(curl -s https://api.ipify.org)
if [ -z "$CURRENT_IP" ]; then
    echo "ERROR: Could not determine external IP address"
    exit 1
fi
echo "Your external IP: $CURRENT_IP"

# Check current authorized networks
echo ""
echo "Checking current authorized networks..."
CURRENT_NETWORKS=$(gcloud container clusters describe $CLUSTER_NAME \
    --zone=$ZONE \
    --project=$PROJECT_ID \
    --format="value(masterAuthorizedNetworksConfig.cidrBlocks[].cidrBlock)" 2>/dev/null || echo "")

if [ -n "$CURRENT_NETWORKS" ]; then
    echo "Current authorized networks:"
    echo "$CURRENT_NETWORKS"
else
    echo "No authorized networks currently configured"
fi

# Add current IP to authorized networks
echo ""
echo "Adding your IP to authorized networks..."
gcloud container clusters update $CLUSTER_NAME \
    --zone=$ZONE \
    --project=$PROJECT_ID \
    --enable-master-authorized-networks \
    --master-authorized-networks="${CURRENT_IP}/32" \
    --quiet

echo ""
echo "Getting cluster credentials..."
gcloud container clusters get-credentials $CLUSTER_NAME \
    --zone=$ZONE \
    --project=$PROJECT_ID

# Test connection
echo ""
echo "Testing connection to cluster..."
if kubectl cluster-info &>/dev/null; then
    echo "✅ Successfully connected to cluster!"
    kubectl get nodes
else
    echo "⚠️  Connection test failed. You may need to wait a moment for changes to propagate."
fi

echo ""
echo "======================================="
echo "Development Access Enabled!"
echo "======================================="
echo ""
echo "Your IP ($CURRENT_IP) has been added to the cluster's authorized networks."
echo "This allows you to access the Kubernetes API from your local machine."
echo ""
echo "⚠️  IMPORTANT SECURITY NOTES:"
echo "1. This opens the master API to your IP address"
echo "2. Remember to remove this access when not needed"
echo "3. Your IP may change if you're on a dynamic connection"
echo ""
echo "To remove access later, run:"
echo "  gcloud container clusters update $CLUSTER_NAME \\"
echo "    --zone=$ZONE --project=$PROJECT_ID \\"
echo "    --no-enable-master-authorized-networks"
echo ""
echo "Environment variables for local development:"
echo "  export GOOGLE_CLOUD_PROJECT=$PROJECT_ID"
echo "  export GKE_CLUSTER_NAME=$CLUSTER_NAME"
echo "  export GKE_CLUSTER_LOCATION=$ZONE"
echo "  export ENABLE_SANDBOX=true"
echo "  export STORAGE_CLASS_NAME=standard-rwo"